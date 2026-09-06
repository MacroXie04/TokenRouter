package settings

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"strconv"
	"testing"
)

func TestCheckinSettingsValidateAndPublishCoherently(t *testing.T) {
	setupAffinitySettingTest(t)
	assert.Equal(t, CheckinSetting{
		MinQuota: DefaultCheckinMinQuota, MaxQuota: DefaultCheckinMaxQuota,
	}, GetCheckinSetting())

	require.NoError(t, UpdateOptions(map[string]string{
		CheckinEnabledOption:  "true",
		CheckinMinQuotaOption: "250",
		CheckinMaxQuotaOption: "500",
	}))
	assert.Equal(t, CheckinSetting{Enabled: true, MinQuota: 250, MaxQuota: 500}, GetCheckinSetting())

	for _, invalid := range []map[string]string{
		{CheckinEnabledOption: "sometimes"},
		{CheckinMinQuotaOption: "0"},
		{CheckinMinQuotaOption: "501"},
		{CheckinMaxQuotaOption: strconv.FormatInt(quotamath.MaxQuota+1, 10)},
	} {
		require.Error(t, UpdateOptions(invalid))
		assert.Equal(t, CheckinSetting{Enabled: true, MinQuota: 250, MaxQuota: 500}, GetCheckinSetting())
	}
}
