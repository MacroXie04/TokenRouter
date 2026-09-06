package jsonutil

import (
	"bytes"
	"encoding/json"
	jsoniter "github.com/json-iterator/go"
	"io"
)

// jsoniter is used as the project-wide JSON codec for performance and to
// centralize numeric handling. All marshal/unmarshal in business code must go
// through the wrapper functions below rather than calling encoding/json
// directly. The type definitions of encoding/json (RawMessage, Number) may
// still be referenced as types.
var jsonapi = jsoniter.Config{
	EscapeHTML:             true,
	SortMapKeys:            true,
	ValidateJsonRawMessage: true,
}.Froze()

// Marshal encodes v to JSON bytes.
func Marshal(v any) ([]byte, error) {
	return jsonapi.Marshal(v)
}

// MarshalIndent encodes v to indented JSON bytes.
func MarshalIndent(v any) ([]byte, error) {
	return jsonapi.MarshalIndent(v, "", "  ")
}

// Unmarshal decodes data into v.
func Unmarshal(data []byte, v any) error {
	return jsonapi.Unmarshal(data, v)
}

// UnmarshalJsonStr decodes a JSON string into v.
func UnmarshalJsonStr(data string, v any) error {
	return jsonapi.UnmarshalFromString(data, v)
}

// DecodeJson decodes JSON from a reader into v.
func DecodeJson(reader io.Reader, v any) error {
	return jsonapi.NewDecoder(reader).Decode(v)
}

// EncodeJson encodes v into the writer.
func EncodeJson(w io.Writer, v any) error {
	return jsonapi.NewEncoder(w).Encode(v)
}

// GetJsonType returns a coarse type tag for a raw JSON value: "object",
// "array", "string", "number", "bool", or "null".
func GetJsonType(data json.RawMessage) string {
	if len(data) == 0 {
		return "null"
	}
	t := bytes.TrimSpace(data)
	if len(t) == 0 {
		return "null"
	}
	switch t[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	default:
		if t[0] == '-' || (t[0] >= '0' && t[0] <= '9') {
			return "number"
		}
		return "unknown"
	}
}
