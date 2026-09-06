package setting

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestAnnouncementsValidateSortAndPublishAtomically(t *testing.T) {
	setupAffinitySettingTest(t)
	assert.True(t, AnnouncementsEnabled())
	assert.Empty(t, GetAnnouncements())

	valid := `[` +
		`{"id":"older","type":"warning","content":"Older update","extra":"Details","publishDate":"2026-01-01T00:00:00Z"},` +
		`{"id":2,"type":"success","content":"Newer update","publishDate":"2026-02-01T00:00:00+00:00"}` +
		`]`
	require.NoError(t, UpdateOptions(map[string]string{
		ConsoleAnnouncementsOption:        valid,
		ConsoleAnnouncementsEnabledOption: "false",
	}))
	assert.False(t, AnnouncementsEnabled())
	announcements := GetAnnouncements()
	require.Len(t, announcements, 2)
	assert.Equal(t, "Newer update", announcements[0].Content)
	assert.Equal(t, "Older update", announcements[1].Content)

	for _, invalid := range []map[string]string{
		{ConsoleAnnouncementsEnabledOption: "yes"},
		{ConsoleAnnouncementsOption: `{}`},
		{ConsoleAnnouncementsOption: `[{"content":"missing date"}]`},
		{ConsoleAnnouncementsOption: `[{"content":"x","publishDate":"not-a-date"}]`},
		{ConsoleAnnouncementsOption: `[{"content":"x","type":"critical","publishDate":"2026-01-01T00:00:00Z"}]`},
		{ConsoleAnnouncementsOption: `[{"id":-1,"content":"x","publishDate":"2026-01-01T00:00:00Z"}]`},
		{ConsoleAnnouncementsOption: `[{"id":"same","content":"x","publishDate":"2026-01-01T00:00:00Z"},{"id":"same","content":"y","publishDate":"2026-01-02T00:00:00Z"}]`},
		{ConsoleAnnouncementsOption: `[{"content":"` + strings.Repeat("x", maxAnnouncementContentRunes+1) + `","publishDate":"2026-01-01T00:00:00Z"}]`},
		{ConsoleAnnouncementsOption: strings.Repeat("x", maxAnnouncementsOptionBytes+1)},
	} {
		require.Error(t, UpdateOptions(invalid))
		assert.Equal(t, valid, GetOption(ConsoleAnnouncementsOption), "a rejected update must not replace the published value")
		assert.False(t, AnnouncementsEnabled())
	}
}

func TestAnnouncementsRemoteSyncRejectsMalformedStoredValue(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(ConsoleAnnouncementsOption,
		`[{"content":"safe","publishDate":"2026-01-01T00:00:00Z"}]`))
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", ConsoleAnnouncementsOption).Update("value", `[{"content":"unsafe"}]`).Error)
	require.Error(t, Sync())
	assert.Equal(t, "safe", GetAnnouncements()[0].Content,
		"an invalid remote value must not replace the last coherent snapshot")
}
