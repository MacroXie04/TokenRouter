package ollama

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxOperationResponseBytes = int64(1 << 20)
	maxVersionResponseBytes   = int64(64 << 10)
	maxPullStreamBytes        = int64(64 << 20)
	maxPullProgressFrames     = 100_000
	maxModelNameBytes         = 1024
	maxVersionBytes           = 256

	pullTimeout       = 30 * time.Minute
	pullStreamTimeout = time.Hour
	operationTimeout  = 15 * time.Second
)

// operationHTTPClient is the bounded, SSRF-safe client used by dashboard Ollama
// lifecycle operations. It intentionally ignores environment proxy settings
// and refuses redirects so a channel credential cannot be replayed elsewhere.
var operationHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           httpx.SafeDialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// PullProgress is one native /api/pull progress frame.
type PullProgress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PullModel requests a non-streaming model pull and verifies Ollama's terminal
// success status before returning.
func PullModel(ctx context.Context, baseURL, apiKey, modelName string) error {
	modelName, err := validateModelName(modelName)
	if err != nil {
		return err
	}
	body, err := protocolkit.MarshalJSON(map[string]any{"name": modelName, "stream": false})
	if err != nil {
		return fmt.Errorf("encode Ollama pull request: %w", err)
	}
	ctx, cancel := boundedContext(ctx, pullTimeout)
	defer cancel()
	resp, err := doOperation(ctx, http.MethodPost, baseURL, "/api/pull", apiKey, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := requireOperationSuccess(resp, apiKey); err != nil {
		return err
	}
	raw, err := httpx.ReadAllLimited(resp.Body, maxOperationResponseBytes)
	if err != nil {
		return fmt.Errorf("read Ollama pull response: %w", err)
	}
	var progress PullProgress
	if err := protocolkit.UnmarshalJSON(raw, &progress); err != nil {
		return fmt.Errorf("decode Ollama pull response: %w", err)
	}
	if err := validateProgress(progress); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(progress.Status), "success") {
		return errors.New("Ollama pull ended without a success status")
	}
	return nil
}

