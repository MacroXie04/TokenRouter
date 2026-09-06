package aws

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func testGinContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return context, recorder
}

func stringEventHeader(name, value string) []byte {
	encoded := make([]byte, 0, 1+len(name)+3+len(value))
	encoded = append(encoded, byte(len(name)))
	encoded = append(encoded, name...)
	encoded = append(encoded, byte(7)) // string
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(value)))
	encoded = append(encoded, length...)
	encoded = append(encoded, value...)
	return encoded
}

func eventStreamFrame(messageType, eventType string, payload []byte) []byte {
	headers := append(stringEventHeader(":message-type", messageType), stringEventHeader(":event-type", eventType)...)
	totalLength := 12 + len(headers) + len(payload) + 4
	frame := make([]byte, totalLength)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLength))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], headers)
	copy(frame[12+len(headers):], payload)
	binary.BigEndian.PutUint32(frame[len(frame)-4:], crc32.ChecksumIEEE(frame[:len(frame)-4]))
	return frame
}

func claudeEventFrame(t *testing.T, raw string) []byte {
	t.Helper()
	wrapper, err := json.Marshal(struct {
		Bytes []byte `json:"bytes"`
	}{Bytes: []byte(raw)})
	require.NoError(t, err)
	return eventStreamFrame("event", "chunk", wrapper)
}

func validClaudeEvents(t *testing.T) []byte {
	t.Helper()
	events := []string{
		`{"type":"message_start","message":{"id":"msg_aws","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`,
		`{"type":"message_stop"}`,
	}
	var stream []byte
	for _, event := range events {
		stream = append(stream, claudeEventFrame(t, event)...)
	}
	return stream
}

func TestEventStreamFrameDecodingAndChecksums(t *testing.T) {
	frame := claudeEventFrame(t, `{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}`)
	event, err := readEventStreamEvent(bytes.NewReader(frame))
	require.NoError(t, err)
	require.NotNil(t, event)
	assert.Equal(t, "message_start", event.eventType)
	assert.JSONEq(t, `{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}`, string(event.payload))

	badPrelude := append([]byte(nil), frame...)
	badPrelude[8] ^= 0xff
	_, err = readEventStreamEvent(bytes.NewReader(badPrelude))
	assert.ErrorContains(t, err, "prelude checksum")

	badMessage := append([]byte(nil), frame...)
	badMessage[len(badMessage)-1] ^= 0xff
	_, err = readEventStreamEvent(bytes.NewReader(badMessage))
	assert.ErrorContains(t, err, "message checksum")

	partial := frame[:len(frame)-1]
	_, err = readEventStreamEvent(bytes.NewReader(partial))
	assert.ErrorContains(t, err, "partial frame")

	invalidLength := append([]byte(nil), frame...)
	binary.BigEndian.PutUint32(invalidLength[0:4], uint32(MaxEventFrameBytes+1))
	binary.BigEndian.PutUint32(invalidLength[8:12], crc32.ChecksumIEEE(invalidLength[:8]))
	_, err = readEventStreamEvent(bytes.NewReader(invalidLength))
	assert.ErrorContains(t, err, "frame length")
}

