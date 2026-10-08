//go:build integration

package temporal

// These tests start public runtime clients against a dedicated loopback Temporal
// server. A child recovers one certified provider failure while completed tools
// remain completed. Recorded native histories prove child identity and planner
// attempts, then replay without calling providers or executing tools again.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	nativeRecoveryParentPlanner struct {
		tool        tools.Ident
		payload     rawjson.Message
		recordInput rawjson.Message
		resumes     atomic.Int32
	}

	nativeRecoveryHelperPlanner struct{}

	nativeRecoveryProvider struct {
		failFirst bool
		text      string
		calls     atomic.Int32
	}

	nativeRecoveryEvents struct {
		mu           sync.Mutex
		events       []hooks.Event
		streamEvents []stream.Event
		closed       bool
	}
)

const (
	nativeRecoveryFinal      = "The original action and inspection completed."
	nativeRecoveryPermission = "permission"
)

func TestTemporalServerSharedProviderRecoveryNative(t *testing.T) {
	runSharedProviderRecoveryNative(t, false)
}

func TestTemporalServerDirectExecuteAgentChildRecoveryNative(t *testing.T) {
	runSharedProviderRecoveryNative(t, true)
}

func (p *nativeRecoveryParentPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name: genrecords.Read, Payload: p.recordInput,
	}}}, nil
}

func (p *nativeRecoveryParentPlanner) PlanResume(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.resumes.Add(1)
	if len(input.ToolOutputs) < 1 || len(input.ToolOutputs) > 2 ||
		input.ToolOutputs[0].Name != genrecords.Read || input.ToolOutputs[0].Failure != nil {
		return nil, errors.New("parent lost its completed action")
	}
	record, err := genrecords.UnmarshalReadResult(input.ToolOutputs[0].Result)
	if err != nil {
		return nil, err
	}
	if record.Count != 1 {
		return nil, errors.New("parent action result changed")
	}
	if len(input.ToolOutputs) == 1 {
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
			Name: p.tool, Payload: p.payload,
		}}}, nil
	}
	output := input.ToolOutputs[1]
	if output.Name != p.tool || output.Failure != nil {
		return nil, errors.New("parent did not receive the helper result")
	}
	selected, err := genpictures.UnmarshalViewResult(output.Result)
	if err != nil {
		return nil, err
	}
	if selected.ID != "selected" {
		return nil, errors.New("helper result changed")
	}
	client, ok := input.Agent.PlannerModelClient("final")
	if !ok {
		return nil, errors.New("final model is not registered")
	}
	response, err := client.Complete(ctx, &model.Request{Model: "synthetic", Messages: input.Messages})
	if err != nil {
		return nil, err
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
}

func (*nativeRecoveryHelperPlanner) PlanStart(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	// A typed parent result has no model-message origin to select. Use the
	// ordinary client for this probe; the parent separately tests selection of
	// the exact response returned by PlannerModelClient.
	client, ok := input.Agent.ModelClient("helper")
	if !ok {
		return nil, errors.New("helper model is not registered")
	}
	if _, err := client.Complete(ctx, &model.Request{Model: "synthetic", Messages: input.Messages}); err != nil {
		return nil, err
	}
	result, err := genpictures.MarshalViewResult(&genpictures.ViewResult{ID: "selected"})
	if err != nil {
		return nil, err
	}
	return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: result}}, nil
}

func (*nativeRecoveryHelperPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return nil, errors.New("helper must recover the unfinished start, not resume")
}

func (p *nativeRecoveryProvider) Complete(context.Context, *model.Request) (*model.Response, error) {
	if p.calls.Add(1) == 1 && p.failFirst {
		return nil, model.NewProviderError(
			"synthetic", "complete", 429, model.ProviderErrorKindRateLimited,
			"rate_limit", "try later", "synthetic-request", true, nil,
		)
	}
	return &model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: p.text}},
		}},
		StopReason: "stop",
	}, nil
}

func (*nativeRecoveryProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, errors.New("native recovery fixture uses unary calls")
}

