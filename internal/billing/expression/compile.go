package expression

import (
	"container/list"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/vm"
)

// VersionPrefix marks a v1 expression (the only version currently supported).
const VersionPrefix = "v1:"

const (
	maxBillingExpressionBytes        = 16 << 10
	maxBillingExpressionLexicalUnits = 4_096
	maxBillingExpressionNesting      = 64
	maxBillingExpressionASTNodes     = 4_096
	maxCompiledExpressionCacheItems  = 512
)

// compiled is a cached, compiled expression plus its variable introspection.
type compiled struct {
	code     string
	program  *vm.Program
	usedVars map[string]bool
}

type compiledCacheEntry struct {
	code     string
	compiled *compiled
}

type boundedCompileCache struct {
	mu      sync.Mutex
	items   map[string]*list.Element
	entries *list.List
}

func newBoundedCompileCache() *boundedCompileCache {
	return &boundedCompileCache{items: make(map[string]*list.Element), entries: list.New()}
}

func (cache *boundedCompileCache) load(code string) (*compiled, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, found := cache.items[code]
	if !found {
		return nil, false
	}
	cache.entries.MoveToFront(element)
	return element.Value.(compiledCacheEntry).compiled, true
}

func (cache *boundedCompileCache) store(code string, value *compiled) *compiled {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, found := cache.items[code]; found {
		cache.entries.MoveToFront(element)
		return element.Value.(compiledCacheEntry).compiled
	}
	element := cache.entries.PushFront(compiledCacheEntry{code: code, compiled: value})
	cache.items[code] = element
	for cache.entries.Len() > maxCompiledExpressionCacheItems {
		oldest := cache.entries.Back()
		entry := oldest.Value.(compiledCacheEntry)
		delete(cache.items, entry.code)
		cache.entries.Remove(oldest)
	}
	return value
}

func (cache *boundedCompileCache) size() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.entries.Len()
}

var compileCache = newBoundedCompileCache()

// CompileFromCache compiles and caches an expression (version prefix stripped),
// returning an error for invalid syntax. The caller must not mutate the result.
func CompileFromCache(exprStr string) (*compiled, error) {
	code, err := validateBillingExpressionSource(exprStr)
	if err != nil {
		return nil, err
	}
	if cached, ok := compileCache.load(code); ok {
		return cached, nil
	}
	// Compile against a template env that declares every variable and built-in
	// function; the concrete values (including a per-run tier state) are
	// supplied as a fresh map at run time.
	prog, err := expr.Compile(code, expr.Env(templateEnv()), expr.AsFloat64())
	if err != nil {
		return nil, fmt.Errorf("compile billing expression: %w", err)
	}
	if countBillingExpressionASTNodes(prog) > maxBillingExpressionASTNodes {
		return nil, fmt.Errorf("billing expression is too complex")
	}
	c := &compiled{code: code, program: prog, usedVars: usedVarsFromProgram(prog)}
	return compileCache.store(code, c), nil
}

func validateBillingExpressionSource(exprStr string) (string, error) {
	code := stripVersion(exprStr)
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("empty billing expression")
	}
	if len(code) > maxBillingExpressionBytes || !utf8.ValidString(code) {
		return "", fmt.Errorf("billing expression exceeds the source limit")
	}
	units, depth := 0, 0
	for index := 0; index < len(code); {
		character := code[index]
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' {
			index++
			continue
		}
		units++
		if units > maxBillingExpressionLexicalUnits {
			return "", fmt.Errorf("billing expression is too complex")
		}
		if character == '\'' || character == '"' || character == '`' {
			quote := character
			index++
			for index < len(code) {
				if quote != '`' && code[index] == '\\' {
					index += min(2, len(code)-index)
					continue
				}
				if code[index] == quote {
					index++
					break
				}
				index++
			}
			continue
		}
		switch character {
		case '(', '[', '{':
			depth++
			if depth > maxBillingExpressionNesting {
				return "", fmt.Errorf("billing expression nesting is too deep")
			}
			index++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
			index++
		default:
			if isBillingExpressionWordByte(character) {
				index++
				for index < len(code) && isBillingExpressionWordByte(code[index]) {
					index++
				}
			} else {
				index++
			}
		}
	}
	return code, nil
}

