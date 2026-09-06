package minimax

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type speechRequest struct {
	Model             string               `json:"model"`
	Text              string               `json:"text"`
	Stream            bool                 `json:"stream,omitempty"`
	StreamOptions     *speechStreamOptions `json:"stream_options,omitempty"`
	VoiceSetting      speechVoiceSetting   `json:"voice_setting"`
	PronunciationDict *pronunciationDict   `json:"pronunciation_dict,omitempty"`
	AudioSetting      *speechAudioSetting  `json:"audio_setting,omitempty"`
	TimbreWeights     []timbreWeight       `json:"timbre_weights,omitempty"`
	LanguageBoost     string               `json:"language_boost,omitempty"`
	VoiceModify       *voiceModify         `json:"voice_modify,omitempty"`
	SubtitleEnable    bool                 `json:"subtitle_enable,omitempty"`
	OutputFormat      string               `json:"output_format,omitempty"`
	AIGCWatermark     bool                 `json:"aigc_watermark,omitempty"`
}

type speechStreamOptions struct {
	ExcludeAggregatedAudio bool `json:"exclude_aggregated_audio,omitempty"`
}

type speechVoiceSetting struct {
	VoiceID           string  `json:"voice_id"`
	Speed             float64 `json:"speed,omitempty"`
	Volume            float64 `json:"vol,omitempty"`
	Pitch             int     `json:"pitch,omitempty"`
	Emotion           string  `json:"emotion,omitempty"`
	TextNormalization bool    `json:"text_normalization,omitempty"`
	LatexRead         bool    `json:"latex_read,omitempty"`
}

type pronunciationDict struct {
	Tone []string `json:"tone,omitempty"`
}

type speechAudioSetting struct {
	SampleRate int    `json:"sample_rate,omitempty"`
	Bitrate    int    `json:"bitrate,omitempty"`
	Format     string `json:"format,omitempty"`
	Channel    int    `json:"channel,omitempty"`
	ForceCBR   bool   `json:"force_cbr,omitempty"`
}

type timbreWeight struct {
	VoiceID string `json:"voice_id"`
	Weight  int    `json:"weight"`
}

type voiceModify struct {
	Pitch        int    `json:"pitch,omitempty"`
	Intensity    int    `json:"intensity,omitempty"`
	Timbre       int    `json:"timbre,omitempty"`
	SoundEffects string `json:"sound_effects,omitempty"`
}

type speechResponse struct {
	Data struct {
		Audio  string `json:"audio"`
		Status int    `json:"status"`
	} `json:"data"`
	ExtraInfo struct {
		UsageCharacters int64 `json:"usage_characters"`
	} `json:"extra_info"`
	TraceID  string              `json:"trace_id"`
	BaseResp miniMaxBaseResponse `json:"base_resp"`
}

func (a *Adaptor) convertSpeechRequest(meta *relaycommon.Meta) ([]byte, error) {
	input, ok := stringExtra(meta.Request.Extra, "input")
	if !ok || strings.TrimSpace(input) == "" {
		return nil, errors.New("MiniMax speech input is required")
	}
	voiceID, ok := stringExtra(meta.Request.Extra, "voice")
	if !ok || strings.TrimSpace(voiceID) == "" || len(voiceID) > maxMiniMaxVoiceIDBytes {
		return nil, errors.New("MiniMax speech voice is invalid")
	}
	responseFormat, _ := stringExtra(meta.Request.Extra, "response_format")
	if responseFormat == "" && meta.Request.ResponseFormat != nil {
		responseFormat = meta.Request.ResponseFormat.Type
	}
	if err := validateAudioFormat(responseFormat, true); err != nil {
		return nil, err
	}
	speed, speedSet, err := optionalFloatExtra(meta.Request.Extra, "speed")
	if err != nil {
		return nil, err
	}
	if speedSet && (speed < 0.5 || speed > 2) {
		return nil, errors.New("MiniMax speech speed must be between 0.5 and 2")
	}

	payload := speechRequest{
		Model: meta.ModelName,
		Text:  input,
		VoiceSetting: speechVoiceSetting{
			VoiceID: voiceID,
			Speed:   speed,
		},
		AudioSetting: &speechAudioSetting{Format: responseFormat},
		OutputFormat: responseFormat,
	}
	if len(meta.Request.Metadata) > 0 {
		metadata, err := protocolkit.MarshalJSON(meta.Request.Metadata)
		if err != nil {
			return nil, fmt.Errorf("encode MiniMax speech metadata: %w", err)
		}
		if err := protocolkit.UnmarshalJSON(metadata, &payload); err != nil {
			return nil, fmt.Errorf("decode MiniMax speech metadata: %w", err)
		}
	}
	// Routing and economic snapshots are bound to the mapped model and caller
	// input, so metadata may extend provider controls but cannot replace them.
	payload.Model = meta.ModelName
	payload.Text = input
	if payload.VoiceSetting.VoiceID == "" || len(payload.VoiceSetting.VoiceID) > maxMiniMaxVoiceIDBytes {
		return nil, errors.New("MiniMax speech voice_id is invalid")
	}
	if payload.Stream {
		return nil, errors.New("MiniMax speech metadata must not enable streaming")
	}
	if payload.VoiceSetting.Speed != 0 && (payload.VoiceSetting.Speed < 0.5 || payload.VoiceSetting.Speed > 2) {
		return nil, errors.New("MiniMax speech voice speed must be between 0.5 and 2")
	}
	if payload.VoiceSetting.Volume != 0 && (payload.VoiceSetting.Volume < 0.1 || payload.VoiceSetting.Volume > 10) {
		return nil, errors.New("MiniMax speech voice volume must be between 0.1 and 10")
	}
	if payload.VoiceSetting.Pitch < -12 || payload.VoiceSetting.Pitch > 12 {
		return nil, errors.New("MiniMax speech voice pitch must be between -12 and 12")
	}
	if len(payload.TimbreWeights) > maxMiniMaxImageCount {
		return nil, errors.New("MiniMax speech timbre_weights exceeds the supported count")
	}
	if payload.AudioSetting != nil {
		if err := validateAudioFormat(payload.AudioSetting.Format, true); err != nil {
			return nil, err
		}
		a.audioFormat = payload.AudioSetting.Format
	}
	if err := validateSpeechOutputFormat(payload.OutputFormat); err != nil {
		return nil, err
	}
	body, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("encode MiniMax speech request: %w", err)
	}
	return body, nil
}

