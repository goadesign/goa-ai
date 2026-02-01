// Package tools defines runtime-facing tool metadata and helpers.
//
// This file defines the opt-in idempotency metadata used by planners to decide
// whether repeated tool calls can be safely de-duplicated across a transcript.
package tools

import (
	"fmt"
	"strings"
)

// IdempotencyScope declares the semantic scope in which a tool call is considered
// idempotent.
//
// When a tool is idempotent for a given scope, orchestration layers may treat
// repeated calls with identical arguments as redundant and avoid executing them.
//
// Default: tools are not idempotent across a transcript unless explicitly tagged.
type IdempotencyScope string

const (
	// IdempotencyScopeTranscript indicates the tool is idempotent across a run
	// transcript: identical calls may be dropped once a successful result exists
	// in the transcript.
	IdempotencyScopeTranscript IdempotencyScope = "transcript"

	// TagIdempotencyTranscript is the design-time tag emitted into ToolSpec.Tags
	// when a tool is declared idempotent across a transcript.
	TagIdempotencyTranscript = "goa-ai.idempotency=transcript"
)

const idempotencyTagPrefix = "goa-ai.idempotency="

// IdempotencyScopeFromTags returns the idempotency scope declared in tags.
//
// Contract:
//   - The idempotency tag appears at most once. Multiple tags are a design bug.
//   - Unknown idempotency values are a design bug and are returned as errors.
func IdempotencyScopeFromTags(tags []string) (IdempotencyScope, bool, error) {
	var (
		scope IdempotencyScope
		found bool
	)
	for _, tag := range tags {
		if !strings.HasPrefix(tag, idempotencyTagPrefix) {
			continue
		}
		if found {
			return "", false, fmt.Errorf("tools: multiple idempotency tags (first=%q, second=%q)", string(scope), tag)
		}
		raw := strings.TrimPrefix(tag, idempotencyTagPrefix)
		switch raw {
		case string(IdempotencyScopeTranscript):
			scope = IdempotencyScopeTranscript
			found = true
		default:
			return "", false, fmt.Errorf("tools: unknown idempotency scope %q", raw)
		}
	}
	return scope, found, nil
}
