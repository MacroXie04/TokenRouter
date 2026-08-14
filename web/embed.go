// Package web embeds the TokenRouter frontend production assets.
package web

import "embed"

// Dist is the embedded frontend build output (web/dist).
//
//go:embed dist
var Dist embed.FS
