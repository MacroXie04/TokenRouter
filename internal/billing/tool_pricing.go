package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ToolBillingMode string
type ToolBillingProvider string

const (
	ToolBillingModeResponses       ToolBillingMode = "responses"
	ToolBillingModeChatCompletions ToolBillingMode = "chat_completions"
	ToolBillingModeClaudeMessages  ToolBillingMode = "claude_messages"
	ToolBillingModeGeminiNative    ToolBillingMode = "gemini_generate_content"
	ToolBillingModeAlphaSearch     ToolBillingMode = "alpha_search"

	ToolBillingProviderOpenAI    ToolBillingProvider = "openai"
	ToolBillingProviderAnthropic ToolBillingProvider = "anthropic"
	ToolBillingProviderGemini    ToolBillingProvider = "gemini"
	ToolBillingProviderOther     ToolBillingProvider = "other"

	ResponsesToolCallWebSearch       = "web_search_call"
	ResponsesToolCallFileSearch      = "file_search_call"
	ResponsesToolCallFunction        = "function_call"
	ResponsesToolCallImageGeneration = "image_generation_call"

	MaxObservedToolCalls        = 4_096
	MaxObservedToolNames        = 1_024
	MaxBillableToolCallCount    = 1_000_000
	MaxImageGenerationCalls     = 128
	MaxImageIdentityAliases     = MaxImageGenerationCalls * 4
	MaxToolObservationTextBytes = 512
	MaxImageResultIdentityBytes = 64 << 20
	MaxToolBillingRatio         = 1_000_000_000_000
)

var (
	ErrInvalidToolPricingContext = errors.New("invalid tool pricing context")
	ErrToolPricingMode           = errors.New("tool observation is incompatible with billing mode")
	ErrToolPricingFinalized      = errors.New("tool pricing counter is finalized")
	ErrAmbiguousToolIdentity     = errors.New("ambiguous tool call identity")
	ErrToolObservationLimit      = errors.New("tool observation exceeds safe limit")
)

// ToolPricingContext is captured with the request. Provider and mode are
// explicit so native built-ins cannot accidentally pass through the custom
// function-call path.
type ToolPricingContext struct {
	Mode                 ToolBillingMode
	Provider             ToolBillingProvider
	ModelName            string
	DeclaredBuiltInTools []string
}

type ResponsesToolOutput struct {
	Type        string
	ID          string
	CallID      string
	OutputIndex *int
	Name        string
	Status      string
	Result      string
}

type ChatToolCallObservation struct {
	ChoiceIndex int
	ToolIndex   *int
	ArrayIndex  int
	ID          string
	Name        string
}

type ClaudeToolUseObservation struct {
	BlockIndex *int
	ID         string
	Name       string
}

type toolIdentityRecord struct {
	signature string
}

// ToolUsageCounter owns one request-start price snapshot. It is deliberately
// not shared between requests; callers observing a retry should call
// ResetForRetry before feeding the next upstream attempt.
type ToolUsageCounter struct {
	snapshot      setting.ToolPriceSnapshot
	context       ToolPricingContext
	webSearchTool string

	identities    map[string]*toolIdentityRecord
	identityCount int
	counts        map[string]int
	reported      map[string]int

	imageAliases map[string]struct{}
	imageCount   int
	geminiSearch bool
	alphaSearch  bool
	responsesEnd bool
	countCapped  bool
	settlement   *ToolPricingSettlement
}

// ToolSurchargeItem retains reference-compatible JSON while carrying the
// request-time rule metadata needed for deterministic internal audit.
type ToolSurchargeItem struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Price float64 `json:"price"`

	PriceExact      string              `json:"-"`
	Rule            string              `json:"-"`
	PriceSource     string              `json:"-"`
	ModelName       string              `json:"-"`
	Mode            ToolBillingMode     `json:"-"`
	Provider        ToolBillingProvider `json:"-"`
	SnapshotVersion uint64              `json:"-"`
}

// ToolPricingSettlement is immutable after construction. Accessors always
// detach the item slice before returning it.
type ToolPricingSettlement struct {
	Quota          int
	ExactQuota     string
	CountSaturated bool
	Clamp          *quotamath.QuotaClamp
	items          []ToolSurchargeItem
}

func (settlement ToolPricingSettlement) Items() []ToolSurchargeItem {
	return append([]ToolSurchargeItem(nil), settlement.items...)
}

