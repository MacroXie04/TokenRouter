package setting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

const (
	// ToolPriceOption stores operator overrides as USD per 1,000 completed
	// calls. Built-in fallbacks are compiled into the immutable index and are
	// deliberately not copied into the stored option.
	ToolPriceOption = "tool_price_setting.prices"

	ToolWebSearch        = "web_search"
	ToolWebSearchPreview = "web_search_preview"
	ToolFileSearch       = "file_search"
	ToolGoogleSearch     = "google_search"
	ToolImageGeneration  = "image_generation"

	MaxToolPriceOptionBytes = 1 << 20
	MaxToolPriceRules       = 20_000
	MaxToolPriceRuleBytes   = 512
	MaxToolPriceNumberBytes = 64
	MaxToolPriceScale       = 12
	MaxToolPriceExponent    = 24
	MaxToolPriceDigits      = 25
	MaxToolPriceUSD         = 1_000_000_000_000
)

const (
	ToolPriceSourceDefault  = "default"
	ToolPriceSourceOperator = "operator"
)

var maxToolPriceDecimal = decimal.NewFromInt(MaxToolPriceUSD)

type toolPriceEntry struct {
	rule   string
	prefix string
	price  decimal.Decimal
	source string
}

type toolPriceState struct {
	version      uint64
	defaults     map[string]toolPriceEntry
	prefixes     map[string][]toolPriceEntry
	operator     map[string]decimal.Decimal
	operatorJSON string
}

// ToolPriceSnapshot is an immutable request-time view of the effective tool
// prices. Its maps are never exposed, so a hot reload cannot change a request
// already in flight.
type ToolPriceSnapshot struct {
	state *toolPriceState
}

// ToolPriceResolution records the exact rule selected for an audit-safe
// charge. Price is the compatibility float used by JSON logs; ExactPrice
// retains the parsed decimal for accounting.
type ToolPriceResolution struct {
	ToolName  string
	ModelName string
	Rule      string
	Price     float64
	Source    string
	Matched   bool
	exact     decimal.Decimal
}

func (resolution ToolPriceResolution) ExactPrice() decimal.Decimal {
	return resolution.exact
}

var (
	currentToolPriceState atomic.Pointer[toolPriceState]
	toolPriceGeneration   atomic.Uint64
)

func init() {
	state, err := parseToolPriceState("{}")
	if err != nil {
		panic(err)
	}
	publishToolPriceState(state)
}

// ToolPriceOptionDefaults returns a fresh option map on each call.
func ToolPriceOptionDefaults() map[string]string {
	return map[string]string{ToolPriceOption: "{}"}
}

func IsToolPriceOption(key string) bool {
	return key == ToolPriceOption
}

// ParseToolPriceSnapshot validates a complete operator override document and
// returns an unpublished immutable snapshot. It is useful for atomic update
// validation and deterministic request tests.
func ParseToolPriceSnapshot(raw string) (ToolPriceSnapshot, error) {
	state, err := parseToolPriceState(raw)
	if err != nil {
		return ToolPriceSnapshot{}, err
	}
	return ToolPriceSnapshot{state: state}, nil
}

func ValidateToolPricesJSON(raw string) error {
	_, err := parseToolPriceState(raw)
	return err
}

// ReplaceToolPricesJSON validates the whole candidate before publishing it.
// On failure the previous live snapshot remains available unchanged.
func ReplaceToolPricesJSON(raw string) error {
	state, err := parseToolPriceState(raw)
	if err != nil {
		return err
	}
	publishToolPriceState(state)
	return nil
}

func publishToolPriceState(candidate *toolPriceState) {
	if candidate == nil {
		return
	}
	copyOf := *candidate
	copyOf.version = toolPriceGeneration.Add(1)
	currentToolPriceState.Store(&copyOf)
}

// refreshToolPriceSetting is the bounded integration hook used by the shared
// settings loader. A missing row selects the empty operator map; an explicitly
// empty or malformed row is rejected rather than partially applied.
func refreshToolPriceSetting(options map[string]string) error {
	raw := "{}"
	if value, present := options[ToolPriceOption]; present {
		raw = value
	}
	return ReplaceToolPricesJSON(raw)
}