func (a *Adaptor) speechResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read MiniMax speech response: %w", err)
	}
	var upstream speechResponse
	if err := protocolkit.UnmarshalJSON(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode MiniMax speech response: %w", err)
	}
	if upstream.BaseResp.StatusCode != 0 {
		return nil, miniMaxBusinessError(meta, http.StatusBadRequest, "minimax_tts_error",
			upstream.BaseResp.StatusCode, upstream.BaseResp.StatusMsg)
	}
	if upstream.Data.Status != 0 {
		return nil, miniMaxBusinessError(meta, http.StatusBadGateway, "minimax_tts_error",
			upstream.Data.Status, "MiniMax speech generation did not complete")
	}
	if upstream.ExtraInfo.UsageCharacters < 0 || upstream.ExtraInfo.UsageCharacters > appcommon.MaxQuota {
		return nil, errors.New("MiniMax speech response usage is outside the supported range")
	}
	if upstream.Data.Audio == "" {
		return nil, errors.New("MiniMax speech response contains no audio")
	}
	usage := &protocolkit.Usage{
		PromptTokens: meta.PromptTokens,
		TotalTokens:  int(upstream.ExtraInfo.UsageCharacters),
	}
	if strings.HasPrefix(upstream.Data.Audio, "http://") || strings.HasPrefix(upstream.Data.Audio, "https://") {
		if err := validateMediaURL(upstream.Data.Audio); err != nil {
			return nil, fmt.Errorf("MiniMax speech response URL is invalid: %w", err)
		}
		// Emit the provider redirect directly. gin.Context.Redirect delegates to
		// net/http.Redirect and assumes a non-nil inbound request, while response
		// conversion is also exercised through request-independent adapter tests.
		c.Header("Location", upstream.Data.Audio)
		c.Status(http.StatusFound)
		c.Writer.WriteHeaderNow()
		return usage, nil
	}
	if len(upstream.Data.Audio)%2 != 0 {
		return nil, errors.New("MiniMax speech response contains invalid hex audio")
	}
	decodedLength := int64(hex.DecodedLen(len(upstream.Data.Audio)))
	if decodedLength > relaycommon.MaxUpstreamBinaryBodyBytes {
		return nil, fmt.Errorf("MiniMax decoded speech response exceeds %d bytes", relaycommon.MaxUpstreamBinaryBodyBytes)
	}
	audio, err := hex.DecodeString(upstream.Data.Audio)
	if err != nil {
		return nil, errors.New("MiniMax speech response contains invalid hex audio")
	}
	format := a.audioFormat
	if format == "" {
		format, _ = stringExtra(meta.Request.Extra, "response_format")
	}
	c.Data(http.StatusOK, audioContentType(format), audio)
	return usage, nil
}

func optionalFloatExtra(extra map[string]any, key string) (float64, bool, error) {
	if extra == nil {
		return 0, false, nil
	}
	value, exists := extra[key]
	if !exists || value == nil {
		return 0, false, nil
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	default:
		return 0, false, fmt.Errorf("MiniMax %s must be a number", key)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false, fmt.Errorf("MiniMax %s must be finite", key)
	}
	return number, true, nil
}

func validateAudioFormat(format string, emptyAllowed bool) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" && emptyAllowed {
		return nil
	}
	switch format {
	case "mp3", "wav", "flac", "aac", "pcm", "opus":
		return nil
	default:
		return errors.New("MiniMax speech response_format is unsupported")
	}
}

func validateSpeechOutputFormat(format string) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		return nil
	}
	switch format {
	case "hex", "url", "mp3", "wav", "flac", "aac", "pcm", "opus":
		return nil
	default:
		return errors.New("MiniMax speech output_format is unsupported")
	}
}

func audioContentType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		return "audio/wav"
	case "flac":
		return "audio/flac"
	case "aac":
		return "audio/aac"
	case "pcm":
		return "audio/pcm"
	case "opus":
		return "audio/ogg"
	default:
		return "audio/mpeg"
	}
}
