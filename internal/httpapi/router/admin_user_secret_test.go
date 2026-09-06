package router_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"testing"
)

func TestAdminUserResponsesNeverExposePasswordHashesOrPATs(t *testing.T) {
	root, _, _ := setupPermissionTest(t)
	target := createManagedUser(t, "secret-response-target", roles.RoleCommonUser, model.UserStatusEnabled, "default")
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).
		Updates(map[string]any{
			"password":     "super-secret-password-hash",
			"access_token": "pat-secret-value",
			"setting": `{"language":"en","notification_email":"private-notify@example.test",` +
				`"webhook_url":"https://hooks.example.test/incoming/private-path?signature=private-query",` +
				`"webhook_secret":"admin-hidden-webhook-secret",` +
				`"bark_url":"https://api.day.app/private-device-key/{{title}}/{{content}}",` +
				`"gotify_url":"https://gotify.example.test/private-instance",` +
				`"gotify_token":"admin-hidden-gotify-token"}`,
		}).Error)

	for _, path := range []string{"/api/user", "/api/user/" + textutil.Int2Str(target.Id)} {
		rec := root.do(http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), "super-secret-password-hash")
		assert.NotContains(t, rec.Body.String(), "pat-secret-value")
		assert.NotContains(t, rec.Body.String(), "admin-hidden-webhook-secret")
		assert.NotContains(t, rec.Body.String(), "admin-hidden-gotify-token")
		assert.NotContains(t, rec.Body.String(), "private-notify@example.test")
		assert.NotContains(t, rec.Body.String(), "private-path")
		assert.NotContains(t, rec.Body.String(), "private-query")
		assert.NotContains(t, rec.Body.String(), "private-device-key")
		assert.NotContains(t, rec.Body.String(), "private-instance")

		data := decodeBody(t, rec)["data"].(map[string]any)
		row := data
		if items, ok := data["items"].([]any); ok {
			row = nil
			for _, item := range items {
				candidate := item.(map[string]any)
				if candidate["username"] == target.Username {
					row = candidate
					break
				}
			}
			require.NotNil(t, row)
		}
		settingsJSON, ok := row["setting"].(string)
		require.True(t, ok)
		var visibleSettings map[string]any
		require.NoError(t, jsonutil.UnmarshalJsonStr(settingsJSON, &visibleSettings))
		assert.Equal(t, true, visibleSettings["webhook_secret_configured"])
		assert.Equal(t, true, visibleSettings["gotify_token_configured"])
		assert.Equal(t, true, visibleSettings["notification_email_configured"])
		assert.Equal(t, true, visibleSettings["webhook_url_configured"])
		assert.Equal(t, true, visibleSettings["bark_url_configured"])
		assert.Equal(t, true, visibleSettings["gotify_url_configured"])
	}
}
