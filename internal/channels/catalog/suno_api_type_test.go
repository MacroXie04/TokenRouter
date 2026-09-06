package catalog

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestSunoRootRelayModes(t *testing.T) {
	assert.Equal(t, 36, int(ChannelTypeSunoAPI))
	assert.Equal(t, "Suno", ChannelTypeName(ChannelTypeSunoAPI))
	assert.Equal(t, 27, int(RelayModeSunoFetch))
	assert.Equal(t, 28, int(RelayModeSunoFetchByID))
	assert.Equal(t, 29, int(RelayModeSunoSubmit))
	assert.Equal(t, RelayModeSunoSubmit, PathToRelayMode("/suno/submit/MUSIC"))
	assert.Equal(t, RelayModeSunoFetch, PathToRelayMode("/suno/fetch"))
	assert.Equal(t, RelayModeSunoFetchByID, PathToRelayMode("/suno/fetch/task_id"))
	assert.Equal(t, RelayModeUnknown, PathToRelayMode("/v1/suno/submit"))
}
