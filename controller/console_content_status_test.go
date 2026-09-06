package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/setting"
)

func TestStatusPublishesValidatedConsoleContentOnlyWhenEnabled(t *testing.T) {
	initSetupTestDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ConsoleAPIInfoOption:           `[{"id":1,"url":"https://api.example.test/v1","route":"Primary","description":"Main endpoint","color":"green"}]`,
		setting.ConsoleAPIInfoEnabledOption:    "true",
		setting.ConsoleFAQOption:               `[{"id":2,"question":"How do I connect?","answer":"Create a token first."}]`,
		setting.ConsoleFAQEnabledOption:        "true",
		setting.ConsoleUptimeKumaEnabledOption: "false",
	}))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/status", GetStatus)
	read := func() map[string]any {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var envelope struct {
			Success bool           `json:"success"`
			Data    map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		require.True(t, envelope.Success)
		return envelope.Data
	}

	data := read()
	assert.Equal(t, true, data["api_info_enabled"])
	assert.Equal(t, true, data["faq_enabled"])
	assert.Equal(t, false, data["uptime_kuma_enabled"])
	apiInfo, ok := data["api_info"].([]any)
	require.True(t, ok)
	require.Len(t, apiInfo, 1)
	assert.Equal(t, "Primary", apiInfo[0].(map[string]any)["route"])
	faq, ok := data["faq"].([]any)
	require.True(t, ok)
	require.Len(t, faq, 1)
	assert.Equal(t, "How do I connect?", faq[0].(map[string]any)["question"])

	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ConsoleAPIInfoEnabledOption: "false",
		setting.ConsoleFAQEnabledOption:     "false",
	}))
	data = read()
	assert.Equal(t, false, data["api_info_enabled"])
	assert.Equal(t, false, data["faq_enabled"])
	assert.NotContains(t, data, "api_info")
	assert.NotContains(t, data, "faq")
}
