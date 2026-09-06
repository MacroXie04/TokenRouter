package setting

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetMaxUserTokensUsesSafeBounds(t *testing.T) {
	setupAffinitySettingTest(t)
	for _, invalid := range []string{"0", "-1", "1000001", strconv.FormatInt(int64(^uint(0)>>1), 10), "invalid"} {
		require.NoError(t, UpdateOption(MaxUserTokensOption, invalid))
		assert.Equal(t, DefaultMaxUserTokens, GetMaxUserTokens(), invalid)
	}
	for _, boundary := range []struct {
		value string
		want  int
	}{{"1", 1}, {"1000000", 1_000_000}} {
		require.NoError(t, UpdateOption(MaxUserTokensOption, boundary.value))
		assert.Equal(t, boundary.want, GetMaxUserTokens())
	}
}
