package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestRelayRoutersApplyLiveModelRequestRateLimitAfterAuthentication(t *testing.T) {
	firstToken, handler := setupDashboardBillingRoutes(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelRequestRateLimitEnabledOption:         "true",
		setting.ModelRequestRateLimitDurationMinutesOption: "30",
		setting.ModelRequestRateLimitCountOption:           "0",
		setting.ModelRequestRateLimitSuccessCountOption:    "1",
	}))
	t.Cleanup(func() { _ = setting.UpdateOption(setting.ModelRequestRateLimitEnabledOption, "false") })

	var firstUser model.User
	require.NoError(t, model.DB.First(&firstUser, firstToken.UserId).Error)
	secondUser := model.User{
		Username: "gemini-rate-limit-user", Status: model.UserStatusEnabled,
		Group: service.GroupDefault, Quota: common.QuotaPerUnit,
	}
	require.NoError(t, model.DB.Create(&secondUser).Error)
	secondToken := model.Token{
		UserId: secondUser.Id, Key: "sk-gemini-rate-limit", Name: "gemini-rate-limit",
		Status: service.TokenStatusEnabled, Group: service.GroupDefault,
		UnlimitedQuota: true, ExpiredTime: 4_102_444_800,
	}
	require.NoError(t, model.DB.Create(&secondToken).Error)

	tests := []struct {
		path  string
		token string
	}{
		{path: "/v1/models", token: firstToken.Key},
		{path: "/v1beta/models", token: secondToken.Key},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, test.path, nil)
				req.Header.Set("Authorization", "Bearer "+test.token)
				req.RemoteAddr = "198.51.100." + strconv.Itoa(20+len(test.path)) + ":1234"
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, req)
				return recorder
			}

			first := request()
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			second := request()
			assert.Equal(t, http.StatusTooManyRequests, second.Code, second.Body.String())
			assert.Contains(t, second.Body.String(), `"code":"rate_limit_exceeded"`)
		})
	}

	require.NoError(t, setting.UpdateOption(setting.ModelRequestRateLimitEnabledOption, "false"))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+firstToken.Key)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String(),
		"a committed disable must affect the already-built router")
}
