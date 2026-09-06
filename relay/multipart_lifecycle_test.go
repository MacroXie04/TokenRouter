package relay_test

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
)

func TestMultipartMediaLifecyclePreservesFilesMapsModelAndSettles(t *testing.T) {
	previousPrices := service.ExportedModelPrices()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{"gpt-4": {Prompt: 1, Completion: 3}})
	t.Cleanup(func() { service.SetModelPriceRegistry(previousPrices) })

	tests := []struct {
		name         string
		path         string
		fileField    string
		responseBody string
	}{
		{
			name: "image edit", path: "/v1/images/edits", fileField: "image",
			responseBody: `{"created":1,"data":[{"url":"https://cdn.example/edited.png"}]}`,
		},
		{
			name: "audio transcription", path: "/v1/audio/transcriptions", fileField: "file",
			responseBody: `{"text":"transcribed"}`,
		},
		{
			name: "audio translation", path: "/v1/audio/translations", fileField: "file",
			responseBody: `{"text":"translated"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filePayload := []byte("binary-media-payload")
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				assert.Equal(t, test.path, request.URL.Path)
				assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
				mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
				require.NoError(t, err)
				assert.Equal(t, "multipart/form-data", mediaType)
				require.NoError(t, request.ParseMultipartForm(16<<20))
				assert.Equal(t, "upstream-media-model", request.FormValue("model"))
				assert.Equal(t, "make it useful", request.FormValue("prompt"))
				file, _, err := request.FormFile(test.fileField)
				require.NoError(t, err)
				defer file.Close()
				gotPayload, err := io.ReadAll(file)
				require.NoError(t, err)
				assert.Equal(t, filePayload, gotPayload)
				response.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(response, test.responseBody)
			}))
			defer upstream.Close()

			key, userID := setupRelayIntegration(t, upstream.URL)
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").
				Update("model_mapping", `{"gpt-4":"upstream-media-model"}`).Error)

			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			require.NoError(t, writer.WriteField("model", "gpt-4"))
			require.NoError(t, writer.WriteField("prompt", "make it useful"))
			part, err := writer.CreateFormFile(test.fileField, "input.bin")
			require.NoError(t, err)
			_, err = part.Write(filePayload)
			require.NoError(t, err)
			require.NoError(t, writer.Close())

			handler := router.SetUpRouter()
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(body.Bytes()))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var log model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
			assert.Greater(t, log.Quota, 0, "accepted media work without provider usage must never be free")
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, log.Quota, reservation.ActualQuota)
		})
	}
}

func TestMultipartRelayRejectsMalformedEnvelopeBeforeDispatch(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer upstream.Close()
	key, _ := setupRelayIntegration(t, upstream.URL)

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewBufferString("not multipart"))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "multipart/form-data")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Zero(t, calls)
}
