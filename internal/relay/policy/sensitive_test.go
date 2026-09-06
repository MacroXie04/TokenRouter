package policy

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"testing"
)

func TestParseWordList(t *testing.T) {
	assert.Equal(t, []string{"foo", "bar", "baz"}, parseWordList("foo,bar\nbaz"))
	assert.Equal(t, []string{"foo", "bar"}, parseWordList("foo，bar"))
	assert.Nil(t, parseWordList(""))
	assert.Nil(t, parseWordList(" , \n"))
}

func TestSensitiveContentCheck(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOption("SensitiveWords", "violence,pornography"))

	LoadSensitiveWords()
	assert.Equal(t, 2, SensitiveWordCount())
	assert.True(t, CheckSensitiveContent("this contains violence in it"))
	assert.False(t, CheckSensitiveContent("this is a normal message"))
}

func TestSensitiveCheckGating(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db
	require.NoError(t, setting.Init())

	// Both toggles default to true (reference defaults): check is active.
	require.NoError(t, setting.UpdateOption("SensitiveWords", "violence"))
	LoadSensitiveWords()
	assert.True(t, ShouldCheckPromptSensitive())

	// Disabling either toggle turns the prompt check off.
	require.NoError(t, setting.UpdateOption(setting.CheckSensitiveEnabledOption, "false"))
	assert.False(t, ShouldCheckPromptSensitive())
	require.NoError(t, setting.UpdateOption(setting.CheckSensitiveEnabledOption, "true"))
	require.NoError(t, setting.UpdateOption(setting.CheckSensitiveOnPromptEnabledOption, "false"))
	assert.False(t, ShouldCheckPromptSensitive())
	require.NoError(t, setting.UpdateOption(setting.CheckSensitiveOnPromptEnabledOption, "true"))
}
