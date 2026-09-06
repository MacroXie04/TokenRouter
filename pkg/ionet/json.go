package ionet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const maxJSONDepth = 64

var errInvalidProviderResponse = errors.New("io.net returned an invalid response")

// DecodeRequest decodes exactly one JSON value, rejecting unknown fields and
// duplicate object keys. The latter prevents security-sensitive fields from
// acquiring different meanings in different JSON implementations.
func DecodeRequest(data []byte, target any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || !utf8.Valid(data) {
		return errors.New("request payload is required")
	}
	if trimmed[0] != '{' {
		return errors.New("invalid request payload")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return errors.New("invalid request payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid request payload")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return errors.New("invalid request payload")
	}
	return nil
}

func decodeResponse(data []byte, target any) error {
	if len(bytes.TrimSpace(data)) == 0 || !utf8.Valid(data) || rejectDuplicateKeys(data) != nil {
		return errInvalidProviderResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || requireJSONEOF(decoder) != nil {
		return errInvalidProviderResponse
	}
	normalized, err := json.Marshal(normalizeTimeValues(value))
	if err != nil || json.Unmarshal(normalized, target) != nil {
		return errInvalidProviderResponse
	}
	return nil
}

func decodeDataResponse(data []byte, target any) error {
	if len(bytes.TrimSpace(data)) == 0 || !utf8.Valid(data) || rejectDuplicateKeys(data) != nil {
		return errInvalidProviderResponse
	}
	var wrapper map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&wrapper); err != nil || requireJSONEOF(decoder) != nil {
		return errInvalidProviderResponse
	}
	raw, ok := wrapper["data"]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errInvalidProviderResponse
	}
	return decodeResponse(raw, target)
}

// containsCredentialJSON detects credentials even when a provider JSON
// response escaped one or more characters. Raw byte checks alone do not catch
// strings such as "secret\u002dvalue", which decode to the original secret.
func containsCredentialJSON(data []byte, credential string) bool {
	if credential == "" || !utf8.Valid(data) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || requireJSONEOF(decoder) != nil {
		return false
	}
	return jsonValueContains(value, credential)
}

func jsonValueContains(value any, needle string) bool {
	switch current := value.(type) {
	case string:
		return strings.Contains(current, needle)
	case map[string]any:
		for key, nested := range current {
			if strings.Contains(key, needle) || jsonValueContains(nested, needle) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if jsonValueContains(nested, needle) {
				return true
			}
		}
	}
	return false
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, 0); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errors.New("JSON nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func normalizeTimeValues(value any) any {
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			current[key] = normalizeTimeValues(nested)
		}
		return current
	case []any:
		for index, nested := range current {
			current[index] = normalizeTimeValues(nested)
		}
		return current
	case string:
		trimmed := strings.TrimSpace(current)
		for _, layout := range []string{
			"2006-01-02T15:04:05.999999999",
			"2006-01-02T15:04:05.999999",
			"2006-01-02T15:04:05",
		} {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return parsed.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	return value
}