func (r *nativeRecoveryEvents) HandleEvent(_ context.Context, event hooks.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

// Send retains each keyed output once and rejects a changed repeat, as required
// by stream.Sink. The assertions inspect the outputs the host would receive.
func (r *nativeRecoveryEvents) Send(_ context.Context, event stream.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("synthetic stream sink is closed")
	}
	if event.EventKey() != "" {
		for _, prior := range r.streamEvents {
			if prior.RunID() == event.RunID() && prior.EventKey() == event.EventKey() {
				if !reflect.DeepEqual(prior, event) {
					return errors.New("synthetic stream received conflicting output")
				}
				return nil
			}
		}
	}
	r.streamEvents = append(r.streamEvents, event)
	return nil
}

func (r *nativeRecoveryEvents) Close(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

// runSharedProviderRecoveryNative uses either generated agent-tool dispatch or
// a public inline executor that calls ExecuteAgentChild. Both receive the real
// active parent's context and recover under that parent's sole allowance.
func runSharedProviderRecoveryNative(t *testing.T, direct bool) {
	t.Helper()
	eng, queue := localRequestEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	store := storageinmem.New()
	events := new(nativeRecoveryEvents)
	rt := agentruntime.New(store, agentruntime.WithEngine(eng),
		agentruntime.WithLogger(telemetry.NoopLogger{}),
		agentruntime.WithStream(events, stream.StreamProfile{Assistant: true, AssistantTurns: true}))
	t.Cleanup(func() { assert.NoError(t, events.Close(t.Context())) })
	subscription, err := rt.Bus.Register(events)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, subscription.Close()) })
	var actions atomic.Int32
	require.NoError(t, rt.RegisterToolset(agentruntime.ToolsetRegistration{
		Name: "records", Specs: genrecords.Specs(),
		Execute: func(_ context.Context, call *agentruntime.ToolCall) (*agentruntime.ToolExecutionResult, error) {
			actions.Add(1)
			return agentruntime.Executed(&planner.ToolResult{
				Name: call.Name, Result: &genrecords.ReadResult{Count: 1},
			}), nil
		},
	}))
	const parentID, helperID = agent.Ident("native.parent"), agent.Ident("native.helper")
	generated := genpictures.SpecView()
	spec := tools.ToolSpec{
		Name:    "delegate.inspect",
		Payload: generated.Payload, Result: generated.Result,
		ExecutionPayloadSchema: generated.ExecutionPayloadSchema,
		ExecutionPayloadCodec:  generated.ExecutionPayloadCodec,
	}
	if !direct {
		spec.IsAgentTool = true
		spec.AgentID = string(helperID)
	}
	helperDefinition := agentruntime.NewAgentDefinition(
		agentruntime.AgentRoute{ID: helperID, WorkflowName: "native.helper.workflow", DefaultTaskQueue: queue},
		[]tools.ToolSpec{spec}, nil, nil, nil, nil, nil,
	)
	helperProvider := &nativeRecoveryProvider{failFirst: true, text: "inspection complete"}
	finalProvider := &nativeRecoveryProvider{text: nativeRecoveryFinal}
	for name, provider := range map[string]*nativeRecoveryProvider{"helper": helperProvider, "final": finalProvider} {
		client, err := model.NewClient(provider)
		require.NoError(t, err)
		require.NoError(t, rt.RegisterModel(name, client))
	}
	require.NoError(t, rt.RegisterAgent(ctx, agentruntime.AgentRegistration{
		Definition: helperDefinition, WorkflowHandler: rt.ExecuteWorkflow,
		Planner:          &nativeRecoveryHelperPlanner{},
		PlanActivityName: "native.helper.plan", ResumeActivityName: "native.helper.resume",
		ExecuteToolActivity: "native.helper.execute",
	}))
	if direct {
		// Inline custom tools receive an admitted ToolCall and workflow context.
		// Derive the child link from that call and let the public runtime method
		// publish its seed and admit the child through normal storage activities.
		require.NoError(t, rt.RegisterToolset(agentruntime.ToolsetRegistration{
			Name: "delegate", Specs: []tools.ToolSpec{spec}, Inline: true,
			Execute: func(ctx context.Context, call *agentruntime.ToolCall) (*agentruntime.ToolExecutionResult, error) {
				w := engine.WorkflowContextFromContext(ctx)
				if w == nil || engine.IsActivityContext(ctx) {
					return nil, errors.New("direct child requires the inline workflow context")
				}
				nested := run.Context{
					RunID:     agentruntime.NestedRunIDForToolCall(call.RunID, call.Name, call.ToolCallID),
					SessionID: call.SessionID, TurnID: call.TurnID,
					ParentRunID: call.RunID, ParentAgentID: call.AgentID,
					ParentToolCallID: call.ToolCallID, Tool: call.Name, ToolArgs: call.Payload,
				}
				out, err := rt.ExecuteAgentChild(w, helperDefinition, []*model.Message{{
					Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: string(call.Payload)}},
				}}, nested)
				if err != nil {
					return nil, err
				}
				if out.FinalToolResult == nil || out.FinalToolResult.Failure != nil {
					return nil, errors.New("direct child did not return its typed final result")
				}
				result, err := genpictures.UnmarshalViewResult(out.FinalToolResult.Result)
				if err != nil {
					return nil, err
				}
				return agentruntime.Executed(&planner.ToolResult{Name: call.Name, Result: result}), nil
			},
		}))
	} else {
		registration := agentruntime.NewAgentToolsetRegistration(agentruntime.AgentToolConfig{
			Definition: helperDefinition, Name: "delegate",
		})
		registration.Specs = []tools.ToolSpec{spec}
		require.NoError(t, rt.RegisterToolset(registration))
	}
	payload, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "selected"})
	require.NoError(t, err)
	recordInput, err := genrecords.MarshalReadPayload(&genrecords.ReadPayload{Query: "selected"})
	require.NoError(t, err)
	parentPlanner := &nativeRecoveryParentPlanner{tool: spec.Name, payload: payload, recordInput: recordInput}
	parentDefinition := agentruntime.NewAgentDefinition(
		agentruntime.AgentRoute{ID: parentID, WorkflowName: "native.parent.workflow", DefaultTaskQueue: queue},
		append(genrecords.Specs(), spec), nil, nil,
		[]tools.Ident{genrecords.Read, spec.Name}, []agentruntime.AgentDefinition{helperDefinition}, nil,
	)
	require.NoError(t, rt.RegisterAgent(ctx, agentruntime.AgentRegistration{
		Definition: parentDefinition, WorkflowHandler: rt.ExecuteWorkflow, Planner: parentPlanner,
		PlanActivityName: "native.parent.plan", ResumeActivityName: "native.parent.resume",
		ExecuteToolActivity: "native.parent.execute",
	}))
	require.NoError(t, rt.Seal(ctx))
	sessionID, rootID := queue+"-session", queue+"-root"
	_, err = store.CreateSession(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	handle, err := rt.MustClient(parentID).Start(ctx, sessionID, []*model.Message{{
		Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Complete the action and inspect the selection."}},
	}}, agentruntime.WithRunID(rootID), agentruntime.WithTurnID("native-turn"),
		agentruntime.WithProviderRetryBudget(time.Hour), agentruntime.WithRunTimeBudget(15*time.Minute))
	require.NoError(t, err)
	output, err := handle.Wait(ctx)
	require.NoError(t, err)
	require.NotNil(t, output.Final)
	assert.Equal(t, nativeRecoveryFinal, output.Final.Text())
	assert.EqualValues(t, 1, actions.Load())
	assert.EqualValues(t, 2, helperProvider.calls.Load())
	assert.EqualValues(t, 1, finalProvider.calls.Load())
	assert.EqualValues(t, 2, parentPlanner.resumes.Load())

	rootRunID := handle.(*workflowHandle).run.GetRunID()
	rootHistory := recoveryTestHistory(t, ctx, eng, rootID, rootRunID)
	var childExecution workflow.Execution
	for _, event := range rootHistory.Events {
		if child := event.GetChildWorkflowExecutionStartedEventAttributes(); child != nil {
			require.Empty(t, childExecution.ID, "parent must issue exactly one child")
			childExecution = workflow.Execution{ID: child.WorkflowExecution.WorkflowId, RunID: child.WorkflowExecution.RunId}
		}
	}
	require.NotEmpty(t, childExecution.ID)
	require.NotEmpty(t, childExecution.RunID)
	childHistory := recoveryTestHistory(t, ctx, eng, childExecution.ID, childExecution.RunID)
	checkNativeRecoveryHistories(t, rootHistory, childHistory,
		workflow.Execution{ID: rootID, RunID: rootRunID}, childExecution)
	events.checkCompletion(t, rootID, childExecution.ID, spec.Name)
	for _, selected := range []struct {
		label     string
		execution workflow.Execution
		history   *historypb.History
	}{
		{"parent", workflow.Execution{ID: rootID, RunID: rootRunID}, rootHistory},
		{"child", childExecution, childHistory},
	} {
		archiveNativeRecoveryHistory(t, selected.label, selected.history)
		replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
			DataConverter: NewAgentDataConverter(), Interceptors: eng.workerOpts.Interceptors,
		})
		require.NoError(t, err)
		name := selected.history.Events[0].GetWorkflowExecutionStartedEventAttributes().WorkflowType.Name
		replayer.RegisterWorkflowWithOptions(eng.temporalWorkflowHandler(rt.ExecuteWorkflow), workflow.RegisterOptions{Name: name})
		require.NoError(t, replayer.ReplayWorkflowExecution(ctx, eng.client.WorkflowService(), nil,
			"default", selected.execution), selected.label)
	}
	assert.EqualValues(t, 1, actions.Load(), "history replay must not execute the completed action")
	assert.EqualValues(t, 2, helperProvider.calls.Load(), "history replay must not call the helper provider")
	assert.EqualValues(t, 1, finalProvider.calls.Load())
	assert.EqualValues(t, 2, parentPlanner.resumes.Load())
	events.checkCompletion(t, rootID, childExecution.ID, spec.Name)
}