// AuditFields returns the reference-compatible structured log field. An empty
// settlement returns an empty map so callers do not publish a false marker.
func (settlement ToolPricingSettlement) AuditFields() map[string]any {
	items := settlement.Items()
	if len(items) == 0 {
		return map[string]any{}
	}
	return map[string]any{"tool_surcharges": items}
}

func cloneToolPricingSettlement(settlement ToolPricingSettlement) ToolPricingSettlement {
	copyOf := settlement
	copyOf.items = settlement.Items()
	return copyOf
}

func NewToolUsageCounter(snapshot setting.ToolPriceSnapshot, context ToolPricingContext) (*ToolUsageCounter, error) {
	if err := validateToolPricingContext(context); err != nil {
		return nil, err
	}
	context.DeclaredBuiltInTools = append([]string(nil), context.DeclaredBuiltInTools...)
	webSearchTool := setting.ToolWebSearchPreview
	declaredWebSearch := false
	for _, toolName := range context.DeclaredBuiltInTools {
		if toolName == setting.ToolWebSearchPreview {
			webSearchTool = setting.ToolWebSearchPreview
			declaredWebSearch = false
			break
		}
		if toolName == setting.ToolWebSearch {
			declaredWebSearch = true
		}
	}
	if declaredWebSearch {
		webSearchTool = setting.ToolWebSearch
	}
	return &ToolUsageCounter{
		snapshot:      snapshot.Frozen(),
		context:       context,
		webSearchTool: webSearchTool,
		identities:    make(map[string]*toolIdentityRecord),
		counts:        make(map[string]int),
		reported:      make(map[string]int),
		imageAliases:  make(map[string]struct{}),
	}, nil
}

// MinimumPotentialToolSurcharge returns a one-call reservation floor for each
// billable tool the request explicitly enables (plus protocol-defined implicit
// search calls). It is not the final charge: authoritative response
// observations are settled later. The floor prevents a free token-price model
// from selecting the durable free_model funding source when tools can still
// incur a charge.
func MinimumPotentialToolSurcharge(
	snapshot setting.ToolPriceSnapshot,
	context ToolPricingContext,
	groupRatio float64,
) (quota int, billable bool, err error) {
	if err := validateToolPricingContext(context); err != nil {
		return 0, false, err
	}
	if math.IsNaN(groupRatio) || math.IsInf(groupRatio, 0) || groupRatio < 0 || groupRatio > MaxToolBillingRatio {
		return 0, false, fmt.Errorf("%w: invalid group ratio", ErrInvalidToolPricingContext)
	}
	candidates := make(map[string]struct{}, len(context.DeclaredBuiltInTools)+1)
	for _, name := range context.DeclaredBuiltInTools {
		candidates[name] = struct{}{}
	}
	if context.Mode == ToolBillingModeAlphaSearch ||
		(context.Mode != ToolBillingModeResponses && strings.HasSuffix(context.ModelName, "search-preview")) {
		candidates[setting.ToolWebSearchPreview] = struct{}{}
	}
	if len(candidates) == 0 {
		return 0, false, nil
	}
	frozen := snapshot.Frozen()
	total := decimal.Zero
	for name := range candidates {
		resolution := frozen.Resolve(name, context.ModelName)
		if !resolution.Matched || !resolution.ExactPrice().GreaterThan(decimal.Zero) {
			continue
		}
		billable = true
		total = total.Add(resolution.ExactPrice())
	}
	if groupRatio == 0 {
		return 0, false, nil
	}
	if !billable {
		return 0, false, nil
	}
	total = total.
		Div(decimal.NewFromInt(1000)).
		Mul(decimal.NewFromFloat(groupRatio)).
		Mul(decimal.NewFromInt(int64(quotamath.QuotaPerUnit)))
	quota, err = quotamath.QuotaFromDecimalStrict(total.Round(0))
	if err != nil || quota < 0 {
		if err == nil {
			err = ErrInvalidQuota
		}
		return 0, false, fmt.Errorf("minimum tool surcharge quota overflow: %w", err)
	}
	return quota, billable, nil
}

