package bedrock

import (
	"context"

	"go.temporal.io/sdk/converter"
	"goa.design/goa-ai/runtime/agent/model"
)

// WorkflowQuerier captures the only workflow-query capability the Bedrock
// ledger bridge needs from a durable engine. The workflowID identifies the
// durable workflow, while queryType selects the engine-specific query handler.
type WorkflowQuerier interface {
	QueryWorkflow(ctx context.Context, workflowID, queryType string, args ...any) (converter.EncodedValue, error)
}

// NewTemporalLedgerSource constructs a ledger source backed by a workflow
// querier. It queries the running workflow for provider-ready messages via the
// "ledger_messages" query.
func NewTemporalLedgerSource(c WorkflowQuerier) ledgerSource {
	return &temporalLedgerSource{c: c}
}

type temporalLedgerSource struct {
	c WorkflowQuerier
}

func (t *temporalLedgerSource) Messages(ctx context.Context, runID string) ([]*model.Message, error) {
	if t == nil || t.c == nil || runID == "" {
		return nil, nil
	}
	qr, err := t.c.QueryWorkflow(ctx, runID, "ledger_messages")
	if err != nil {
		return nil, err
	}
	var msgs []*model.Message
	if err := qr.Get(&msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}
