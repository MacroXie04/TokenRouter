package protocolkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneralOpenAIRequestAcceptsObjectAndLegacyStringResponseFormats(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "object", body: `{"model":"m","response_format":{"type":"json_object"}}`, want: "json_object"},
		{name: "image and speech string", body: `{"model":"m","response_format":"wav"}`, want: "wav"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request GeneralOpenAIRequest
			require.NoError(t, UnmarshalJSON([]byte(test.body), &request))
			require.NotNil(t, request.ResponseFormat)
			assert.Equal(t, test.want, request.ResponseFormat.Type)
		})
	}
}
