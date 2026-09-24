package storage

// Initial history is written before engine submission under the destination run
// ID. Stores publish the complete ordered chain once; starting the run attaches
// that publication without copying its records.

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// SeedKind identifies the source permitted in one run's initial history.
	SeedKind string

	// SeedDeclaration freezes the owner and source before the first record is
	// appended. Literal seeds have no source. Next-turn seeds name a completed
	// source and let the store capture its terminal position. Continuations name
	// the exact saved position in their suspended predecessor.
	SeedDeclaration struct {
		// AgentID is the agent that will execute the destination run.
		AgentID string
		// RunID is the destination workflow ID, including before submission.
		RunID string
		// SessionID owns all linked history; empty is allowed only for literals.
		SessionID string
		// CommandID identifies the original operation, before mutable resolution.
		CommandID string
		// AttemptID identifies this candidate upload and survives worker replacement.
		AttemptID string
		// Kind selects literal, completed-turn, or continuation history.
		Kind SeedKind
		// SourceRunID names the exact completed or suspended predecessor.
		SourceRunID string
		// SourceEndID is required only for a continuation's saved history.
		SourceEndID string
		// ExcludeReasoning applies only to a completed-turn reference.
		ExcludeReasoning bool
		// RenderedPrompts records the identities of prompts already in literals.
		RenderedPrompts []prompt.RenderEvent
	}

	// HistoryPrefix selects a saved run through an exact committed position.
	// Exclusions compose: an outer reference cannot restore content an inner
	// reference already excluded.
	HistoryPrefix struct {
		// RunID owns the selected history.
		RunID string
		// EndID is a committed run-record ID, not an initial-history position.
		EndID string
		// ExcludeSystem omits the source's system messages for a new turn.
		ExcludeSystem bool
		// ExcludeReasoning omits completed-turn native reasoning.
		ExcludeReasoning bool
	}

	// RunSeed is the immutable declaration and current accepted end position.
	// Begin returns it during preparation; LoadRunSeed returns only publication.
	RunSeed struct {
		// Declaration contains the exact originally accepted owner and source.
		Declaration SeedDeclaration
		// Source is the store-resolved reference; literal seeds leave it nil.
		Source *HistoryPrefix
		// EndID is the final accepted initial-history record, or EmptySeedEndID.
		EndID string
	}

	// SeedRecord contains one complete literal, literal fragment, declared
	// history reference or compiled-start part. The store assigns ID; callers
	// supply a stable Key and exact PreviousID for retry and ordering checks.
	SeedRecord struct {
		// ID is the store-assigned position; append requests leave it empty.
		ID string
		// Key identifies the same append across a lost response.
		Key string
		// PreviousID names the accepted preceding record, or EmptySeedEndID.
		PreviousID string
		// Messages uses the canonical literal transcript encoding.
		Messages rawjson.Message
		// LiteralPart continues one literal whose encoded bytes span records.
		// It is omitted for complete literals so their encoding stays unchanged.
		LiteralPart *LiteralPart `json:"literal_part,omitempty"`
		// Prefix replaces a literal with the declaration's exact source.
		Prefix *HistoryPrefix
		// Prepared holds the next bytes of the complete compiled start. These
		// records follow all history records and are never transcript messages.
		Prepared []byte
	}

	// SeedAppend writes one record to the destination run's initial history.
	SeedAppend struct {
		// RunID identifies the declaration accepted by BeginRunSeed.
		RunID string
		// AttemptID identifies the candidate receiving this immutable record.
		AttemptID string
		// Record contains the exact bytes and preceding position for this append.
		Record SeedRecord
	}

	// SeedPage is one bounded, ordered slice of a published initial history.
	SeedPage struct {
		// Records contains independently owned records in append order.
		Records []SeedRecord
		// NextCursor names the last returned record when more remain.
		NextCursor string
	}
)

const (
	// SeedLiteral imports exactly the caller-supplied messages.
	SeedLiteral SeedKind = "literal"
	// SeedNextTurn inherits one exact successfully completed run.
	SeedNextTurn SeedKind = "next_turn"
	// SeedContinuation inherits the saved history of one suspended run.
	SeedContinuation SeedKind = "continuation"
	// EmptySeedEndID is the accepted position before the first seed record.
	EmptySeedEndID = "0"
	// MaxSeedCommandBytes bounds a complete encoded publication command.
	MaxSeedCommandBytes = 1_000_000
	// MaxSeedPageBytes bounds the complete logical JSON page, including its
	// assigned record IDs and continuation cursor. Compared with one append,
	// the page adds eight framing bytes and two assigned IDs, and removes a
	// nonempty run ID. Signed 64-bit positions need at most 19 decimal bytes.
	// The append limit stays inclusive and unchanged.
	MaxSeedPageBytes = MaxSeedCommandBytes + 8 + 2*len("9223372036854775807") - 1
)