func TestEventStreamRejectsInvalidWrappersSequencesAndTruncation(t *testing.T) {
	wrapperCases := [][]byte{
		[]byte(`{"bytes":"%%%"}`),
		[]byte(`{"bytes":"e30=","extra":true}`),
		[]byte(`{"bytes":"e30=","bytes":"e30="}`),
	}
	for _, wrapper := range wrapperCases {
		_, err := readEventStreamEvent(bytes.NewReader(eventStreamFrame("event", "chunk", wrapper)))
		assert.Error(t, err, string(wrapper))
	}

	duplicateInner := base64.StdEncoding.EncodeToString([]byte(`{"type":"ping","type":"message_stop"}`))
	_, err := readEventStreamEvent(bytes.NewReader(eventStreamFrame("event", "chunk", []byte(`{"bytes":"`+duplicateInner+`"}`))))
	assert.Error(t, err)

	outOfSequence := newEventStreamSSEReader(bytes.NewReader(claudeEventFrame(t,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`,
	)))
	_, err = io.ReadAll(outOfSequence)
	assert.ErrorContains(t, err, "out of sequence")

	missingStop := newEventStreamSSEReader(bytes.NewReader(claudeEventFrame(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}`,
	)))
	_, err = io.ReadAll(missingStop)
	assert.ErrorContains(t, err, "before message_stop")

	empty := newEventStreamSSEReader(bytes.NewReader(nil))
	_, err = io.ReadAll(empty)
	assert.ErrorContains(t, err, "before message_stop")

	afterStop := append(claudeEventFrame(t, `{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}`),
		claudeEventFrame(t, `{"type":"message_stop"}`)...)
	afterStop = append(afterStop, claudeEventFrame(t, `{"type":"ping"}`)...)
	_, err = io.ReadAll(newEventStreamSSEReader(bytes.NewReader(afterStop)))
	assert.ErrorContains(t, err, "after message_stop")

	overEventLimit := &eventStreamSSEReader{reader: bytes.NewReader(claudeEventFrame(t, `{"type":"ping"}`)), events: MaxStreamEvents}
	_, err = io.ReadAll(overEventLimit)
	assert.ErrorContains(t, err, "exceeds")
}

func TestEventStreamProviderExceptionsAreTypedAndSanitized(t *testing.T) {
	frame := eventStreamFrame("exception", "ThrottlingException", []byte(`{"message":"secret"}`))
	_, err := readEventStreamEvent(bytes.NewReader(frame))
	require.Error(t, err)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusTooManyRequests, upstream.StatusCode)
	assert.NotContains(t, upstream.Body, "secret")

	assert.Equal(t, http.StatusBadRequest, streamErrorStatus("ValidationException"))
	assert.Equal(t, http.StatusForbidden, streamErrorStatus("AccessDeniedException"))
	assert.Equal(t, http.StatusBadGateway, streamErrorStatus("ModelStreamErrorException"))
}

func TestClaudeNonStreamResponsesOpenAIAndNative(t *testing.T) {
	providerBody := []byte(`{
		"id":"msg_aws","type":"message","role":"assistant",
		"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","stop_sequence":null,
		"model":"anthropic.claude-test","usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}
	}`)

	openAIMeta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, false)
	adaptor := initializedAdaptor(openAIMeta)
	context, recorder := testGinContext()
	usage, err := adaptor.DoResponse(context, &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerBody)),
	}, openAIMeta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 15, usage.PromptTokens)
	assert.Equal(t, 4, usage.CompletionTokens)
	assert.Equal(t, 19, usage.TotalTokens)
	var response protocolkit.ChatCompletionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Choices, 1)
	assert.Equal(t, "hello", response.Choices[0].Message.Content)
	assert.Equal(t, "stop", response.Choices[0].FinishReason)

	nativeMeta := testMeta(testClaudeModel, constant.RelayFormatClaude, false)
	nativeAdaptor := initializedAdaptor(nativeMeta)
	nativeContext, nativeRecorder := testGinContext()
	nativeUsage, err := nativeAdaptor.DoResponse(nativeContext, &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerBody)),
	}, nativeMeta)
	require.NoError(t, err)
	assert.Equal(t, usage, nativeUsage)
	assert.JSONEq(t, string(providerBody), nativeRecorder.Body.String())
}

