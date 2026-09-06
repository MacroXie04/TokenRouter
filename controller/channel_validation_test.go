package controller_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestChannelRoutesRejectUnsafeOrOversizedPersistentValues(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)

	response := do(http.MethodPost, "/api/channel",
		`{"name":"unsafe\u202ename","type":1,"key":"sk-test","models":"gpt-4o","group":"default"}`)
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	var count int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name LIKE ?", "unsafe%").Count(&count).Error)
	assert.Zero(t, count)

	channel := createSearchChannel(t, "bounded-update", int(constant.ChannelTypeOpenAI),
		constant.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	response = do(http.MethodPut, "/api/channel",
		fmt.Sprintf(`{"id":%d,"models":%q}`, channel.Id, strings.Repeat("m", 256)))
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	var reloaded model.Channel
	require.NoError(t, model.DB.First(&reloaded, channel.Id).Error)
	assert.Equal(t, "gpt-4o", reloaded.Models)

	response = do(http.MethodPost,
		"/api/channel/copy/"+fmt.Sprint(channel.Id)+"?suffix="+url.QueryEscape(strings.Repeat("x", 65)), "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, false, decodeBody(t, response)["success"])
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name LIKE ?", "bounded-update%").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}
