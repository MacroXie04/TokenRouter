package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
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
