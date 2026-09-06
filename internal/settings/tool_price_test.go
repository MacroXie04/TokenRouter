package settings

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolPriceDefaultsAndModelOverrides(t *testing.T) {
	snapshot, err := ParseToolPriceSnapshot(`{}`)
	require.NoError(t, err)

	defaults := map[string]float64{
		ToolWebSearch: 10, ToolWebSearchPreview: 10, ToolFileSearch: 2.5,
		ToolGoogleSearch: 14, ToolImageGeneration: 150,
	}
	for name, expected := range defaults {
		resolution := snapshot.Resolve(name, "unmatched-model")
		assert.True(t, resolution.Matched, name)
		assert.Equal(t, expected, resolution.Price, name)
		assert.Equal(t, ToolPriceSourceDefault, resolution.Source, name)
	}
	for _, modelName := range []string{"gpt-4o", "gpt-4o-2024-11-20", "gpt-4.1", "gpt-4.1-mini-2025"} {
		resolution := snapshot.Resolve(ToolWebSearchPreview, modelName)
		assert.Equal(t, 25.0, resolution.Price, modelName)
		assert.Equal(t, ToolPriceSourceDefault, resolution.Source, modelName)
	}
	unpriced := snapshot.Resolve("custom_unpriced", "gpt-4o")
	assert.False(t, unpriced.Matched)
	assert.Zero(t, unpriced.Price)
}

func TestToolPriceLongestPrefixAndExplicitZeroAreTerminal(t *testing.T) {
	snapshot, err := ParseToolPriceSnapshot(`{
		"web_search_preview":0,
		"web_search_preview:gpt-4o*":30,
		"web_search_preview:gpt-4o-mini*":0,
		"custom_fn":5,
		"custom_fn:model*":4,
		"custom_fn:model-pro*":0
	}`)
	require.NoError(t, err)

	tests := []struct {
		tool, model, rule string
		price             float64
	}{
		{ToolWebSearchPreview, "other", ToolWebSearchPreview, 0},
		{ToolWebSearchPreview, "gpt-4o", ToolWebSearchPreview + ":gpt-4o*", 30},
		{ToolWebSearchPreview, "gpt-4o-mini", ToolWebSearchPreview + ":gpt-4o-mini*", 0},
		{"custom_fn", "model-basic", "custom_fn:model*", 4},
		{"custom_fn", "model-pro-v2", "custom_fn:model-pro*", 0},
		{"custom_fn", "other", "custom_fn", 5},
	}
	for _, test := range tests {
		resolution := snapshot.Resolve(test.tool, test.model)
		assert.True(t, resolution.Matched, "%s/%s", test.tool, test.model)
		assert.Equal(t, test.price, resolution.Price, "%s/%s", test.tool, test.model)
		assert.Equal(t, test.rule, resolution.Rule, "%s/%s", test.tool, test.model)
		assert.Equal(t, ToolPriceSourceOperator, resolution.Source, "%s/%s", test.tool, test.model)
	}
}

func TestToolPriceCanonicalJSONIsLexicalAndDetached(t *testing.T) {
	snapshot, err := ParseToolPriceSnapshot(` { "z_tool": 2.5000, "a_tool": 0, "m_tool:model*": 1e1 } `)
	require.NoError(t, err)
	assert.Equal(t, `{"a_tool":0,"m_tool:model*":10,"z_tool":2.5}`, snapshot.CanonicalJSON())

	copyOf := snapshot.OperatorPrices()
	copyOf["a_tool"] = 999
	delete(copyOf, "z_tool")
	assert.Equal(t, 0.0, snapshot.Resolve("a_tool", "").Price)
	assert.Equal(t, 2.5, snapshot.Resolve("z_tool", "").Price)
}

func TestToolPriceStrictParserRejectsMalformedAndAmbiguousConfiguration(t *testing.T) {
	invalid := []string{
		``, `null`, `[]`, `true`, `{"tool":null}`, `{"tool":true}`, `{"tool":"1"}`,
		`{"tool":-1}`, `{"tool":-0}`, `{"tool":1000000000001}`, `{"tool":1e-13}`,
		`{"tool":1.0000000000000}`, `{"tool":1,"tool":2}`, `{"tool":1} {}`,
		`{"":1}`, `{" tool":1}`, `{"tool ":1}`, `{"bad\u0000name":1}`, `{"bad\u202ename":1}`,
		`{"tool*":1}`, `{"tool:model":1}`, `{"tool:*":1}`, `{"tool:model**":1}`,
		`{"tool:model:variant*":1}`,
	}
	for _, raw := range invalid {
		assert.Error(t, ValidateToolPricesJSON(raw), raw)
	}
	assert.Error(t, ValidateToolPricesJSON(string([]byte{'{', '"', 'x', 0xff, '"', ':', '1', '}'})))

	for _, raw := range []string{
		`{}`, `{"tool":0}`, `{"tool":1e12}`, `{"tool":1e-12}`,
		`{"tool":0.000000000001e24}`, `{"tool":1000000000000000000000000e-12}`,
		`{"tool":0.0000000000001e1}`, `{"tool":1000000000000.000000000000}`,
		`{"Custom tool.name":2.5}`, `{"tool:model-prefix*":3}`,
	} {
		assert.NoError(t, ValidateToolPricesJSON(raw), raw)
	}
}