// checkNativeRecoveryHistories reads only the two executions started by this
// test. Native child-start records supply addresses; inputs and activity results
// prove inheritance and certification without constructing a recovery message.
func checkNativeRecoveryHistories(t *testing.T, parent, child *historypb.History, root, leaf workflow.Execution) {
	t.Helper()
	converter := NewAgentDataConverter()
	parentStart := parent.Events[0].GetWorkflowExecutionStartedEventAttributes()
	childStart := child.Events[0].GetWorkflowExecutionStartedEventAttributes()
	require.NotNil(t, parentStart)
	require.NotNil(t, childStart)
	require.NotNil(t, childStart.ParentWorkflowExecution)
	require.Equal(t, root.RunID, parentStart.OriginalExecutionRunId)
	require.Equal(t, leaf.RunID, childStart.OriginalExecutionRunId)
	require.NotEmpty(t, parentStart.FirstExecutionRunId)
	require.NotEmpty(t, childStart.FirstExecutionRunId)
	require.Equal(t, root.ID, childStart.ParentWorkflowExecution.WorkflowId)
	require.Equal(t, root.RunID, childStart.ParentWorkflowExecution.RunId)
	var parentInput, childInput api.RunInput
	require.NoError(t, converter.FromPayloads(parentStart.Input, &parentInput))
	require.NoError(t, converter.FromPayloads(childStart.Input, &childInput))
	require.NotNil(t, parentInput.Policy)
	assert.Equal(t, time.Hour, parentInput.Policy.ProviderRetryBudget)
	if childInput.Policy != nil {
		assert.Zero(t, childInput.Policy.ProviderRetryBudget)
	}
	assert.Equal(t, root.ID, childInput.ParentRunID)
	assert.Equal(t, leaf.ID, childInput.RunID)
	var binding recoveryBinding
	require.NotNil(t, childStart.Header)
	require.Contains(t, childStart.Header.Fields, recoveryHeaderName)
	require.NoError(t, converter.FromPayload(childStart.Header.Fields[recoveryHeaderName], &binding))
	assert.True(t, binding.Inherited)
	assert.Equal(t, root.ID, binding.Parent.WorkflowID)
	assert.Equal(t, root.RunID, binding.Parent.RunID)

	parentPlans := nativeRecoveryPlans(t, parent)
	childPlans := nativeRecoveryPlans(t, child)
	assert.Len(t, parentPlans, 3)
	require.Len(t, childPlans, 2)
	var first, second api.PlanActivityInput
	require.NoError(t, converter.FromPayloads(childPlans[0].Input, &first))
	require.NoError(t, converter.FromPayloads(childPlans[1].Input, &second))
	assert.Equal(t, first, second, "recovery must retain the exact unfinished planner input")
	assert.Equal(t, "native.helper.plan", childPlans[0].ActivityType.Name)
	assert.Equal(t, "native.helper.plan", childPlans[1].ActivityType.Name)
	var certified, opens, permissions int
	scheduled := make(map[int64]*historypb.ActivityTaskScheduledEventAttributes)
	for _, event := range child.Events {
		if activity := event.GetActivityTaskScheduledEventAttributes(); activity != nil {
			scheduled[event.EventId] = activity
		}
		if completed := event.GetActivityTaskCompletedEventAttributes(); completed != nil {
			activity := scheduled[completed.ScheduledEventId]
			require.NotNil(t, activity)
			if activity.ActivityType.Name == "native.helper.plan" {
				var output api.PlanActivityOutput
				require.NoError(t, converter.FromPayloads(completed.Result, &output))
				if output.ProviderFailure != nil {
					certified++
					assert.Equal(t, 429, output.ProviderFailure.HTTPStatus())
					assert.Equal(t, model.ProviderErrorKindRateLimited, output.ProviderFailure.Kind())
					assert.True(t, output.ProviderFailure.Retryable())
					assert.Nil(t, output.Result)
				}
			}
		}
		if signal := event.GetWorkflowExecutionSignaledEventAttributes(); signal != nil && signal.SignalName == recoverySignalName {
			var frame recoveryFrame
			require.NoError(t, converter.FromPayloads(signal.Input, &frame))
			if !frame.Ack && frame.Message != nil && frame.Message.Kind == nativeRecoveryPermission {
				permissions++
				assert.Equal(t, root.ID, frame.Source.WorkflowID)
				assert.Equal(t, root.RunID, frame.Source.RunID)
				assert.Equal(t, parentStart.FirstExecutionRunId, frame.FirstRunID)
			}
		}
	}
	for _, event := range parent.Events {
		if signal := event.GetWorkflowExecutionSignaledEventAttributes(); signal != nil && signal.SignalName == recoverySignalName {
			var frame recoveryFrame
			require.NoError(t, converter.FromPayloads(signal.Input, &frame))
			if !frame.Ack && frame.Open != nil {
				opens++
				assert.Equal(t, leaf.ID, frame.Source.WorkflowID)
				assert.Equal(t, leaf.RunID, frame.Source.RunID)
				assert.Equal(t, childStart.FirstExecutionRunId, frame.FirstRunID)
				assert.Equal(t, leaf.RunID, frame.Request.RunID)
				assert.Equal(t, 429, frame.Open.Provider.HTTP)
			}
		}
	}
	assert.Equal(t, 1, certified)
	assert.Equal(t, 1, opens)
	assert.Equal(t, 2, permissions, "one wait and one retry require root permission")
}

