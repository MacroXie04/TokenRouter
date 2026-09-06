package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

// rejectDuplicateJSONKeys validates one complete JSON value and rejects
// duplicate object names. encoding/json otherwise silently keeps the last
// value, which could let a client present one model to routing and another to
// a provider-specific conversion.
func rejectDuplicateJSONKeys(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("JSON body is empty")
	}
	if !utf8.Valid(raw) {
		return errors.New("JSON body is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	if err := walkJSONValue(decoder, 0, &nodes); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, depth int, nodes *int) error {
	if depth > MaxJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", MaxJSONDepth)
	}
	(*nodes)++
	if *nodes > MaxJSONNodes {
		return fmt.Errorf("JSON contains more than %d values", MaxJSONNodes)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if text, ok := token.(string); ok {
		if !utf8.ValidString(text) || len(text) > MaxJSONTextBytes {
			return errors.New("JSON text is outside the supported range")
		}
	}
	if number, ok := token.(json.Number); ok {
		value, err := number.Float64()
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > MaxJSONNumberMagnitude {
			return errors.New("JSON number is outside the supported range")
		}
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
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
			if !ok || !utf8.ValidString(key) || len(key) > MaxJSONKeyBytes {
				return errors.New("JSON object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if len(seen) > MaxJSONObjectEntries {
				return fmt.Errorf("JSON object contains more than %d entries", MaxJSONObjectEntries)
			}
			if err := walkJSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		entries := 0
		for decoder.More() {
			entries++
			if entries > MaxJSONArrayEntries {
				return fmt.Errorf("JSON array contains more than %d entries", MaxJSONArrayEntries)
			}
			if err := walkJSONValue(decoder, depth+1, nodes); err != nil {
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

func strictJSON(raw []byte, target any, disallowUnknown bool) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if disallowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}
