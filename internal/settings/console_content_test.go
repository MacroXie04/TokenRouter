package settings

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
	"testing"
)

func TestConsoleContentValidatesAndPublishesOneCoherentSnapshot(t *testing.T) {
	t.Setenv("SSRF_DISABLE", "false")
	httpx.InitSSRF()
	t.Cleanup(httpx.InitSSRF)
	setupAffinitySettingTest(t)

	defaults := GetConsoleContentSetting()
	assert.True(t, defaults.APIInfoEnabled)
	assert.True(t, defaults.FAQEnabled)
	assert.True(t, defaults.UptimeKumaEnabled)
	assert.True(t, defaults.AnnouncementsEnabled)
	assert.Empty(t, defaults.APIInfo)
	assert.Empty(t, defaults.FAQ)
	assert.Empty(t, defaults.UptimeKumaGroups)

	validAPIInfo := `[{
		"id":1,"url":"https://api.example.test/v1?region=eu","route":"Primary",
		"description":"Primary API route","color":"blue"
	}]`
	validFAQ := `[{"id":2,"question":"Where is usage shown?","answer":"Open the usage dashboard.\nTotals may lag briefly."}]`
	validGroups := `[{
		"id":3,"categoryName":"Core services","url":"https://status.example.test/base",
		"slug":"public_status-1","description":"Primary monitors"
	}]`
	recentAnnouncement := `[{"content":"Current","publishDate":"2026-02-01T00:00:00Z"}]`
	require.NoError(t, UpdateOptions(map[string]string{
		ConsoleAPIInfoOption:              validAPIInfo,
		ConsoleAPIInfoEnabledOption:       "false",
		ConsoleFAQOption:                  validFAQ,
		ConsoleFAQEnabledOption:           "true",
		ConsoleUptimeKumaGroupsOption:     validGroups,
		ConsoleUptimeKumaEnabledOption:    "true",
		ConsoleAnnouncementsOption:        recentAnnouncement,
		ConsoleAnnouncementsEnabledOption: "false",
	}))

	content := GetConsoleContentSetting()
	assert.False(t, content.APIInfoEnabled)
	require.Len(t, content.APIInfo, 1)
	assert.Equal(t, "Primary", content.APIInfo[0].Route)
	require.Len(t, content.FAQ, 1)
	assert.Equal(t, "Where is usage shown?", content.FAQ[0].Question)
	require.Len(t, content.UptimeKumaGroups, 1)
	assert.Equal(t, "public_status-1", content.UptimeKumaGroups[0].Slug)
	assert.False(t, content.AnnouncementsEnabled)

	// Callers receive detached slices and optional IDs rather than mutable cache storage.
	content.APIInfo[0].Route = "mutated"
	*content.APIInfo[0].ID = 99
	fresh := GetConsoleContentSetting()
	assert.Equal(t, "Primary", fresh.APIInfo[0].Route)
	assert.Equal(t, int64(1), *fresh.APIInfo[0].ID)

	invalidUpdates := []map[string]string{
		{ConsoleAPIInfoOption: `{}`},
		{ConsoleAPIInfoOption: `[{"url":"javascript:alert(1)","route":"x","description":"x","color":"blue"}]`},
		{ConsoleAPIInfoOption: `[{"url":"https://user:secret@api.example.test","route":"x","description":"x","color":"blue"}]`},
		{ConsoleAPIInfoOption: `[{"url":"https://api.example.test","route":"x","description":"x","color":"unknown"}]`},
		{ConsoleAPIInfoOption: `[{"url":"https://api.example.test","route":"x","description":"x","color":"blue","secret":"no"}]`},
		{ConsoleFAQOption: `[{"question":"Missing answer"}]`},
		{ConsoleFAQOption: `[{"id":1,"question":"One","answer":"A"},{"id":1,"question":"Two","answer":"B"}]`},
		{ConsoleFAQOption: `[{"question":"` + strings.Repeat("q", 201) + `","answer":"A"}]`},
		{ConsoleUptimeKumaGroupsOption: `[{"categoryName":"Private","url":"http://127.0.0.1:3001","slug":"status"}]`},
		{ConsoleUptimeKumaGroupsOption: `[{"categoryName":"Credentials","url":"https://user:secret@status.example.test","slug":"status"}]`},
		{ConsoleUptimeKumaGroupsOption: `[{"categoryName":"Fragment","url":"https://status.example.test/#private","slug":"status"}]`},
		{ConsoleUptimeKumaGroupsOption: `[{"categoryName":"Query","url":"https://status.example.test?token=x","slug":"status"}]`},
		{ConsoleUptimeKumaGroupsOption: `[{"categoryName":"One","url":"https://status.example.test","slug":"bad/slug"}]`},
		{ConsoleUptimeKumaGroupsOption: `[{"categoryName":"Same","url":"https://one.example.test","slug":"one"},{"categoryName":"Same","url":"https://two.example.test","slug":"two"}]`},
		{ConsoleAPIInfoEnabledOption: "yes"},
		{ConsoleFAQEnabledOption: ""},
		{ConsoleUptimeKumaEnabledOption: "1"},
	}
	for _, update := range invalidUpdates {
		require.Error(t, UpdateOptions(update))
		assert.Equal(t, validAPIInfo, GetOption(ConsoleAPIInfoOption))
		assert.Equal(t, validFAQ, GetOption(ConsoleFAQOption))
		assert.Equal(t, validGroups, GetOption(ConsoleUptimeKumaGroupsOption))
		assert.Equal(t, "Primary", GetConsoleContentSetting().APIInfo[0].Route)
	}
}

func TestConsoleContentRemoteSyncRejectsInvalidStateWithoutReplacingCache(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		ConsoleFAQOption:        `[{"question":"Safe question","answer":"Safe answer"}]`,
		ConsoleFAQEnabledOption: "true",
	}))

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", ConsoleFAQOption).
		Update("value", `[{"question":"Remote value is incomplete"}]`).Error)
	require.Error(t, Sync())

	content := GetConsoleContentSetting()
	require.Len(t, content.FAQ, 1)
	assert.Equal(t, "Safe answer", content.FAQ[0].Answer)
	assert.Contains(t, GetOption(ConsoleFAQOption), "Safe answer")
}
