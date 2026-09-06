package volcengine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxTTSInputBytes               = 1 << 20
	maxTTSVoiceBytes               = 1 << 10
	maxTTSMetadataBytes            = 64 << 10
	maxTTSRequestBytes             = 2 << 20
	maxTTSResponseFrameBytes       = 4 << 20
	maxTTSAudioBytes         int64 = 128 << 20
	maxTTSJSONBytes          int64 = 176 << 20
	maxTTSFrames                   = 65_536
	ttsHandshakeTimeout            = 10 * time.Second
	ttsWriteTimeout                = 10 * time.Second
	ttsResponseTimeout             = 10 * time.Minute
)

var voiceMap = map[string]string{
	"alloy":   "zh_male_M392_conversation_wvae_bigtts",
	"echo":    "zh_male_wenhao_mars_bigtts",
	"fable":   "zh_female_tianmei_mars_bigtts",
	"onyx":    "zh_male_zhibei_mars_bigtts",
	"nova":    "zh_female_shuangkuaisisi_mars_bigtts",
	"shimmer": "zh_female_cancan_mars_bigtts",
}

var encodingMap = map[string]string{
	"mp3": "mp3", "opus": "ogg_opus", "aac": "mp3", "flac": "mp3",
	"wav": "wav", "pcm": "pcm",
}

type ttsRequest struct {
	App struct {
		AppID   string `json:"appid"`
		Token   string `json:"token"`
		Cluster string `json:"cluster"`
	} `json:"app"`
	User struct {
		UID string `json:"uid"`
	} `json:"user"`
	Audio struct {
		VoiceType        string  `json:"voice_type"`
		Encoding         string  `json:"encoding"`
		SpeedRatio       float64 `json:"speed_ratio"`
		Rate             int     `json:"rate"`
		Bitrate          int     `json:"bitrate,omitempty"`
		LoudnessRatio    float64 `json:"loudness_ratio,omitempty"`
		EnableEmotion    bool    `json:"enable_emotion,omitempty"`
		Emotion          string  `json:"emotion,omitempty"`
		EmotionScale     float64 `json:"emotion_scale,omitempty"`
		ExplicitLanguage string  `json:"explicit_language,omitempty"`
		ContextLanguage  string  `json:"context_language,omitempty"`
	} `json:"audio"`
	Request struct {
		RequestID       string  `json:"reqid"`
		Text            string  `json:"text"`
		Operation       string  `json:"operation"`
		Model           string  `json:"model,omitempty"`
		TextType        string  `json:"text_type,omitempty"`
		SilenceDuration float64 `json:"silence_duration,omitempty"`
		WithTimestamp   any     `json:"with_timestamp,omitempty"`
		ExtraParam      any     `json:"extra_param,omitempty"`
	} `json:"request"`
}

type ttsResponse struct {
	RequestID string `json:"reqid"`
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Sequence  int    `json:"sequence"`
	Data      string `json:"data"`
}

// TTSDialContextFunc makes the fixed provider WebSocket transport testable.
type TTSDialContextFunc func(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error)

