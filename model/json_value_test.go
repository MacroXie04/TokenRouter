package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

func TestJSONValueLegacyScanCompatibility(t *testing.T) {
	var empty JSONValue
	require.NoError(t, empty.Scan(""))
	encoded, err := common.Marshal(empty)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(encoded))

	var array JSONValue
	require.NoError(t, array.Scan(`["a","b"]`))
	encoded, err = common.Marshal(array)
	require.NoError(t, err)
	assert.JSONEq(t, `["a","b"]`, string(encoded))

	var legacy JSONValue
	require.NoError(t, legacy.Scan("a,b"))
	encoded, err = common.Marshal(legacy)
	require.NoError(t, err)
	assert.Equal(t, `"a,b"`, string(encoded))
}
