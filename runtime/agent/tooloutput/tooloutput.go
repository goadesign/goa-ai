// Package tooloutput runs a model operation that must return a typed value by
// calling one ordinary tool. The package builds a private in-memory agent for
// each call, uses the agent runtime's bounded correction flow for invalid tool
// arguments, and returns the payload accepted by the supplied generated codec.
// Failed calls retain original observed errors and complete cause messages;
// applications decide where to display or export them.
package tooloutput

import (
	"context"
	"errors"
	"fmt"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/completion"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/reminder"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	outputPlanner struct {
		request model.Request
		tool    tools.Ident
		choice  model.ToolChoice
	}
)

const (
	modelID        = "tooloutput"
	workflowName   = "tooloutput.workflow"
	taskQueue      = "tooloutput"
	planActivity   = "tooloutput.plan"
	resumeActivity = "tooloutput.resume"
	toolActivity   = "tooloutput.execute"

	agentID agent.Ident = "tooloutput.runner"
)

// Run sends request to client and returns the typed value from one forced
// ordinary tool call. spec supplies only the completion name, description,
// schema, example, and typed codec; Run privately constructs the executable
// tool contract. Invalid model-authored JSON or schema/codec output uses the
// runtime's bounded correction turns. Provider failures and internal contract
// failures are terminal. Errors retain the terminal failure and earlier observed
// causes for errors.Is/As, with full diagnostic text and available response facts.
// Every failure is a *RunError; its TerminalError excludes earlier observations.
// A successful correction returns the accepted value and nil error.
func Run[T any](ctx context.Context, client model.Client, request *model.Request, spec completion.Spec[T]) (T, error) {
	return run(ctx, client, request, spec, model.ToolChoice{
		Mode: model.ToolChoiceModeTool,
		Name: string(spec.Name),
	})
}

// RunAutomatic sends request with automatic tool choice and returns a typed
// value only when the accepted response calls the supplied tool exactly once.
// Text alone is not a result. Invalid arguments use the same bounded structural
// corrections as Run; rejected responses execute no tools. A corrected response
// may succeed even when an earlier invalid response contained multiple calls.
// Provider failures are terminal, and this operation never falls back to forced
// tools or native structured output. Every failure is a *RunError, as in Run.
func RunAutomatic[T any](ctx context.Context, client model.Client, request *model.Request, spec completion.Spec[T]) (T, error) {
	return run(ctx, client, request, spec, model.ToolChoice{Mode: model.ToolChoiceModeAuto})
}

// PlanStart asks the model for the required tool call on the first turn.
func (p outputPlanner) PlanStart(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	return p.plan(ctx, input.Messages, input.Reminders, input.Agent)
}

// PlanResume asks the model to replace invalid tool arguments. A finalization
// request means the configured correction limit was reached without a valid
// call, so the method returns a terminal error.
func (p outputPlanner) PlanResume(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	if input.Finalize != nil {
		return nil, fmt.Errorf("tool output %q was not accepted before the correction limit", p.tool)
	}
	return p.plan(ctx, input.Messages, input.Reminders, input.Agent)
}

// run registers the supplied result contract in a private runtime and applies
// the selected tool choice on every model turn. The caller receives only the
// completed tool's decoded value or the runtime's failure diagnostics.
func run[T any](
	ctx context.Context,
	client model.Client,
	request *model.Request,
	spec completion.Spec[T],
	choice model.ToolChoice,
) (_ T, err error) {
	diagnostics := &runDiagnostics{}
	// Apply the same frozen diagnostic snapshot to every failed exit, including
	// registration and decoding after the private workflow has returned.
	defer func() {
		if err != nil {
			err = &RunError{terminal: err, diagnostics: diagnostics.failure(err)}
		}
	}()
	var zero T
	if err := validateRequest(request); err != nil {
		return zero, err
	}
	privateSpec := privateToolSpec(spec)

	rt := agentruntime.New(storageinmem.New(), agentruntime.WithTracer(diagnostics))
	if err := rt.RegisterModel(modelID, client); err != nil {
		return zero, fmt.Errorf("tool output: %w", err)
	}
	if err := rt.RegisterToolset(agentruntime.ToolsetRegistration{
		Name:  "tooloutput",
		Specs: []tools.ToolSpec{privateSpec},
		Execute: func(_ context.Context, call *agentruntime.ToolCall) (*agentruntime.ToolExecutionResult, error) {
			value, err := privateSpec.ExecutionPayloadCodec.FromJSON(call.Payload)
			if err != nil {
				return nil, fmt.Errorf("decode accepted tool output %q: %w", call.Name, err)
			}
			return agentruntime.Executed(&planner.ToolResult{Name: call.Name, Result: value}), nil
		},
	}); err != nil {
		return zero, fmt.Errorf("tool output: %w", err)
	}
	definition := agentruntime.NewAgentDefinition(
		agentruntime.AgentRoute{
			ID:               agentID,
			WorkflowName:     workflowName,
			DefaultTaskQueue: taskQueue,
		},
		[]tools.ToolSpec{privateSpec},
		nil,
		nil,
		[]tools.Ident{privateSpec.Name},
		nil,
		nil)
	planner := outputPlanner{request: *request, tool: privateSpec.Name, choice: choice}
	if err := rt.RegisterAgent(ctx, agentruntime.AgentRegistration{
		Definition:          definition,
		Planner:             planner,
		WorkflowHandler:     rt.ExecuteWorkflow,
		PlanActivityName:    planActivity,
		ResumeActivityName:  resumeActivity,
		ExecuteToolActivity: toolActivity,
		Policy:              agentruntime.RunPolicy{MaxToolCalls: 1},
	}); err != nil {
		return zero, fmt.Errorf("tool output: %w", err)
	}
	out, err := rt.MustClientFor(definition).OneShotRun(
		ctx,
		request.Messages,
		agentruntime.WithRunCompletionTool(privateSpec.Name),
	)
	if err != nil {
		return zero, fmt.Errorf("tool output: %w", err)
	}
	if out.FinalToolResult == nil {
		return zero, fmt.Errorf("tool output %q completed without a tool result", privateSpec.Name)
	}
	value, err := spec.Codec.FromJSON(out.FinalToolResult.Result)
	if err != nil {
		return zero, fmt.Errorf("decode completed tool output %q: %w", privateSpec.Name, err)
	}
	return value, nil
}

