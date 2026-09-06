package vertex

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	defaultAPIVersion    = "v1"
	openSourceAPIVersion = "v1beta1"
	defaultRegion        = "global"
	maxBaseURLBytes      = 4 << 10
	maxModelBytes        = 1 << 10
)

var (
	regionPattern  = regexp.MustCompile(`^(?:global|[a-z][a-z0-9-]{0,62})$`)
	projectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,126}[A-Za-z0-9]$`)
	modelPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:/-]*$`)
)

func validateBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxBaseURLBytes || strings.ContainsAny(raw, "\r\n\x00\\") {
		return nil, errors.New("Vertex AI base URL is missing or invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, errors.New("Vertex AI base URL must be a valid HTTP or HTTPS URL")
	}
	// Vertex service-account requests carry short-lived bearer credentials.
	// Never permit a production channel to transmit them over plaintext; HTTP
	// remains available only behind the explicit local-development SSRF escape
	// hatch used by the other provider adapters.
	if parsed.Scheme != "https" && !(appcommon.SSRFDisabled() && parsed.Scheme == "http") {
		return nil, errors.New("Vertex AI base URL must use HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Vertex AI base URL must not contain credentials, query, or fragment")
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return nil, errors.New("Vertex AI base URL path is invalid")
	}
	for _, segment := range strings.Split(decodedPath, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("Vertex AI base URL path must not contain dot traversal")
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed, nil
}

func normalizeRegion(region string) (string, error) {
	region = strings.TrimSpace(region)
	if region == "" {
		region = defaultRegion
	}
	if !regionPattern.MatchString(region) {
		return "", errors.New("Vertex AI region is invalid")
	}
	return region, nil
}

func validateProjectID(projectID string) error {
	if !projectPattern.MatchString(strings.TrimSpace(projectID)) {
		return errors.New("Vertex AI project_id is invalid")
	}
	return nil
}

func validateModel(model string) error {
	model = strings.TrimSpace(model)
	if len(model) == 0 || len(model) > maxModelBytes || !modelPattern.MatchString(model) ||
		strings.Contains(model, "..") || strings.ContainsAny(model, "?#\r\n\x00\\") {
		return errors.New("Vertex AI mapped model is invalid")
	}
	return nil
}

func apiBaseURL(customBase, version, projectID, region string) (string, error) {
	region, err := normalizeRegion(region)
	if err != nil {
		return "", err
	}
	version = strings.Trim(version, "/ ")
	if version != defaultAPIVersion && version != openSourceAPIVersion {
		return "", errors.New("Vertex AI API version is invalid")
	}
	var base *url.URL
	if strings.TrimSpace(customBase) != "" {
		base, err = validateBaseURL(customBase)
		if err != nil {
			return "", err
		}
	} else {
		host := "aiplatform.googleapis.com"
		if region != defaultRegion {
			host = region + "-aiplatform.googleapis.com"
		}
		base = &url.URL{Scheme: "https", Host: host}
	}
	if !strings.HasSuffix(base.Path, "/"+version) {
		base.Path = strings.TrimRight(base.Path, "/") + "/" + version
	}
	if projectID != "" {
		if err := validateProjectID(projectID); err != nil {
			return "", err
		}
		base.Path = path.Join(base.Path, "projects", projectID, "locations", region)
	}
	return strings.TrimRight(base.String(), "/"), nil
}

func publisherModelURL(customBase, version, projectID, region, publisher, model, action string) (string, error) {
	if err := validateModel(model); err != nil {
		return "", err
	}
	if publisher != "google" && publisher != "anthropic" {
		return "", errors.New("Vertex AI publisher is invalid")
	}
	switch action {
	case "generateContent", "streamGenerateContent", "rawPredict", "streamRawPredict", "predict", "predictLongRunning", "fetchPredictOperation":
	default:
		return "", errors.New("Vertex AI model action is invalid")
	}
	base, err := apiBaseURL(customBase, version, projectID, region)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/publishers/%s/models/%s:%s", base, publisher, url.PathEscape(model), action), nil
}

func openSourceChatURL(customBase, projectID, region string) (string, error) {
	base, err := apiBaseURL(customBase, openSourceAPIVersion, projectID, region)
	if err != nil {
		return "", err
	}
	return base + "/endpoints/openapi/chat/completions", nil
}

// ResolveTaskRegion applies the same channel-owned per-model location mapping
// used by synchronous Vertex requests. Client request fields are deliberately
// not accepted as a source of routing authority.
func ResolveTaskRegion(channelOther, originalModel string) (string, error) {
	return modelRegion(&relaycommon.Meta{
		Channel:           &model.Channel{Other: channelOther},
		OriginalModelName: originalModel,
	})
}

// CanonicalTaskBaseURL snapshots a validated, non-empty Vertex API base for a
// durable task. The version is included so an empty configured base can be
// persisted without later consulting mutable channel configuration.
func CanonicalTaskBaseURL(customBase, region string) (string, error) {
	return apiBaseURL(customBase, defaultAPIVersion, "", region)
}