func isBillingExpressionWordByte(value byte) bool {
	return value >= utf8.RuneSelf || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '_'
}

func countBillingExpressionASTNodes(program *vm.Program) int {
	if program == nil || program.Node() == nil {
		return 0
	}
	count := 0
	ast.Find(program.Node(), func(ast.Node) bool {
		count++
		return false
	})
	return count
}

// templateEnv declares all names the expression language may reference, with
// type hints for variables and function values for the built-ins.
func templateEnv() map[string]any {
	return map[string]any{
		"p": 0.0, "c": 0.0, "len": 0.0, "cr": 0.0, "cc": 0.0, "cc1h": 0.0,
		"img": 0.0, "ai": 0.0, "ao": 0.0, "img_o": 0.0,
		"tier":    func(name string, value float64) float64 { return value },
		"param":   func(path string) any { return nil },
		"header":  func(key string) string { return "" },
		"has":     strings.Contains,
		"hour":    func(tz string) int { return 0 },
		"minute":  func(tz string) int { return 0 },
		"weekday": func(tz string) int { return 0 },
		"month":   func(tz string) int { return 0 },
		"day":     func(tz string) int { return 0 },
		"max":     math.Max,
		"min":     math.Min,
		"abs":     math.Abs,
		"ceil":    math.Ceil,
		"floor":   math.Floor,
	}
}

func stripVersion(s string) string {
	if strings.HasPrefix(s, VersionPrefix) {
		return s[len(VersionPrefix):]
	}
	return s
}

// UsedVars returns the set of subcategory variables referenced by an expression
// as identifier nodes. This drives token normalization: tokens for a used
// subcategory are pulled out of the base p/c variables. Inspecting the compiled
// AST, instead of scanning source text, prevents strings such as header("img")
// or param("audio.ai") from accidentally changing the bill.
func UsedVars(code string) map[string]bool {
	c, err := CompileFromCache(code)
	if err != nil {
		return map[string]bool{}
	}
	found := make(map[string]bool, len(c.usedVars))
	for name, used := range c.usedVars {
		found[name] = used
	}
	return found
}

func usedVarsFromProgram(program *vm.Program) map[string]bool {
	found := make(map[string]bool, len(subcategoryVars))
	if program == nil {
		return found
	}
	allowed := make(map[string]struct{}, len(subcategoryVars))
	for _, name := range subcategoryVars {
		allowed[name] = struct{}{}
	}
	ast.Find(program.Node(), func(node ast.Node) bool {
		identifier, ok := node.(*ast.IdentifierNode)
		if !ok {
			return false
		}
		if _, ok := allowed[identifier.Value]; ok {
			found[identifier.Value] = true
		}
		return false
	})
	return found
}

// buildEnv constructs the per-run expression environment with variables and
// built-in functions.
func buildEnv(params TokenParams, req RequestInput, state *evalState) map[string]any {
	if req.Header == nil {
		req.Header = map[string]string{}
	}
	if req.Body == nil {
		req.Body = map[string]any{}
	}
	return map[string]any{
		"p":       params.P,
		"c":       params.C,
		"len":     params.Len,
		"cr":      params.Cr,
		"cc":      params.Cc,
		"cc1h":    params.Cc1h,
		"img":     params.Img,
		"ai":      params.Ai,
		"ao":      params.Ao,
		"img_o":   params.ImgO,
		"tier":    func(name string, value float64) float64 { state.matchedTier = boundedMatchedTier(name); return value },
		"param":   func(path string) any { return lookupPath(req.Body, path) },
		"header":  func(key string) string { return req.Header[key] },
		"has":     strings.Contains,
		"hour":    func(tz string) int { return nowInTZ(tz).Hour() },
		"minute":  func(tz string) int { return nowInTZ(tz).Minute() },
		"weekday": func(tz string) int { return int(nowInTZ(tz).Weekday()) },
		"month":   func(tz string) int { return int(nowInTZ(tz).Month()) },
		"day":     func(tz string) int { return nowInTZ(tz).Day() },
		"max":     math.Max,
		"min":     math.Min,
		"abs":     math.Abs,
		"ceil":    math.Ceil,
		"floor":   math.Floor,
	}
}
