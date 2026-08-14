package billingexpr

import (
	"fmt"
	"strings"
	"time"

	"github.com/expr-lang/expr"
)

// evalState captures the matched tier during a single evaluation.
type evalState struct {
	matchedTier string
}

// Run evaluates a compiled expression against normalized token params and
// request input, returning the cost and the matched tier (if any).
func (c *compiled) Run(params TokenParams, req RequestInput) (EvalResult, error) {
	state := &evalState{}
	env := buildEnv(params, req, state)
	out, err := expr.Run(c.program, env)
	if err != nil {
		return EvalResult{}, fmt.Errorf("run billing expression: %w", err)
	}
	cost, ok := out.(float64)
	if !ok {
		cost = toFloat(out)
	}
	return EvalResult{Cost: cost, MatchedTier: state.matchedTier}, nil
}

// RunExpr compiles (cached) and evaluates an expression in one call. It is the
// convenient entry point for settlement code.
func RunExpr(exprStr string, params TokenParams, req RequestInput) (EvalResult, error) {
	c, err := CompileFromCache(exprStr)
	if err != nil {
		return EvalResult{}, err
	}
	return c.Run(params, req)
}

// BuildTokenParams normalizes raw usage into the expression variables, applying
// the subcategory-exclusion rule for GPT-format usage (prompt_tokens includes
// everything) and the no-adjustment rule for Claude-format usage.
func BuildTokenParams(u Usage, used map[string]bool) TokenParams {
	p := TokenParams{}
	if u.IsClaudeSemantic {
		// Claude input_tokens is text-only; cache is separate and never
		// subtracted from the base prompt variable.
		p.Len = float64(u.PromptTokens + u.CacheReadTokens + u.CacheCreationTokens + u.CacheCreation1hTokens)
		p.P = float64(u.PromptTokens)
		p.Cr = float64(u.CacheReadTokens)
		p.Cc = float64(u.CacheCreationTokens)
		p.Cc1h = float64(u.CacheCreation1hTokens)
	} else {
		// GPT/OpenAI prompt_tokens includes all subcategories; subtract the ones
		// the expression prices separately.
		p.Len = float64(u.PromptTokens)
		p.P = float64(u.PromptTokens)
		if used["cr"] {
			p.Cr = float64(u.CacheReadTokens)
			p.P -= p.Cr
		}
		if used["cc"] {
			p.Cc = float64(u.CacheCreationTokens)
			p.P -= p.Cc
		}
		if used["cc1h"] {
			p.Cc1h = float64(u.CacheCreation1hTokens)
			p.P -= p.Cc1h
		}
		if used["img"] {
			p.Img = float64(u.ImageInputTokens)
			p.P -= p.Img
		}
		if used["ai"] {
			p.Ai = float64(u.AudioInputTokens)
			p.P -= p.Ai
		}
	}
	p.C = float64(u.CompletionTokens)
	if used["ao"] {
		p.Ao = float64(u.AudioOutputTokens)
		p.C -= p.Ao
	}
	if used["img_o"] {
		p.ImgO = float64(u.ImageOutputTokens)
		p.C -= p.ImgO
	}
	return p
}

// lookupPath resolves a dotted path over a decoded JSON body.
func lookupPath(body map[string]any, path string) any {
	if path == "" {
		return nil
	}
	var cur any = body
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[part]
		if !ok {
			return nil
		}
	}
	return cur
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func nowInTZ(tz string) time.Time {
	if tz == "" {
		return time.Now()
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Now()
	}
	return time.Now().In(loc)
}
