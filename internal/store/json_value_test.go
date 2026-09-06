package store

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"testing"
)

func TestJSONValueLegacyScanCompatibility(t *testing.T) {
	var empty JSONValue
	require.NoError(t, empty.Scan(""))
	encoded, err := jsonutil.Marshal(empty)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(encoded))

	var array JSONValue
	require.NoError(t, array.Scan(`["a","b"]`))
	encoded, err = jsonutil.Marshal(array)
	require.NoError(t, err)
	assert.JSONEq(t, `["a","b"]`, string(encoded))

	var legacy JSONValue
	require.NoError(t, legacy.Scan("a,b"))
	encoded, err = jsonutil.Marshal(legacy)
	require.NoError(t, err)
	assert.Equal(t, `"a,b"`, string(encoded))
}
