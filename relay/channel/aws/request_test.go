package aws

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func openAIMetaFromJSON(t *testing.T, raw string, mappedModel string) *relaycommon.Meta {
	t.Helper()
	var request protocolkit.GeneralOpenAIRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &request))
	var extra map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &extra))
	request.Extra = extra
	meta := testMeta(request.Model, constant.RelayFormatOpenAI, request.Stream)
	meta.Request = &request
	meta.RawBody = []byte(raw)
	meta.OriginalModelName = request.Model
	if mappedModel != "" {
		meta.ModelName = mappedModel
	} else {
		meta.ModelName = request.Model
	}
	return meta
}

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal(body, &value))
	return value
}

func TestConvertOpenAIClaudeWireAndMappedModelProtection(t *testing.T) {
	raw := `{
		"model":"client-alias","messages":[
			{"role":"system","content":"system rules"},
			{"role":"user","content":"hello"}
		],
		"stream":false,"max_tokens":12,"max_completion_tokens":77,
		"temperature":0.25,"top_p":0.75,"top_k":10,"stop":["END"],
		"thinking":{"type":"enabled","budget_tokens":20},
		"context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]},
		"output_config":{"effort":"high"},
		"anthropic_beta":["body-beta"]
	}`
	meta := openAIMetaFromJSON(t, raw, "claude-opus-4-20250514")
	meta.ClientHeaders = http.Header{"Anthropic-Beta": []string{"header-beta-one, header-beta-two"}}
	adaptor := initializedAdaptor(meta)

	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"anthropic_version":"bedrock-2023-05-31",
		"anthropic_beta":["header-beta-one","header-beta-two"],
		"system":"system rules",
		"messages":[{"role":"user","content":"hello"}],
		"max_tokens":77,"temperature":0.25,"top_p":0.75,"top_k":10,
		"stop_sequences":["END"],
		"context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]},
		"thinking":{"type":"enabled","budget_tokens":20},
		"output_config":{"effort":"high"}
	}`, string(body))
	object := decodeObject(t, body)
	assert.NotContains(t, object, "model")
	assert.NotContains(t, object, "stream")
	assert.NotContains(t, string(body), "client-alias")
	assert.NotContains(t, string(body), "claude-opus-4-20250514")

	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Contains(t, requestURL, "us.anthropic.claude-opus-4-20250514-v1:0")
	assert.NotContains(t, requestURL, "client-alias")

	attacker := openAIMetaFromJSON(t, strings.Replace(raw, "client-alias", "different-model", 1), "claude-opus-4-20250514")
	attacker.OriginalModelName = "client-alias"
	attacker.Request.Model = "client-alias"
	_, err = initializedAdaptor(attacker).ConvertRequest(attacker)
	assert.ErrorContains(t, err, "model does not match")
}

func TestConvertOpenAIClaudeDefaultsAndTools(t *testing.T) {
	raw := `{
		"model":"claude-3-haiku-20240307",
		"messages":[
			{"role":"user","content":"weather"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"forecast","arguments":"{\"city\":\"Paris\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"sunny"}
		],
		"tools":[{"type":"function","function":{"name":"forecast","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],
		"tool_choice":"auto"
	}`
	meta := openAIMetaFromJSON(t, raw, "")
	body, err := initializedAdaptor(meta).ConvertRequest(meta)
	require.NoError(t, err)
	object := decodeObject(t, body)
	assert.EqualValues(t, 8192, object["max_tokens"])
	assert.Equal(t, AnthropicVersion, object["anthropic_version"])
	tools, ok := object["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	messages, ok := object["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 3)
	assert.Contains(t, string(body), `"type":"tool_use"`)
	assert.Contains(t, string(body), `"type":"tool_result"`)
}

func TestConvertOpenAIClaudeAppliesThinkingPolicyBeforeBedrockWire(t *testing.T) {
	raw := `{
		"model":"claude-3-haiku-20240307-thinking",
		"messages":[{"role":"user","content":"hello"}],
		"top_p":0.7
	}`
	meta := openAIMetaFromJSON(t, raw, "")
	adaptor := initializedAdaptor(meta)

	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	object := decodeObject(t, body)

	assert.EqualValues(t, 8192, object["max_tokens"])
	assert.EqualValues(t, 1, object["temperature"])
	assert.NotContains(t, object, "top_p")
	assert.Equal(t, map[string]any{
		"type":          "enabled",
		"budget_tokens": float64(6553),
	}, object["thinking"])

	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Contains(t, requestURL, "anthropic.claude-3-haiku-20240307-v1:0")
	assert.NotContains(t, requestURL, "-thinking")
}

func TestConvertNativeClaudeStrictWire(t *testing.T) {
	meta := testMeta(testClaudeModel, constant.RelayFormatClaude, false)
	meta.RawBody = []byte(`{
		"model":"claude-3-5-sonnet-20240620","anthropic_version":"attacker-version",
		"messages":[{"role":"user","content":"hello"}],"max_tokens":128,
		"metadata":{"user_id":"not-forwarded"},"cache_control":{"type":"ephemeral"},
		"top_k":5
	}`)
	adaptor := initializedAdaptor(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	object := decodeObject(t, body)
	assert.Equal(t, AnthropicVersion, object["anthropic_version"])
	assert.NotContains(t, object, "model")
	assert.NotContains(t, object, "metadata")
	assert.NotContains(t, object, "cache_control")
	assert.EqualValues(t, 5, object["top_k"])

	invalidBodies := []string{
		`{"model":"claude-3-5-sonnet-20240620","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"unknown":true}`,
		`{"model":"claude-3-5-sonnet-20240620","model":"other","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`,
		`{"model":"claude-3-5-sonnet-20240620","messages":[{"role":"user","content":"hello"}],"max_tokens":1} {}`,
		`{"model":"other","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`,
		`{"model":"claude-3-5-sonnet-20240620","stream":true,"messages":[{"role":"user","content":"hello"}],"max_tokens":1}`,
		`{"model":"claude-3-5-sonnet-20240620","prompt":"legacy","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`,
	}
	for _, raw := range invalidBodies {
		meta.RawBody = []byte(raw)
		_, err := adaptor.ConvertRequest(meta)
		assert.Error(t, err, raw)
	}
}

func TestConvertClaudePassThroughPreservesFutureFieldsButProtectsEnvelope(t *testing.T) {
	meta := testMeta(testClaudeModel, constant.RelayFormatClaude, true)
	meta.Channel.Setting = `{"pass_through_body_enabled":true}`
	meta.RawBody = []byte(`{
		"model":"claude-3-5-sonnet-20240620","stream":true,
		"anthropic_version":"untrusted","messages":[{"role":"user","content":"hello"}],"max_tokens":64,
		"future_field":{"nested":true}
	}`)
	body, err := initializedAdaptor(meta).ConvertRequest(meta)
	require.NoError(t, err)
	object := decodeObject(t, body)
	assert.NotContains(t, object, "model")
	assert.NotContains(t, object, "stream")
	assert.Equal(t, AnthropicVersion, object["anthropic_version"])
	assert.Equal(t, map[string]any{"nested": true}, object["future_field"])

	meta.RawBody = []byte(`{"model":"other","messages":[],"max_tokens":1}`)
	_, err = initializedAdaptor(meta).ConvertRequest(meta)
	assert.ErrorContains(t, err, "model does not match")

	meta.RawBody = []byte(`{"model":"claude-3-5-sonnet-20240620","x":1,"x":2}`)
	_, err = initializedAdaptor(meta).ConvertRequest(meta)
	assert.Error(t, err)

	meta.Channel.Setting = `{"pass_through_body_enabled":true,"pass_through_body_enabled":false}`
	_, err = initializedAdaptor(meta).ConvertRequest(meta)
	assert.Error(t, err)
}

func TestClaudeURLMediaIsFetchedWithoutProviderCredentials(t *testing.T) {
	raw := `{
		"model":"claude-3-5-sonnet-20240620","messages":[{"role":"user","content":[
			{"type":"text","text":"inspect"},
			{"type":"image_url","image_url":{"url":"https://media.example.test/image.png"}}
		]}],"max_tokens":32
	}`
	meta := openAIMetaFromJSON(t, raw, "")
	requests := 0
	adaptor := initializedAdaptor(meta)
	adaptor.MediaHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "https://media.example.test/image.png", request.URL.String())
		assert.Empty(t, request.Header.Get("Authorization"))
		assert.Empty(t, request.Header.Get("x-api-key"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png; charset=binary"}},
			Body:       io.NopCloser(bytes.NewReader([]byte("png-bytes"))),
			Request:    request,
		}, nil
	})}
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.Equal(t, 1, requests)
	assert.Contains(t, string(body), `"type":"base64"`)
	assert.Contains(t, string(body), `"media_type":"image/png"`)
	assert.Contains(t, string(body), base64.StdEncoding.EncodeToString([]byte("png-bytes")))
	assert.NotContains(t, string(body), "media.example.test")

	for _, rawURL := range []string{"http://media.example.test/a.png", "https://user@media.example.test/a.png", "https://media.example.test/a.png#fragment"} {
		bad := strings.Replace(raw, "https://media.example.test/image.png", rawURL, 1)
		badMeta := openAIMetaFromJSON(t, bad, "")
		badAdaptor := initializedAdaptor(badMeta)
		badAdaptor.MediaHTTPClient = adaptor.MediaHTTPClient
		_, err := badAdaptor.ConvertRequest(badMeta)
		assert.Error(t, err, rawURL)
	}
}

