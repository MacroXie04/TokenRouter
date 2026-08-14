package protocolkit

import (
	"encoding/json"
	"strconv"
)

// strOr returns v as a string if it is a string, else the empty string.
func strOr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ToJSONString marshals v to a compact JSON string, returning "" on error.
func ToJSONString(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// MarshalJSON marshals v to JSON bytes.
func MarshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// UnmarshalJSON unmarshals data into v.
func UnmarshalJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// IntFromAny extracts an int from a json.Number-compatible value.
func IntFromAny(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(t)
		return i
	}
	return 0
}
