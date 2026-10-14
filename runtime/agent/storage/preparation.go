package storage

// Preparation binds a complete uploaded body to one original operation.
// Settlement and publication share the owner's transaction: a caller receives
// either the accepted value or a durable rejection of that upload attempt.

import (
	"errors"
	"fmt"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
)

type (
	// RunPreparationBegin binds a complete original invocation before provider
	// resolution. Exact repetition preserves the owner; changed values conflict.
	RunPreparationBegin struct {
		Operation PreparationOperation
		Origin    genregistry.ExecutionInvocation
	}

	// PreparationOperation identifies the authenticated original command. Its
	// identity stays fixed while separate attempts may resolve current inputs.
	PreparationOperation struct {
		// Subject identifies the executing Agent or service tool.
		Subject genrunidentity.RunSubject
		// RunID is the original allocated workflow identifier.
		RunID string
		// SessionID owns the body; empty identifies a sessionless operation.
		SessionID string
		// CommandID identifies immutable original intent, not resolved prompts.
		CommandID string
	}

	// PreparationAttempt identifies one candidate whose publication can be
	// permanently prevented without changing the original workflow identifier.
	PreparationAttempt struct {
		// Operation is the original command and its owner.
		Operation PreparationOperation
		// AttemptID is retained by the command owner before the first upload.
		AttemptID string
	}

	// SeedPublication accepts only a complete history and compiled start.
	SeedPublication struct {
		// RunID identifies the original allocated workflow.
		RunID string
		// AttemptID identifies the exact candidate being accepted.
		AttemptID string
		// SeedEndID is the final history record, excluding compiled-start parts.
		SeedEndID string
		// EndID is the final compiled-start part in the same body.
		EndID string
		// PreparedBytes is the complete compiled value's byte length.
		PreparedBytes int64
	}

	// RunPreparation describes the immutable accepted body. Recovery reads
	// compiled-start parts after Seed.EndID through EndID using bounded pages.
	RunPreparation struct {
		// Seed describes the exact history portion of this same body.
		Seed RunSeed
		// EndID is the complete body's final position.
		EndID string
		// PreparedBytes is the exact complete compiled-start byte length.
		PreparedBytes int64
	}
)

// ValidatePreparationOperation checks identity where it enters a store.
func ValidatePreparationOperation(operation PreparationOperation) error {
	if _, err := genrunidentity.EncodeRunSubject(&operation.Subject); err != nil {
		return fmt.Errorf("preparation subject: %w", err)
	}
	if operation.RunID == "" || operation.CommandID == "" {
		return errors.New("preparation run and command IDs are required")
	}
	return nil
}

// ValidatePreparationAttempt checks the original operation and candidate.
func ValidatePreparationAttempt(attempt PreparationAttempt) error {
	if err := ValidatePreparationOperation(attempt.Operation); err != nil {
		return err
	}
	if attempt.AttemptID == "" {
		return errors.New("preparation attempt ID is required")
	}
	return nil
}

// ValidateRunPreparationBegin checks the generated invocation and original key
// before either value can reserve an operation in the owning store.
func ValidateRunPreparationBegin(command RunPreparationBegin) error {
	if err := ValidatePreparationOperation(command.Operation); err != nil {
		return err
	}
	if _, err := genregistry.EncodeExecutionInvocation(&command.Origin); err != nil {
		return fmt.Errorf("preparation origin: %w", err)
	}
	return nil
}
