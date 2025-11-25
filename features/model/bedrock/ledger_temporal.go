package bedrock

import (
	"context"

	"go.temporal.io/sdk/client"
	"goa.design/goa-ai/runtime/agent/model"
)

// NewTemporalLedgerSource constructs a ledger source backed by a Temporal client.
// It queries the running workflow for provider-ready messages via the
// "ledger_messages" query.
func NewTemporalLedgerSource(c client.Client) ledgerSource {
	return &temporalLedgerSource{c: c}
}

type temporalLedgerSource struct {
	c client.Client
}

func (t *temporalLedgerSource) Messages(ctx context.Context, runID string) ([]*model.Message, error) {
	if t == nil || t.c == nil || runID == "" {
		return nil, nil
	}
	qr, err := t.c.QueryWorkflow(ctx, runID, "", "ledger_messages")
	if err != nil {
		return nil, err
	}
	var msgs []*model.Message
	if err := qr.Get(&msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}