func TestClaudeInlineMediaValidation(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("image"))
	raw := `{"model":"claude-3-5-sonnet-20240620","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + encoded + `"}}]}],"max_tokens":8}`
	meta := openAIMetaFromJSON(t, raw, "")
	body, err := initializedAdaptor(meta).ConvertRequest(meta)
	require.NoError(t, err)
	assert.Contains(t, string(body), encoded)

	for _, invalid := range []string{
		`data:text/html;base64,` + encoded,
		`data:image/png;base64,%%%`,
		"data:image/png;base64,aW1h\\nZ2U=",
		`data:image/png,not-base64`,
	} {
		bad := strings.Replace(raw, `data:image/png;base64,`+encoded, invalid, 1)
		badMeta := openAIMetaFromJSON(t, bad, "")
		_, err := initializedAdaptor(badMeta).ConvertRequest(badMeta)
		assert.Error(t, err, invalid)
	}

	wrongKind := strings.Replace(raw, `data:image/png;base64,`+encoded, `data:application/pdf;base64,`+encoded, 1)
	wrongKindMeta := openAIMetaFromJSON(t, wrongKind, "")
	_, err = initializedAdaptor(wrongKindMeta).ConvertRequest(wrongKindMeta)
	assert.ErrorContains(t, err, "non-image")
}

