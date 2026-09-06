package replicate

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
)

var tinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}

func replicateMeta(mode channelcatalog.RelayMode) *relaycommon.Meta {
	one := 1
	request := &protocolkit.GeneralOpenAIRequest{
		Model:  "client-image-model",
		Prompt: "draw a careful fox",
		N:      &one,
		Extra: map[string]any{
			"model": "client-image-model", "prompt": "draw a careful fox", "n": float64(1),
		},
	}
	return &relaycommon.Meta{
		Context: context.Background(), Channel: &model.Channel{Type: int(channelcatalog.ChannelTypeReplicate)},
		Mode: mode, Format: channelcatalog.RelayFormatOpenAIImage, ModelName: DefaultModel,
		APIKey: "replicate-secret", Request: request, RequestContentType: "application/json",
		RawBody: []byte(`{"model":"client-image-model","prompt":"draw a careful fox","n":1}`),
	}
}

func replicateResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestReplicateURLHeadersModesAndCatalog(t *testing.T) {
	meta := replicateMeta(channelcatalog.RelayModeImagesGenerations)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	got, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://api.replicate.com/v1/models/black-forest-labs/flux-1.1-pro/predictions", got)

	meta.BaseURL = "https://replicate.example/gateway/"
	got, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://replicate.example/gateway/v1/models/black-forest-labs/flux-1.1-pro/predictions", got)

	request := httptest.NewRequest(http.MethodPost, got, nil)
	request.Header.Set("x-api-key", "client-secret")
	request.Header.Set("x-goog-api-key", "client-secret")
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "Bearer replicate-secret", request.Header.Get("Authorization"))
	assert.Equal(t, "wait", request.Header.Get("Prefer"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	assert.Empty(t, request.Header.Get("x-api-key"))
	assert.Empty(t, request.Header.Get("x-goog-api-key"))

	first := ModelList()
	require.Equal(t, []string{DefaultModel}, first)
	first[0] = "mutated"
	assert.Equal(t, DefaultModel, ModelList()[0])

	for _, test := range []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{name: "chat mode", mutate: func(meta *relaycommon.Meta) { meta.Mode = channelcatalog.RelayModeChatCompletions }},
		{name: "wrong format", mutate: func(meta *relaycommon.Meta) { meta.Format = channelcatalog.RelayFormatOpenAI }},
		{name: "stream", mutate: func(meta *relaycommon.Meta) { meta.IsStream = true }},
		{name: "bad model path", mutate: func(meta *relaycommon.Meta) { meta.ModelName = "owner/model/extra" }},
		{name: "model traversal", mutate: func(meta *relaycommon.Meta) { meta.ModelName = "owner/.." }},
		{name: "base credentials", mutate: func(meta *relaycommon.Meta) { meta.BaseURL = "https://user:pass@replicate.example" }},
		{name: "base query", mutate: func(meta *relaycommon.Meta) { meta.BaseURL = "https://replicate.example?key=value" }},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			invalid := replicateMeta(channelcatalog.RelayModeImagesGenerations)
			test.mutate(invalid)
			candidate := &Adaptor{}
			candidate.Init(invalid)
			_, err := candidate.GetRequestURL(invalid)
			assert.Error(t, err)
		})
	}

	meta = replicateMeta(channelcatalog.RelayModeImagesGenerations)
	meta.APIKey = "bad\r\nkey"
	adaptor.Init(meta)
	assert.Error(t, adaptor.SetupRequestHeader(httptest.NewRequest(http.MethodPost, "https://replicate.example", nil), meta))
}

