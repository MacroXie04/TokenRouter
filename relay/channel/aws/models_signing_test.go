package aws

import (
	"bytes"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	testClaudeModel = "claude-3-5-sonnet-20240620"
	testNovaModel   = "nova-pro-v1:0"
)

func testMeta(modelName string, format constant.RelayFormat, stream bool) *relaycommon.Meta {
	maximum := 128
	request := &protocolkit.GeneralOpenAIRequest{
		Model: modelName,
		Messages: []protocolkit.Message{{
			Role: "user", Content: "hello",
		}},
		MaxTokens: &maximum,
		Stream:    stream,
		Extra:     map[string]any{},
	}
	raw, _ := protocolkit.MarshalJSON(request)
	return &relaycommon.Meta{
		Channel: &model.Channel{
			Type:          int(constant.ChannelTypeAws),
			OtherSettings: `{"aws_key_type":"ak_sk"}`,
		},
		Mode:              constant.RelayModeChatCompletions,
		Format:            format,
		OriginalModelName: modelName,
		ModelName:         modelName,
		APIKey:            "access-key|secret-key|us-east-1",
		Request:           request,
		RawBody:           raw,
		IsStream:          stream,
	}
}

func initializedAdaptor(meta *relaycommon.Meta) *Adaptor {
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	return adaptor
}

func TestModelCatalogAndRegionalResolution(t *testing.T) {
	models := ModelList()
	require.Len(t, models, 25)
	assert.True(t, strings.HasPrefix(models[0], "claude-"), models)
	assert.Contains(t, models, "claude-opus-4-8")
	assert.Contains(t, models, "nova-sonic-v1:0")

	models[0] = "mutated"
	assert.NotEqual(t, "mutated", ModelList()[0], "ModelList must return a defensive copy")

	assert.Equal(t, "anthropic.claude-3-5-sonnet-20240620-v1:0", ModelID(testClaudeModel))
	assert.Equal(t, "custom.provider-model", ModelID(" custom.provider-model "))
	assert.True(t, IsNovaModel(testNovaModel))
	assert.True(t, IsNovaModel("amazon.nova-lite-v1:0"))
	assert.False(t, IsNovaModel(testClaudeModel))

	assert.Equal(t, "us.anthropic.claude-3-5-sonnet-20240620-v1:0", RegionalModelID(testClaudeModel, "us-east-1"))
	assert.Equal(t, "eu.anthropic.claude-3-5-sonnet-20240620-v1:0", RegionalModelID(testClaudeModel, "eu-west-1"))
	assert.Equal(t, "apac.amazon.nova-pro-v1:0", RegionalModelID(testNovaModel, "ap-southeast-2"))
	assert.Equal(t, "amazon.nova-premier-v1:0", RegionalModelID("nova-premier-v1:0", "eu-west-1"))
	assert.Equal(t, "custom.provider-model", RegionalModelID("custom.provider-model", "us-east-1"))
}

func TestCredentialParsingIsExactAndFailClosed(t *testing.T) {
	api, err := ParseCredential("api-key|us-west-2", CredentialModeAPIKey)
	require.NoError(t, err)
	assert.Equal(t, Credential{Mode: CredentialModeAPIKey, APIKey: "api-key", Region: "us-west-2"}, api)

	aksk, err := ParseCredential("access|secret|eu-central-1", CredentialModeAKSK)
	require.NoError(t, err)
	assert.Equal(t, Credential{Mode: CredentialModeAKSK, AccessKey: "access", SecretKey: "secret", Region: "eu-central-1"}, aksk)

	tests := []struct {
		name string
		raw  string
		mode CredentialMode
	}{
		{"empty", "", ""},
		{"one part", "key", ""},
		{"four parts", "a|b|c|d", ""},
		{"empty part", "a||us-east-1", ""},
		{"surrounding whitespace", " a|us-east-1", ""},
		{"control", "a\n|us-east-1", ""},
		{"bad region case", "a|US-EAST-1", ""},
		{"bad region shape", "a|useast1", ""},
		{"leading hyphen", "a|-east-1", ""},
		{"configured mismatch", "a|us-east-1", CredentialModeAKSK},
		{"unknown configured mode", "a|us-east-1", "unknown"},
		{"oversized part", strings.Repeat("a", MaxCredentialPartBytes+1) + "|us-east-1", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseCredential(test.raw, test.mode)
			assert.Error(t, err)
		})
	}
}

