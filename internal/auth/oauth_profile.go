package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// decodeBoundedOAuthJSONObject parses an untrusted provider response while
// enforcing per-field, cardinality, depth, and duplicate-key bounds. The
// aggregate body cap alone is insufficient: a tiny response can still carry
// thousands of keys or an identity string too large for persistent fields.
func decodeBoundedOAuthJSONObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	nodes := 0
	value, err := decodeBoundedOAuthJSONValue(decoder, 1, &nodes)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("OAuth JSON response has trailing data")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("OAuth JSON response must be an object")
	}
	return object, nil
}

func decodeBoundedOAuthJSONValue(decoder *json.Decoder, depth int, nodes *int) (any, error) {
	if decoder == nil || nodes == nil || depth > maxOAuthJSONDepth || *nodes >= maxOAuthJSONNodes {
		return nil, errors.New("OAuth JSON response exceeds safe limits")
	}
	(*nodes)++
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		switch value := token.(type) {
		case string:
			if !validOAuthText(value, maxOAuthJSONStringBytes, true) {
				return nil, errors.New("OAuth JSON string exceeds safe limits")
			}
		case json.Number:
			if value.String() == "" || len(value.String()) > maxOAuthJSONNumberBytes {
				return nil, errors.New("OAuth JSON number exceeds safe limits")
			}
		}
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			if len(object) >= maxOAuthJSONObjectItems {
				return nil, errors.New("OAuth JSON object exceeds safe limits")
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok || !validOAuthText(key, maxOAuthJSONKeyBytes, false) {
				return nil, errors.New("OAuth JSON key exceeds safe limits")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, errors.New("OAuth JSON response contains a duplicate key")
			}
			value, err := decodeBoundedOAuthJSONValue(decoder, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return nil, errors.New("OAuth JSON object is invalid")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			if len(array) >= maxOAuthJSONArrayItems {
				return nil, errors.New("OAuth JSON array exceeds safe limits")
			}
			value, err := decodeBoundedOAuthJSONValue(decoder, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
			return nil, errors.New("OAuth JSON array is invalid")
		}
		return array, nil
	default:
		return nil, errors.New("OAuth JSON response is invalid")
	}
}

func validateOAuthToken(token *OAuthToken) error {
	if token == nil || !validOAuthText(token.AccessToken, maxOAuthAccessTokenBytes, false) {
		return errors.New("OAuth access token is invalid")
	}
	for _, character := range token.AccessToken {
		if character < 0x21 || character > 0x7e {
			return errors.New("OAuth access token is invalid")
		}
	}
	token.TokenType = strings.TrimSpace(token.TokenType)
	if token.TokenType != "" && (len(token.TokenType) > maxOAuthTokenTypeBytes || !oauthTokenTypePattern.MatchString(token.TokenType)) {
		return errors.New("OAuth token type is invalid")
	}
	return nil
}

func normalizeProviderUser(provider string, input *ProviderUser) (*ProviderUser, error) {
	if input == nil || !validOAuthProviderSlug(provider) {
		return nil, errors.New("provider identity is invalid")
	}
	pu := *input
	maximumSubjectBytes := maxOAuthBuiltInSubjectBytes
	if pu.CustomProviderId > 0 {
		maximumSubjectBytes = maxOAuthCustomSubjectBytes
		configured, err := model.GetCustomOAuthProviderById(pu.CustomProviderId)
		if err != nil || !configured.Enabled || configured.Slug != provider || IsBuiltInOAuthProvider(provider) {
			return nil, errors.New("provider identity origin is invalid")
		}
	} else if _, ok := model.BuiltInExternalIdentityColumn(provider); !ok {
		return nil, errors.New("unsupported external identity provider")
	}
	pu.ProviderID = strings.TrimSpace(pu.ProviderID)
	if !validOAuthText(pu.ProviderID, maximumSubjectBytes, false) {
		if pu.CustomProviderId == 0 {
			return nil, fmt.Errorf("%w: provider identity exceeds safe limits", model.ErrInvalidPersistentIdentifier)
		}
		return nil, errors.New("provider identity exceeds safe limits")
	}
	if pu.LegacyProviderID != "" {
		pu.LegacyProviderID = strings.TrimSpace(pu.LegacyProviderID)
		if !validOAuthText(pu.LegacyProviderID, maxOAuthBuiltInSubjectBytes, false) {
			return nil, errors.New("legacy provider identity exceeds safe limits")
		}
	}
	pu.Username = normalizeOAuthProfileText(pu.Username, maxOAuthUsernameBytes)
	pu.DisplayName = normalizeOAuthProfileText(pu.DisplayName, maxOAuthDisplayNameBytes)
	if pu.Username == "" && pu.CustomProviderId == 0 {
		pu.Username = normalizeOAuthProfileText(pu.ProviderID, maxOAuthUsernameBytes)
	}
	if pu.DisplayName == "" {
		pu.DisplayName = pu.Username
	}
	if pu.Email != "" {
		if email, _, err := model.NormalizeVerifiedEmail(pu.Email); err == nil {
			pu.Email = email
		} else {
			// Provider emails are profile hints, not verified recovery identities.
			// Drop malformed values instead of rejecting an otherwise valid login.
			pu.Email = ""
		}
	}
	return &pu, nil
}

func normalizeOAuthProfileText(value string, maximumBytes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return -1
		}
		return character
	}, value)
	if len(value) <= maximumBytes {
		return value
	}
	for len(value) > maximumBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return ""
		}
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func boundedOAuthInteger(value any, minimum, maximum int) (int, bool) {
	var raw string
	switch typed := value.(type) {
	case json.Number:
		raw = typed.String()
	case int:
		raw = strconv.Itoa(typed)
	default:
		return 0, false
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < minimum || parsed > maximum || strconv.Itoa(parsed) != raw {
		return 0, false
	}
	return parsed, true
}

// firstProviderID extracts the provider subject, handling string (OIDC "sub")
// and numeric (GitHub/Discord "id") shapes.
func firstProviderID(m map[string]any) string {
	if v, ok := m["sub"].(string); ok && v != "" {
		return v
	}
	if v, ok := m["id"].(string); ok && v != "" {
		return v
	}
	switch v := m["id"].(type) {
	case json.Number:
		value := v.String()
		if value != "" {
			for _, character := range value {
				if character < '0' || character > '9' {
					return ""
				}
			}
		}
		return value
	case float64:
		value := strconv.FormatFloat(v, 'f', -1, 64)
		if value == "" {
			return ""
		}
		for _, character := range value {
			if character < '0' || character > '9' {
				return ""
			}
		}
		return value
	case int:
		if v < 0 {
			return ""
		}
		return strconv.Itoa(v)
	}
	return ""
}
