package billingexpr

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// VersionPrefix marks a v1 expression (the only version currently supported).
const VersionPrefix = "v1:"

// compiled is a cached, compiled expression plus its variable introspection.
type compiled struct {
	code     string
	program  *vm.Program
	usedVars map[string]bool
}

var compileCache sync.Map // code -> *compiled

// CompileFromCache compiles and caches an expression (version prefix stripped),
// returning an error for invalid syntax. The caller must not mutate the result.
func CompileFromCache(exprStr string) (*compiled, error) {
	code := stripVersion(exprStr)
	if code == "" {
		return nil, fmt.Errorf("empty billing expression")
	}
	if v, ok := compileCache.Load(code); ok {
		return v.(*compiled), nil
	}
	// Compile against a template env that declares every variable and built-in
	// function; the concrete values (including a per-run tier state) are
	// supplied as a fresh map at run time.
	prog, err := expr.Compile(code, expr.Env(templateEnv()), expr.AsFloat64())
	if err != nil {
		return nil, fmt.Errorf("compile billing expression: %w", err)
	}
	c := &compiled{code: code, program: prog, usedVars: UsedVars(code)}
	compileCache.Store(code, c)
	return c, nil
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
// as standalone identifiers. This drives token normalization: tokens for a used
// subcategory are pulled out of the base p/c variables.
func UsedVars(code string) map[string]bool {
	found := make(map[string]bool, len(subcategoryVars))
	code = stripVersion(code)
	for _, name := range subcategoryVars {
		if containsIdentifier(code, name) {
			found[name] = true
		}
	}
	return found
}

func isIdentChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func containsIdentifier(code, name string) bool {
	for i := 0; i <= len(code)-len(name); i++ {
		j := strings.Index(code[i:], name)
		if j < 0 {
			return false
		}
		idx := i + j
		beforeOK := idx == 0 || !isIdentChar(code[idx-1])
		after := idx + len(name)
		afterOK := after == len(code) || !isIdentChar(code[after])
		if beforeOK && afterOK {
			return true
		}
		i = idx + 1
	}
	return false
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
		"p":      params.P,
		"c":      params.C,
		"len":    params.Len,
		"cr":     params.Cr,
		"cc":     params.Cc,
		"cc1h":   params.Cc1h,
		"img":    params.Img,
		"ai":     params.Ai,
		"ao":     params.Ao,
		"img_o":  params.ImgO,
		"tier":   func(name string, value float64) float64 { state.matchedTier = name; return value },
		"param":  func(path string) any { return lookupPath(req.Body, path) },
		"header": func(key string) string { return req.Header[key] },
		"has":    strings.Contains,
		"hour":   func(tz string) int { return nowInTZ(tz).Hour() },
		"minute": func(tz string) int { return nowInTZ(tz).Minute() },
		"weekday": func(tz string) int { return int(nowInTZ(tz).Weekday()) },
		"month":  func(tz string) int { return int(nowInTZ(tz).Month()) },
		"day":    func(tz string) int { return nowInTZ(tz).Day() },
		"max":    math.Max,
		"min":    math.Min,
		"abs":    math.Abs,
		"ceil":   math.Ceil,
		"floor":  math.Floor,
	}
}
