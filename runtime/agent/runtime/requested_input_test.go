// Public answers select a typed branch. The saved request supplies correlation
// identifiers, ordering, and the original schema used to validate the answer.
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genruninput "goa.design/goa-ai/registry/gen/runinput"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

func TestRequestedInputDerivesPrivateIdentifiers(t *testing.T) {
	cases := []struct {
		name    string
		pending *api.PendingInput
		answer  genruninput.RunInputAnswer
		check   func(*testing.T, *api.PendingInputResponse)
	}{
		{"host", &api.PendingInput{Kind: api.PendingInputKindMCP, MCP: &api.PendingMCPInput{ToolName: "tools.form", ToolCallID: "private-call", Requests: map[string]mcp.InputRequest{"server-question": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Accept?","requestedSchema":{"type":"object","properties":{"accepted":{"type":"boolean"}},"required":["accepted"]}}`)}}}}, genruninput.NewRunInputAnswerMcp(map[string][]byte{"server-question": []byte(`{"action":"accept","content":{"accepted":true},"extension":{"id":9007199254740993}}`)}), func(t *testing.T, r *api.PendingInputResponse) {
			assert.Equal(t, "private-call", r.MCP.ToolCallID)
			assert.Contains(t, string(r.MCP.Responses["server-question"]), "9007199254740993")
		}},
		{"confirmation", &api.PendingInput{Kind: api.PendingInputKindConfirmation, Confirmation: &api.PendingConfirmation{ID: "private-confirmation", ToolName: "tools.write", ToolCallID: "private-call", Prompt: "Approve?", Payload: rawjson.Message(`{}`)}}, genruninput.NewRunInputAnswerConfirmation(false), func(t *testing.T, r *api.PendingInputResponse) {
			assert.Equal(t, "private-confirmation", r.Confirmation.ID)
			assert.False(t, r.Confirmation.Approved)
		}},
		{"clarification", &api.PendingInput{Kind: api.PendingInputKindClarification, Await: &planner.AwaitItem{Kind: planner.AwaitItemKindClarification, Clarification: &planner.AwaitClarification{ID: "private-question", Question: "Which record?"}}}, genruninput.NewRunInputAnswerClarification("record A"), func(t *testing.T, r *api.PendingInputResponse) {
			assert.Equal(t, "private-question", r.Clarification.ID)
			assert.Equal(t, "record A", r.Clarification.Answer)
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			state := &requestedInputState{pending: test.pending}
			public, err := projectRequestedInput(state)
			require.NoError(t, err)
			encoded, err := genruninput.EncodeRequestedInput(public)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "private-call")
			assert.NotContains(t, string(encoded), "private-question")
			assert.NotContains(t, string(encoded), "private-confirmation")
			answer, err := prepareRequestedInputResponse(state, &genruninput.InputResponse{Answer: test.answer})
			require.NoError(t, err)
			test.check(t, answer)
			_, err = prepareRequestedInputResponse(state, &genruninput.InputResponse{Answer: genruninput.NewRunInputAnswerQuestions(&genruninput.InputToolResult{})})
			assert.Error(t, err)
		})
	}
}

func TestRequestedExternalResultsUseExactSavedCallsAndSchema(t *testing.T) {
	schema := rawjson.Message(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)
	await := &planner.AwaitItem{Kind: planner.AwaitItemKindExternalTools, ExternalTools: &planner.AwaitExternalTools{ID: "private-await", Items: []planner.AwaitToolItem{{Name: "tools.read", ToolCallID: "first", ModelToolCallID: "provider-first", Payload: rawjson.Message(`{}`)}, {Name: "tools.read", ToolCallID: "second", ModelToolCallID: "provider-second", Payload: rawjson.Message(`{}`)}}}}
	checkpoint := &workflowCheckpoint{Pending: []checkpointPendingInput{{Await: await, ResponseContracts: map[tools.Ident]checkpointResponseContract{"tools.read": {ResultSchema: schema}}}}}
	state := &requestedInputState{pending: &api.PendingInput{Kind: api.PendingInputKindToolResults, Await: await}, checkpoint: checkpoint}
	public, err := projectRequestedInput(state)
	require.NoError(t, err)
	calls, ok := public.Request.AsExternalTools()
	require.True(t, ok)
	assert.Equal(t, []byte(schema), calls["first"].ResultContract.Schema)
	result := func(value string) *genruninput.InputToolResult {
		return &genruninput.InputToolResult{Outcome: genruninput.NewInputToolOutcomeSuccess(&genruninput.InputToolSuccess{Result: []byte(value)})}
	}
	values := map[string]*genruninput.InputToolResult{"second": result(`{"answer":"B"}`), "first": result(`{"answer":"A"}`)}
	answer := &genruninput.InputResponse{Answer: genruninput.NewRunInputAnswerExternalTools(values)}
	response, err := prepareRequestedInputResponse(state, answer)
	require.NoError(t, err)
	assert.Equal(t, "private-await", response.ToolResults.ID)
	require.Len(t, response.ToolResults.Results, 2)
	assert.Equal(t, "first", response.ToolResults.Results[0].ToolCallID)
	assert.Equal(t, "second", response.ToolResults.Results[1].ToolCallID)
	values["first"] = result(`{"answer":17}`)
	_, err = prepareRequestedInputResponse(state, answer)
	assert.Error(t, err) //nolint:testifylint // The missing-call check uses a separately changed answer and does not use this result.
	delete(values, "first")
	values["unrelated"] = result(`{"answer":"A"}`)
	_, err = prepareRequestedInputResponse(state, answer)
	assert.ErrorContains(t, err, "missing external call")
}

func TestRequestedQuestionsAndToolTextDeriveCorrelation(t *testing.T) {
	questions := &planner.AwaitItem{Kind: planner.AwaitItemKindQuestions, Questions: &planner.AwaitQuestions{ID: "private-await", ToolName: "tools.question", ToolCallID: "private-call", ModelToolCallID: "provider-call", Payload: rawjson.Message(`{}`), Questions: []planner.AwaitQuestion{{ID: "q", Prompt: "Choose", Options: []planner.AwaitQuestionOption{{ID: "a", Label: "A"}}}}}}
	checkpoint := &workflowCheckpoint{Pending: []checkpointPendingInput{{Await: questions, ResponseContracts: map[tools.Ident]checkpointResponseContract{"tools.question": {ResultSchema: rawjson.Message(`{"type":"object","properties":{"choice":{"type":"string","enum":["a"]}},"required":["choice"],"additionalProperties":false}`)}}}}}
	state := &requestedInputState{pending: &api.PendingInput{Kind: api.PendingInputKindToolResults, Await: questions}, checkpoint: checkpoint}
	public, err := projectRequestedInput(state)
	require.NoError(t, err)
	displayed, ok := public.Request.AsQuestions()
	require.True(t, ok)
	require.Len(t, displayed.Questions, 1)
	assert.Equal(t, "q", displayed.Questions[0].ID)
	assert.Equal(t, "Choose", displayed.Questions[0].Prompt)
	require.Len(t, displayed.Questions[0].Options, 1)
	assert.Equal(t, "a", displayed.Questions[0].Options[0].ID)
	assert.Equal(t, "A", displayed.Questions[0].Options[0].Label)
	answer := &genruninput.InputResponse{Answer: genruninput.NewRunInputAnswerQuestions(&genruninput.InputToolResult{Outcome: genruninput.NewInputToolOutcomeSuccess(&genruninput.InputToolSuccess{Result: []byte(`{"choice":"a"}`)})})}
	response, err := prepareRequestedInputResponse(state, answer)
	require.NoError(t, err)
	assert.Equal(t, "private-await", response.ToolResults.ID)
	require.Len(t, response.ToolResults.Results, 1)
	assert.Equal(t, "private-call", response.ToolResults.Results[0].ToolCallID)
	assert.Equal(t, tools.Ident("tools.question"), response.ToolResults.Results[0].Name)
	state = &requestedInputState{pending: &api.PendingInput{Kind: api.PendingInputKindClarification, Await: &planner.AwaitItem{Kind: planner.AwaitItemKindToolClarification, ToolClarification: &planner.AwaitToolClarification{ID: "private-text", ToolName: "tools.text", ToolCallID: "text-call", ModelToolCallID: "provider-text", Payload: rawjson.Message(`{}`), Question: "Explain?"}}}}
	public, err = projectRequestedInput(state)
	require.NoError(t, err)
	text, ok := public.Request.AsClarification()
	require.True(t, ok)
	assert.Equal(t, "Explain?", text.Question)
	response, err = prepareRequestedInputResponse(state, &genruninput.InputResponse{Answer: genruninput.NewRunInputAnswerClarification("A free-text answer")})
	require.NoError(t, err)
	assert.Equal(t, "private-text", response.Clarification.ID)
	assert.Equal(t, "A free-text answer", response.Clarification.Answer)
}

// Saved pagination fields remain part of the requested result contract even
// when no fields are retained; changing the returned slice cannot edit storage.
func TestRequestedResultContractPreservesRetainedFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields []string
	}{
		{name: "no retained fields"},
		{name: "explicit empty fields", fields: []string{}},
		{name: "selected fields", fields: []string{"selection", "window"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			paging := &tools.PagingSpec{ContinueTool: "tools.more", SourceTool: "tools.read", RetainedFields: test.fields, CursorField: "cursor", NextCursorField: "next"}
			state := &requestedInputState{checkpoint: &workflowCheckpoint{Pending: []checkpointPendingInput{{ResponseContracts: map[tools.Ident]checkpointResponseContract{
				"tools.read": {ResultSchema: rawjson.Message(`{"type":"object"}`), Bounds: &tools.BoundsSpec{Paging: paging}},
			}}}}}
			result, err := requestedResultContract(state, "tools.read")
			require.NoError(t, err)
			require.NotNil(t, result.Bounds.Paging.RetainedFields)
			assert.Len(t, result.Bounds.Paging.RetainedFields, len(test.fields))
			assert.Equal(t, "tools.more", *result.Bounds.Paging.ContinueTool)
			assert.Equal(t, "tools.read", *result.Bounds.Paging.SourceTool)
			assert.Equal(t, "cursor", result.Bounds.Paging.CursorField)
			assert.Equal(t, "next", result.Bounds.Paging.NextCursorField)
			encoded, err := genruninput.EncodeInputResultContract(result)
			require.NoError(t, err)
			restored, err := genruninput.DecodeInputResultContract(encoded)
			require.NoError(t, err)
			assert.Equal(t, result, restored)
			if len(test.fields) > 0 {
				assert.Equal(t, test.fields, result.Bounds.Paging.RetainedFields)
				result.Bounds.Paging.RetainedFields[0] = "changed"
				assert.Equal(t, "selection", paging.RetainedFields[0])
			}
		})
	}
}
