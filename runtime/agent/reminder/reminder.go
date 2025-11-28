// Package reminder defines core types for run-scoped system reminders used to
// provide backstage guidance to planners (safety, correctness, workflow, and
// context hints). The package is intentionally small and policy-agnostic; the
// runtime owns evaluation and injection semantics.
package reminder

import (
	"goa.design/goa-ai/runtime/agent/tools"
)

// Tier represents the priority tier for a reminder. Lower-valued tiers carry
// higher precedence when enforcing caps or resolving conflicts.
type Tier int

const (
	// TierSafety is the highest priority tier (P0). Safety reminders must
	// never be dropped by policy; they may be de-duplicated but not
	// suppressed due to lower-priority budgets.
	TierSafety Tier = iota
	// TierGuidance carries workflow suggestions and soft nudges (P2). These
	// are lowest priority and are the first to be suppressed when prompt
	// budgets are tight.
	TierGuidance
)

// AttachmentKind describes where a reminder should conceptually attach in the
// conversation. The current implementation distinguishes only between run
// start and per-turn attachments.
type AttachmentKind string

const (
	// AttachmentRunStart reminders attach to the start of a run, alongside
	// session context and user preferences.
	AttachmentRunStart AttachmentKind = "run_start"
	// AttachmentUserTurn reminders attach to user turns, shaping how the
	// planner interprets the next user message.
	AttachmentUserTurn AttachmentKind = "user_turn"
)

// Attachment scopes a reminder to a particular attachment point in the
// conversation.
type Attachment struct {
	// Kind identifies the conceptual attachment location (run start, user
	// turn).
	Kind AttachmentKind

	// Tool identifies the fully qualified tool name. It is reserved for
	// future use and left empty by current callers.
	Tool tools.Ident
}

// Reminder describes concrete guidance that should be injected into prompts.
// Reminders are produced by application code and evaluated by the Engine on a
// per-run basis to enforce lifetime and rate limiting.
type Reminder struct {
	// ID is the stable identifier for this reminder type within a run. It is
	// used for de-duplication, rate limiting, and telemetry. IDs should be
	// deterministic (e.g., "pending_todos", "partial_result.ad.search").
	ID string

	// Text is the natural-language guidance to inject, typically wrapped in a
	// domain-specific tag such as <system-reminder>...</system-reminder>.
	Text string

	// Priority controls ordering and suppression. Lower tiers (TierSafety)
	// always take precedence over higher tiers.
	Priority Tier

	// Attachment indicates where in the conversation this reminder should be
	// associated (run start or user turn).
	Attachment Attachment

	// MaxPerRun caps how many times this reminder may be emitted in a single
	// run. Zero means unlimited. TierSafety reminders should generally leave
	// this unset to avoid ever being dropped, though the Engine still
	// de-duplicates repeated emissions per turn.
	MaxPerRun int

	// MinTurnsBetween enforces a minimum number of planner turns between
	// emissions. Zero means no rate limit. This is typically used for
	// TierCorrect/TierGuidance reminders to avoid noisy repetition.
	MinTurnsBetween int
}

// DefaultExplanation is a generic explanation of system reminders suitable for
// inclusion in agent system prompts. It documents <system-reminder> blocks as
// platform-added guidance that should not be surfaced verbatim to end users.
const DefaultExplanation = `
- **System reminders**
  - You may see <system-reminder>...</system-reminder> blocks in system text.
    These blocks are added by the platform to provide contextual guidance.
    They are not part of the end user's message, but you **should** read and
    follow them when they apply to the current task. Do not expose the raw
    <system-reminder> markup or its wording directly back to the user.`