// nativeRecoveryPlans checks the recorded attempt policy on every planner
// activity and rejects native retries before returning its scheduled inputs.
func nativeRecoveryPlans(t *testing.T, history *historypb.History) []*historypb.ActivityTaskScheduledEventAttributes {
	t.Helper()
	var plans []*historypb.ActivityTaskScheduledEventAttributes
	scheduled := make(map[int64]bool)
	var completions int
	for _, event := range history.Events {
		if value := event.GetActivityTaskScheduledEventAttributes(); value != nil {
			switch value.ActivityType.Name {
			case "native.parent.plan", "native.parent.resume", "native.helper.plan", "native.helper.resume":
				require.NotNil(t, value.RetryPolicy)
				assert.EqualValues(t, 1, value.RetryPolicy.MaximumAttempts)
				plans = append(plans, value)
				scheduled[event.EventId] = true
			}
		}
		if value := event.GetActivityTaskStartedEventAttributes(); value != nil && scheduled[value.ScheduledEventId] {
			assert.EqualValues(t, 1, value.Attempt)
		}
		if event.GetWorkflowExecutionCompletedEventAttributes() != nil {
			completions++
		}
	}
	assert.Equal(t, 1, completions)
	return plans
}

func (r *nativeRecoveryEvents) checkCompletion(t *testing.T, parent, child string, tool tools.Ident) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var actions, helperResults, finals, parentCompletions, childCompletions int
	for _, event := range r.events {
		switch value := event.(type) {
		case *hooks.ToolResultReceivedEvent:
			if value.RunID() == parent && value.ToolName == genrecords.Read {
				actions++
			}
			if value.RunID() == parent && value.ToolName == tool {
				helperResults++
			}
		case *hooks.RunCompletedEvent:
			assert.Equal(t, "success", value.Status)
			if value.RunID() == parent {
				parentCompletions++
			}
			if value.RunID() == child {
				childCompletions++
			}
		}
	}
	for _, event := range r.streamEvents {
		assert.NotEqual(t, child, event.RunID(), "the helper probe must not publish assistant output")
		if turn, ok := event.(stream.AssistantTurn); ok {
			assert.Equal(t, parent, turn.RunID())
			assert.NotEmpty(t, turn.Data.ResponseID)
			require.Len(t, turn.Data.Messages, 1)
			assert.Equal(t, nativeRecoveryFinal, turn.Data.Messages[0].Text())
			finals++
		}
	}
	assert.Equal(t, 1, actions)
	assert.Equal(t, 1, helperResults)
	assert.Equal(t, 1, finals, "the canonical final response is committed once")
	assert.Equal(t, 1, parentCompletions)
	assert.Equal(t, 1, childCompletions)
}