func validateToolPricingContext(context ToolPricingContext) error {
	switch context.Mode {
	case ToolBillingModeResponses, ToolBillingModeChatCompletions, ToolBillingModeClaudeMessages,
		ToolBillingModeGeminiNative, ToolBillingModeAlphaSearch:
	default:
		return fmt.Errorf("%w: unknown mode %q", ErrInvalidToolPricingContext, context.Mode)
	}
	if context.Provider == "" || !validToolObservationText(string(context.Provider), false, 64) {
		return fmt.Errorf("%w: invalid provider", ErrInvalidToolPricingContext)
	}
	if !validToolObservationText(context.ModelName, true, MaxToolObservationTextBytes) {
		return fmt.Errorf("%w: invalid model name", ErrInvalidToolPricingContext)
	}
	if len(context.DeclaredBuiltInTools) > 128 {
		return fmt.Errorf("%w: too many declared tools", ErrInvalidToolPricingContext)
	}
	for _, toolName := range context.DeclaredBuiltInTools {
		if !validToolObservationText(toolName, false, MaxToolObservationTextBytes) || toolName != strings.TrimSpace(toolName) {
			return fmt.Errorf("%w: invalid declared tool", ErrInvalidToolPricingContext)
		}
	}
	return nil
}

// ObserveResponsesOutput counts only actual output items. Tool declarations
// are constructor context and never increment a count.
func (counter *ToolUsageCounter) ObserveResponsesOutput(output ResponsesToolOutput) (bool, error) {
	if err := counter.requireOpenMode(ToolBillingModeResponses); err != nil {
		return false, err
	}
	if counter.responsesEnd {
		return false, ErrToolPricingFinalized
	}
	if err := validateResponseToolOutput(output); err != nil {
		return false, err
	}
	switch output.Type {
	case ResponsesToolCallWebSearch:
		return counter.observeIdentified(
			responsesOutputAliases(output), "builtin:"+counter.webSearchTool, counter.webSearchTool,
		)
	case ResponsesToolCallFileSearch:
		return counter.observeIdentified(
			responsesOutputAliases(output), "builtin:"+setting.ToolFileSearch, setting.ToolFileSearch,
		)
	case ResponsesToolCallFunction:
		return counter.observeCustom(
			responsesOutputAliases(output), "responses:function:"+output.Name, output.Name,
		)
	case ResponsesToolCallImageGeneration:
		return counter.observeImage(output)
	default:
		return false, nil
	}
}

func validateResponseToolOutput(output ResponsesToolOutput) error {
	if !validToolObservationText(output.Type, false, 64) ||
		!validToolObservationText(output.ID, true, MaxToolObservationTextBytes) ||
		!validToolObservationText(output.CallID, true, MaxToolObservationTextBytes) ||
		!validToolObservationText(output.Name, true, MaxToolObservationTextBytes) ||
		!validToolObservationText(output.Status, true, 64) {
		return fmt.Errorf("%w: invalid Responses output", ErrInvalidToolPricingContext)
	}
	if output.OutputIndex != nil && (*output.OutputIndex < 0 || *output.OutputIndex > MaxBillableToolCallCount) {
		return fmt.Errorf("%w: invalid Responses output index", ErrInvalidToolPricingContext)
	}
	return nil
}

func responsesOutputAliases(output ResponsesToolOutput) []string {
	aliases := make([]string, 0, 3)
	if output.ID != "" {
		aliases = append(aliases, "responses:id:"+output.ID)
	}
	if output.CallID != "" {
		aliases = append(aliases, "responses:call:"+output.CallID)
	}
	if output.OutputIndex != nil {
		aliases = append(aliases, fmt.Sprintf("responses:index:%d", *output.OutputIndex))
	}
	return aliases
}

func (counter *ToolUsageCounter) ObserveChatToolCall(observation ChatToolCallObservation) (bool, error) {
	if err := counter.requireOpenMode(ToolBillingModeChatCompletions); err != nil {
		return false, err
	}
	if observation.ChoiceIndex < 0 || observation.ChoiceIndex > MaxBillableToolCallCount ||
		observation.ArrayIndex < 0 || observation.ArrayIndex > MaxBillableToolCallCount ||
		(observation.ToolIndex != nil && (*observation.ToolIndex < 0 || *observation.ToolIndex > MaxBillableToolCallCount)) ||
		!validToolObservationText(observation.ID, true, MaxToolObservationTextBytes) ||
		!validToolObservationText(observation.Name, true, MaxToolObservationTextBytes) {
		return false, fmt.Errorf("%w: invalid Chat tool call", ErrInvalidToolPricingContext)
	}
	aliases := make([]string, 0, 2)
	if observation.ID != "" {
		aliases = append(aliases, "chat:id:"+observation.ID)
	}
	toolIndex := observation.ArrayIndex
	if observation.ToolIndex != nil {
		toolIndex = *observation.ToolIndex
	}
	aliases = append(aliases, fmt.Sprintf("chat:choice:%d:tool:%d", observation.ChoiceIndex, toolIndex))
	return counter.observeCustom(aliases, "chat:function:"+observation.Name, observation.Name)
}

