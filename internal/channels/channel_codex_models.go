package channels

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	codexLatestReleaseURL      = "https://api.github.com/repos/openai/codex/releases/latest"
	maxCodexReleaseBodyBytes   = int64(256 << 10)
	codexClientVersionCacheTTL = time.Hour
)

var codexClientVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

var codexClientVersionCache struct {
	sync.Mutex
	version   string
	expiresAt time.Time
}

func fetchCodexUpstreamModels(parent context.Context, channel *model.Channel, base string) ([]string, error) {
	accessToken, accountID, err := parseCodexOperationalCredential(GetChannelKey(channel))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	clientVersion, err := currentCodexClientVersion(ctx)
	if err != nil {
		return nil, err
	}

	modelsURL, err := url.Parse(strings.TrimRight(base, "/") + "/backend-api/codex/models")
	if err != nil {
		return nil, errors.New("invalid Codex models URL")
	}
	query := modelsURL.Query()
	query.Set("client_version", clientVersion)
	modelsURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("User-Agent", "codex-cli/"+clientVersion)
	req.Header.Set("Accept", "application/json")

	resp, err := channelUpstreamHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Codex models upstream returned status %s", resp.Status)
	}
	body, err := httpx.ReadAllLimited(resp.Body, maxChannelUpstreamResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read Codex models response: %w", err)
	}
	var payload struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := jsonutil.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("decode Codex models response")
	}
	ids := make([]string, 0, len(payload.Models))
	for _, entry := range payload.Models {
		ids = append(ids, entry.Slug)
	}
	normalized, err := normalizeUpstreamModelNames(ids)
	if err != nil {
		return nil, fmt.Errorf("Codex model list is invalid: %w", err)
	}
	return normalized, nil
}

func currentCodexClientVersion(ctx context.Context) (string, error) {
	if configured := strings.TrimSpace(env.GetEnv("CODEX_CLIENT_VERSION", "")); configured != "" {
		if !codexClientVersionPattern.MatchString(configured) {
			return "", errors.New("CODEX_CLIENT_VERSION is invalid")
		}
		return configured, nil
	}

	now := time.Now()
	codexClientVersionCache.Lock()
	defer codexClientVersionCache.Unlock()
	if codexClientVersionCache.version != "" && now.Before(codexClientVersionCache.expiresAt) {
		return codexClientVersionCache.version, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexLatestReleaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "TokenRouter")
	resp, err := channelUpstreamHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch current Codex client version: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch current Codex client version: upstream status %s", resp.Status)
	}
	body, err := httpx.ReadAllLimited(resp.Body, maxCodexReleaseBodyBytes)
	if err != nil {
		return "", fmt.Errorf("read current Codex client version: %w", err)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := jsonutil.Unmarshal(body, &release); err != nil {
		return "", errors.New("decode current Codex client version")
	}
	version := strings.TrimSpace(release.TagName)
	version = strings.TrimPrefix(version, "rust-v")
	version = strings.TrimPrefix(version, "v")
	if !codexClientVersionPattern.MatchString(version) {
		return "", errors.New("current Codex release has an invalid version")
	}
	codexClientVersionCache.version = version
	codexClientVersionCache.expiresAt = now.Add(codexClientVersionCacheTTL)
	return version, nil
}
