package setting

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestChatPresetOptionIsBoundedValidatedAndAtomicallyPublished(t *testing.T) {
	setupAffinitySettingTest(t)

	valid := `[{"Hosted chat":"https://chat.example.test/?key={key}&base={address}"},{"Desktop client":"clientapp://configure?data={cherryConfig}"}]`
	require.NoError(t, UpdateOption(ChatsOption, valid))
	assert.Equal(t, []ChatPreset{
		{Name: "Hosted chat", URL: "https://chat.example.test/?key={key}&base={address}"},
		{Name: "Desktop client", URL: "clientapp://configure?data={cherryConfig}"},
	}, GetChatPresets())

	for _, invalid := range []string{
		`{"Hosted chat":"https://chat.example.test"}`,
		`[{"":"https://chat.example.test"}]`,
		`[{"One":"https://one.example.test","Two":"https://two.example.test"}]`,
		`[{"Unsafe":"javascript:alert(1)"}]`,
		`[{"Duplicate":"https://one.example.test"},{"Duplicate":"https://two.example.test"}]`,
		strings.Repeat("x", maxChatConfigBytes+1),
	} {
		require.Error(t, UpdateOption(ChatsOption, invalid), invalid)
		assert.Len(t, GetChatPresets(), 2, "a rejected update must not replace the published snapshot")
	}
}

func TestChatPresetOptionFailsClosedForMalformedLegacyValue(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, model.DB.Create(&model.Option{Key: ChatsOption, Value: `[{"Unsafe":"data:text/html,x"}]`}).Error)
	require.NoError(t, Init())
	assert.Empty(t, GetChatPresets())
}