// CaptureToolPriceSnapshot returns the current immutable request-time policy.
func CaptureToolPriceSnapshot() ToolPriceSnapshot {
	state := currentToolPriceState.Load()
	if state == nil {
		fallback, _ := parseToolPriceState("{}")
		return ToolPriceSnapshot{state: fallback}
	}
	return ToolPriceSnapshot{state: state}
}

func (snapshot ToolPriceSnapshot) effectiveState() *toolPriceState {
	if snapshot.state != nil {
		return snapshot.state
	}
	state := currentToolPriceState.Load()
	if state != nil {
		return state
	}
	fallback, _ := parseToolPriceState("{}")
	return fallback
}

// Frozen materializes a zero-value snapshot against the current live state.
// Parsed and captured snapshots are returned unchanged.
func (snapshot ToolPriceSnapshot) Frozen() ToolPriceSnapshot {
	return ToolPriceSnapshot{state: snapshot.effectiveState()}
}

func (snapshot ToolPriceSnapshot) Version() uint64 {
	return snapshot.effectiveState().version
}

// CanonicalJSON returns only operator overrides, with lexically sorted keys
// and canonical decimal literals. It is detached from the live snapshot.
func (snapshot ToolPriceSnapshot) CanonicalJSON() string {
	return snapshot.effectiveState().operatorJSON
}

// OperatorPrices returns a defensive compatibility copy.
func (snapshot ToolPriceSnapshot) OperatorPrices() map[string]float64 {
	state := snapshot.effectiveState()
	result := make(map[string]float64, len(state.operator))
	for key, exact := range state.operator {
		value, _ := exact.Float64()
		result[key] = value
	}
	return result
}

// Resolve selects the longest model prefix, then the tool default. Matched is
// distinct from Price so an explicit zero remains terminal.
func (snapshot ToolPriceSnapshot) Resolve(toolName, modelName string) ToolPriceResolution {
	state := snapshot.effectiveState()
	if modelName != "" {
		for _, entry := range state.prefixes[toolName] {
			if strings.HasPrefix(modelName, entry.prefix) {
				return resolvedToolPrice(toolName, modelName, entry)
			}
		}
	}
	if entry, found := state.defaults[toolName]; found {
		return resolvedToolPrice(toolName, modelName, entry)
	}
	return ToolPriceResolution{ToolName: toolName, ModelName: modelName}
}

func resolvedToolPrice(toolName, modelName string, entry toolPriceEntry) ToolPriceResolution {
	price, _ := entry.price.Float64()
	return ToolPriceResolution{
		ToolName:  toolName,
		ModelName: modelName,
		Rule:      entry.rule,
		Price:     price,
		Source:    entry.source,
		Matched:   true,
		exact:     entry.price,
	}
}

func ResolveToolPrice(toolName, modelName string) ToolPriceResolution {
	return CaptureToolPriceSnapshot().Resolve(toolName, modelName)
}

func GetToolPriceForModel(toolName, modelName string) float64 {
	return ResolveToolPrice(toolName, modelName).Price
}

func GetToolPrice(toolName string) float64 {
	return GetToolPriceForModel(toolName, "")
}

