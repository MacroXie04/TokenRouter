// Package buildinfo exposes product identity and linker-supplied build metadata.
package buildinfo

// Version is set via -ldflags "-X github.com/tokenrouter/tokenrouter/internal/platform/buildinfo.Version=...".
// It defaults to "dev" for local builds.
var Version = "dev"

// Product identity.
const (
	ProductName    = "TokenRouter"
	ProductVersion = "1.0.0"
)
