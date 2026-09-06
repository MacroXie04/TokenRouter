package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseHeaderNavAccessCompatibilityForms(t *testing.T) {
	fallback := headerNavAccess{enabled: true}
	tests := []struct {
		name  string
		value any
		want  headerNavAccess
	}{
		{name: "legacy disabled", value: false, want: headerNavAccess{}},
		{name: "legacy string", value: "0", want: headerNavAccess{}},
		{name: "numeric enabled", value: float64(1), want: headerNavAccess{enabled: true}},
		{name: "structured private", value: map[string]any{"enabled": true, "requireAuth": true}, want: headerNavAccess{enabled: true, requireAuth: true}},
		{name: "structured hidden", value: map[string]any{"enabled": false, "requireAuth": true}, want: headerNavAccess{requireAuth: true}},
		{name: "invalid falls back", value: map[string]any{"enabled": "maybe", "requireAuth": 3.0}, want: fallback},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, parseHeaderNavAccess(testCase.value, fallback))
		})
	}
}