func TestReplicateGenerationConversionAndBounds(t *testing.T) {
	two := 2
	meta := replicateMeta(channelcatalog.RelayModeImagesGenerations)
	meta.Request.N = &two
	meta.Request.Extra = map[string]any{
		"model": "client-image-model", "prompt": "fox in the snow", "n": float64(2),
		"size": "1536x1024", "quality": "hd", "output_format": "webp", "response_format": "url",
		"extra_fields":     map[string]any{"negative_prompt": "blurry", "guidance": float64(4)},
		"input":            map[string]any{"seed": float64(17)},
		"safety_tolerance": float64(2),
	}
	meta.Request.Prompt = "fox in the snow"
	meta.RawBody = []byte(`{"model":"client-image-model","prompt":"fox in the snow","n":2,"size":"1536x1024","quality":"hd","output_format":"webp","response_format":"url","extra_fields":{"negative_prompt":"blurry","guidance":4},"input":{"seed":17},"safety_tolerance":2}`)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var envelope struct {
		Input map[string]any `json:"input"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	assert.Equal(t, "fox in the snow", envelope.Input["prompt"])
	assert.Equal(t, "3:2", envelope.Input["aspect_ratio"])
	assert.Equal(t, "webp", envelope.Input["output_format"])
	assert.Equal(t, true, envelope.Input["prompt_upsampling"])
	assert.EqualValues(t, 2, envelope.Input["num_outputs"])
	assert.EqualValues(t, 17, envelope.Input["seed"])
	assert.Equal(t, "blurry", envelope.Input["negative_prompt"])
	assert.NotContains(t, envelope.Input, "model")
	assert.NotContains(t, envelope.Input, "response_format")

	custom := replicateMeta(channelcatalog.RelayModeImagesGenerations)
	custom.Request.Extra["size"] = "997x601"
	custom.RawBody = []byte(`{"model":"client-image-model","prompt":"draw a careful fox","size":"997x601"}`)
	adaptor.Init(custom)
	body, err = adaptor.ConvertRequest(custom)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &envelope))
	assert.Equal(t, "custom", envelope.Input["aspect_ratio"])
	assert.EqualValues(t, 992, envelope.Input["width"])
	assert.EqualValues(t, 608, envelope.Input["height"])

	for _, test := range []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{name: "content type", mutate: func(meta *relaycommon.Meta) { meta.RequestContentType = "text/plain" }},
		{name: "duplicate JSON", mutate: func(meta *relaycommon.Meta) { meta.RawBody = []byte(`{"model":"a","model":"b","prompt":"x"}`) }},
		{name: "oversized JSON", mutate: func(meta *relaycommon.Meta) { meta.RawBody = bytes.Repeat([]byte{'x'}, maxJSONRequestBytes+1) }},
		{name: "empty prompt", mutate: func(meta *relaycommon.Meta) { meta.Request.Prompt = ""; meta.Request.Extra["prompt"] = "" }},
		{name: "too many", mutate: func(meta *relaycommon.Meta) { value := maxImageCount + 1; meta.Request.N = &value }},
		{name: "bad size", mutate: func(meta *relaycommon.Meta) { meta.Request.Extra["size"] = "0x1024" }},
		{name: "bad output format", mutate: func(meta *relaycommon.Meta) { meta.Request.Extra["output_format"] = "svg" }},
		{name: "bad response format", mutate: func(meta *relaycommon.Meta) { meta.Request.Extra["response_format"] = "base64" }},
		{name: "non-string size", mutate: func(meta *relaycommon.Meta) { meta.Request.Extra["size"] = float64(1024) }},
		{name: "native count override", mutate: func(meta *relaycommon.Meta) {
			meta.Request.Extra["input"] = map[string]any{"num_outputs": float64(999)}
		}},
		{name: "native custom dimension", mutate: func(meta *relaycommon.Meta) {
			meta.Request.Extra["input"] = map[string]any{"aspect_ratio": "custom", "width": float64(257), "height": float64(512)}
		}},
		{name: "huge native number", mutate: func(meta *relaycommon.Meta) {
			meta.Request.Extra["input"] = map[string]any{"seed": float64(maxProviderOptionNumber * 2)}
		}},
		{name: "too deep", mutate: func(meta *relaycommon.Meta) {
			var value any = "leaf"
			for range maxProviderOptionDepth + 2 {
				value = map[string]any{"nested": value}
			}
			meta.Request.Extra["input"] = map[string]any{"root": value}
		}},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			invalid := replicateMeta(channelcatalog.RelayModeImagesGenerations)
			test.mutate(invalid)
			candidate := &Adaptor{}
			candidate.Init(invalid)
			_, err := candidate.ConvertRequest(invalid)
			assert.Error(t, err)
		})
	}
}

func TestReplicateMultipartEditUploadsOneValidatedImage(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()

	var uploads atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/gateway/v1/files", request.URL.Path)
		assert.Equal(t, "Bearer replicate-secret", request.Header.Get("Authorization"))
		require.NoError(t, request.ParseMultipartForm(maxEditImageBytes+1))
		file, header, err := request.FormFile("content")
		require.NoError(t, err)
		defer file.Close()
		assert.Equal(t, "image.png", header.Filename)
		got, err := io.ReadAll(file)
		require.NoError(t, err)
		assert.Equal(t, tinyPNG, got)
		uploads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"urls":{"get":"`+server.URL+`/uploaded/image.png"}}`)
	}))
	defer server.Close()

	var raw bytes.Buffer
	writer := multipart.NewWriter(&raw)
	require.NoError(t, writer.WriteField("model", "client-image-model"))
	require.NoError(t, writer.WriteField("prompt", "make the fox blue"))
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="image"; filename="fox.png"`)
	header.Set("Content-Type", "image/png; charset=binary")
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(tinyPNG)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	meta := replicateMeta(channelcatalog.RelayModeImagesEdits)
	meta.BaseURL = server.URL + "/gateway"
	meta.Request.Prompt = "make the fox blue"
	meta.Request.Extra = map[string]any{"model": "client-image-model", "prompt": "make the fox blue"}
	meta.RequestContentType = writer.FormDataContentType()
	meta.RawBody = raw.Bytes()
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.Equal(t, int32(1), uploads.Load())
	var envelope struct {
		Input map[string]any `json:"input"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	assert.Equal(t, server.URL+"/uploaded/image.png", envelope.Input["image_prompt"])
	assert.Equal(t, "make the fox blue", envelope.Input["prompt"])

	wrongType := replicateMeta(channelcatalog.RelayModeImagesEdits)
	wrongType.RequestContentType = writer.FormDataContentType()
	wrongType.RawBody = raw.Bytes()
	wrongType.BaseURL = server.URL
	// Rebuild with a declared JPEG type around PNG bytes; mismatches fail before upload.
	var mismatched bytes.Buffer
	mismatchWriter := multipart.NewWriter(&mismatched)
	require.NoError(t, mismatchWriter.WriteField("model", "client-image-model"))
	require.NoError(t, mismatchWriter.WriteField("prompt", "edit"))
	mismatchHeader := make(textproto.MIMEHeader)
	mismatchHeader.Set("Content-Disposition", `form-data; name="image"; filename="fox.jpg"`)
	mismatchHeader.Set("Content-Type", "image/jpeg")
	mismatchPart, err := mismatchWriter.CreatePart(mismatchHeader)
	require.NoError(t, err)
	_, err = mismatchPart.Write(tinyPNG)
	require.NoError(t, err)
	require.NoError(t, mismatchWriter.Close())
	wrongType.RequestContentType = mismatchWriter.FormDataContentType()
	wrongType.RawBody = mismatched.Bytes()
	adaptor.Init(wrongType)
	_, err = adaptor.ConvertRequest(wrongType)
	assert.Error(t, err)
	assert.Equal(t, int32(1), uploads.Load())

	invalidAfterUploadRisk := replicateMeta(channelcatalog.RelayModeImagesEdits)
	invalidAfterUploadRisk.BaseURL = server.URL + "/gateway"
	invalidAfterUploadRisk.RequestContentType = writer.FormDataContentType()
	invalidAfterUploadRisk.RawBody = raw.Bytes()
	invalidAfterUploadRisk.Request.Extra["response_format"] = "invalid"
	adaptor.Init(invalidAfterUploadRisk)
	_, err = adaptor.ConvertRequest(invalidAfterUploadRisk)
	assert.Error(t, err)
	assert.Equal(t, int32(1), uploads.Load(), "deterministically invalid options must fail before file upload")
}

