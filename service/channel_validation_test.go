package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func validChannelForValidation() *model.Channel {
	return &model.Channel{
		Name: "bounded channel", Type: int(constant.ChannelTypeOpenAI), Key: "sk-test",
		Models: "gpt-4o,gpt-4.1", Group: "default", Tag: "fast", Remark: "operator note",
		OtherSettings: `{}`,
	}
}

func TestValidateChannelForCreateBoundsPersistentFields(t *testing.T) {
	require.NoError(t, validateChannelForCreate(validChannelForValidation()))

	tests := []struct {
		name   string
		mutate func(*model.Channel)
	}{
		{"missing credential", func(channel *model.Channel) { channel.Key = "" }},
		{"invalid type", func(channel *model.Channel) { channel.Type = 0 }},
		{"oversized name", func(channel *model.Channel) { channel.Name = strings.Repeat("n", maxChannelNameBytes+1) }},
		{"unsafe name", func(channel *model.Channel) { channel.Name = "safe\u202ename" }},
		{"oversized credential", func(channel *model.Channel) { channel.Key = strings.Repeat("k", maxChannelCredentialBytes+1) }},
		{"oversized group", func(channel *model.Channel) { channel.Group = strings.Repeat("g", maxChannelGroupBytes+1) }},
		{"oversized model", func(channel *model.Channel) { channel.Models = strings.Repeat("m", maxChannelModelNameBytes+1) }},
		{"unsafe tag", func(channel *model.Channel) { channel.Tag = "ops\nroot" }},
		{"invalid settings", func(channel *model.Channel) { channel.OtherSettings = "[]" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := validChannelForValidation()
			test.mutate(channel)
			err := validateChannelForCreate(channel)
			assert.ErrorIs(t, err, ErrInvalidChannelInput)
		})
	}
}

func TestChannelMutationServiceRejectsInvalidValuesBeforeStorage(t *testing.T) {
	invalid := validChannelForValidation()
	invalid.Name = "bad\x00name"
	err := CreateChannelWithAbilities(invalid)
	assert.ErrorIs(t, err, ErrInvalidChannelInput)

	err = UpdateChannelWithAbilities(1, map[string]any{"models": strings.Repeat("m", maxChannelModelNameBytes+1)})
	assert.ErrorIs(t, err, ErrInvalidChannelInput)
	err = UpdateChannelWithAbilities(1, map[string]any{"future_field": "value"})
	assert.ErrorIs(t, err, ErrInvalidChannelInput)
	err = UpdateChannelWithAbilities(1, map[string]any{"weight": int64(1)})
	assert.ErrorIs(t, err, ErrInvalidChannelInput)

	_, err = CopyChannel(1, "unsafe\u202esuffix", true)
	assert.True(t, errors.Is(err, ErrInvalidChannelInput))
}