func parseToolPriceState(raw string) (*toolPriceState, error) {
	if len(raw) > MaxToolPriceOptionBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", ToolPriceOption, MaxToolPriceOptionBytes)
	}
	if !utf8.ValidString(raw) {
		return nil, fmt.Errorf("%s must be valid UTF-8", ToolPriceOption)
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, fmt.Errorf("%s must be a JSON object", ToolPriceOption)
	}

	operator := make(map[string]decimal.Decimal)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%s must be a JSON object", ToolPriceOption)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("%s contains a non-string key", ToolPriceOption)
		}
		if err := validateToolPriceRule(key); err != nil {
			return nil, err
		}
		if _, duplicate := operator[key]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate key %q", ToolPriceOption, key)
		}

		valueToken, err := decoder.Token()
		number, numeric := valueToken.(json.Number)
		if err != nil || !numeric {
			return nil, fmt.Errorf("%s price for %q must be a number", ToolPriceOption, key)
		}
		literal := string(number)
		if len(literal) == 0 || len(literal) > MaxToolPriceNumberBytes {
			return nil, fmt.Errorf("%s price for %q has an invalid numeric literal", ToolPriceOption, key)
		}
		price, err := parseBoundedToolPrice(literal)
		if err != nil {
			return nil, fmt.Errorf("%s price for %q must be between 0 and %d with at most %d decimal places", ToolPriceOption, key, MaxToolPriceUSD, MaxToolPriceScale)
		}
		operator[key] = price
		if len(operator) > MaxToolPriceRules {
			return nil, fmt.Errorf("%s may contain at most %d rules", ToolPriceOption, MaxToolPriceRules)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, fmt.Errorf("%s must be a JSON object", ToolPriceOption)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s must contain exactly one JSON value", ToolPriceOption)
	}

	canonical, err := canonicalToolPricesJSON(operator)
	if err != nil {
		return nil, err
	}
	return buildToolPriceState(operator, canonical), nil
}

// parseBoundedToolPrice rejects hostile exponents before constructing a
// decimal. shopspring/decimal comparisons align exponents internally, so an
// unchecked value such as 1e2147483647 can otherwise force pathological work
// even when it will ultimately exceed the configured ceiling.
func parseBoundedToolPrice(literal string) (decimal.Decimal, error) {
	if literal == "" || len(literal) > MaxToolPriceNumberBytes || literal[0] == '-' {
		return decimal.Zero, errors.New("invalid tool price literal")
	}
	mantissa := literal
	exponentText := ""
	if index := strings.IndexAny(literal, "eE"); index >= 0 {
		mantissa = literal[:index]
		exponentText = literal[index+1:]
	}

	explicitExponent := 0
	if exponentText != "" {
		negative := false
		if exponentText[0] == '+' || exponentText[0] == '-' {
			negative = exponentText[0] == '-'
			exponentText = exponentText[1:]
		}
		if exponentText == "" {
			return decimal.Zero, errors.New("invalid tool price exponent")
		}
		for _, digit := range []byte(exponentText) {
			if digit < '0' || digit > '9' {
				return decimal.Zero, errors.New("invalid tool price exponent")
			}
			// Check before multiplication, and stop as soon as the configured
			// bound is crossed. No machine-sized exponent is ever constructed.
			if explicitExponent > MaxToolPriceExponent/10 ||
				explicitExponent*10+int(digit-'0') > MaxToolPriceExponent {
				return decimal.Zero, errors.New("tool price exponent is too large")
			}
			explicitExponent = explicitExponent*10 + int(digit-'0')
		}
		if negative {
			explicitExponent = -explicitExponent
		}
	}

	fractionDigits := 0
	coefficientDigits := 0
	sawNonzero := false
	sawDot := false
	for _, character := range []byte(mantissa) {
		switch {
		case character == '.' && !sawDot:
			sawDot = true
		case character >= '0' && character <= '9':
			if sawDot {
				fractionDigits++
			}
			if character != '0' || sawNonzero {
				sawNonzero = true
				coefficientDigits++
			}
		default:
			return decimal.Zero, errors.New("invalid tool price mantissa")
		}
	}
	if coefficientDigits > MaxToolPriceDigits {
		return decimal.Zero, errors.New("tool price coefficient is too long")
	}
	effectiveExponent := explicitExponent - fractionDigits
	if effectiveExponent < -MaxToolPriceScale || effectiveExponent > MaxToolPriceScale {
		return decimal.Zero, errors.New("tool price effective exponent is out of range")
	}
	price, err := decimal.NewFromString(literal)
	if err != nil || price.IsNegative() || price.GreaterThan(maxToolPriceDecimal) {
		return decimal.Zero, errors.New("tool price is out of range")
	}
	return price, nil
}

