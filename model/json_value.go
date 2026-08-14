package model

import (
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
)

// JSONValue stores JSON without turning arrays or objects into quoted strings
// in API responses.
type JSONValue []byte

func (value JSONValue) Value() (driver.Value, error) {
	if value == nil {
		return nil, nil
	}
	if len(value) == 0 {
		return []byte("[]"), nil
	}
	if !validJSON(value) {
		return nil, fmt.Errorf("invalid JSON value")
	}
	return []byte(value), nil
}

func (value *JSONValue) Scan(source any) error {
	var raw []byte
	switch typed := source.(type) {
	case nil:
		*value = nil
		return nil
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		encoded, err := common.Marshal(typed)
		if err != nil {
			return err
		}
		raw = encoded
	}
	if strings.TrimSpace(string(raw)) == "" {
		raw = []byte("[]")
	} else if !validJSON(raw) {
		encoded, err := common.Marshal(string(raw))
		if err != nil {
			return err
		}
		raw = encoded
	}
	*value = append((*value)[:0], raw...)
	return nil
}

func (value JSONValue) MarshalJSON() ([]byte, error) {
	if value == nil {
		return []byte("null"), nil
	}
	if len(value) == 0 {
		return []byte("[]"), nil
	}
	if !validJSON(value) {
		return nil, fmt.Errorf("invalid JSON value")
	}
	return value, nil
}

func (value *JSONValue) UnmarshalJSON(data []byte) error {
	if !validJSON(data) {
		return fmt.Errorf("invalid JSON value")
	}
	*value = append((*value)[:0], data...)
	return nil
}

func validJSON(data []byte) bool {
	var decoded any
	return common.Unmarshal(data, &decoded) == nil
}