func TestAdaptorURLsUseMappedModelAndValidatedOrigin(t *testing.T) {
	meta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, false)
	meta.ModelName = "claude-opus-4-20250514"
	meta.OriginalModelName = testClaudeModel
	adaptor := initializedAdaptor(meta)

	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t,
		"https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-opus-4-20250514-v1:0/invoke",
		requestURL,
	)
	assert.NotContains(t, requestURL, testClaudeModel)

	meta.IsStream = true
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(requestURL, "/invoke-with-response-stream"))

	meta.IsStream = false
	meta.BaseURL = "https://bedrock.example.test:8443/"
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(requestURL, "https://bedrock.example.test:8443/model/"))

	meta.BaseURL = ""
	meta.ModelName = "arn:aws:bedrock:us-east-1:123456789012:inference-profile/profile-name"
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Contains(t, requestURL, "inference-profile%2Fprofile-name")
	assert.NotContains(t, requestURL, "inference-profile/profile-name")
	parsedARN, err := url.Parse(requestURL)
	require.NoError(t, err)
	assert.Contains(t, canonicalURI(parsedARN), "inference-profile%252Fprofile-name")

	for _, invalidBase := range []string{
		"http://bedrock.example.test", "https://user@bedrock.example.test", "https://bedrock.example.test/path",
		"https://bedrock.example.test?x=1", "https://bedrock.example.test?", "https://bedrock.example.test/#fragment", "https://",
	} {
		meta.BaseURL = invalidBase
		_, err := adaptor.GetRequestURL(meta)
		assert.Error(t, err, invalidBase)
	}

	meta.BaseURL = ""
	for _, invalidModel := range []string{"", " model", "model ", "../model", "/model", "model/..", "model//name", "model?query", "model#fragment", "model%2Fother", "model\\other", "model@other", "模型", "model\nother"} {
		meta.ModelName = invalidModel
		_, err := adaptor.GetRequestURL(meta)
		assert.Error(t, err, invalidModel)
	}
}

func TestSetupRequestHeaderUsesRawAPIKeyAndRejectsURLSubstitution(t *testing.T) {
	meta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, false)
	meta.Channel.OtherSettings = `{"aws_key_type":"api_key"}`
	meta.APIKey = "bedrock-api-key|us-east-1"
	adaptor := initializedAdaptor(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	body := []byte(`{"anthropic_version":"bedrock-2023-05-31"}`)
	request, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("x-api-key", "stale")
	request.Header.Set("anthropic-beta", "must-not-leak")
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "Bearer bedrock-api-key", request.Header.Get("Authorization"))
	assert.NotContains(t, request.Header.Get("Authorization"), "us-east-1")
	assert.Empty(t, request.Header.Get("x-api-key"))
	assert.Empty(t, request.Header.Get("anthropic-beta"))
	assert.Empty(t, request.Header.Get("X-Amz-Date"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	assert.Empty(t, request.Header.Get("X-Amzn-Bedrock-Accept"))

	meta.IsStream = true
	meta.Request.Stream = true
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	streamRequest, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, adaptor.SetupRequestHeader(streamRequest, meta))
	assert.Empty(t, streamRequest.Header.Get("Accept"))
	assert.Equal(t, "application/json", streamRequest.Header.Get("X-Amzn-Bedrock-Accept"))
	meta.IsStream = false
	meta.Request.Stream = false

	tampered, err := http.NewRequest(http.MethodPost, requestURL+"?redirect=https://evil.test", bytes.NewReader(body))
	require.NoError(t, err)
	err = adaptor.SetupRequestHeader(tampered, meta)
	require.Error(t, err)
	assert.Empty(t, tampered.Header.Get("Authorization"))

	wrongMethod, err := http.NewRequest(http.MethodGet, requestURL, bytes.NewReader(body))
	require.NoError(t, err)
	err = adaptor.SetupRequestHeader(wrongMethod, meta)
	require.Error(t, err)
	assert.Empty(t, wrongMethod.Header.Get("Authorization"))
}

func TestSigV4SignatureAndBodyPreservation(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	request, err := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-3-5-sonnet-20240620-v1:0/invoke",
		bytes.NewReader(body),
	)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	credential := Credential{Mode: CredentialModeAKSK, AccessKey: "access-key", SecretKey: "secret-key", Region: "us-east-1"}
	now := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.FixedZone("test", 9*60*60))
	require.NoError(t, SignRequestAt(request, body, credential, now))

	assert.Equal(t, "20240101T180405Z", request.Header.Get("X-Amz-Date"))
	assert.Equal(t, "5e4ce7b36ba37b78a5d5f9fd08e6b7b54ba6879d651aa46ec9e1d6fa24ebe30a", request.Header.Get("X-Amz-Content-Sha256"))
	assert.Equal(t,
		"AWS4-HMAC-SHA256 Credential=access-key/20240101/us-east-1/bedrock/aws4_request, "+
			"SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, "+
			"Signature=22a39d165d0df66c23d877db8f2147a76efb1549d42875dfc782ad40dd55b919",
		request.Header.Get("Authorization"),
	)
	assert.NotContains(t, request.Header.Get("Authorization"), "secret-key")

	readBack, err := requestBody(request)
	require.NoError(t, err)
	assert.Equal(t, body, readBack)

	request.GetBody = nil
	readBack, err = requestBody(request)
	require.NoError(t, err)
	assert.Equal(t, body, readBack)
	readBackAgain, err := requestBody(request)
	require.NoError(t, err)
	assert.Equal(t, body, readBackAgain)

	for _, invalid := range []Credential{
		{Mode: CredentialModeAKSK, AccessKey: " access", SecretKey: "secret", Region: "us-east-1"},
		{Mode: CredentialModeAKSK, AccessKey: "access\nkey", SecretKey: "secret", Region: "us-east-1"},
		{Mode: CredentialModeAKSK, AccessKey: "access", SecretKey: "secret|extra", Region: "us-east-1"},
	} {
		unsigned, makeErr := http.NewRequest(http.MethodPost, request.URL.String(), bytes.NewReader(body))
		require.NoError(t, makeErr)
		unsigned.Header.Set("Content-Type", "application/json")
		assert.Error(t, SignRequestAt(unsigned, body, invalid, now))
		assert.Empty(t, unsigned.Header.Get("Authorization"))
	}
}

