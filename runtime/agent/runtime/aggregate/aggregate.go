// Package aggregate provides tools for aggregating child results into a parent ToolResult.
package aggregate

import (
	"context"
	"strings"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
)

type (
	// Aggregator constructs the parent ToolResult from all child results of a nested run.
	Aggregator func(ctx context.Context, parent *runtime.ParentCall, children []*runtime.ChildCall) (*planner.ToolResult, error)

	// Option customizes the ProvenancedEnvelope aggregator.
	Option func(*envOpts)

	envOpts struct {
		includeCalls    bool
		includeEvidence bool
		summaryFn       func(primary any, children []*runtime.ChildCall) string
		codeFn          func(primary any, children []*runtime.ChildCall) string
	}
)

// WithCalls toggles inclusion of combined calls entries in the parent envelope.
func WithCalls(v bool) Option { return func(o *envOpts) { o.includeCalls = v } }

// WithEvidence toggles inclusion of combined evidence entries in the parent envelope.
func WithEvidence(v bool) Option { return func(o *envOpts) { o.includeEvidence = v } }

// WithSummary overrides the default summary derivation.
func WithSummary(fn func(primary any, children []*runtime.ChildCall) string) Option {
	return func(o *envOpts) { o.summaryFn = fn }
}

// WithCode overrides the default code computation.
func WithCode(fn func(primary any, children []*runtime.ChildCall) string) Option {
	return func(o *envOpts) { o.codeFn = fn }
}

// PassThrough returns the first non-nil child result as the parent result.
func PassThrough() Aggregator {
	return func(_ context.Context, _ *runtime.ParentCall, children []*runtime.ChildCall) (*planner.ToolResult, error) {
		var primary any
		for _, c := range children {
			if c.Result != nil {
				primary = c.Result
				break
			}
		}
		return &planner.ToolResult{Result: primary}, nil
	}
}

// ProvenancedEnvelope returns an aggregator that produces a compact envelope:
// { code, result, calls?, evidence?, summary? }.
//
// Defaults:
// - code: "error_internal" if any child has Status=="error"; else "ok_no_data" if no primary result; else "ok".
// - result: first non-nil child result.
// - calls: merged from child results' "calls" when includeCalls=true.
// - evidence: merged from child results' "evidence" when includeEvidence=true.
// - summary: from result fields "summary"|"result_summary"|"message" (best-effort), or empty.
func ProvenancedEnvelope(opts ...Option) Aggregator {
	cfg := envDefaults()
	for _, o := range opts {
		o(&cfg)
	}
	return func(_ context.Context, _ *runtime.ParentCall, children []*runtime.ChildCall) (*planner.ToolResult, error) {
		primary := firstNonNil(children)
		code := cfg.codeFn(primary, children)
		out := map[string]any{
			"code":   code,
			"result": primary,
		}
		if s := cfg.summaryFn(primary, children); strings.TrimSpace(s) != "" {
			out["summary"] = s
		}
		if cfg.includeCalls {
			if merged := mergeArrays(children, "calls"); merged != nil {
				out["calls"] = merged
			}
		}
		if cfg.includeEvidence {
			if merged := mergeArrays(children, "evidence"); merged != nil {
				out["evidence"] = merged
			}
		}
		return &planner.ToolResult{Result: out}, nil
	}
}

func envDefaults() envOpts {
	return envOpts{
		includeCalls:    true,
		includeEvidence: false,
		summaryFn: func(primary any, _ []*runtime.ChildCall) string {
			if m, ok := primary.(map[string]any); ok {
				if s, ok := m["summary"].(string); ok {
					return s
				}
				if s, ok := m["result_summary"].(string); ok {
					return s
				}
				if s, ok := m["message"].(string); ok {
					return s
				}
			}
			return ""
		},
		codeFn: func(primary any, children []*runtime.ChildCall) string {
			for _, c := range children {
				if c.Status == "error" {
					return "error_internal"
				}
			}
			if primary == nil {
				return "ok_no_data"
			}
			return "ok"
		},
	}
}

func firstNonNil(children []*runtime.ChildCall) any {
	for _, c := range children {
		if c.Result != nil {
			return c.Result
		}
	}
	return nil
}

func mergeArrays(children []*runtime.ChildCall, keys ...string) []any {
	var merged []any
	for _, c := range children {
		m, ok := c.Result.(map[string]any)
		if !ok {
			continue
		}
		for _, k := range keys {
			if v, ok := m[k]; ok {
				if arr, ok := v.([]any); ok && len(arr) > 0 {
					merged = append(merged, arr...)
				}
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}