func (counter *ToolUsageCounter) ObserveClaudeToolUse(observation ClaudeToolUseObservation) (bool, error) {
	if err := counter.requireOpenMode(ToolBillingModeClaudeMessages); err != nil {
		return false, err
	}
	if (observation.BlockIndex != nil && (*observation.BlockIndex < 0 || *observation.BlockIndex > MaxBillableToolCallCount)) ||
		!validToolObservationText(observation.ID, true, MaxToolObservationTextBytes) ||
		!validToolObservationText(observation.Name, true, MaxToolObservationTextBytes) {
		return false, fmt.Errorf("%w: invalid Claude tool use", ErrInvalidToolPricingContext)
	}
	aliases := make([]string, 0, 2)
	if observation.ID != "" {
		aliases = append(aliases, "claude:id:"+observation.ID)
	}
	if observation.BlockIndex != nil {
		aliases = append(aliases, fmt.Sprintf("claude:index:%d", *observation.BlockIndex))
	}
	return counter.observeCustom(aliases, "claude:tool_use:"+observation.Name, observation.Name)
}

// SetClaudeWebSearchRequests stores the provider's authoritative cumulative
// usage count. Replayed usage frames replace, rather than add to, this value.
func (counter *ToolUsageCounter) SetClaudeWebSearchRequests(count int) error {
	if err := counter.requireOpenMode(ToolBillingModeClaudeMessages); err != nil {
		return err
	}
	if count < 0 || count > MaxBillableToolCallCount {
		return fmt.Errorf("%w: invalid Claude web-search count", ErrToolObservationLimit)
	}
	counter.reported[setting.ToolWebSearch] = count
	return nil
}

func (counter *ToolUsageCounter) MarkGeminiGoogleSearch() error {
	if err := counter.requireOpenMode(ToolBillingModeGeminiNative); err != nil {
		return err
	}
	counter.geminiSearch = true
	return nil
}

func (counter *ToolUsageCounter) MarkAlphaSearchCompleted() error {
	if err := counter.requireOpenMode(ToolBillingModeAlphaSearch); err != nil {
		return err
	}
	counter.alphaSearch = true
	return nil
}

func (counter *ToolUsageCounter) observeCustom(aliases []string, signature, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	if name != strings.TrimSpace(name) || !validToolObservationText(name, false, MaxToolObservationTextBytes) {
		return false, fmt.Errorf("%w: invalid custom tool name", ErrInvalidToolPricingContext)
	}
	if isReservedToolName(name) {
		return counter.observeIdentified(aliases, signature, "")
	}
	resolution := counter.snapshot.Resolve(name, counter.context.ModelName)
	if !resolution.Matched || !resolution.ExactPrice().GreaterThan(decimal.Zero) {
		return counter.observeIdentified(aliases, signature, "")
	}
	return counter.observeIdentified(aliases, signature, name)
}

func isReservedToolName(name string) bool {
	switch name {
	case setting.ToolWebSearchPreview, setting.ToolWebSearch, setting.ToolFileSearch,
		setting.ToolGoogleSearch, setting.ToolImageGeneration:
		return true
	default:
		return false
	}
}

func (counter *ToolUsageCounter) observeIdentified(aliases []string, signature, chargeName string) (bool, error) {
	if len(aliases) == 0 {
		return false, fmt.Errorf("%w: tool call has no stable identity", ErrAmbiguousToolIdentity)
	}
	var existing *toolIdentityRecord
	for _, alias := range aliases {
		if record := counter.identities[alias]; record != nil {
			if existing != nil && existing != record {
				return false, fmt.Errorf("%w: aliases refer to different calls", ErrAmbiguousToolIdentity)
			}
			existing = record
		}
	}
	if existing != nil {
		if existing.signature != signature {
			return false, fmt.Errorf("%w: replay changed call kind or name", ErrAmbiguousToolIdentity)
		}
		newAliases := 0
		for _, alias := range aliases {
			if counter.identities[alias] == nil {
				newAliases++
			}
		}
		if len(counter.identities)+newAliases > MaxObservedToolCalls*3 {
			return false, ErrToolObservationLimit
		}
		for _, alias := range aliases {
			counter.identities[alias] = existing
		}
		return false, nil
	}
	newAliases := 0
	for _, alias := range aliases {
		if counter.identities[alias] == nil {
			newAliases++
		}
	}
	if len(counter.identities)+newAliases > MaxObservedToolCalls*3 {
		return false, ErrToolObservationLimit
	}
	if counter.identityCount >= MaxObservedToolCalls {
		return false, ErrToolObservationLimit
	}
	if chargeName != "" {
		if _, exists := counter.counts[chargeName]; !exists && len(counter.counts) >= MaxObservedToolNames {
			return false, ErrToolObservationLimit
		}
	}
	record := &toolIdentityRecord{signature: signature}
	for _, alias := range aliases {
		counter.identities[alias] = record
	}
	counter.identityCount++
	if chargeName == "" {
		return false, nil
	}
	if err := counter.addCount(chargeName, 1); err != nil {
		return false, err
	}
	return true, nil
}

