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
// Compression separates the trigger budget from the exact-retention budget:
// CompressAtMaxInputTokens and CompressAtTurns decide when summarization runs,
// while KeepMaxInputTokens and KeepMaxTurns decide which newest complete turns
// remain unsummarized afterward. Token budgets are evaluated at runtime using
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

// CompressAtTurns configures compression to run once at least n logical turns
// have accumulated. It is optional when CompressAtMaxInputTokens is set.
//
// A logical turn starts with a user request and includes subsequent assistant
// messages and tool_use/tool_result exchanges up to the next user request.
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

// CompressAtMaxInputTokens configures compression to run when the
// provider-visible input transcript exceeds n tokens. Token counting happens at
// runtime through the configured history model because tokenization is
// model-specific.
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

// KeepMaxInputTokens keeps the newest whole logical turns whose exact transcript
// fits under n input tokens after compression summarizes older history. The
// runtime never truncates or edits a turn to fit this budget; if adding the next
// older turn would exceed the budget, that whole turn is summarized instead.
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
