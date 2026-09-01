// These tests exercise the public rules custom engines use to retain requests
// and results without sharing mutable caller memory.
package contract_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/engine/contract"
	"goa.design/goa-ai/runtime/agent/model"
)

// contractAdapter is the smallest custom-engine ownership path: it retains one
// accepted request and result and copies both before calling outside code.
type contractAdapter struct {
	request engine.WorkflowStartRequest
	output  *api.RunOutput
}

const changedValue = "changed"

func TestNormalizeRootRequestOwnsValuesAndIdentifiesExactRetries(t *testing.T) {
	request := engine.WorkflowStartRequest{
		ID:        "run-1",
		Workflow:  "assistant",
		TaskQueue: "agents",
		Input: &api.RunInput{
			RunID:  "run-1",
			Labels: map[string]string{"tenant": "acme"},
			Messages: []*model.Message{{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "help"}},
			}},
		},
		Memo: map[string]engine.EncodedValue{
			"trace": {Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte(`"abc"`)},
		},
		SearchAttributes: map[string]any{"tenant": "acme"},
	}

	first, err := contract.NormalizeRootRequest(request)
	require.NoError(t, err)
	second, err := contract.NormalizeRootRequest(request)
	require.NoError(t, err)
	assert.Equal(t, first.Digest, second.Digest)

	request.Input.Labels["tenant"] = changedValue
	request.Input.Messages[0].Parts[0] = model.TextPart{Text: changedValue}
	request.Memo["trace"].Metadata["encoding"][0] = 'x'
	request.SearchAttributes["tenant"] = changedValue
	assert.Equal(t, "acme", first.Request.Input.Labels["tenant"])
	assert.Equal(t, model.TextPart{Text: "help"}, first.Request.Input.Messages[0].Parts[0])
	assert.Equal(t, []byte("json/plain"), first.Request.Memo["trace"].Metadata["encoding"])
	assert.Equal(t, "acme", first.Request.SearchAttributes["tenant"])

	changed, err := contract.NormalizeRootRequest(request)
	require.NoError(t, err)
	assert.NotEqual(t, first.Digest, changed.Digest)
}

func TestNormalizeRootRequestRejectsInvalidRequest(t *testing.T) {
	_, err := contract.NormalizeRootRequest(engine.WorkflowStartRequest{})
	assert.EqualError(t, err, "validate workflow start request: workflow id is required")
}

func TestNormalizeRootRequestRejectsReservedMemo(t *testing.T) {
	_, err := contract.NormalizeRootRequest(engine.WorkflowStartRequest{
		ID:        "run-1",
		Workflow:  "assistant",
		TaskQueue: "agents",
		Input:     &api.RunInput{RunID: "run-1"},
		Memo: map[string]engine.EncodedValue{
			"goa_ai_engine_start_recipe_v1": {},
		},
	})
	assert.EqualError(t, err, `workflow memo key "goa_ai_engine_start_recipe_v1" is reserved`)
}

func TestNormalizeChildRequestOwnsInput(t *testing.T) {
	request := engine.ChildWorkflowRequest{
		ID:        "child-1",
		Workflow:  "assistant-child",
		TaskQueue: "agents",
		Input:     &api.RunInput{RunID: "child-1", Labels: map[string]string{"tenant": "acme"}},
	}

	owned, err := contract.NormalizeChildRequest(request)
	require.NoError(t, err)
	request.Input.Labels["tenant"] = changedValue
	assert.Equal(t, "acme", owned.Input.Labels["tenant"])
}

func TestCustomAdapterIsolatesAttemptsAndReads(t *testing.T) {
	adapter, err := newContractAdapter(engine.WorkflowStartRequest{
		ID:        "run-1",
		Workflow:  "assistant",
		TaskQueue: "agents",
		Input: &api.RunInput{
			RunID:  "run-1",
			Labels: map[string]string{"tenant": "acme"},
		},
	})
	require.NoError(t, err)

	firstInput, err := adapter.inputForAttempt()
	require.NoError(t, err)
	firstInput.Labels["tenant"] = changedValue
	secondInput, err := adapter.inputForAttempt()
	require.NoError(t, err)
	assert.Equal(t, "acme", secondInput.Labels["tenant"])

	require.NoError(t, adapter.retainOutput(&api.RunOutput{
		RunID: "run-1",
		Final: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		},
	}))
	firstRead, err := adapter.readOutput()
	require.NoError(t, err)
	firstRead.Final.Parts[0] = model.TextPart{Text: changedValue}
	secondRead, err := adapter.readOutput()
	require.NoError(t, err)
	assert.Equal(t, model.TextPart{Text: "done"}, secondRead.Final.Parts[0])
}

func TestCopyRunOutputOwnsValuesAndEnforcesLimit(t *testing.T) {
	output := &api.RunOutput{
		RunID: "run-1",
		Final: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		},
	}

	copied, err := contract.CopyRunOutput(output)
	require.NoError(t, err)
	output.Final.Parts[0] = model.TextPart{Text: changedValue}
	assert.Equal(t, model.TextPart{Text: "done"}, copied.Final.Parts[0])

	_, err = contract.CopyRunOutput(&api.RunOutput{RunID: strings.Repeat("x", engine.MaxPayloadBytes)})
	assert.ErrorContains(t, err, "payloads exceed maximum aggregate size")
}

// newContractAdapter retains the private request a custom engine would keep
// after it accepts a start command.
func newContractAdapter(request engine.WorkflowStartRequest) (*contractAdapter, error) {
	normalized, err := contract.NormalizeRootRequest(request)
	if err != nil {
		return nil, err
	}
	return &contractAdapter{request: normalized.Request}, nil
}

// inputForAttempt returns a fresh input for one workflow handler attempt.
func (a *contractAdapter) inputForAttempt() (*api.RunInput, error) {
	return contract.CopyRunInput(a.request.Input)
}

// retainOutput saves one private copy of a successful workflow result.
func (a *contractAdapter) retainOutput(output *api.RunOutput) error {
	owned, err := contract.CopyRunOutput(output)
	if err != nil {
		return err
	}
	a.output = owned
	return nil
}

// readOutput returns a fresh copy for one caller-facing result read.
func (a *contractAdapter) readOutput() (*api.RunOutput, error) {
	return contract.CopyRunOutput(a.output)
}
