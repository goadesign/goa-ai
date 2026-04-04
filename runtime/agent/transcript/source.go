// Package transcript exposes runtime-owned transcript loading and validation
// seams so provider adapters can rehydrate prior turns without owning workflow
// or engine-specific plumbing.
package transcript

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/converter"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// LedgerSource provides provider-ready messages for a given run when
	// available. Provider adapters use this runtime-owned seam to rehydrate
	// transcripts by RunID without depending on engine internals.
	LedgerSource interface {
		Messages(ctx context.Context, runID string) ([]*model.Message, error)
	}

	// WorkflowQuerier captures the workflow-query capability needed by the
	// Temporal-backed ledger bridge.
	WorkflowQuerier interface {
		QueryWorkflow(ctx context.Context, workflowID, queryType string, args ...any) (converter.EncodedValue, error)
	}

	// temporalLedgerSource loads provider-ready messages through a workflow query.
	// It is engine-owned infrastructure, not provider-specific behavior.
	temporalLedgerSource struct {
		querier WorkflowQuerier
	}
)

const (
	// QueryLedgerMessages is the workflow query that returns provider-ready
	// messages reconstructed from the runtime ledger.
	QueryLedgerMessages = "ledger_messages"
)

// NewTemporalLedgerSource constructs a ledger source backed by workflow queries.
func NewTemporalLedgerSource(querier WorkflowQuerier) LedgerSource {
	return &temporalLedgerSource{querier: querier}
}

// RehydrateMessages returns the provider-ready transcript for a request by
// loading prior run messages first and appending request-local messages after.
//
// Contract:
//   - Empty RunID means "use only request-local messages".
//   - Non-empty RunID requires a configured ledger source.
//   - Ledger lookup failures are returned to the caller; adapters must not
//     silently degrade to request-local messages.
func RehydrateMessages(
	ctx context.Context,
	ledger LedgerSource,
	runID string,
	messages []*model.Message,
) ([]*model.Message, error) {
	merged := make([]*model.Message, 0, len(messages))
	if runID == "" {
		return append(merged, messages...), nil
	}
	if ledger == nil {
		return nil, errors.New("transcript: RunID requires a configured ledger source")
	}
	prior, err := ledger.Messages(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("transcript: load messages for run %q: %w", runID, err)
	}
	merged = make([]*model.Message, 0, len(prior)+len(messages))
	merged = append(merged, prior...)
	merged = append(merged, messages...)
	return merged, nil
}

// Messages returns the provider-ready transcript for the given run.
func (s *temporalLedgerSource) Messages(ctx context.Context, runID string) ([]*model.Message, error) {
	if runID == "" {
		return nil, nil
	}
	if s == nil || s.querier == nil {
		return nil, errors.New("transcript: ledger source is not configured")
	}
	queryResult, err := s.querier.QueryWorkflow(ctx, runID, QueryLedgerMessages)
	if err != nil {
		return nil, err
	}
	if queryResult == nil {
		return nil, fmt.Errorf("transcript: ledger query for run %q returned no result", runID)
	}
	var msgs []*model.Message
	if err := queryResult.Get(&msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}