func validateRequest(request *model.Request) error {
	if request == nil {
		return errors.New("tool output request is required")
	}
	if len(request.Tools) > 0 {
		return errors.New("tool output request does not allow tool definitions")
	}
	if request.ToolChoice != nil {
		return errors.New("tool output request does not allow tool choice")
	}
	if request.StructuredOutput != nil {
		return errors.New("tool output request does not allow structured output")
	}
	if request.Stream {
		return errors.New("tool output request does not support streaming")
	}
	return nil
}

// privateToolSpec adapts a typed completion contract to the ordinary tool used
// only inside the private runtime. Callers receive its accepted value without
// attaching executable behavior, policy, or result semantics to model output.
func privateToolSpec[T any](spec completion.Spec[T]) tools.ToolSpec {
	codec := tools.JSONCodec[any]{}
	if spec.Codec.ToJSON != nil {
		codec.ToJSON = func(value any) ([]byte, error) {
			typed, ok := value.(T)
			if !ok {
				var zero T
				return nil, fmt.Errorf(
					"tool output %q internal result has Go type %T, want %T",
					spec.Name,
					value,
					zero,
				)
			}
			return spec.Codec.ToJSON(typed)
		}
	}
	if spec.Codec.FromJSON != nil {
		codec.FromJSON = func(data []byte) (any, error) {
			return spec.Codec.FromJSON(data)
		}
	}
	typeSpec := tools.TypeSpec{
		Name:                     string(spec.Name),
		Schema:                   append(tools.RawJSON(nil), spec.Schema...),
		SchemaWithoutRootExample: append(tools.RawJSON(nil), spec.SchemaWithoutRootExample...),
		ExampleJSON:              append(tools.RawJSON(nil), spec.ExampleJSON...),
		Fields:                   tools.CloneFieldMetadata(spec.Fields),
		Codec:                    codec,
	}
	return tools.ToolSpec{
		Name:                   tools.Ident(spec.Name),
		Description:            spec.Description,
		Payload:                typeSpec,
		ExecutionPayloadSchema: append(tools.RawJSON(nil), spec.SchemaWithoutRootExample...),
		ExecutionPayloadCodec:  codec,
		Result:                 typeSpec,
	}
}

// plan sends the advertised result tool with the selected request choice.
// After argument validation succeeds, it accepts exactly one call for execution.
func (p outputPlanner) plan(
	ctx context.Context,
	messages []*model.Message,
	reminders []reminder.Reminder,
	agentContext planner.PlannerContext,
) (*planner.PlanResult, error) {
	messages, err := reminder.InjectMessages(messages, reminders)
	if err != nil {
		return nil, fmt.Errorf("prepare tool output messages: %w", err)
	}
	client, ok := agentContext.PlannerModelClient(modelID)
	if !ok {
		return nil, errors.New("tool output model is not registered")
	}
	request := p.request
	request.Messages = messages
	request.Tools = agentContext.AdvertisedToolDefinitions()
	request.ToolChoice = &p.choice
	response, err := client.Complete(ctx, &request)
	if err != nil {
		return nil, err
	}
	calls := response.ToolCalls()
	if len(calls) != 1 {
		if p.choice.Mode == model.ToolChoiceModeAuto {
			return nil, planner.NewOutputContractError(
				fmt.Errorf("model returned %d calls for required tool %q with automatic tool choice", len(calls), p.tool),
			)
		}
		return nil, planner.NewOutputContractError(
			fmt.Errorf("model returned %d calls for forced tool %q", len(calls), p.tool),
		)
	}
	call, err := planner.ToolRequestFromModelCall(calls[0])
	if err != nil {
		return nil, err
	}
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{call}}, nil
}
