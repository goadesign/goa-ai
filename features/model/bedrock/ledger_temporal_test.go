package bedrock

import (
	"context"
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
	require.Equal(t, "ledger_messages", querier.queryType)
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
