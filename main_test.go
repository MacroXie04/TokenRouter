package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInjectAnalytics(t *testing.T) {
	html := `<html><head><title>X</title></head><body></body></html>`

	// No analytics configured -> unchanged.
	t.Setenv("GOOGLE_ANALYTICS_ID", "")
	t.Setenv("UMAMI_WEBSITE_ID", "")
	assert.Equal(t, html, injectAnalytics(html))

	// Google Analytics + Umami -> injected before </head>.
	t.Setenv("GOOGLE_ANALYTICS_ID", "G-XXX")
	t.Setenv("UMAMI_WEBSITE_ID", "umami-id")
	out := injectAnalytics(html)
	assert.Contains(t, out, "googletagmanager.com/gtag/js?id=G-XXX")
	assert.Contains(t, out, "data-website-id=\"umami-id\"")
	assert.Contains(t, out, "</head>")
}