func parseTTSCredential(raw string) (string, string, error) {
	if len(raw) > maxCredentialBytes || !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw {
		return "", "", errors.New("VolcEngine TTS credential is invalid")
	}
	parts := strings.Split(raw, "|")
	if len(parts) != 2 {
		return "", "", errors.New("VolcEngine TTS credential must use appid|access_token format")
	}
	for _, value := range parts {
		if value == "" || len(value) > maxCredentialBytes/2 || hasUnsafeText(value) || strings.ContainsAny(value, `"\`) {
			return "", "", errors.New("VolcEngine TTS credential is invalid")
		}
	}
	return parts[0], parts[1], nil
}

func buildTTSRequest(meta *relaycommon.Meta) ([]byte, error) {
	appID, token, err := parseTTSCredential(meta.APIKey)
	if err != nil {
		return nil, err
	}
	input, ok := meta.Request.Extra["input"].(string)
	if !ok || strings.TrimSpace(input) == "" || len(input) > maxTTSInputBytes || !utf8.ValidString(input) {
		return nil, errors.New("VolcEngine TTS input is invalid or too large")
	}
	voice, ok := meta.Request.Extra["voice"].(string)
	if !ok || strings.TrimSpace(voice) == "" || len(voice) > maxTTSVoiceBytes || !utf8.ValidString(voice) {
		return nil, errors.New("VolcEngine TTS voice is invalid")
	}
	if mapped, found := voiceMap[voice]; found {
		voice = mapped
	}
	responseFormat, _ := meta.Request.Extra["response_format"].(string)
	encoding := encodingMap[responseFormat]
	if encoding == "" {
		encoding = "mp3"
	}
	speed := 0.0
	if value, present := meta.Request.Extra["speed"]; present {
		parsed, valid := jsonNumber(value)
		if !valid || !finite(parsed) || parsed < -10 || parsed > 10 {
			return nil, errors.New("VolcEngine TTS speed is invalid")
		}
		speed = parsed
	}
	requestID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, errors.New("generate VolcEngine TTS request identifier")
	}
	payload := ttsRequest{}
	payload.App.AppID, payload.App.Token, payload.App.Cluster = appID, token, "volcano_tts"
	payload.User.UID = "openai_relay_user"
	payload.Audio.VoiceType, payload.Audio.Encoding = voice, encoding
	payload.Audio.SpeedRatio, payload.Audio.Rate = speed, 24000
	payload.Request.RequestID, payload.Request.Text = requestID, input
	payload.Request.Operation, payload.Request.Model = "submit", meta.OriginalModelName
	if payload.Request.Model == "" {
		payload.Request.Model = meta.ModelName
	}
	if metadata, present := meta.Request.Extra["metadata"]; present && metadata != nil {
		encoded, marshalErr := protocolkit.MarshalJSON(metadata)
		if marshalErr != nil || len(encoded) > maxTTSMetadataBytes {
			return nil, errors.New("VolcEngine TTS metadata is invalid or too large")
		}
		if err := decodeStrictTTSJSON(encoded, &payload); err != nil {
			return nil, errors.New("VolcEngine TTS metadata is invalid")
		}
	}
	if err := validateTTSRequest(payload, appID, token); err != nil {
		return nil, err
	}
	encoded, err := protocolkit.MarshalJSON(payload)
	if err != nil || len(encoded) > maxTTSRequestBytes {
		return nil, errors.New("VolcEngine TTS request is too large")
	}
	return encoded, nil
}

func validateTTSRequest(payload ttsRequest, appID, token string) error {
	if payload.App.AppID != appID || payload.App.Token != token || payload.App.Cluster == "" ||
		len(payload.App.Cluster) > maxTTSVoiceBytes || payload.User.UID == "" ||
		len(payload.User.UID) > maxTTSVoiceBytes || payload.Audio.VoiceType == "" ||
		len(payload.Audio.VoiceType) > maxTTSVoiceBytes || payload.Audio.Encoding == "" ||
		len(payload.Audio.Encoding) > 32 || payload.Audio.Rate < 1 || payload.Audio.Rate > 384_000 ||
		!finite(payload.Audio.SpeedRatio) || payload.Audio.SpeedRatio < -10 || payload.Audio.SpeedRatio > 10 ||
		payload.Request.RequestID == "" || len(payload.Request.RequestID) > 256 ||
		strings.TrimSpace(payload.Request.Text) == "" || len(payload.Request.Text) > maxTTSInputBytes ||
		(payload.Request.Operation != "submit" && payload.Request.Operation != "query") {
		return errors.New("VolcEngine TTS request metadata is outside supported bounds")
	}
	for _, value := range []string{payload.App.Cluster, payload.User.UID, payload.Audio.VoiceType,
		payload.Audio.Encoding, payload.Request.RequestID, payload.Request.Model, payload.Request.TextType} {
		if !utf8.ValidString(value) {
			return errors.New("VolcEngine TTS request metadata is invalid")
		}
	}
	return nil
}

func jsonNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (a *Adaptor) DoDirectRequest(c *gin.Context, requestURL string, body []byte, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || meta == nil || meta.Context == nil || requestURL != defaultTTSURL ||
		meta.Mode != channelcatalog.RelayModeAudioSpeech || len(body) == 0 || len(body) > maxTTSRequestBytes {
		return nil, errors.New("VolcEngine direct TTS request is invalid")
	}
	expected, err := buildTTSRequest(meta)
	if err != nil {
		return nil, err
	}
	// Request IDs are intentionally random, so compare all durable fields after
	// replacing the newly generated identifier with the dispatched one.
	var actual, rebuilt ttsRequest
	if decodeStrictTTSJSON(body, &actual) != nil || decodeStrictTTSJSON(expected, &rebuilt) != nil {
		return nil, errors.New("VolcEngine direct TTS request is invalid")
	}
	rebuilt.Request.RequestID = actual.Request.RequestID
	rebuiltBody, _ := protocolkit.MarshalJSON(rebuilt)
	if !equalJSON(body, rebuiltBody) {
		return nil, errors.New("VolcEngine direct TTS request does not match relay metadata")
	}
	_, token, err := parseTTSCredential(meta.APIKey)
	if err != nil {
		return nil, err
	}
	dial := a.DialContext
	if dial == nil {
		dial = newTTSDialer().DialContext
	}
	header := http.Header{"Authorization": []string{"Bearer;" + token}}
	connection, response, err := dial(meta.Context, defaultTTSURL, header)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, safeTTSHandshakeError(response)
	}
	if connection == nil {
		return nil, relaycommon.ErrUpstreamTransportFailed
	}
	defer connection.Close()
	connection.SetReadLimit(maxTTSResponseFrameBytes)
	if err := connection.SetWriteDeadline(time.Now().Add(ttsWriteTimeout)); err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	frame, err := marshalTTSClientFrame(body)
	if err != nil {
		return nil, err
	}
	if err := connection.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	if err := connection.SetReadDeadline(ttsDeadline(meta.Context)); err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	encoding := actual.Audio.Encoding
	contentType := ttsContentType(encoding)
	c.Header("Content-Type", contentType)
	c.Header("X-Content-Type-Options", "nosniff")
	var total int64
	terminal := false
	started := false
	for count := 0; count < maxTTSFrames; count++ {
		messageType, raw, readErr := connection.ReadMessage()
		if readErr != nil {
			return ttsUsageAfterPartial(started, meta), relaycommon.SanitizeTransportError(readErr)
		}
		if messageType != websocket.BinaryMessage || len(raw) > maxTTSResponseFrameBytes {
			return ttsUsageAfterPartial(started, meta), invalidTTSResponse()
		}
		message, parseErr := parseTTSServerFrame(raw)
		if parseErr != nil {
			return ttsUsageAfterPartial(started, meta), invalidTTSResponse()
		}
		switch message.kind {
		case ttsFrameError:
			return ttsUsageAfterPartial(started, meta), &relaycommon.UpstreamError{
				StatusCode: http.StatusBadRequest,
				Cause:      fmt.Errorf("VolcEngine TTS rejected request with code %d", message.errorCode),
			}
		case ttsFrameIgnore:
			continue
		case ttsFrameAudio:
			total += int64(len(message.payload))
			if total > maxTTSAudioBytes {
				return ttsUsageAfterPartial(started, meta), invalidTTSResponse()
			}
			if len(message.payload) > 0 {
				if _, err := c.Writer.Write(message.payload); err != nil {
					return ttsUsage(meta), fmt.Errorf("write VolcEngine TTS audio: %w", err)
				}
				c.Writer.Flush()
				started = true
			}
			if message.sequence < 0 {
				terminal = true
			}
		}
		if terminal {
			break
		}
	}
	if !terminal {
		return ttsUsageAfterPartial(started, meta), invalidTTSResponse()
	}
	return ttsUsage(meta), nil
}

func handleTTSHTTPResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if response.StatusCode >= http.StatusBadRequest {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, maxTTSJSONBytes)
	if err != nil {
		return nil, fmt.Errorf("read VolcEngine TTS response: %w", err)
	}
	var result ttsResponse
	if err := decodeStrictTTSJSON(body, &result); err != nil || result.Code != 3000 ||
		len(result.Message) > 4<<10 || len(result.RequestID) > 256 {
		return nil, invalidTTSResponse()
	}
	decodedLength := base64.StdEncoding.DecodedLen(len(result.Data))
	if int64(decodedLength) > maxTTSAudioBytes {
		return nil, invalidTTSResponse()
	}
	audio, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil || int64(len(audio)) > maxTTSAudioBytes {
		return nil, invalidTTSResponse()
	}
	var request ttsRequest
	converted, err := buildTTSRequest(meta)
	if err != nil || decodeStrictTTSJSON(converted, &request) != nil {
		return nil, errors.New("VolcEngine TTS request state is invalid")
	}
	contentType := ttsContentType(request.Audio.Encoding)
	c.Header("Content-Type", contentType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, audio)
	return ttsUsage(meta), nil
}

func ttsUsage(meta *relaycommon.Meta) *protocolkit.Usage {
	prompt := 0
	if meta != nil && meta.PromptTokens > 0 {
		prompt = meta.PromptTokens
	}
	return &protocolkit.Usage{PromptTokens: prompt, TotalTokens: prompt}
}

func ttsUsageAfterPartial(started bool, meta *relaycommon.Meta) *protocolkit.Usage {
	if !started {
		return nil
	}
	return ttsUsage(meta)
}

func ttsContentType(encoding string) string {
	switch encoding {
	case "mp3":
		return "audio/mpeg"
	case "ogg_opus":
		return "audio/ogg"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	default:
		return "application/octet-stream"
	}
}

func decodeStrictTTSJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing VolcEngine TTS JSON")
	}
	return nil
}

func newTTSDialer() *websocket.Dialer {
	return &websocket.Dialer{
		NetDialContext: httpx.SafeDialContext,
		Proxy:          nil, HandshakeTimeout: ttsHandshakeTimeout,
		ReadBufferSize: 64 << 10, WriteBufferSize: 64 << 10,
		EnableCompression: false,
	}
}

func ttsDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(ttsResponseTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func safeTTSHandshakeError(response *http.Response) error {
	if response == nil {
		return relaycommon.ErrUpstreamTransportFailed
	}
	status := response.StatusCode
	if status < 400 || status > 499 {
		status = http.StatusBadGateway
	}
	return &relaycommon.UpstreamError{StatusCode: status, Cause: errors.New("VolcEngine TTS WebSocket handshake failed")}
}

func invalidTTSResponse() error {
	return &relaycommon.UpstreamError{StatusCode: http.StatusBadGateway, Cause: errors.New("invalid VolcEngine TTS response")}
}

type ttsFrameKind uint8

const (
	ttsFrameIgnore ttsFrameKind = iota
	ttsFrameAudio
	ttsFrameError
)

type ttsServerFrame struct {
	kind      ttsFrameKind
	sequence  int32
	errorCode uint32
	payload   []byte
}

func marshalTTSClientFrame(payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > maxTTSRequestBytes {
		return nil, errors.New("VolcEngine TTS request payload is invalid")
	}
	frame := make([]byte, 8+len(payload))
	frame[0], frame[1], frame[2], frame[3] = 0x11, 0x10, 0x10, 0x00
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame, nil
}

func parseTTSServerFrame(raw []byte) (ttsServerFrame, error) {
	if len(raw) < 8 {
		return ttsServerFrame{}, errors.New("VolcEngine TTS frame is too short")
	}
	version, headerWords := raw[0]>>4, raw[0]&0x0f
	headerSize := int(headerWords) * 4
	if version != 1 || headerSize < 4 || headerSize > 64 || len(raw) < headerSize+4 {
		return ttsServerFrame{}, errors.New("VolcEngine TTS frame header is invalid")
	}
	messageType, flags := raw[1]>>4, raw[1]&0x0f
	serialization, compression := raw[2]>>4, raw[2]&0x0f
	if compression != 0 || (serialization != 0 && serialization != 1) {
		return ttsServerFrame{}, errors.New("VolcEngine TTS frame encoding is unsupported")
	}
	offset := headerSize
	frame := ttsServerFrame{}
	switch messageType {
	case 0x0b: // audio-only server response
		frame.kind = ttsFrameAudio
		if flags == 1 || flags == 3 {
			if len(raw) < offset+8 {
				return ttsServerFrame{}, errors.New("VolcEngine TTS audio frame is truncated")
			}
			frame.sequence = int32(binary.BigEndian.Uint32(raw[offset : offset+4]))
			offset += 4
		} else if flags != 0 {
			return ttsServerFrame{}, errors.New("VolcEngine TTS audio frame flags are invalid")
		}
	case 0x0f: // provider error
		frame.kind = ttsFrameError
		if len(raw) < offset+8 {
			return ttsServerFrame{}, errors.New("VolcEngine TTS error frame is truncated")
		}
		frame.errorCode = binary.BigEndian.Uint32(raw[offset : offset+4])
		offset += 4
	case 0x09, 0x0c: // full/front-end informational response
		frame.kind = ttsFrameIgnore
		if flags == 1 || flags == 3 {
			if len(raw) < offset+8 {
				return ttsServerFrame{}, errors.New("VolcEngine TTS response frame is truncated")
			}
			frame.sequence = int32(binary.BigEndian.Uint32(raw[offset : offset+4]))
			offset += 4
		} else if flags != 0 {
			return ttsServerFrame{}, errors.New("VolcEngine TTS response frame flags are invalid")
		}
	default:
		return ttsServerFrame{}, errors.New("VolcEngine TTS frame type is unsupported")
	}
	if len(raw) < offset+4 {
		return ttsServerFrame{}, errors.New("VolcEngine TTS frame payload is truncated")
	}
	payloadSize := int(binary.BigEndian.Uint32(raw[offset : offset+4]))
	offset += 4
	if payloadSize < 0 || payloadSize > maxTTSResponseFrameBytes || payloadSize != len(raw)-offset {
		return ttsServerFrame{}, errors.New("VolcEngine TTS frame payload size is invalid")
	}
	frame.payload = raw[offset:]
	return frame, nil
}
