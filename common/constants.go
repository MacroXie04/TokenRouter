// Package common provides shared infrastructure used across TokenRouter:
// configuration, logging, JSON handling, quota math, cryptography, caching,
// and rate limiting. It has no dependency on the reference implementation and
// is original to TokenRouter.
package common

import "fmt"

// Version is set at build time via -ldflags "-X github.com/tokenrouter/tokenrouter/common.Version=...".
// It defaults to "dev" for local builds.
var Version = "dev"

// QuotaPerUnit is the internal accounting unit per one US dollar of value.
// A quota column value of QuotaPerUnit therefore represents $1.00.
const QuotaPerUnit = 500000

// UserDisplayRole values.
const (
	RoleCommonUser = 1
	RoleAdminUser  = 10
	RoleRootUser   = 100
)

// System model names used internally by the gateway.
const (
	ModelForDalle = "dall-e"
	ModelForMidjourney = "midjourney"
	ModelForSuno    = "suno"
)

// Product identity.
const (
	ProductName    = "TokenRouter"
	ProductVersion = "1.0.0"
)

// NewID returns a stable placeholder identity helper; real IDs come from
// google/uuid in model code. Kept here to avoid import cycles in constants.
func init() {
	// Guard against accidental zero quota unit.
	if QuotaPerUnit <= 0 {
		panic(fmt.Sprintf("invalid QuotaPerUnit: %d", QuotaPerUnit))
	}
}