func TestClaudeResponseValidationAndBounds(t *testing.T) {
	meta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, false)
	adaptor := initializedAdaptor(meta)
	cases := []string{
		`{}`,
		`{"id":"msg","type":"wrong","role":"assistant","model":"model","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":0}}`,
		`{"id":"msg","type":"message","role":"user","model":"model","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":0}}`,
		`{"id":"msg","model":"model","content":[],"stop_reason":"end_turn"}`,
		`{"id":"msg","model":"model","content":[],"stop_reason":"end_turn","usage":{"input_tokens":-1,"output_tokens":0}}`,
		`{"id":"msg","model":"model","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":0},"id":"other"}`,
		`{"id":"msg","model":"model","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":0}} trailing`,
	}
	for _, body := range cases {
		context, _ := testGinContext()
		_, err := adaptor.DoResponse(context, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, meta)
		assert.Error(t, err, body)
	}

	context, _ := testGinContext()
	_, err := adaptor.DoResponse(context, &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(io.LimitReader(strings.NewReader(strings.Repeat("x", int(MaxResponseBodyBytes)+1)), MaxResponseBodyBytes+1)),
	}, meta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
}

func TestAWSHTTPErrorIsBoundedStructuredAndSecretFreeAfterSanitization(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(strings.NewReader(`{"message":"credential secret-key rejected","__type":"com.amazon#AccessDeniedException"}`)),
	}
	err := awsHTTPError(response)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusForbidden, upstream.StatusCode)
	assert.Contains(t, upstream.Body, "AccessDeniedException")
	sanitized := relaycommon.SanitizeUpstreamError(err, "access-key|secret-key|us-east-1")
	assert.NotContains(t, sanitized.(*relaycommon.UpstreamError).Body, "secret-key")

	duplicate := &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"message":"one","message":"two"}`))}
	err = awsHTTPError(duplicate)
	require.ErrorAs(t, err, &upstream)
	assert.Contains(t, upstream.Body, "AWS Bedrock request failed")
	assert.NotContains(t, upstream.Body, "one")
	assert.NotContains(t, upstream.Body, "two")

	oversized := &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(strings.Repeat("secret", int(MaxErrorBodyBytes))))}
	err = awsHTTPError(oversized)
	require.ErrorAs(t, err, &upstream)
	assert.Empty(t, upstream.Body)
	assert.Error(t, upstream.Cause)
	assert.NotContains(t, err.Error(), "secret")
}

func TestClaudeSuccessStatusErrorEnvelopeIsTypedAndSanitized(t *testing.T) {
	meta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, false)
	context, _ := testGinContext()
	_, err := initializedAdaptor(meta).DoResponse(context, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"type":"error","error":{"type":"model_error","message":"secret-key rejected"}}`,
		)),
	}, meta)
	require.Error(t, err)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusBadGateway, upstream.StatusCode)
	assert.Contains(t, upstream.Body, "model_error")
	sanitized := relaycommon.SanitizeUpstreamError(err, meta.APIKey)
	assert.NotContains(t, sanitized.Error(), "secret-key")

	context, _ = testGinContext()
	_, err = initializedAdaptor(meta).DoResponse(context, &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)),
	}, nil)
	assert.ErrorContains(t, err, "metadata")
}

func TestNovaResponseWireUsageAndFailures(t *testing.T) {
	meta := testMeta(testNovaModel, constant.RelayFormatOpenAI, false)
	adaptor := initializedAdaptor(meta)
	adaptor.Now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	providerBody := `{
		"output":{"message":{"role":"assistant","content":[{"text":"nova answer"}]}},
		"stopReason":"max_tokens","usage":{"inputTokens":11,"outputTokens":7,"totalTokens":18}
	}`
	context, recorder := testGinContext()
	usage, err := adaptor.DoResponse(context, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Amzn-Requestid": []string{"request-123"}},
		Body:       io.NopCloser(strings.NewReader(providerBody)),
	}, meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}, usage)
	var output protocolkit.ChatCompletionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	assert.Equal(t, "chatcmpl-request-123", output.Id)
	assert.Equal(t, int64(1_700_000_000), output.Created)
	assert.Equal(t, testNovaModel, output.Model)
	assert.Equal(t, "length", output.Choices[0].FinishReason)
	assert.Equal(t, "nova answer", output.Choices[0].Message.Content)

	invalid := []string{
		`{}`,
		`{"output":{"message":{"role":"user","content":[{"text":"bad"}]}},"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`,
		`{"output":{"message":{"role":"assistant","content":[]}},"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`,
		`{"output":{"message":{"role":"assistant","content":[{"text":"bad"}]}},"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":3}}`,
		`{"output":{"message":{"role":"assistant","content":[{"text":"bad"}]}},"usage":{"inputTokens":-1,"outputTokens":1,"totalTokens":0}}`,
		`{"output":{"message":{"role":"assistant","content":[{"text":"bad"}]}},"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2},"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`,
	}
	for _, body := range invalid {
		context, _ := testGinContext()
		_, err := adaptor.DoResponse(context, &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, meta)
		assert.Error(t, err, body)
	}
}

