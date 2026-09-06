package engine

import (
	"errors"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	awsprovider "github.com/tokenrouter/tokenrouter/internal/relay/providers/aws"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"sort"
	"strings"
)

func prepareToolPricingAttempt(info *RelayInfo) error {
	if info == nil || info.Channel == nil {
		return errors.New("tool pricing channel is unavailable")
	}
	pricingContext := toolPricingContext(info, toolBillingProvider(info.Channel))
	counter, err := billingsvc.NewToolUsageCounter(
		info.ToolPriceSnapshot,
		pricingContext,
	)
	if err != nil {
		return err
	}
	info.ToolUsageCounter = counter
	hooks := &relaycommon.ToolUsageHooks{}
	switch pricingContext.Mode {
	case billingsvc.ToolBillingModeResponses:
		hooks.ObserveResponsesOutput = func(observation relaycommon.ToolResponsesObservation) error {
			_, err := counter.ObserveResponsesOutput(billingsvc.ResponsesToolOutput{
				Type: observation.Type, ID: observation.ID, CallID: observation.CallID,
				OutputIndex: observation.OutputIndex, Name: observation.Name,
				Status: observation.Status, Result: observation.Result,
			})
			return err
		}
		hooks.FinishResponses = counter.FinishResponses
	case billingsvc.ToolBillingModeChatCompletions:
		hooks.ObserveChatToolCall = func(observation relaycommon.ToolChatObservation) error {
			_, err := counter.ObserveChatToolCall(billingsvc.ChatToolCallObservation{
				ChoiceIndex: observation.ChoiceIndex, ToolIndex: observation.ToolIndex,
				ArrayIndex: observation.ArrayIndex, ID: observation.ID, Name: observation.Name,
			})
			return err
		}
	case billingsvc.ToolBillingModeClaudeMessages:
		hooks.ObserveClaudeToolUse = func(observation relaycommon.ToolClaudeObservation) error {
			_, err := counter.ObserveClaudeToolUse(billingsvc.ClaudeToolUseObservation{
				BlockIndex: observation.BlockIndex, ID: observation.ID, Name: observation.Name,
			})
			return err
		}
		hooks.SetClaudeWebSearchCount = counter.SetClaudeWebSearchRequests
	case billingsvc.ToolBillingModeGeminiNative:
		hooks.MarkGeminiGoogleSearch = counter.MarkGeminiGoogleSearch
	case billingsvc.ToolBillingModeAlphaSearch:
		hooks.MarkAlphaSearchComplete = counter.MarkAlphaSearchCompleted
	}
	info.ToolUsageHooks = hooks
	return nil
}

func toolPricingContext(info *RelayInfo, provider billingsvc.ToolBillingProvider) billingsvc.ToolPricingContext {
	context := billingsvc.ToolPricingContext{
		Mode: billingsvc.ToolBillingModeChatCompletions, Provider: provider,
	}
	if info == nil {
		return context
	}
	context.ModelName = info.ModelName
	context.DeclaredBuiltInTools = append([]string(nil), info.ToolDeclaredNames...)
	switch {
	case info.Mode == channelcatalog.RelayModeAlphaSearch:
		context.Mode = billingsvc.ToolBillingModeAlphaSearch
	case info.Mode == channelcatalog.RelayModeResponses || info.Format == channelcatalog.RelayFormatOpenAIResponses:
		context.Mode = billingsvc.ToolBillingModeResponses
	case info.Format == channelcatalog.RelayFormatClaude:
		context.Mode = billingsvc.ToolBillingModeClaudeMessages
	case info.Format == channelcatalog.RelayFormatGemini:
		context.Mode = billingsvc.ToolBillingModeGeminiNative
	case info.Channel != nil && toolResponseModeForChannel(info.Channel, info.ModelName) == billingsvc.ToolBillingModeClaudeMessages:
		context.Mode = billingsvc.ToolBillingModeClaudeMessages
	case info.Channel != nil && toolResponseModeForChannel(info.Channel, info.ModelName) == billingsvc.ToolBillingModeGeminiNative:
		context.Mode = billingsvc.ToolBillingModeGeminiNative
	}
	return context
}

func toolResponseModeForChannel(channel *model.Channel, modelName string) billingsvc.ToolBillingMode {
	if channel == nil {
		return billingsvc.ToolBillingModeChatCompletions
	}
	switch channelcatalog.ChannelType(channel.Type) {
	case channelcatalog.ChannelTypeAnthropic:
		return billingsvc.ToolBillingModeClaudeMessages
	case channelcatalog.ChannelTypeAws:
		if !awsprovider.IsNovaModel(relaycommon.GetMappedModel(channel, modelName)) {
			return billingsvc.ToolBillingModeClaudeMessages
		}
	case channelcatalog.ChannelTypeGemini, channelcatalog.ChannelTypeVertexAi:
		return billingsvc.ToolBillingModeGeminiNative
	}
	return billingsvc.ToolBillingModeChatCompletions
}

func toolBillingProvider(channel *model.Channel) billingsvc.ToolBillingProvider {
	if channel == nil {
		return billingsvc.ToolBillingProviderOther
	}
	switch channelcatalog.ChannelType(channel.Type) {
	case channelcatalog.ChannelTypeOpenAI, channelcatalog.ChannelTypeAzure, channelcatalog.ChannelTypeCodex:
		return billingsvc.ToolBillingProviderOpenAI
	case channelcatalog.ChannelTypeAnthropic, channelcatalog.ChannelTypeAws:
		return billingsvc.ToolBillingProviderAnthropic
	case channelcatalog.ChannelTypeGemini, channelcatalog.ChannelTypeVertexAi:
		return billingsvc.ToolBillingProviderGemini
	default:
		return billingsvc.ToolBillingProviderOther
	}
}

func declaredToolPricingNames(info *RelayInfo) []string {
	if info == nil {
		return nil
	}
	seen := make(map[string]struct{})
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	addTool := func(toolType, functionName string) {
		switch strings.ToLower(strings.TrimSpace(toolType)) {
		case setting.ToolWebSearch, "web_search_20250305":
			add(setting.ToolWebSearch)
		case setting.ToolWebSearchPreview:
			add(setting.ToolWebSearchPreview)
		case setting.ToolFileSearch:
			add(setting.ToolFileSearch)
		case setting.ToolImageGeneration, "image_generation_call":
			add(setting.ToolImageGeneration)
		case "function", "custom":
			add(functionName)
		default:
			add(functionName)
		}
	}
	if info.Request != nil {
		for _, tool := range info.Request.Tools {
			name := ""
			if tool.Function != nil {
				name = tool.Function.Name
			}
			addTool(tool.Type, name)
		}
		if info.Request.WebSearchOptions != nil {
			add(setting.ToolWebSearchPreview)
		}
	}
	if info.ClaudeRequest != nil {
		for _, tool := range info.ClaudeRequest.Tools {
			if strings.HasPrefix(strings.ToLower(tool.Name), "web_search") {
				add(setting.ToolWebSearch)
			} else {
				add(tool.Name)
			}
		}
	}
	if info.GeminiRequest != nil {
		for _, tool := range info.GeminiRequest.Tools {
			if tool.GoogleSearch != nil || tool.GoogleSearchRetrieval != nil {
				add(setting.ToolGoogleSearch)
			}
			for _, function := range tool.FunctionDeclarations {
				add(function.Name)
			}
		}
	}
	if info.Mode == channelcatalog.RelayModeAlphaSearch {
		add(setting.ToolWebSearchPreview)
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
