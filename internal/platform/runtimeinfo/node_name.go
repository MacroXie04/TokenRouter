package runtimeinfo

import (
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"strings"
)

// NodeName returns the configured node name for this instance.
func NodeName() string {
	return strings.TrimSpace(env.GetEnv("NODE_NAME", "tokenrouter-node-1"))
}