func TestClaudeBinaryEventStreamOpenAIAndNative(t *testing.T) {
	stream := validClaudeEvents(t)
	openAIMeta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, true)
	openAIAdaptor := initializedAdaptor(openAIMeta)
	context, recorder := testGinContext()
	usage, err := openAIAdaptor.DoResponse(context, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
		Body:       io.NopCloser(bytes.NewReader(stream)),
	}, openAIMeta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 15, usage.PromptTokens)
	assert.Equal(t, 4, usage.CompletionTokens)
	assert.Equal(t, 19, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), `"content":"hello"`)
	assert.Contains(t, recorder.Body.String(), "data: [DONE]")
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))

	nativeMeta := testMeta(testClaudeModel, constant.RelayFormatClaude, true)
	nativeAdaptor := initializedAdaptor(nativeMeta)
	nativeContext, nativeRecorder := testGinContext()
	nativeUsage, err := nativeAdaptor.DoResponse(nativeContext, &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(stream)),
	}, nativeMeta)
	require.NoError(t, err)
	assert.Equal(t, usage, nativeUsage)
	assert.Contains(t, nativeRecorder.Body.String(), "event: message_start")
	assert.Contains(t, nativeRecorder.Body.String(), `"text":"hello"`)
	assert.NotContains(t, nativeRecorder.Body.String(), "[DONE]")
}

func TestClaudeBinaryStreamFailureRetainsPartialUsage(t *testing.T) {
	stream := claudeEventFrame(t, `{"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":1}}}`)
	bad := claudeEventFrame(t, `{"type":"message_stop"}`)
	bad[len(bad)-1] ^= 0xff
	stream = append(stream, bad...)

	meta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, true)
	context, _ := testGinContext()
	usage, err := initializedAdaptor(meta).DoResponse(context, &http.Response{
		StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(stream)),
	}, meta)
	require.Error(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 9, usage.PromptTokens)
	assert.Equal(t, 1, usage.CompletionTokens)
	assert.Contains(t, err.Error(), "checksum")
}

func TestClaudeBinaryStreamTotalSizeIsBounded(t *testing.T) {
	stream := validClaudeEvents(t)
	meta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, true)
	context, _ := testGinContext()
	usage, err := initializedAdaptor(meta).claudeStreamResponseWithLimit(context, &http.Response{
		StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(stream)),
	}, meta, int64(len(stream)-1))
	require.Error(t, err)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
	assert.NotNil(t, usage)

	context, _ = testGinContext()
	_, err = initializedAdaptor(meta).claudeStreamResponseWithLimit(context, &http.Response{
		StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(stream)),
	}, meta, 0)
	assert.ErrorContains(t, err, "limit")
}

func TestDoResponseRejectsNilAndRoutesHTTPErrorBeforeCodec(t *testing.T) {
	meta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, false)
	context, _ := testGinContext()
	_, err := initializedAdaptor(meta).DoResponse(context, nil, meta)
	assert.Error(t, err)

	context, _ = testGinContext()
	_, err = initializedAdaptor(meta).DoResponse(context, &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(strings.NewReader(`{"message":"unavailable","code":"ServiceUnavailableException"}`)),
	}, meta)
	require.Error(t, err)
	var upstream *relaycommon.UpstreamError
	assert.True(t, errors.As(err, &upstream))
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
}