func TestConvertNovaExactWireAndValidation(t *testing.T) {
	raw := `{
		"model":"nova-pro-v1:0","messages":[
			{"role":"system","content":"rules"},
			{"role":"user","content":[{"type":"text","text":"one"},{"type":"text","text":" two"}]}
		],
		"max_tokens":12,"max_completion_tokens":42,"temperature":0.2,"top_p":0.8,"top_k":7,"stop":"END"
	}`
	meta := openAIMetaFromJSON(t, raw, "amazon.nova-lite-v1:0")
	body, err := initializedAdaptor(meta).ConvertRequest(meta)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"schemaVersion":"messages-v1",
		"messages":[
			{"role":"system","content":[{"text":"rules"}]},
			{"role":"user","content":[{"text":"one two"}]}
		],
		"inferenceConfig":{"maxTokens":42,"temperature":0.2,"topP":0.8,"topK":7,"stopSequences":["END"]}
	}`, string(body))
	assert.NotContains(t, string(body), "nova-pro")
	requestURL, err := initializedAdaptor(meta).GetRequestURL(meta)
	require.NoError(t, err)
	assert.Contains(t, requestURL, "us.amazon.nova-lite-v1:0")

	meta.IsStream = true
	meta.Request.Stream = true
	_, err = initializedAdaptor(meta).ConvertRequest(meta)
	assert.ErrorContains(t, err, "streaming")
}

func TestRequestValidationBoundaries(t *testing.T) {
	base := openAIMetaFromJSON(t, `{"model":"claude-3-5-sonnet-20240620","messages":[{"role":"user","content":"ok"}],"max_tokens":1}`, "")
	tests := []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{"no messages", func(meta *relaycommon.Meta) { meta.Request.Messages = nil }},
		{"invalid role", func(meta *relaycommon.Meta) { meta.Request.Messages[0].Role = "developer" }},
		{"negative max", func(meta *relaycommon.Meta) { value := -1; meta.Request.MaxTokens = &value }},
		{"too many max", func(meta *relaycommon.Meta) { value := MaxOutputTokens + 1; meta.Request.MaxTokens = &value }},
		{"nan temperature", func(meta *relaycommon.Meta) { value := math.NaN(); meta.Request.Temperature = &value }},
		{"infinite top p", func(meta *relaycommon.Meta) { value := math.Inf(1); meta.Request.TopP = &value }},
		{"top k", func(meta *relaycommon.Meta) { meta.Request.Extra["top_k"] = 501 }},
		{"long stop", func(meta *relaycommon.Meta) { meta.Request.Stop = strings.Repeat("x", MaxStopSequenceBytes+1) }},
		{"too many tools", func(meta *relaycommon.Meta) {
			meta.Request.Tools = make([]protocolkit.ToolCallRequest, MaxTools+1)
			for index := range meta.Request.Tools {
				meta.Request.Tools[index].Function = &protocolkit.FunctionRequest{Name: "tool"}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyMeta := *base
			copyRequest := *base.Request
			copyRequest.Messages = append([]protocolkit.Message(nil), base.Request.Messages...)
			copyRequest.Extra = map[string]any{}
			copyMeta.Request = &copyRequest
			copyMeta.RawBody = nil // validate the typed boundary mutation itself.
			test.mutate(&copyMeta)
			_, err := initializedAdaptor(&copyMeta).ConvertRequest(&copyMeta)
			assert.Error(t, err)
		})
	}

	valid := *base
	validRequest := *base.Request
	valid.Request = &validRequest
	valid.RawBody = nil
	zero, one := 0.0, 1.0
	valid.Request.Temperature = &zero
	valid.Request.TopP = &one
	valid.Request.Extra = map[string]any{"top_k": 500}
	valid.Request.Stop = strings.Repeat("x", MaxStopSequenceBytes)
	_, err := initializedAdaptor(&valid).ConvertRequest(&valid)
	assert.NoError(t, err)
}

func TestSettingsAndFormatValidation(t *testing.T) {
	meta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, false)
	tests := []struct {
		name   string
		mutate func()
	}{
		{"unsupported mode", func() { meta.Mode = constant.RelayModeEmbeddings }},
		{"unsupported format", func() { meta.Mode = constant.RelayModeChatCompletions; meta.Format = constant.RelayFormatGemini }},
		{"duplicate key setting", func() {
			meta.Format = constant.RelayFormatOpenAI
			meta.Channel.OtherSettings = `{"aws_key_type":"ak_sk","aws_key_type":"api_key"}`
		}},
		{"invalid key setting", func() { meta.Channel.OtherSettings = `{"aws_key_type":"invalid"}` }},
		{"mismatched key shape", func() { meta.Channel.OtherSettings = `{"aws_key_type":"api_key"}` }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.mutate()
			_, err := initializedAdaptor(meta).ConvertRequest(meta)
			assert.Error(t, err)
		})
	}
}

func TestMediaTransportErrorsAreSanitized(t *testing.T) {
	meta := openAIMetaFromJSON(t, `{"model":"claude-3-5-sonnet-20240620","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://media.example.test/image.png"}}]}],"max_tokens":8}`, "")
	adaptor := initializedAdaptor(meta)
	adaptor.MediaHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial https://media.example.test/private?token=secret failed")
	})}
	_, err := adaptor.ConvertRequest(meta)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "token=secret")
}