var (
	// ErrSeedNotFound means the destination has no initial-history declaration.
	ErrSeedNotFound = errors.New("run seed not found")
	// ErrSeedConflict means an immutable declaration, append, or publication
	// differs from the value already accepted under that identity.
	ErrSeedConflict = errors.New("run seed conflict")
	// ErrSeedNotPublished means the requested exact history cannot execute yet.
	ErrSeedNotPublished = errors.New("run seed is not published")
	// ErrPreparationAbandoned means settlement permanently closed this attempt,
	// including when settlement arrived before its first begin.
	ErrPreparationAbandoned = errors.New("run preparation attempt abandoned")
)

// ValidateSeedDeclaration checks the cross-field source contract before a store
// resolves source ownership and lifecycle state.
func ValidateSeedDeclaration(d SeedDeclaration) error {
	if d.AgentID == "" || d.RunID == "" || d.CommandID == "" || d.AttemptID == "" {
		return errors.New("preparation agent, run, command and attempt IDs are required")
	}
	switch d.Kind {
	case SeedLiteral:
		if d.SourceRunID != "" || d.SourceEndID != "" || d.ExcludeReasoning {
			return errors.New("literal seed cannot declare a source or source transform")
		}
	case SeedNextTurn, SeedContinuation:
		if d.SessionID == "" || d.SourceRunID == "" || d.SourceRunID == d.RunID {
			return errors.New("referenced history requires a session and a distinct source run")
		}
		if d.Kind == SeedNextTurn && d.SourceEndID != "" {
			return errors.New("completed source position is assigned by the store")
		}
		if d.Kind == SeedContinuation && (d.SourceEndID == "" || d.ExcludeReasoning) {
			return errors.New("continuation requires its exact saved position and preserves reasoning")
		}
	default:
		return fmt.Errorf("invalid seed kind %q", d.Kind)
	}
	return ValidateSeedCommandSize(d)
}

// ValidateSeedAppend checks record identity and the literal/reference union.
// The runtime validates literal model messages before publishing the chain.
func ValidateSeedAppend(command SeedAppend) error {
	r := command.Record
	if command.RunID == "" || command.AttemptID == "" || r.ID != "" || r.Key == "" || r.PreviousID == "" {
		return errors.New("preparation append requires run, attempt, key and previous position, with no assigned ID")
	}
	var variants int
	if len(r.Messages) > 0 {
		variants++
	}
	if r.Prefix != nil {
		variants++
	}
	if len(r.Prepared) > 0 {
		variants++
	}
	if r.LiteralPart != nil {
		variants++
		if err := r.LiteralPart.validate(); err != nil {
			return err
		}
	}
	if variants != 1 {
		return errors.New("preparation record requires exactly one complete literal, literal fragment, history prefix or compiled-start part")
	}
	if len(r.Messages) > 0 && !json.Valid(r.Messages) {
		return errors.New("seed literal is not valid JSON")
	}
	return ValidateSeedCommandSize(command)
}

// ValidateSeedCommandSize checks the complete JSON command, including identity
// and framing. Adapters also enforce their actual encoded transport bound.
func ValidateSeedCommandSize(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode seed command: %w", err)
	}
	if len(data) > MaxSeedCommandBytes {
		return fmt.Errorf("seed command size %d exceeds %d bytes", len(data), MaxSeedCommandBytes)
	}
	return nil
}

// ValidateSeedPageSize counts the complete logical response independently from
// append admission. Stores also use it before committing an assigned record,
// with that record's ID as a possible continuation cursor.
func ValidateSeedPageSize(page SeedPage) error {
	data, err := json.Marshal(page)
	if err != nil {
		return fmt.Errorf("encode seed page: %w", err)
	}
	if len(data) > MaxSeedPageBytes {
		return fmt.Errorf("seed page size %d exceeds %d bytes", len(data), MaxSeedPageBytes)
	}
	return nil
}

// EqualSeedDeclarations compares immutable preparation intent. Empty optional
// collections have the same meaning whether a transport decodes them as nil or
// empty; prompt order and every actual scope value remain significant.
func EqualSeedDeclarations(a, b SeedDeclaration) bool {
	return a.AgentID == b.AgentID && a.RunID == b.RunID && a.SessionID == b.SessionID &&
		a.CommandID == b.CommandID && a.AttemptID == b.AttemptID &&
		a.Kind == b.Kind && a.SourceRunID == b.SourceRunID && a.SourceEndID == b.SourceEndID &&
		a.ExcludeReasoning == b.ExcludeReasoning &&
		slices.EqualFunc(a.RenderedPrompts, b.RenderedPrompts, func(a, b prompt.RenderEvent) bool {
			return a.PromptID == b.PromptID && a.Version == b.Version &&
				a.Scope.SessionID == b.Scope.SessionID && maps.Equal(a.Scope.Labels, b.Scope.Labels)
		})
}