func (counter *ToolUsageCounter) addCount(name string, increment int) error {
	if increment <= 0 {
		return nil
	}
	current, exists := counter.counts[name]
	if !exists && len(counter.counts) >= MaxObservedToolNames {
		return ErrToolObservationLimit
	}
	if current >= MaxBillableToolCallCount || increment > MaxBillableToolCallCount-current {
		counter.counts[name] = MaxBillableToolCallCount
		counter.countCapped = true
		return nil
	}
	counter.counts[name] = current + increment
	return nil
}

func (counter *ToolUsageCounter) observeImage(output ResponsesToolOutput) (bool, error) {
	if strings.TrimSpace(output.Result) == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(output.Status)) {
	case "failed", "cancelled", "canceled", "incomplete", "partial":
		return false, nil
	}
	if counter.imageCount >= MaxImageGenerationCalls {
		return false, nil
	}
	if len(output.Result) > MaxImageResultIdentityBytes {
		return false, fmt.Errorf("%w: image result identity exceeds %d bytes", ErrToolObservationLimit, MaxImageResultIdentityBytes)
	}
	aliases := responsesOutputAliases(output)
	hash := sha256.Sum256([]byte(output.Result))
	aliases = append(aliases, "responses:image:result:"+hex.EncodeToString(hash[:]))
	duplicate := false
	newAliases := 0
	for _, alias := range aliases {
		if _, seen := counter.imageAliases[alias]; seen {
			duplicate = true
		} else {
			newAliases++
		}
	}
	if len(counter.imageAliases)+newAliases > MaxImageIdentityAliases {
		if duplicate {
			return false, nil
		}
		return false, ErrToolObservationLimit
	}
	for _, alias := range aliases {
		counter.imageAliases[alias] = struct{}{}
	}
	if duplicate {
		return false, nil
	}
	counter.imageCount++
	return true, nil
}

// FinishResponses closes the Responses stream. The reference only discards
// pending image outputs on failed/incomplete/cancelled terminals; completed
// web, file, and custom calls remain observed.
func (counter *ToolUsageCounter) FinishResponses(status string) error {
	if err := counter.requireOpenMode(ToolBillingModeResponses); err != nil {
		return err
	}
	if status != strings.TrimSpace(status) || !validToolObservationText(status, false, 64) {
		return fmt.Errorf("%w: invalid Responses status", ErrInvalidToolPricingContext)
	}
	switch strings.ToLower(status) {
	case "failed", "incomplete", "cancelled", "canceled":
		counter.imageAliases = make(map[string]struct{})
		counter.imageCount = 0
		counter.responsesEnd = true
	case "completed", "done":
		counter.responsesEnd = true
	}
	return nil
}

// ResetForRetry discards every upstream-attempt observation but preserves the
// request's immutable price snapshot and mode/provider context.
func (counter *ToolUsageCounter) ResetForRetry() error {
	if counter == nil {
		return ErrInvalidToolPricingContext
	}
	if counter.settlement != nil {
		return ErrToolPricingFinalized
	}
	counter.identities = make(map[string]*toolIdentityRecord)
	counter.identityCount = 0
	counter.counts = make(map[string]int)
	counter.reported = make(map[string]int)
	counter.imageAliases = make(map[string]struct{})
	counter.imageCount = 0
	counter.geminiSearch = false
	counter.alphaSearch = false
	counter.responsesEnd = false
	counter.countCapped = false
	counter.settlement = nil
	return nil
}

func (counter *ToolUsageCounter) requireOpenMode(mode ToolBillingMode) error {
	if counter == nil {
		return ErrInvalidToolPricingContext
	}
	if counter.settlement != nil {
		return ErrToolPricingFinalized
	}
	if counter.context.Mode != mode {
		return fmt.Errorf("%w: have %s, need %s", ErrToolPricingMode, counter.context.Mode, mode)
	}
	return nil
}