// PullModelStream streams validated native progress frames to callback and
// succeeds only after a terminal success frame. The frame, count, total-byte,
// and duration bounds prevent a malicious upstream from consuming unbounded
// memory, CPU, or connection lifetime.
func PullModelStream(
	ctx context.Context,
	baseURL, apiKey, modelName string,
	callback func(PullProgress) error,
) error {
	modelName, err := validateModelName(modelName)
	if err != nil {
		return err
	}
	body, err := protocolkit.MarshalJSON(map[string]any{"name": modelName, "stream": true})
	if err != nil {
		return fmt.Errorf("encode Ollama streaming pull request: %w", err)
	}
	ctx, cancel := boundedContext(ctx, pullStreamTimeout)
	defer cancel()
	resp, err := doOperation(ctx, http.MethodPost, baseURL, "/api/pull", apiKey, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := requireOperationSuccess(resp, apiKey); err != nil {
		return err
	}

	limited := &io.LimitedReader{R: resp.Body, N: maxPullStreamBytes + 1}
	scanner := relaycommon.NewUpstreamSSEScanner(limited)
	frames := 0
	terminal := false
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		frames++
		if frames > maxPullProgressFrames {
			return fmt.Errorf("Ollama pull stream exceeds %d progress frames", maxPullProgressFrames)
		}
		var progress PullProgress
		if err := protocolkit.UnmarshalJSON([]byte(raw), &progress); err != nil {
			return fmt.Errorf("decode Ollama pull progress frame %d: %w", frames, err)
		}
		if err := validateProgress(progress); err != nil {
			return fmt.Errorf("invalid Ollama pull progress frame %d: %w", frames, err)
		}
		if callback != nil {
			if err := callback(progress); err != nil {
				return fmt.Errorf("write Ollama pull progress: %w", err)
			}
		}
		if strings.EqualFold(strings.TrimSpace(progress.Status), "success") {
			terminal = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Ollama pull stream (maximum frame %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if limited.N <= 0 && !terminal {
		return fmt.Errorf("Ollama pull stream exceeds %d bytes", maxPullStreamBytes)
	}
	if !terminal {
		return errors.New("Ollama pull stream ended without a success status")
	}
	return nil
}

// DeleteModel removes one named model through Ollama's native endpoint.
func DeleteModel(ctx context.Context, baseURL, apiKey, modelName string) error {
	modelName, err := validateModelName(modelName)
	if err != nil {
		return err
	}
	body, err := protocolkit.MarshalJSON(map[string]any{"name": modelName})
	if err != nil {
		return fmt.Errorf("encode Ollama delete request: %w", err)
	}
	ctx, cancel := boundedContext(ctx, operationTimeout)
	defer cancel()
	resp, err := doOperation(ctx, http.MethodDelete, baseURL, "/api/delete", apiKey, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := requireOperationSuccess(resp, apiKey); err != nil {
		return err
	}
	raw, err := httpx.ReadAllLimited(resp.Body, maxOperationResponseBytes)
	if err != nil {
		return fmt.Errorf("read Ollama delete response: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var result struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := protocolkit.UnmarshalJSON(raw, &result); err != nil {
		return fmt.Errorf("decode Ollama delete response: %w", err)
	}
	if strings.TrimSpace(result.Error) != "" {
		return errors.New("Ollama delete reported an error")
	}
	return nil
}

// FetchVersion returns the bounded /api/version value.
func FetchVersion(ctx context.Context, baseURL, apiKey string) (string, error) {
	ctx, cancel := boundedContext(ctx, operationTimeout)
	defer cancel()
	resp, err := doOperation(ctx, http.MethodGet, baseURL, "/api/version", apiKey, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := requireOperationSuccess(resp, apiKey); err != nil {
		return "", err
	}
	raw, err := httpx.ReadAllLimited(resp.Body, maxVersionResponseBytes)
	if err != nil {
		return "", fmt.Errorf("read Ollama version response: %w", err)
	}
	var result struct {
		Version string `json:"version"`
		Error   string `json:"error"`
	}
	if err := protocolkit.UnmarshalJSON(raw, &result); err != nil {
		return "", fmt.Errorf("decode Ollama version response: %w", err)
	}
	version := strings.TrimSpace(result.Version)
	if strings.TrimSpace(result.Error) != "" {
		return "", errors.New("Ollama version endpoint reported an error")
	}
	if version == "" || len(version) > maxVersionBytes || !utf8.ValidString(version) || hasControlCharacter(version) {
		return "", errors.New("Ollama version response is invalid")
	}
	return version, nil
}

// FetchModels returns normalized model names from the native /api/tags shape.
func FetchModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	ctx, cancel := boundedContext(ctx, operationTimeout)
	defer cancel()
	resp, err := doOperation(ctx, http.MethodGet, baseURL, "/api/tags", apiKey, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := requireOperationSuccess(resp, apiKey); err != nil {
		return nil, err
	}
	raw, err := httpx.ReadAllLimited(resp.Body, maxOperationResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read Ollama tags response: %w", err)
	}
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
		Error string `json:"error"`
	}
	if err := protocolkit.UnmarshalJSON(raw, &result); err != nil {
		return nil, fmt.Errorf("decode Ollama tags response: %w", err)
	}
	if strings.TrimSpace(result.Error) != "" {
		return nil, errors.New("Ollama tags endpoint reported an error")
	}
	seen := make(map[string]struct{}, len(result.Models))
	models := make([]string, 0, len(result.Models))
	for _, item := range result.Models {
		name, err := validateModelName(item.Name)
		if err != nil {
			return nil, fmt.Errorf("invalid Ollama model in tags response: %w", err)
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		models = append(models, name)
	}
	return models, nil
}

func doOperation(ctx context.Context, method, baseURL, path, apiKey string, body []byte) (*http.Response, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, relaycommon.JoinURL(base, path), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Ollama operation request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(apiKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := operationHTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("perform Ollama operation: %w", err)
	}
	return response, nil
}

func requireOperationSuccess(resp *http.Response, apiKey string) error {
	if resp == nil {
		return errors.New("Ollama operation returned no response")
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	return relaycommon.SanitizeUpstreamError(relaycommon.HandleErrorResponse(resp), apiKey)
}

func validateProgress(progress PullProgress) error {
	status := strings.TrimSpace(progress.Status)
	if status == "" || len(status) > 512 || hasControlCharacter(status) {
		return errors.New("Ollama pull progress has invalid status")
	}
	if progress.Total < 0 || progress.Completed < 0 || progress.Total > 0 && progress.Completed > progress.Total {
		return errors.New("Ollama pull progress has invalid byte counts")
	}
	if strings.TrimSpace(progress.Error) != "" || strings.EqualFold(status, "error") {
		return errors.New("Ollama pull reported an error")
	}
	return nil
}

func validateModelName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > maxModelNameBytes || !utf8.ValidString(name) || hasControlCharacter(name) {
		return "", errors.New("Ollama model name is invalid")
	}
	return name, nil
}

func hasControlCharacter(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func boundedContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, maximum)
}
