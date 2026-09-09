// Package dsl records agent designs for generation. This file declares history
// and cache policy defaults; the runtime owns history grouping and provider
// cache behavior. These declarations validate and record configuration only.
package dsl

import (
	expragents "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// History defines how the agent runtime manages conversation history before
// each planner invocation. It can either:
//
//   - KeepRecentTurns(N) to retain only the last N turns without summarizing, or
//   - CompressAt... plus KeepMax... to summarize older turns while preserving
//     a bounded exact tail of whole recent turns.
//
// A turn keeps a complete contiguous assistant response with its following tool
// results and reminders, including a result message that also contains text.
// Separate reasoning, text, and parallel-call messages in one response are not
// separate turns. A user request stays with its first response; later responses
// after completed results start new turns even without another user request.
// A pending ordinary user request is also a turn and remains intact.
//
// Compression separates the trigger budget from the exact-retention budget:
// CompressAtMaxInputTokens and CompressAtTurns decide when summarization runs,
// while KeepMaxInputTokens and KeepMaxTurns bound eligible exact retention.
// With a total token ceiling, the summary covers all turns older than newest;
// some may also remain exact if the summary and complete retained history fit.
// Without that ceiling, only the excluded prefix is summarized.
// Token budgets are evaluated at runtime using
// the configured history model, so design values are defaults that applications
// may override for a specific deployment/model.
//
// At most one history policy may be configured per agent.
//
// History must appear inside a RunPolicy expression.
//
// Example:
//
//	RunPolicy(func() {
//	    History(func() {
//	        CompressAtMaxInputTokens(120000)
//	        KeepMaxInputTokens(40000)
//	        KeepMaxTurns(12)
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
// N complete turns as defined by History, preserving system prompts and intact
// response/result exchanges even within an autonomous run.
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

// CompressAtTurns configures compression to run once at least n logical turns
// have accumulated. It is optional when CompressAtMaxInputTokens is set.
//
// History defines complete turns: one assistant response and its results stay
// together, while later completed exchanges after one user request are separate.
//
// CompressAtTurns must appear inside a History expression.
//
// Example:
//
//	RunPolicy(func() {
//	    History(func() {
//	        CompressAtTurns(30)
//	        KeepMaxTurns(10)
//	    })
//	})
func CompressAtTurns(n int) {
	h, ok := eval.Current().(*expragents.HistoryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if !selectCompression(h) {
		return
	}
	if n <= 0 {
		eval.ReportError("CompressAtTurns requires n > 0, got %d", n)
		return
	}
	h.CompressAtTurns = n
}

// CompressAtMaxInputTokens configures compression to run when the history
// model counts the preserved system messages, conversation turns, and
// advertised tools above n input tokens. A count equal to n fits. Thinking and
// structured output chosen later by a planner are outside this history policy.
// After one summary, the runtime counts it with eligible complete turns,
// removing oldest optional turns until the combination fits. Summary plus
// newest exceeding n remains an error; the newest turn is never split or lost.
//
// CompressAtMaxInputTokens must appear inside a History expression.
func CompressAtMaxInputTokens(n int) {
	h, ok := eval.Current().(*expragents.HistoryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if !selectCompression(h) {
		return
	}
	if n <= 0 {
		eval.ReportError("CompressAtMaxInputTokens requires n > 0, got %d", n)
		return
	}
	h.CompressAtMaxInputTokens = n
}

// KeepMaxTurns caps the exact retention tail to at most n newest logical turns
// after compression summarizes older history.
//
// KeepMaxTurns must appear inside a History expression with a compression
// trigger.
func KeepMaxTurns(n int) {
	h, ok := eval.Current().(*expragents.HistoryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if !selectCompression(h) {
		return
	}
	if n <= 0 {
		eval.ReportError("KeepMaxTurns requires n > 0, got %d", n)
		return
	}
	h.KeepMaxTurns = n
}

// KeepMaxInputTokens bounds the additional token cost of older whole turns
// retained with the mandatory newest turn. The runtime counts complete requests
// before adding the summary and compares each candidate with the newest-only
// request, so fixed system and tool-catalog overhead cancels out. Equality fits.
// The summary counts against CompressAtMaxInputTokens, not this older-turn
// allowance, and may require retaining fewer older turns. No turn is split.
//
// KeepMaxInputTokens must appear inside a History expression with a compression
// trigger.
func KeepMaxInputTokens(n int) {
	h, ok := eval.Current().(*expragents.HistoryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if !selectCompression(h) {
		return
	}
	if n <= 0 {
		eval.ReportError("KeepMaxInputTokens requires n > 0, got %d", n)
		return
	}
	h.KeepMaxInputTokens = n
}

func selectCompression(h *expragents.HistoryExpr) bool {
	if h.Mode == "" {
		h.Mode = expragents.HistoryModeCompress
		return true
	}
	if h.Mode != expragents.HistoryModeCompress {
		eval.ReportError("only one history policy may be configured per agent")
		return false
	}
	return true
}
