package jsonutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const maxValidatedJSONDepth = 64

// ValidateJSONNoDuplicateKeys verifies that data contains exactly one JSON
// value and rejects duplicate keys at every object depth. The ordinary JSON
// decoders intentionally keep the last duplicate value, which is unsafe for
// billing configuration because the value an operator reviews may differ
// from the value another parser consumes.
func ValidateJSONNoDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateJSONValueNoDuplicateKeys(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateJSONValueNoDuplicateKeys(decoder *json.Decoder, depth int) error {
	if depth > maxValidatedJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxValidatedJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := validateJSONValueNoDuplicateKeys(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValueNoDuplicateKeys(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("invalid JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}