func TestToolPriceParserBoundsSizeCardinalityAndCoefficient(t *testing.T) {
	oversized := "{" + strings.Repeat(" ", MaxToolPriceOptionBytes) + "}"
	assert.Error(t, ValidateToolPricesJSON(oversized))

	var document strings.Builder
	document.WriteByte('{')
	for index := 0; index <= MaxToolPriceRules; index++ {
		if index > 0 {
			document.WriteByte(',')
		}
		fmt.Fprintf(&document, "%q:1", fmt.Sprintf("t%d", index))
	}
	document.WriteByte('}')
	require.Less(t, document.Len(), MaxToolPriceOptionBytes)
	assert.Error(t, ValidateToolPricesJSON(document.String()))

	assert.Error(t, ValidateToolPricesJSON(`{"tool":12345678901234567890123456}`))
	assert.Error(t, ValidateToolPricesJSON(`{"`+strings.Repeat("x", MaxToolPriceRuleBytes+1)+`":1}`))
}

func TestToolPriceHostileExponentsRejectBeforeDecimalOperations(t *testing.T) {
	tests := []string{
		`{"tool":1e2147483647}`,
		`{"tool":1e-2147483648}`,
		`{"tool":1e25}`,
		`{"tool":1e-25}`,
		`{"tool":0.000000000001e25}`,
		`{"tool":0e2147483647}`,
	}
	done := make(chan error, 1)
	go func() {
		for iteration := 0; iteration < 1_000; iteration++ {
			for _, raw := range tests {
				if err := ValidateToolPricesJSON(raw); err == nil {
					done <- fmt.Errorf("accepted hostile exponent: %s", raw)
					return
				}
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("hostile exponents were not rejected within the hard timeout")
	}
}

func TestToolPriceAtomicReplacementPreservesOldSnapshotsAndRejectsInvalid(t *testing.T) {
	original := CaptureToolPriceSnapshot().CanonicalJSON()
	t.Cleanup(func() { require.NoError(t, ReplaceToolPricesJSON(original)) })

	require.NoError(t, ReplaceToolPricesJSON(`{"custom_fn":2}`))
	oldSnapshot := CaptureToolPriceSnapshot()
	oldVersion := oldSnapshot.Version()
	require.NoError(t, ReplaceToolPricesJSON(`{"custom_fn":7}`))
	newSnapshot := CaptureToolPriceSnapshot()
	assert.Greater(t, newSnapshot.Version(), oldVersion)
	assert.Equal(t, 2.0, oldSnapshot.Resolve("custom_fn", "").Price)
	assert.Equal(t, 7.0, newSnapshot.Resolve("custom_fn", "").Price)

	beforeInvalid := CaptureToolPriceSnapshot()
	require.Error(t, ReplaceToolPricesJSON(`{"custom_fn":-1}`))
	afterInvalid := CaptureToolPriceSnapshot()
	assert.Equal(t, beforeInvalid.Version(), afterInvalid.Version())
	assert.Equal(t, beforeInvalid.CanonicalJSON(), afterInvalid.CanonicalJSON())
}

func TestToolPriceConcurrentSnapshotReadsRemainCoherent(t *testing.T) {
	original := CaptureToolPriceSnapshot().CanonicalJSON()
	t.Cleanup(func() { require.NoError(t, ReplaceToolPricesJSON(original)) })
	require.NoError(t, ReplaceToolPricesJSON(`{"custom_fn":1}`))

	const readers = 16
	const iterations = 200
	var wait sync.WaitGroup
	errorsFound := make(chan error, readers)
	for reader := 0; reader < readers; reader++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				snapshot := CaptureToolPriceSnapshot()
				first := snapshot.Resolve("custom_fn", "model").Price
				second := snapshot.Resolve("custom_fn", "model").Price
				if first != second || (first != 1 && first != 2) {
					errorsFound <- fmt.Errorf("incoherent snapshot price %v then %v", first, second)
					return
				}
			}
		}()
	}
	for iteration := 0; iteration < iterations; iteration++ {
		price := 1
		if iteration%2 == 0 {
			price = 2
		}
		require.NoError(t, ReplaceToolPricesJSON(fmt.Sprintf(`{"custom_fn":%d}`, price)))
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}
}

func TestToolPriceOptionDefaultsAndMissingOption(t *testing.T) {
	defaults := ToolPriceOptionDefaults()
	assert.Equal(t, "{}", defaults[ToolPriceOption])
	defaults[ToolPriceOption] = `{"mutated":1}`
	assert.Equal(t, "{}", ToolPriceOptionDefaults()[ToolPriceOption])

	original := CaptureToolPriceSnapshot().CanonicalJSON()
	t.Cleanup(func() { require.NoError(t, ReplaceToolPricesJSON(original)) })
	require.NoError(t, refreshToolPriceSetting(map[string]string{}))
	assert.Equal(t, "{}", CaptureToolPriceSnapshot().CanonicalJSON())
	version := CaptureToolPriceSnapshot().Version()
	require.Error(t, refreshToolPriceSetting(map[string]string{ToolPriceOption: ""}))
	assert.Equal(t, version, CaptureToolPriceSnapshot().Version())
}