// Settle computes the exact surcharge and then performs one parity-compatible
// half-away-from-zero rounding step before a strict int32 quota conversion.
// Overflow returns an error; it is never silently clamped into a charge.
func (counter *ToolUsageCounter) Settle(groupRatio float64) (ToolPricingSettlement, error) {
	if counter == nil {
		return ToolPricingSettlement{}, ErrInvalidToolPricingContext
	}
	if counter.settlement != nil {
		return cloneToolPricingSettlement(*counter.settlement), nil
	}
	if math.IsNaN(groupRatio) || math.IsInf(groupRatio, 0) || groupRatio < 0 || groupRatio > MaxToolBillingRatio {
		return ToolPricingSettlement{}, fmt.Errorf("%w: invalid group ratio", ErrInvalidToolPricingContext)
	}
	counts := make(map[string]int, len(counter.counts)+len(counter.reported)+3)
	for name, count := range counter.counts {
		counts[name] = count
	}
	for name, count := range counter.reported {
		counts[name], counter.countCapped = saturatedToolCount(counts[name], count, counter.countCapped)
	}
	if counter.imageCount > 0 {
		counts[setting.ToolImageGeneration], counter.countCapped = saturatedToolCount(
			counts[setting.ToolImageGeneration], counter.imageCount, counter.countCapped,
		)
	}
	if counter.geminiSearch {
		counts[setting.ToolGoogleSearch], counter.countCapped = saturatedToolCount(
			counts[setting.ToolGoogleSearch], 1, counter.countCapped,
		)
	}
	if counter.alphaSearch {
		counts[setting.ToolWebSearchPreview], counter.countCapped = saturatedToolCount(
			counts[setting.ToolWebSearchPreview], 1, counter.countCapped,
		)
	}
	if counter.context.Mode != ToolBillingModeResponses && strings.HasSuffix(counter.context.ModelName, "search-preview") {
		counts[setting.ToolWebSearchPreview], counter.countCapped = saturatedToolCount(
			counts[setting.ToolWebSearchPreview], 1, counter.countCapped,
		)
	}

	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	ratio := decimal.NewFromFloat(groupRatio)
	quotaPerUnit := decimal.NewFromInt(int64(quotamath.QuotaPerUnit))
	thousand := decimal.NewFromInt(1000)
	items := make([]ToolSurchargeItem, 0, len(names))
	total := decimal.Zero
	for _, name := range names {
		count := counts[name]
		if count <= 0 {
			continue
		}
		resolution := counter.snapshot.Resolve(name, counter.context.ModelName)
		if !resolution.Matched || !resolution.ExactPrice().GreaterThan(decimal.Zero) {
			continue
		}
		price := resolution.ExactPrice()
		total = total.Add(price.
			Mul(decimal.NewFromInt(int64(count))).
			Div(thousand).
			Mul(ratio).
			Mul(quotaPerUnit))
		items = append(items, ToolSurchargeItem{
			Name: name, Count: count, Price: resolution.Price,
			PriceExact: price.String(), Rule: resolution.Rule, PriceSource: resolution.Source,
			ModelName: counter.context.ModelName, Mode: counter.context.Mode, Provider: counter.context.Provider,
			SnapshotVersion: counter.snapshot.Version(),
		})
	}
	rounded := total.Round(0)
	quota, clamp := quotamath.QuotaFromDecimalChecked(rounded)
	if clamp != nil {
		settlement := ToolPricingSettlement{Quota: quota, ExactQuota: total.String(), Clamp: clamp, items: items}
		return settlement, fmt.Errorf("tool surcharge quota overflow: %w", clamp)
	}
	if quota < 0 {
		return ToolPricingSettlement{}, fmt.Errorf("tool surcharge quota overflow: %w", ErrInvalidQuota)
	}
	settlement := ToolPricingSettlement{
		Quota: quota, ExactQuota: total.String(), CountSaturated: counter.countCapped, items: items,
	}
	counter.settlement = &settlement
	return cloneToolPricingSettlement(settlement), nil
}

func saturatedToolCount(current, increment int, alreadyCapped bool) (int, bool) {
	if current < 0 || increment < 0 {
		return 0, true
	}
	if current >= MaxBillableToolCallCount || increment > MaxBillableToolCallCount-current {
		return MaxBillableToolCallCount, true
	}
	return current + increment, alreadyCapped
}

func validToolObservationText(value string, allowEmpty bool, maximumBytes int) bool {
	if (!allowEmpty && value == "") || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}
