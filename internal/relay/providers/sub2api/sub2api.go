// Package sub2api implements the Sub2API gateway provider. Its wire contract
// is the NewAPI gateway contract with a distinct persisted provider identity.
package sub2api

import "github.com/tokenrouter/tokenrouter/internal/relay/providers/newapi"

const ChannelName = "sub2api"

func ModelList() []string { return []string{} }

type Adaptor struct {
	newapi.Adaptor
}
