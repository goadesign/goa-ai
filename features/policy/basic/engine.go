// Package basic provides a simple policy.Engine implementation that enforces
// optional allow/block lists and honors planner retry hints. It is intended to
// cover the common case where teams want lightweight filtering without
// building a bespoke policy service.
package basic

import (
	"context"
	"strings"

	"goa.design/goa-ai/runtime/agents/policy"
)

// Options configures the basic policy engine.
type Options struct {
	// AllowTags restricts tool execution to metadata tags. Empty means no tag filter.
	AllowTags []string
	// BlockTags excludes tools containing any of these tags.
	BlockTags []string
	// AllowTools explicitly allowlists tool IDs. Takes precedence over tags.
	AllowTools []string
	// BlockTools explicitly block tool IDs.
	BlockTools []string
	// DisableRetryHints disables automatic handling of planner RetryHints. Enabled by default.
	DisableRetryHints bool
	// Label annotates emitted policy labels; defaults to "basic".
	Label string
}

// Engine implements policy.Engine with allow/block filtering and retry-hint awareness.
type Engine struct {
	allowTags  map[string]struct{}
	blockTags  map[string]struct{}
	allowTools map[string]struct{}
	blockTools map[string]struct{}
	honorHints bool
	label      string
}

// New builds a new Engine using the supplied options.
//
//nolint:unparam // Error return maintained for consistency with other constructors.
func New(opts Options) (*Engine, error) {
	label := strings.TrimSpace(opts.Label)
	if label == "" {
		label = "basic"
	}
	e := &Engine{
		allowTags:  toSet(opts.AllowTags),
		blockTags:  toSet(opts.BlockTags),
		allowTools: toSet(opts.AllowTools),
		blockTools: toSet(opts.BlockTools),
		honorHints: !opts.DisableRetryHints,
		label:      label,
	}
	if !e.honorHints && len(e.allowTools) == 0 && len(e.allowTags) == 0 &&
		len(e.blockTools) == 0 && len(e.blockTags) == 0 {
		// Default to honoring retry hints so the engine always influences behavior.
		e.honorHints = true
	}
	return e, nil
}

// Decide evaluates the tool allowlist for the current turn.
//
//nolint:unparam // Error return maintained for interface compatibility.
func (e *Engine) Decide(_ context.Context, input policy.Input) (policy.Decision, error) {
	meta := indexMetadata(input.Tools)
	candidates := candidateHandles(input, meta)
	allowed := e.filterAllowed(candidates, meta)
	caps := input.RemainingCaps
	if e.honorHints && input.RetryHint != nil {
		allowed, caps = e.applyRetryHint(allowed, meta, caps, input.RetryHint)
	}
	labels := map[string]string{"policy_engine": e.label}
	if input.RetryHint != nil && e.honorHints {
		labels["policy_hint"] = string(input.RetryHint.Reason)
	}
	return policy.Decision{
		AllowedTools: allowed,
		Caps:         caps,
		Labels:       labels,
		Metadata: map[string]any{
			"engine": e.label,
		},
	}, nil
}

func (e *Engine) filterAllowed(handles []policy.ToolHandle, meta map[string]policy.ToolMetadata) []policy.ToolHandle {
	filtered := make([]policy.ToolHandle, 0, len(handles))
	seen := make(map[string]struct{}, len(handles))
	for _, handle := range handles {
		if _, ok := seen[handle.ID]; ok {
			continue
		}
		md, ok := meta[handle.ID]
		if !ok {
			continue
		}
		if !e.isAllowed(md) {
			continue
		}
		filtered = append(filtered, handle)
		seen[handle.ID] = struct{}{}
	}
	return filtered
}

func (e *Engine) isAllowed(meta policy.ToolMetadata) bool {
	if len(e.blockTools) > 0 {
		if _, blocked := e.blockTools[meta.ID]; blocked {
			return false
		}
	}
	if len(e.blockTags) > 0 {
		for _, tag := range meta.Tags {
			if _, blocked := e.blockTags[tag]; blocked {
				return false
			}
		}
	}
	if len(e.allowTools) > 0 {
		_, ok := e.allowTools[meta.ID]
		return ok
	}
	if len(e.allowTags) > 0 {
		for _, tag := range meta.Tags {
			if _, ok := e.allowTags[tag]; ok {
				return true
			}
		}
		return false
	}
	return true
}

func (e *Engine) applyRetryHint(
	allowed []policy.ToolHandle, meta map[string]policy.ToolMetadata,
	caps policy.CapsState, hint *policy.RetryHint,
) ([]policy.ToolHandle, policy.CapsState) {
	if hint == nil || hint.Tool == "" {
		return allowed, caps
	}
	switch {
	case hint.RestrictToTool:
		if _, ok := meta[hint.Tool]; ok {
			allowed = []policy.ToolHandle{{ID: hint.Tool}}
			caps.RemainingToolCalls = limitCap(caps.RemainingToolCalls, 1)
		} else {
			allowed = nil
		}
	case hint.Reason == policy.RetryReasonToolUnavailable:
		allowed = removeHandle(allowed, hint.Tool)
	default:
		// Use existing allowed slice as-is
	}
	return allowed, caps
}

func candidateHandles(input policy.Input, meta map[string]policy.ToolMetadata) []policy.ToolHandle {
	if len(input.Requested) > 0 {
		return cloneHandles(input.Requested)
	}
	handles := make([]policy.ToolHandle, 0, len(meta))
	for id := range meta {
		handles = append(handles, policy.ToolHandle{ID: id})
	}
	return handles
}

func removeHandle(handles []policy.ToolHandle, id string) []policy.ToolHandle {
	filtered := handles[:0]
	for _, handle := range handles {
		if handle.ID == id {
			continue
		}
		filtered = append(filtered, handle)
	}
	return filtered
}

func cloneHandles(handles []policy.ToolHandle) []policy.ToolHandle {
	dup := make([]policy.ToolHandle, len(handles))
	copy(dup, handles)
	return dup
}

func indexMetadata(list []policy.ToolMetadata) map[string]policy.ToolMetadata {
	index := make(map[string]policy.ToolMetadata, len(list))
	for _, meta := range list {
		index[meta.ID] = meta
	}
	return index
}

func toSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func limitCap(current int, limit int) int {
	if limit <= 0 {
		return current
	}
	if current == 0 {
		return limit
	}
	if current < limit {
		return current
	}
	return limit
}
