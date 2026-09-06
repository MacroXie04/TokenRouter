package catalog

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/catalog/ionet"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxDeploymentKeywordBytes = 256
	maxDeploymentQueryBytes   = 16 << 10
)

type deploymentPage struct {
	Page     int
	PageSize int
}

func parseDeploymentPage(c *gin.Context) (deploymentPage, bool) {
	page := 1
	if raw, present, valid := optionalSingleQuery(c, "p", 16); !valid {
		deploymentError(c, "invalid pagination")
		return deploymentPage{}, false
	} else if present {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > ionet.MaxPage {
			deploymentError(c, "invalid pagination")
			return deploymentPage{}, false
		}
		page = parsed
	}
	pageSize := 10
	foundSize := false
	for _, name := range []string{"page_size", "ps", "size"} {
		raw, present, valid := optionalSingleQuery(c, name, 16)
		if !valid || (present && foundSize) {
			deploymentError(c, "invalid pagination")
			return deploymentPage{}, false
		}
		if present {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > ionet.MaxPageSize {
				deploymentError(c, "invalid pagination")
				return deploymentPage{}, false
			}
			pageSize, foundSize = parsed, true
		}
	}
	return deploymentPage{Page: page, PageSize: pageSize}, true
}

func parseDeploymentLogOptions(c *gin.Context) (*ionet.GetLogsOptions, bool) {
	options := &ionet.GetLogsOptions{Limit: 100}
	for name, destination := range map[string]*string{"level": &options.Level, "stream": &options.Stream, "cursor": &options.Cursor} {
		value, present, valid := optionalSingleQuery(c, name, 1024)
		if !valid {
			deploymentError(c, "invalid log query")
			return nil, false
		}
		if present {
			*destination = value
		}
	}
	if raw, present, valid := optionalSingleQuery(c, "limit", 16); !valid {
		deploymentError(c, "invalid log limit")
		return nil, false
	} else if present {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > ionet.MaxLogLimit {
			deploymentError(c, "invalid log limit")
			return nil, false
		}
		options.Limit = limit
	}
	if raw, present, valid := optionalSingleQuery(c, "follow", 5); !valid {
		deploymentError(c, "invalid follow parameter")
		return nil, false
	} else if present {
		if raw != "true" && raw != "false" {
			deploymentError(c, "invalid follow parameter")
			return nil, false
		}
		options.Follow = raw == "true"
	}
	for name, destination := range map[string]**time.Time{"start_time": &options.StartTime, "end_time": &options.EndTime} {
		raw, present, valid := optionalSingleQuery(c, name, 64)
		if !valid {
			deploymentError(c, "invalid log time range")
			return nil, false
		}
		if present {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				deploymentError(c, "invalid log time range")
				return nil, false
			}
			*destination = &parsed
		}
	}
	options.Level = strings.ToLower(strings.TrimSpace(options.Level))
	options.Stream = strings.ToLower(strings.TrimSpace(options.Stream))
	if ionet.ValidateLogsOptions(options) != nil {
		deploymentError(c, "invalid log query")
		return nil, false
	}
	return options, true
}

func bindDeploymentJSON(c *gin.Context, target any) bool {
	raw, ok := readOptionalDeploymentJSON(c)
	if !ok {
		return false
	}
	if len(raw) == 0 || ionet.DecodeRequest(raw, target) != nil {
		deploymentError(c, "invalid request payload")
		return false
	}
	return true
}

func readOptionalDeploymentJSON(c *gin.Context) ([]byte, bool) {
	if c.Request.ContentLength > ionet.MaxRequestBodyBytes {
		deploymentPayloadTooLarge(c)
		return nil, false
	}
	raw, err := httpx.ReadAllLimited(c.Request.Body, ionet.MaxRequestBodyBytes)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			deploymentPayloadTooLarge(c)
		} else {
			deploymentError(c, "invalid request payload")
		}
		return nil, false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, true
	}
	return raw, true
}

func requiredDeploymentQuery(c *gin.Context, name string, maxBytes int) (string, bool) {
	value, present, valid := optionalSingleQuery(c, name, maxBytes)
	if !valid || !present || strings.TrimSpace(value) == "" {
		deploymentError(c, name+" parameter is required")
		return "", false
	}
	return value, true
}

func deploymentQuery(c *gin.Context, name string, maxBytes int) (string, bool) {
	value, _, valid := optionalSingleQuery(c, name, maxBytes)
	if !valid {
		deploymentError(c, "invalid "+name+" parameter")
		return "", false
	}
	return value, true
}

func optionalSingleQuery(c *gin.Context, name string, maxBytes int) (string, bool, bool) {
	if len(c.Request.URL.RawQuery) > maxDeploymentQueryBytes {
		return "", true, false
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return "", true, false
	}
	values, present := query[name]
	if !present {
		return "", false, true
	}
	if len(values) != 1 || len(values[0]) > maxBytes || !safeDeploymentText(values[0]) {
		return "", true, false
	}
	return values[0], true, true
}

func safeDeploymentText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}
