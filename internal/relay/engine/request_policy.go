package engine

import (
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	relaypolicy "github.com/tokenrouter/tokenrouter/internal/relay/policy"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"strings"
)

// requestContainsSensitive reports whether a request's text content contains
// any configured sensitive word.
func requestContainsSensitive(req *protocolkit.GeneralOpenAIRequest) bool {
	if req == nil {
		return false
	}
	var sb strings.Builder
	for _, m := range req.Messages {
		sb.WriteString(relaycommon.MessageToText(m))
	}
	if s, ok := req.Prompt.(string); ok {
		sb.WriteString(s)
	}
	return relaypolicy.CheckSensitiveContent(sb.String())
}
