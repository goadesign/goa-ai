package dsl

import (
	expragents "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// History defines how the agent runtime manages conversation history before
// each planner invocation. It can either:
//
//   - KeepRecentTurns(N) to retain only the last N turns, or
//   - Compress(triggerAt, keepRecent) to summarize older turns once the
//     trigger threshold is reached.
//
// At most one history policy may be configured per agent.
//
// History must appear inside a RunPolicy expression.
//
// Example:
//
//	RunPolicy(func() {
//	    History(func() {
//	        KeepRecentTurns(20)
//	    })
//	})
func History(fn func()) {
	policy, ok := eval.Current().(*expragents.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if policy.History != nil {
		eval.ReportError("History already defined for agent %q", policy.Agent.Name)
		return
	}
	h := &expragents.HistoryExpr{
		Policy: policy,
	}
	policy.History = h
	if fn != nil {
		eval.Execute(fn, h)
	}
}

// Cache defines the prompt caching policy for the current agent. It configures
// where the runtime should place cache checkpoints relative to system prompts
// and tool definitions for providers that support caching.
//
// Cache must appear inside a RunPolicy expression.
//
// Example:
//
//	RunPolicy(func() {
//	    Cache(func() {
//	        AfterSystem()
//	        AfterTools()
//	    })
//	})
func Cache(fn func()) {
	policy, ok := eval.Current().(*expragents.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if policy.Cache != nil {
		eval.ReportError("Cache already defined for agent %q", policy.Agent.Name)
		return
	}
	c := &expragents.CacheExpr{
		Policy: policy,
	}
	policy.Cache = c
	if fn != nil {
		eval.Execute(fn, c)
	}
}

// AfterSystem configures the cache policy to place a checkpoint after all
// system messages. Providers that support prompt caching interpret this as a
// cache boundary immediately following the system preamble.
//
// AfterSystem must appear inside a Cache expression.
func AfterSystem() {
	cache, ok := eval.Current().(*expragents.CacheExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	cache.AfterSystem = true
}

// AfterTools configures the cache policy to place a checkpoint after tool
// definitions. Providers that support tool-level cache checkpoints interpret
// this as a cache boundary immediately following the tool configuration
// section.
//
// AfterTools must appear inside a Cache expression.
func AfterTools() {
	cache, ok := eval.Current().(*expragents.CacheExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	cache.AfterTools = true
}

// KeepRecentTurns configures a history policy that retains only the most recent
// N user/assistant turns while preserving system prompts and tool exchanges.
//
// KeepRecentTurns must appear inside a History expression.
//
// Example:
//
//	RunPolicy(func() {
//	    History(func() {
//	        KeepRecentTurns(20)
//	    })
//	})
func KeepRecentTurns(n int) {
	h, ok := eval.Current().(*expragents.HistoryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if h.Mode != "" {
		eval.ReportError("only one history policy may be configured per agent")
		return
	}
	if n <= 0 {
		eval.ReportError("KeepRecentTurns requires n > 0, got %d", n)
		return
	}
	h.Mode = expragents.HistoryModeKeepRecent
	h.KeepRecent = n
}

// Compress configures a history policy that summarizes older turns once a
// trigger threshold is reached, retaining the most recent keepRecent turns in
// full fidelity. The runtime uses the configured thresholds to construct a
// compression policy; applications supply the model client via generated
// configuration when Compress is enabled.
//
// Compress must appear inside a History expression. At most one of
// KeepRecentTurns or Compress may be configured.
//
// Example:
//
//	RunPolicy(func() {
//	    History(func() {
//	        Compress(30, 10) // trigger at 30 turns, keep 10 recent
//	    })
//	})
func Compress(triggerAt, keepRecent int) {
	h, ok := eval.Current().(*expragents.HistoryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if h.Mode != "" {
		eval.ReportError("only one history policy may be configured per agent")
		return
	}
	if triggerAt <= 0 {
		eval.ReportError("Compress requires triggerAt > 0, got %d", triggerAt)
		return
	}
	if keepRecent < 0 {
		eval.ReportError("Compress requires keepRecent >= 0, got %d", keepRecent)
		return
	}
	if keepRecent >= triggerAt {
		eval.ReportError("Compress requires keepRecent < triggerAt (got %d >= %d)", keepRecent, triggerAt)
		return
	}
	h.Mode = expragents.HistoryModeCompress
	h.TriggerAt = triggerAt
	h.CompressKeepRecent = keepRecent
}
