package transcript

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/model"
)

type fakeEncodedValue struct {
	value any
}

func (f fakeEncodedValue) HasValue() bool {
	return f.value != nil
}

func (f fakeEncodedValue) Get(valuePtr interface{}) error {
	reflect.ValueOf(valuePtr).Elem().Set(reflect.ValueOf(f.value))
	return nil
}

type fakeWorkflowQuerier struct {
	workflowID string
	queryType  string
	result     converter.EncodedValue
	err        error
}

func (f *fakeWorkflowQuerier) QueryWorkflow(ctx context.Context, workflowID, queryType string, args ...any) (converter.EncodedValue, error) {
	f.workflowID = workflowID
	f.queryType = queryType
	return f.result, f.err
}

func TestTemporalLedgerSourceQueriesLedgerMessagesByWorkflowID(t *testing.T) {
	t.Parallel()

	expected := []*model.Message{
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.TextPart{Text: "hello"},
			},
		},
	}
	querier := &fakeWorkflowQuerier{
		result: fakeEncodedValue{value: expected},
	}

	source := NewTemporalLedgerSource(querier)
	msgs, err := source.Messages(context.Background(), "run-123")
	require.NoError(t, err)
	require.Equal(t, "run-123", querier.workflowID)
	require.Equal(t, QueryLedgerMessages, querier.queryType)
	require.Equal(t, expected, msgs)
}

func TestTemporalLedgerSourceSkipsEmptyRunID(t *testing.T) {
	t.Parallel()

	querier := &fakeWorkflowQuerier{}
	source := NewTemporalLedgerSource(querier)
	msgs, err := source.Messages(context.Background(), "")
	require.NoError(t, err)
	require.Nil(t, msgs)
	require.Empty(t, querier.workflowID)
}

func TestTemporalLedgerSourceRejectsMissingQuerierForRunID(t *testing.T) {
	t.Parallel()

	source := NewTemporalLedgerSource(nil)
	msgs, err := source.Messages(context.Background(), "run-123")
	require.Error(t, err)
	require.Nil(t, msgs)
	require.Contains(t, err.Error(), "ledger source is not configured")
}

func TestRehydrateMessagesPrependsLedgerMessages(t *testing.T) {
	t.Parallel()

	prior := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.TextPart{Text: "prior"},
		},
	}}
	local := []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{
			model.TextPart{Text: "local"},
		},
	}}
	querier := &fakeWorkflowQuerier{
		result: fakeEncodedValue{value: prior},
	}

	merged, err := RehydrateMessages(context.Background(), NewTemporalLedgerSource(querier), "run-123", local)
	require.NoError(t, err)
	require.Len(t, merged, 2)
	require.Equal(t, prior[0], merged[0])
	require.Equal(t, local[0], merged[1])
}

func TestRehydrateMessagesRequiresLedgerForRunID(t *testing.T) {
	t.Parallel()

	merged, err := RehydrateMessages(context.Background(), nil, "run-123", nil)
	require.Error(t, err)
	require.Nil(t, merged)
	require.Contains(t, err.Error(), "RunID requires a configured ledger source")
}

func TestRehydrateMessagesPropagatesLedgerErrors(t *testing.T) {
	t.Parallel()

	querier := &fakeWorkflowQuerier{err: errors.New("query failed")}

	merged, err := RehydrateMessages(context.Background(), NewTemporalLedgerSource(querier), "run-123", nil)
	require.Error(t, err)
	require.Nil(t, merged)
	require.Contains(t, err.Error(), `load messages for run "run-123"`)
	require.Contains(t, err.Error(), "query failed")
}