// archiveNativeRecoveryHistory optionally saves these selected synthetic
// histories for review. The caller supplies an existing absolute directory;
// exclusive creation prevents a repeated test from replacing earlier evidence.
func archiveNativeRecoveryHistory(t *testing.T, label string, history *historypb.History) {
	t.Helper()
	directory := os.Getenv("GOA_AI_RECOVERY_TEST_ARTIFACT_DIR")
	if directory == "" {
		return
	}
	require.True(t, filepath.IsAbs(directory), "history artifact directory must be absolute")
	info, err := os.Lstat(directory) //nolint:gosec // G703: the test runner chooses this existing absolute directory; it is not application input.
	require.NoError(t, err)
	require.True(t, info.IsDir(), "history artifact directory must already exist and not be a symlink")
	// This byte limit bounds one saved synthetic test history, not production
	// workflow history. The shared reader requires fewer than 500 events in each history.
	require.LessOrEqual(t, proto.Size(history), 2<<20)
	data, err := protojson.Marshal(history)
	require.NoError(t, err)
	require.LessOrEqual(t, len(data), 8<<20)
	path := filepath.Join(directory, fmt.Sprintf("%s-%s.json", t.Name(), label))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: fixed test names and labels select files in the runner's directory; exclusive creation preserves earlier evidence.
	require.NoError(t, err)
	_, writeErr := file.Write(data)
	require.NoError(t, errors.Join(writeErr, file.Close()))
}
