package engine

import (
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
)

// RelayInfo carries state through a single relay request lifecycle.
type RelayInfo struct {
	Mode                    channelcatalog.RelayMode
	Format                  channelcatalog.RelayFormat
	ModelName               string
	Request                 *protocolkit.GeneralOpenAIRequest
	ClaudeRequest           *protocolkit.ClaudeRequest // set for Claude-format /v1/messages relays
	ClaudeMaxTokensProvided bool
	claudePolicyPrepared    bool
	GeminiRequest           *protocolkit.GeminiChatRequest
	GeminiUpstreamModel     string
	RawBody                 []byte // verbatim request body for native passthrough relays
	RequestContentType      string
	APIVersion              string
	PromptTokens            int
	Quota                   int
	QuotaClamp              *quotamath.QuotaClamp
	ToolQuotaClamp          *quotamath.QuotaClamp
	ReferencePricing        map[string]billingsvc.ReferenceBillingPlan
	FreeModelPricing        map[string]bool
	UsageBillingFields      map[string]any
	ToolBillingFields       map[string]any
	ToolPriceSnapshot       setting.ToolPriceSnapshot
	ToolDeclaredNames       []string
	ToolGroupRatios         map[string]float64
	ToolPotential           map[string]bool
	ToolUsageCounter        *billingsvc.ToolUsageCounter
	ToolUsageHooks          *relaycommon.ToolUsageHooks
	UserID                  int
	Channel                 *model.Channel
	Usage                   *protocolkit.Usage
	UserGroup               string
	Group                   string
	AuthorizedGroups        []string
	CrossGroupRetry         bool
	IsStream                bool
}

// Execute runs an already decoded OpenAI-compatible request using the explicit
// authentication snapshot supplied by its HTTP adapter. The Gin context remains
// only as the existing provider streaming/affinity transport contract.
func Execute(c *gin.Context, state relaycommon.RequestState, info *RelayInfo) error {
	return relayAndSettle(c, state, info)
}