func TestReplicateResponseConversionURLBase64AndFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := replicateMeta(channelcatalog.RelayModeImagesGenerations)
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	usage, err := adaptor.DoResponse(ctx, replicateResponse(http.StatusCreated,
		`{"status":"succeeded","output":["https://cdn.example/one.png","https://cdn.example/two.webp"],"error": null }`), meta)
	require.NoError(t, err)
	assert.Nil(t, usage)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var output imageResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	require.Len(t, output.Data, 2)
	assert.Equal(t, "https://cdn.example/one.png", output.Data[0].URL)

	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/image.png", request.URL.Path)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer imageServer.Close()
	b64Meta := replicateMeta(channelcatalog.RelayModeImagesGenerations)
	b64Meta.Request.Extra["response_format"] = "b64_json"
	adaptor.Init(b64Meta)
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	_, err = adaptor.DoResponse(ctx, replicateResponse(http.StatusOK,
		`{"status":"succeeded","output":"`+imageServer.URL+`/image.png"}`), b64Meta)
	require.NoError(t, err)
	output = imageResponse{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	require.Len(t, output.Data, 1)
	assert.Equal(t, base64.StdEncoding.EncodeToString(tinyPNG), output.Data[0].B64JSON)
	assert.Empty(t, output.Data[0].URL)

	t.Setenv("SSRF_DISABLE", "false")
	httpx.InitSSRF()
	replicateAuxiliaryHTTPClient.CloseIdleConnections()
	_, err = downloadImage(context.Background(), imageServer.URL+"/image.png")
	assert.Error(t, err, "loopback downloads must be rejected when SSRF protection is enabled")

	for _, test := range []struct {
		name     string
		body     string
		accepted bool
	}{
		{name: "malformed", body: `{"status":`, accepted: true},
		{name: "duplicate field", body: `{"status":"succeeded","status":"failed","output":"https://cdn.example/x.png"}`, accepted: true},
		{name: "pending", body: `{"status":"processing","output":"https://cdn.example/x.png"}`},
		{name: "provider error", body: `{"status":"failed","error":{"message":"provider rejected it"}}`},
		{name: "missing output", body: `{"status":"succeeded","output":null}`, accepted: true},
		{name: "duplicate output", body: `{"status":"succeeded","output":["https://cdn.example/x.png","https://cdn.example/x.png"]}`, accepted: true},
		{name: "unsafe URL", body: `{"status":"succeeded","output":"file:///etc/passwd"}`, accepted: true},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			usage, err := adaptor.DoResponse(ctx, replicateResponse(http.StatusOK, test.body), meta)
			assert.Error(t, err)
			if test.accepted {
				assert.NotNil(t, usage)
				assert.Positive(t, usage.PromptTokens)
			} else {
				assert.Nil(t, usage)
			}
		})
	}
}