func validateToolPriceRule(rule string) error {
	if rule == "" || rule != strings.TrimSpace(rule) || len(rule) > MaxToolPriceRuleBytes || !utf8.ValidString(rule) || containsUnsafeToolPriceRune(rule) {
		return fmt.Errorf("%s contains invalid rule %q", ToolPriceOption, rule)
	}
	colonCount := strings.Count(rule, ":")
	switch colonCount {
	case 0:
		if strings.Contains(rule, "*") {
			return fmt.Errorf("%s rule %q has an invalid wildcard", ToolPriceOption, rule)
		}
		return nil
	case 1:
		colon := strings.IndexByte(rule, ':')
		toolName := rule[:colon]
		modelRule := rule[colon+1:]
		if toolName == "" || toolName != strings.TrimSpace(toolName) || strings.Contains(toolName, "*") ||
			!strings.HasSuffix(modelRule, "*") {
			return fmt.Errorf("%s rule %q must use tool:model-prefix*", ToolPriceOption, rule)
		}
		prefix := strings.TrimSuffix(modelRule, "*")
		if prefix == "" || prefix != strings.TrimSpace(prefix) || strings.Contains(prefix, "*") {
			return fmt.Errorf("%s rule %q must use a non-empty model prefix", ToolPriceOption, rule)
		}
		return nil
	default:
		return fmt.Errorf("%s rule %q may contain at most one colon", ToolPriceOption, rule)
	}
}

func containsUnsafeToolPriceRune(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return true
		}
	}
	return false
}

func canonicalToolPricesJSON(prices map[string]decimal.Decimal) (string, error) {
	keys := make([]string, 0, len(prices))
	for key := range prices {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output strings.Builder
	output.Grow(len(keys) * 24)
	output.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			output.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return "", fmt.Errorf("encode %s key: %w", ToolPriceOption, err)
		}
		output.Write(encodedKey)
		output.WriteByte(':')
		output.WriteString(prices[key].String())
	}
	output.WriteByte('}')
	return output.String(), nil
}

func buildToolPriceState(operator map[string]decimal.Decimal, canonical string) *toolPriceState {
	merged := map[string]toolPriceEntry{}
	seed := func(rule, price string) {
		exact := decimal.RequireFromString(price)
		merged[rule] = toolPriceEntry{rule: rule, price: exact, source: ToolPriceSourceDefault}
	}
	seed(ToolWebSearch, "10")
	seed(ToolWebSearchPreview, "10")
	seed(ToolFileSearch, "2.5")
	seed(ToolGoogleSearch, "14")
	seed(ToolImageGeneration, "150")
	seed(ToolWebSearchPreview+":gpt-4o*", "25")
	seed(ToolWebSearchPreview+":gpt-4.1*", "25")
	seed(ToolWebSearchPreview+":gpt-4o-mini*", "25")
	seed(ToolWebSearchPreview+":gpt-4.1-mini*", "25")
	for rule, price := range operator {
		merged[rule] = toolPriceEntry{rule: rule, price: price, source: ToolPriceSourceOperator}
	}

	state := &toolPriceState{
		defaults:     make(map[string]toolPriceEntry),
		prefixes:     make(map[string][]toolPriceEntry),
		operator:     make(map[string]decimal.Decimal, len(operator)),
		operatorJSON: canonical,
	}
	for key, value := range operator {
		state.operator[key] = value
	}
	for rule, entry := range merged {
		colon := strings.IndexByte(rule, ':')
		if colon < 0 {
			state.defaults[rule] = entry
			continue
		}
		toolName := rule[:colon]
		entry.prefix = strings.TrimSuffix(rule[colon+1:], "*")
		state.prefixes[toolName] = append(state.prefixes[toolName], entry)
	}
	for toolName, entries := range state.prefixes {
		sort.Slice(entries, func(left, right int) bool {
			if len(entries[left].prefix) != len(entries[right].prefix) {
				return len(entries[left].prefix) > len(entries[right].prefix)
			}
			return entries[left].prefix < entries[right].prefix
		})
		state.prefixes[toolName] = entries
	}
	return state
}
