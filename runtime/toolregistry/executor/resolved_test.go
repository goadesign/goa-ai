package executor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genreader "goa.design/goa-ai/internal/testpresentation/gen/records/agents/reader"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	"goa.design/goa-ai/internal/tooloperation"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
)

// The executor runs calls saved with the full declaration by earlier releases
// and calls saved with only their compiled registration.
func TestResolvedExecutorKeepsGeneratedResultAndExactAdmissionOnRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resolve func(*genregistry.ResolvedToolset) (*registrycontract.Resolution, error)
	}{
		{name: "direct", resolve: registrycontract.Resolve},
		{name: "compiled", resolve: registrycontract.ResolveCompiled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testResolvedExecutorAdmission(t, tc.resolve)
		})
	}
}

func testResolvedExecutorAdmission(t *testing.T, resolve func(*genregistry.ResolvedToolset) (*registrycontract.Resolution, error)) {
	t.Helper()
	selected := &genregistry.ResolvedToolset{Toolset: genrecords.Toolset(), RegistrationToken: testRegistrationTokenA}
	selected.Toolset.Name = "owner.records"
	selected.Toolset.RegisteredAt = "2026-09-17T00:00:00Z"
	resolved, err := resolve(selected)
	require.NoError(t, err)
	binding, err := resolved.Select("company", genrecords.Read)
	require.NoError(t, err)
	operation, err := tooloperation.NewTaskGet("")
	require.NoError(t, err)
	call := &runtime.ToolCall{Name: genrecords.Read, Registry: binding, Payload: rawjson.Message(`{"query":"authorized selection"}`), ModelPayload: rawjson.Message(`{}`), ExecutionSequence: 1, ExecutionContinuation: operation}
	deadline := testResultStreamExpiration(toolregistry.MaxToolCallWait)
	expiration := testResultStreamExpiration(toolregistry.DefaultResultStreamTTL)
	calls := 0
	client := &genregistry.Client{
		CallResolvedToolEndpoint: func(_ context.Context, input any) (any, error) {
			payload := input.(*genregistry.CallResolvedToolPayload)
			calls++
			assert.Equal(t, testRegistrationTokenA, payload.ExpectedRegistrationToken)
			assert.Equal(t, "owner.records", payload.Toolset)
			assert.Equal(t, "records.read", payload.Tool)
			assert.JSONEq(t, `{"query":"authorized selection"}`, string(payload.PayloadJSON))
			assert.Equal(t, uint64(1), payload.Meta.ExecutionSequence)
			assert.Equal(t, "original-run", payload.Meta.RunID)
			assert.Equal(t, "original-call", payload.Meta.ToolCallID)
			assert.Equal(t, "session", *payload.Meta.SessionID)
			assert.Equal(t, "turn", *payload.Meta.TurnID)
			assert.Equal(t, "parent", *payload.Meta.ParentToolCallID)
			assert.Equal(t, "owner", payload.Meta.Labels["scope"])
			require.NotNil(t, payload.Meta.ExecutionContinuation)
			_, ok := payload.Meta.ExecutionContinuation.Operation.AsTaskGet()
			assert.True(t, ok)
			return &genregistry.CallToolResult{ToolUseID: "original-use", RegistrationToken: testRegistrationTokenA, ExecutionDeadline: deadline.Format(time.RFC3339Nano), ResultStreamExpiresAt: expiration.Format(time.RFC3339Nano)}, nil
		},
		RetryToolEndpoint: func(context.Context, any) (any, error) {
			t.Fatal("resolved retries must use CallResolvedTool")
			return nil, nil
		},
		CallToolEndpoint: func(context.Context, any) (any, error) {
			t.Fatal("resolved admission must use CallResolvedTool")
			return nil, nil
		},
	}
	stream := &fakeStream{t: t, requiredStart: "0", events: []*streaming.Event{
		{ID: "1-0", EventName: toolregistry.ResultEventKey, Payload: mustJSON(t, toolregistry.NewToolResultRetryMessage(testRegistrationTokenA, "original-use", toolregistry.ToolRetryReasonProviderOverloaded, toolregistry.ProviderOverloadRetryAfter))},
		{ID: "2-0", EventName: toolregistry.ResultEventKey, Payload: mustJSON(t, toolregistry.NewToolResultMessage(testRegistrationTokenA, "original-use", json.RawMessage(`{"count":9}`)))},
	}}
	spec := genrecords.SpecRead()
	executor, err := NewResolved("company", client, fakePulseClient{streamID: toolregistry.ResultStreamID("original-use"), stream: stream}, fakeSpecs{spec: &spec})
	require.NoError(t, err)
	result, err := executor.Execute(t.Context(), &runtime.ToolCallMeta{RunID: "original-run", ToolCallID: "original-call", SessionID: "session", TurnID: "turn", ParentToolCallID: "parent", Labels: map[string]string{"scope": "owner"}}, call)
	require.NoError(t, err)
	require.NotNil(t, result.ToolResult)
	require.Nil(t, result.ToolResult.Failure)
	assert.Equal(t, &genrecords.ReadResult{Count: 9}, result.ToolResult.Result)
	assert.Equal(t, 2, calls)
	assert.JSONEq(t, `{}`, string(call.ModelPayload))
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	registration, err := genreader.NewReaderAgentRegistration(rt, genreader.ReaderAgentConfig{Planner: resolvedTestPlanner{}})
	require.NoError(t, err)
	registration.Definition = registration.Definition.WithRegistryTools(resolvedTestSources{})
	require.NoError(t, rt.RegisterRegistry("company", client, fakePulseClient{streamID: toolregistry.ResultStreamID("original-use"), stream: stream}))
	require.NoError(t, rt.RegisterAgent(t.Context(), registration))
	require.NoError(t, genreader.RegisterUsedToolsets(t.Context(), rt, genreader.WithRecordsExecutor(executor)))
	activity, err := rt.ExecuteToolActivity(t.Context(), &runtime.ToolInput{
		Subject:     genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(genreader.AgentID))},
		ToolsetName: genreader.RecordsToolsetName, ToolName: genrecords.Read, Registry: binding,
		RunID: "original-run", ToolCallID: "original-call", SessionID: "session", TurnID: "turn", ParentToolCallID: "parent",
		Payload: call.Payload, OriginalArguments: call.ModelPayload, Labels: map[string]string{"scope": "owner"},
		ExecutionSequence: 1, ExecutionContinuation: operation,
	})
	require.NoError(t, err)
	require.Nil(t, activity.Failure)
	assert.JSONEq(t, `{"count":9}`, string(activity.Payload))
	assert.Equal(t, 4, calls)
	call.Registry = nil
	_, err = executor.Execute(t.Context(), &runtime.ToolCallMeta{}, call)
	require.ErrorContains(t, err, "saved binding")
	assert.Equal(t, 4, calls)
	call.Registry = binding.Clone()
	call.Registry.Registry = "other"
	_, err = executor.Execute(t.Context(), &runtime.ToolCallMeta{}, call)
	require.ErrorContains(t, err, "saved binding")
	assert.Equal(t, 4, calls)
}

type (
	resolvedTestSources struct{}
	resolvedTestPlanner struct{}
)

func (resolvedTestSources) Resolve(context.Context, *runtime.RegistryCatalog) error { return nil }
func (resolvedTestSources) Allows(registry, route, version string) bool {
	return registry == "company" && route == "owner.records" && version == ""
}
func (resolvedTestPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	return nil, assert.AnError
}
func (resolvedTestPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return nil, assert.AnError
}
