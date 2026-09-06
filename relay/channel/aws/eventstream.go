package aws

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"

	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type eventStreamSSEReader struct {
	reader  io.Reader
	pending []byte
	started bool
	stopped bool
	events  int
}

func newEventStreamSSEReader(reader io.Reader) io.Reader {
	return &eventStreamSSEReader{reader: reader}
}

func (reader *eventStreamSSEReader) Read(destination []byte) (int, error) {
	for len(reader.pending) == 0 {
		event, err := readEventStreamEvent(reader.reader)
		if err != nil {
			return 0, err
		}
		if event == nil {
			if !reader.started || !reader.stopped {
				return 0, errors.New("AWS event stream ended before message_stop")
			}
			return 0, io.EOF
		}
		reader.events++
		if reader.events > MaxStreamEvents {
			return 0, fmt.Errorf("AWS event stream exceeds %d events", MaxStreamEvents)
		}
		if err := reader.validateSequence(event.eventType); err != nil {
			return 0, err
		}
		reader.pending = append(reader.pending, "event: "...)
		reader.pending = append(reader.pending, event.eventType...)
		reader.pending = append(reader.pending, '\n')
		reader.pending = append(reader.pending, "data: "...)
		reader.pending = append(reader.pending, event.payload...)
		reader.pending = append(reader.pending, '\n', '\n')
	}
	written := copy(destination, reader.pending)
	reader.pending = reader.pending[written:]
	return written, nil
}

func (reader *eventStreamSSEReader) validateSequence(eventType string) error {
	switch eventType {
	case "ping":
		if reader.stopped {
			return errors.New("AWS event stream contains data after message_stop")
		}
		return nil
	case "message_start":
		if reader.started || reader.stopped {
			return errors.New("AWS event stream contains an invalid message_start")
		}
		reader.started = true
		return nil
	case "content_block_start", "content_block_delta", "content_block_stop", "message_delta":
		if !reader.started || reader.stopped {
			return errors.New("AWS event stream event is out of sequence")
		}
		return nil
	case "message_stop":
		if !reader.started || reader.stopped {
			return errors.New("AWS event stream contains an invalid message_stop")
		}
		reader.stopped = true
		return nil
	default:
		return errors.New("AWS event stream contains an unsupported Claude event")
	}
}

type decodedEventStreamEvent struct {
	eventType string
	payload   []byte
}

func readEventStreamEvent(reader io.Reader) (*decodedEventStreamEvent, error) {
	if reader == nil {
		return nil, errors.New("AWS event stream is nil")
	}
	prelude := make([]byte, 12)
	read, err := io.ReadFull(reader, prelude)
	if errors.Is(err, io.EOF) && read == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("AWS event stream ended in a partial prelude")
	}
	totalLength := int64(binary.BigEndian.Uint32(prelude[0:4]))
	headersLength := int64(binary.BigEndian.Uint32(prelude[4:8]))
	if totalLength < 16 || totalLength > int64(MaxEventFrameBytes) || headersLength < 0 ||
		headersLength > MaxEventHeadersBytes || headersLength > totalLength-16 {
		return nil, errors.New("AWS event stream frame length is invalid")
	}
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:12]) {
		return nil, errors.New("AWS event stream prelude checksum is invalid")
	}
	remainder := make([]byte, totalLength-12)
	if _, err := io.ReadFull(reader, remainder); err != nil {
		return nil, errors.New("AWS event stream ended in a partial frame")
	}
	message := make([]byte, 0, totalLength-4)
	message = append(message, prelude...)
	message = append(message, remainder[:len(remainder)-4]...)
	wantCRC := binary.BigEndian.Uint32(remainder[len(remainder)-4:])
	if crc32.ChecksumIEEE(message) != wantCRC {
		return nil, errors.New("AWS event stream message checksum is invalid")
	}
	headers, err := decodeEventStreamHeaders(remainder[:headersLength])
	if err != nil {
		return nil, err
	}
	payload := remainder[headersLength : len(remainder)-4]
	messageType := headers[":message-type"]
	eventType := headers[":event-type"]
	switch messageType {
	case "event":
		if eventType != "chunk" {
			return nil, errors.New("AWS event stream contains an unsupported event type")
		}
	case "error", "exception":
		return nil, eventStreamProviderError(eventType)
	default:
		return nil, errors.New("AWS event stream contains an unsupported message type")
	}
	var wrapper struct {
		Bytes string `json:"bytes"`
	}
	if err := strictJSON(payload, &wrapper, true); err != nil || wrapper.Bytes == "" {
		return nil, errors.New("AWS event stream chunk payload is invalid")
	}
	if len(wrapper.Bytes) > base64.StdEncoding.EncodedLen(MaxEventPayloadBytes)+2 {
		return nil, fmt.Errorf("AWS event stream payload exceeds %d bytes", MaxEventPayloadBytes)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(wrapper.Bytes)
	if err != nil || len(decoded) > MaxEventPayloadBytes {
		return nil, errors.New("AWS event stream chunk contains invalid base64")
	}
	innerType, err := validateClaudeStreamEvent(decoded)
	if err != nil {
		return nil, err
	}
	return &decodedEventStreamEvent{eventType: innerType, payload: decoded}, nil
}

