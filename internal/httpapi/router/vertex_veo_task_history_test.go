package router_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"testing"
)

func TestVertexVeoTaskHistoryExposesOnlyBoundedInlineMetadata(t *testing.T) {
	_, do, userID := setupTaskHistory(t, roles.RoleCommonUser)
	providerSecret := "vertex-provider-operation-secret"
	credentialSecret := "vertex-service-account-secret"
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID: "task_VertexVeoHistory12345678901234", Platform: model.TaskOperationPlatformVertexVeo,
		UserId: userID, Group: "default", ChannelId: 41, Quota: 4000,
		Action: "textGenerate", Status: model.TaskStatusSuccess, Progress: "100%", SubmitTime: 100,
		Properties: `{"input":"history-safe","upstream_model_name":"veo-3.1-fast-generate-preview",` +
			`"origin_model_name":"veo-3.1-generate-preview"}`,
		Data: `{"state":"succeeded","has_inline_video":true,"inline_mime_type":"video/mp4","inline_bytes":24}`,
		PrivateData: `{"encrypted_channel_key":"` + credentialSecret + `",` +
			`"encrypted_provider_task_id":"` + providerSecret + `"}`,
		FailReason: "",
	}).Error)

	recorder := do(http.MethodGet, "/api/task/self?platform=41", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	items, page := taskHistoryItems(t, recorder)
	require.Len(t, items, 1)
	assert.Equal(t, float64(1), page["total"])
	assert.Equal(t, model.TaskOperationPlatformVertexVeo, items[0]["platform"])
	assert.NotContains(t, items[0], "result_url")
	data, ok := items[0]["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["has_inline_video"])
	assert.Equal(t, "video/mp4", data["inline_mime_type"])
	assert.Equal(t, float64(24), data["inline_bytes"])
	assert.NotContains(t, recorder.Body.String(), providerSecret)
	assert.NotContains(t, recorder.Body.String(), credentialSecret)
	assert.NotContains(t, recorder.Body.String(), "private_data")
}
