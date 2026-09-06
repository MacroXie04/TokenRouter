package channels

import (
	"github.com/stretchr/testify/assert"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"testing"
)

func TestParseMultiKeys(t *testing.T) {
	assert.Equal(t, []string{"k1", "k2"}, parseMultiKeys(`["k1","k2"]`))
	assert.Equal(t, []string{"k1", "k2"}, parseMultiKeys(`{"keys":["k1","k2"]}`))
	assert.Nil(t, parseMultiKeys(""))
	assert.Nil(t, parseMultiKeys("not-json"))
}

func TestChannelCredentialResponseBoundaryHandlesLegacyMultiKeys(t *testing.T) {
	legacy := &model.Channel{Key: "", Other: `{"keys":["legacy-one","legacy-two"]}`}
	assert.Equal(t, "legacy-one\nlegacy-two", ChannelCredentialForDisclosure(legacy))

	MaskChannelCredentialsForResponse(legacy)
	assert.Empty(t, legacy.Key)
	assert.Empty(t, legacy.Other)

	providerMetadata := &model.Channel{Key: "secret", Other: `{"region":"west"}`}
	assert.Equal(t, "secret", ChannelCredentialForDisclosure(providerMetadata))
	MaskChannelCredentialsForResponse(providerMetadata)
	assert.Empty(t, providerMetadata.Key)
	assert.JSONEq(t, `{"region":"west"}`, providerMetadata.Other)

	sensitive := &model.Channel{
		Key: "secret", BaseURL: "https://internal.example", OpenAIOrganization: "org",
		Other: "region", Setting: `{"balance_url":"https://balance.example"}`,
		OtherSettings: `{"advanced_custom":{"auth":"fixed-secret"}}`,
		ParamOverride: `{"prompt":"fixed"}`, HeaderOverride: `{"Authorization":"fixed-secret"}`,
	}
	MaskChannelSensitiveFieldsForResponse(sensitive)
	assert.Empty(t, sensitive.Key)
	assert.Empty(t, sensitive.BaseURL)
	assert.Empty(t, sensitive.OpenAIOrganization)
	assert.Empty(t, sensitive.Other)
	assert.Empty(t, sensitive.Setting)
	assert.Empty(t, sensitive.OtherSettings)
	assert.Empty(t, sensitive.ParamOverride)
	assert.Empty(t, sensitive.HeaderOverride)
}

func TestGetChannelKeyRotation(t *testing.T) {
	ch := &model.Channel{Id: 1, Key: "single", Other: `["a","b","c"]`}

	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		seen[GetChannelKey(ch)] = true
	}
	// All three keys are used across calls.
	assert.True(t, seen["a"] && seen["b"] && seen["c"], "rotation must use all keys: %v", seen)

	// A channel with a single key returns it directly.
	single := &model.Channel{Id: 2, Key: "single"}
	assert.Equal(t, "single", GetChannelKey(single))
}

func TestGetChannelKeyUsesOnlyEnabledCurrentMultiKeys(t *testing.T) {
	channel := &model.Channel{
		Id:          301,
		Key:         "first-secret\nsecond-secret\nthird-secret",
		ChannelInfo: `{"is_multi_key":true,"multi_key_size":3,"multi_key_status_list":{"0":2,"1":1,"2":3}}`,
	}
	for i := 0; i < 5; i++ {
		assert.Equal(t, "second-secret", GetChannelKey(channel))
	}

	channel.ChannelInfo = `{"is_multi_key":true,"multi_key_size":3,"multi_key_status_list":{"0":2,"1":3,"2":2}}`
	assert.Empty(t, GetChannelKey(channel), "a disabled key must not be sent merely because it is the first stored key")
}

func TestGetChannelKeyNeverReturnsSerializedKeyCollection(t *testing.T) {
	channel := &model.Channel{Id: 302, Key: "first-secret\nsecond-secret"}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		key := GetChannelKey(channel)
		assert.NotContains(t, key, "\n")
		seen[key] = true
	}
	assert.True(t, seen["first-secret"] && seen["second-secret"], "defensive rotation must use each key: %v", seen)
}

func TestGetChannelKeyPreservesPrettyPrintedStructuredCredential(t *testing.T) {
	raw := "{\n  \"access_token\": \"access-secret\",\n  \"account_id\": \"acct-1\"\n}"
	channel := &model.Channel{Id: 303, Key: raw}
	assert.JSONEq(t, raw, GetChannelKey(channel))
}