func decodeEventStreamHeaders(encoded []byte) (map[string]string, error) {
	headers := make(map[string]string)
	for offset := 0; offset < len(encoded); {
		nameLength := int(encoded[offset])
		offset++
		if nameLength == 0 || offset+nameLength+1 > len(encoded) {
			return nil, errors.New("AWS event stream header is invalid")
		}
		name := string(encoded[offset : offset+nameLength])
		offset += nameLength
		if _, duplicate := headers[name]; duplicate {
			return nil, errors.New("AWS event stream contains a duplicate header")
		}
		headerType := encoded[offset]
		offset++
		var value string
		switch headerType {
		case 0, 1: // bool true/false
			value = map[bool]string{true: "true", false: "false"}[headerType == 0]
		case 2: // byte
			if offset+1 > len(encoded) {
				return nil, errors.New("AWS event stream header is truncated")
			}
			offset++
		case 3: // int16
			if offset+2 > len(encoded) {
				return nil, errors.New("AWS event stream header is truncated")
			}
			offset += 2
		case 4: // int32
			if offset+4 > len(encoded) {
				return nil, errors.New("AWS event stream header is truncated")
			}
			offset += 4
		case 5, 8: // int64/timestamp
			if offset+8 > len(encoded) {
				return nil, errors.New("AWS event stream header is truncated")
			}
			offset += 8
		case 6, 7: // byte array/string
			if offset+2 > len(encoded) {
				return nil, errors.New("AWS event stream header is truncated")
			}
			length := int(binary.BigEndian.Uint16(encoded[offset : offset+2]))
			offset += 2
			if offset+length > len(encoded) {
				return nil, errors.New("AWS event stream header is truncated")
			}
			if headerType == 7 {
				value = string(encoded[offset : offset+length])
			}
			offset += length
		case 9: // UUID
			if offset+16 > len(encoded) {
				return nil, errors.New("AWS event stream header is truncated")
			}
			offset += 16
		default:
			return nil, errors.New("AWS event stream header type is invalid")
		}
		headers[name] = value
	}
	return headers, nil
}

func validateClaudeStreamEvent(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > MaxEventPayloadBytes {
		return "", errors.New("AWS Claude stream event is outside the supported range")
	}
	var event map[string]any
	if err := strictJSON(raw, &event, false); err != nil {
		return "", errors.New("AWS Claude stream event is invalid")
	}
	eventType, _ := event["type"].(string)
	switch eventType {
	case "message_start":
		message, ok := event["message"].(map[string]any)
		if !ok {
			return "", errors.New("AWS Claude message_start is invalid")
		}
		if err := validateUsageValue(message["usage"], false); err != nil {
			return "", err
		}
	case "message_delta":
		if err := validateUsageValue(event["usage"], true); err != nil {
			return "", err
		}
	case "content_block_delta":
		if delta, ok := event["delta"].(map[string]any); ok {
			for _, field := range []string{"text", "thinking", "partial_json"} {
				if text, exists := delta[field]; exists {
					value, ok := text.(string)
					if !ok || len(value) > MaxEventPayloadBytes {
						return "", errors.New("AWS Claude stream delta is invalid")
					}
				}
			}
		}
	case "error":
		code := "stream_error"
		if detail, ok := event["error"].(map[string]any); ok {
			if typed, ok := detail["type"].(string); ok {
				code = safeProviderCode(typed)
			}
		}
		return "", eventStreamProviderError(code)
	case "ping", "content_block_start", "content_block_stop", "message_stop":
	default:
		return "", errors.New("AWS Claude stream event type is unsupported")
	}
	return eventType, nil
}

func validateUsageValue(value any, optional bool) error {
	if value == nil {
		if optional {
			return nil
		}
		return errors.New("AWS Claude stream usage is missing")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("AWS Claude stream usage is invalid")
	}
	var usage protocolkit.ClaudeUsage
	if err := strictJSON(raw, &usage, false); err != nil {
		return errors.New("AWS Claude stream usage is invalid")
	}
	return validateClaudeUsage(&usage)
}

func eventStreamProviderError(code string) error {
	code = safeProviderCode(code)
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"message": "AWS Bedrock stream failed", "type": "aws_bedrock_error", "code": code,
	}})
	return &relaycommon.UpstreamError{StatusCode: streamErrorStatus(code), Body: string(body)}
}

func streamErrorStatus(code string) int {
	lower := strings.ToLower(code)
	switch {
	case strings.Contains(lower, "throttl"), strings.Contains(lower, "serviceunavailable"):
		return http.StatusTooManyRequests
	case strings.Contains(lower, "validation"):
		return http.StatusBadRequest
	case strings.Contains(lower, "accessdenied"), strings.Contains(lower, "unauthor"):
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}