func TestSetupRequestHeaderAKSKDeterministic(t *testing.T) {
	meta := testMeta(testClaudeModel, constant.RelayFormatOpenAI, false)
	adaptor := initializedAdaptor(meta)
	adaptor.Now = func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }
	body := []byte(`{"messages":[]}`)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "20240102T030405Z", request.Header.Get("X-Amz-Date"))
	assert.Contains(t, request.Header.Get("Authorization"), "Credential=access-key/20240102/us-east-1/bedrock/aws4_request")
	// Expected signature was independently generated with botocore's SigV4Auth.
	assert.Contains(t, request.Header.Get("Authorization"), "Signature=ec203e29b7982e7538b463c30b25e71e670faf090c44b9c52088939f273ce0c4")
}

func TestSigV4CanonicalURIAndQueryMatchAWSEncoding(t *testing.T) {
	parsed, err := url.Parse("https://bedrock.example.test/a:b/%2F/c%20d")
	require.NoError(t, err)
	assert.Equal(t, "/a%3Ab/%252F/c%2520d", canonicalURI(parsed))
	assert.Equal(t, "/", canonicalURI(&url.URL{}))

	query := url.Values{
		"a b":   []string{"z", "x/y"},
		"tilde": []string{"~"},
		"empty": nil,
	}
	assert.Equal(t, "a%20b=x%2Fy&a%20b=z&empty=&tilde=~", canonicalQuery(query))
	assert.Equal(t, "%C3%A9=one&z=two", canonicalQuery(url.Values{"z": {"two"}, "é": {"one"}}))
}

func TestMediaClientIsSSRFAndRedirectSafe(t *testing.T) {
	client := mediaHTTPClient()
	require.NotNil(t, client)
	assert.Equal(t, mediaRequestTimeout, client.Timeout)
	require.NotNil(t, client.CheckRedirect)
	request, err := http.NewRequest(http.MethodGet, "https://example.test/file", nil)
	require.NoError(t, err)
	assert.ErrorIs(t, client.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(appcommon.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, transport.TLSClientConfig)
	assert.GreaterOrEqual(t, transport.TLSClientConfig.MinVersion, uint16(0x0303))
	assert.Positive(t, transport.TLSHandshakeTimeout)
	assert.Positive(t, transport.ResponseHeaderTimeout)
}

func TestJSONCodecRejectsDuplicatesTrailingAndResourceAbuse(t *testing.T) {
	valid := []byte(`{"a":[1,true,null,{"b":"text"}]}`)
	assert.NoError(t, rejectDuplicateJSONKeys(valid))

	for _, invalid := range [][]byte{
		{}, []byte(`{"a":1,"a":2}`), []byte(`{"a":1} {"b":2}`), []byte(`{"a":1} trailing`),
		[]byte(`{"a":1e999}`), []byte(`{"a":10000000000000000000}`), {0xff},
	} {
		assert.Error(t, rejectDuplicateJSONKeys(invalid), string(invalid))
	}

	deep := strings.Repeat("[", MaxJSONDepth+2) + "0" + strings.Repeat("]", MaxJSONDepth+2)
	assert.Error(t, rejectDuplicateJSONKeys([]byte(deep)))
	longKey := `{"` + strings.Repeat("k", MaxJSONKeyBytes+1) + `":1}`
	assert.Error(t, rejectDuplicateJSONKeys([]byte(longKey)))

	var decoded struct {
		A int `json:"a"`
	}
	assert.NoError(t, strictJSON([]byte(`{"a":1}`), &decoded, true))
	assert.Error(t, strictJSON([]byte(`{"a":1,"unknown":2}`), &decoded, true))
	assert.Error(t, strictJSON([]byte(`{"a":1}{"a":2}`), &decoded, false))
}

func TestRequestBodyRejectsOversizeAndNil(t *testing.T) {
	_, err := requestBody(nil)
	assert.Error(t, err)

	request, err := http.NewRequest(http.MethodPost, "https://example.test", bytes.NewReader(bytes.Repeat([]byte{'a'}, int(MaxRequestBodyBytes)+1)))
	require.NoError(t, err)
	_, err = requestBody(request)
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), strings.Repeat("a", 32))
}
