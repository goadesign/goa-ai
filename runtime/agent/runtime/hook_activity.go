package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
)

type (
	// runCompletedPayload is used to serialize RunCompletedEvent for transport.
	// It converts the error to a string since errors cannot be directly serialized.
	runCompletedPayload struct {
		Status string    `json:"status"`
		Phase  run.Phase `json:"phase"`
		Error  string    `json:"error,omitempty"`
	}

	// turnIDSetter is implemented by hook events that support turn ID stamping.
	turnIDSetter interface {
		SetTurnID(string)
	}
)

// hookActivityName is the engine-registered activity that publishes hook events
// on behalf of workflow code.
const hookActivityName = "runtime.publish_hook"

// hookActivity publishes workflow-emitted hook events outside of deterministic
// workflow execution. It decodes the serialized event from the input, stamps
// the turn ID if present, and publishes to the hook bus.
func (r *Runtime) hookActivity(ctx context.Context, input *HookActivityInput) error {
	evt, err := decodeHookActivityEvent(input)
	if err != nil {
		return err
	}
	if input.TurnID != "" {
		stampHookEventTurnID(evt, input.TurnID)
	}
	if err := r.Bus.Publish(ctx, evt); err != nil {
		r.logWarn(ctx, "hook publish failed", err, "event", evt.Type())
	}
	return nil
}

// newHookActivityInput creates a HookActivityInput from a hook event for
// serialization and transport to the hook activity. The turnID is attached
// to the input so it can be stamped on the event after deserialization.
func newHookActivityInput(evt hooks.Event, turnID string) (*HookActivityInput, error) {
	var payload json.RawMessage
	switch e := evt.(type) {
	case *hooks.RunCompletedEvent:
		p := runCompletedPayload{
			Status: e.Status,
			Phase:  e.Phase,
		}
		if e.Error != nil {
			p.Error = e.Error.Error()
		}
		b, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("marshal run completed payload: %w", err)
		}
		payload = b
	default:
		b, err := json.Marshal(evt)
		if err != nil {
			return nil, fmt.Errorf("marshal hook event payload %q: %w", evt.Type(), err)
		}
		payload = b
	}

	return &HookActivityInput{
		Type:      evt.Type(),
		RunID:     evt.RunID(),
		AgentID:   agent.Ident(evt.AgentID()),
		SessionID: evt.SessionID(),
		TurnID:    turnID,
		Payload:   payload,
	}, nil
}

// decodeHookActivityEvent reconstructs a hooks.Event from the serialized
// HookActivityInput payload. It dispatches based on event type and uses the
// appropriate constructor for each event kind.
func decodeHookActivityEvent(input *HookActivityInput) (hooks.Event, error) {
	switch input.Type {
	case hooks.RunStarted:
		var p hooks.RunStartedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.RunStarted, err)
		}
		return hooks.NewRunStartedEvent(input.RunID, input.AgentID, p.RunContext, p.Input), nil

	case hooks.RunPhaseChanged:
		var p hooks.RunPhaseChangedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.RunPhaseChanged, err)
		}
		return hooks.NewRunPhaseChangedEvent(input.RunID, input.AgentID, input.SessionID, p.Phase), nil

	case hooks.RunPaused:
		var p hooks.RunPausedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.RunPaused, err)
		}
		return hooks.NewRunPausedEvent(input.RunID, input.AgentID, input.SessionID, p.Reason, p.RequestedBy, p.Labels, p.Metadata), nil

	case hooks.RunResumed:
		var p hooks.RunResumedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.RunResumed, err)
		}
		return hooks.NewRunResumedEvent(input.RunID, input.AgentID, input.SessionID, p.Notes, p.RequestedBy, p.Labels, p.MessageCount), nil

	case hooks.RunCompleted:
		var p runCompletedPayload
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.RunCompleted, err)
		}
		var runErr error
		if p.Error != "" {
			runErr = errors.New(p.Error)
		}
		return hooks.NewRunCompletedEvent(input.RunID, input.AgentID, input.SessionID, p.Status, p.Phase, runErr), nil

	case hooks.AgentRunStarted:
		var p hooks.AgentRunStartedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.AgentRunStarted, err)
		}
		return hooks.NewAgentRunStartedEvent(input.RunID, input.AgentID, input.SessionID, p.ToolName, p.ToolCallID, p.ChildRunID, p.ChildAgentID), nil

	case hooks.AwaitClarification:
		var p hooks.AwaitClarificationEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.AwaitClarification, err)
		}
		return hooks.NewAwaitClarificationEvent(input.RunID, input.AgentID, input.SessionID, p.ID, p.Question, p.MissingFields, p.RestrictToTool, p.ExampleInput), nil

	case hooks.AwaitConfirmation:
		var p hooks.AwaitConfirmationEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.AwaitConfirmation, err)
		}
		return hooks.NewAwaitConfirmationEvent(input.RunID, input.AgentID, input.SessionID, p.ID, p.Title, p.Prompt, p.ToolName, p.ToolCallID, p.Payload), nil

	case hooks.AwaitExternalTools:
		var p hooks.AwaitExternalToolsEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.AwaitExternalTools, err)
		}
		return hooks.NewAwaitExternalToolsEvent(input.RunID, input.AgentID, input.SessionID, p.ID, p.Items), nil

	case hooks.ToolAuthorization:
		var p hooks.ToolAuthorizationEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.ToolAuthorization, err)
		}
		return hooks.NewToolAuthorizationEvent(input.RunID, input.AgentID, input.SessionID, p.ToolName, p.ToolCallID, p.Approved, p.Summary, p.ApprovedBy), nil

	case hooks.AssistantMessage:
		var p hooks.AssistantMessageEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.AssistantMessage, err)
		}
		return hooks.NewAssistantMessageEvent(input.RunID, input.AgentID, input.SessionID, p.Message, p.Structured), nil

	case hooks.PlannerNote:
		var p hooks.PlannerNoteEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.PlannerNote, err)
		}
		return hooks.NewPlannerNoteEvent(input.RunID, input.AgentID, input.SessionID, p.Note, p.Labels), nil

	case hooks.ThinkingBlock:
		var p hooks.ThinkingBlockEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.ThinkingBlock, err)
		}
		return hooks.NewThinkingBlockEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			p.Text,
			p.Signature,
			p.Redacted,
			p.ContentIndex,
			p.Final,
		), nil

	case hooks.ToolCallScheduled:
		var p hooks.ToolCallScheduledEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.ToolCallScheduled, err)
		}
		return hooks.NewToolCallScheduledEvent(input.RunID, input.AgentID, input.SessionID, p.ToolName, p.ToolCallID, p.Payload, p.Queue, p.ParentToolCallID, p.ExpectedChildrenTotal), nil

	case hooks.ToolCallUpdated:
		var p hooks.ToolCallUpdatedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.ToolCallUpdated, err)
		}
		return hooks.NewToolCallUpdatedEvent(input.RunID, input.AgentID, input.SessionID, p.ToolCallID, p.ExpectedChildrenTotal), nil

	case hooks.ToolResultReceived:
		var p hooks.ToolResultReceivedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.ToolResultReceived, err)
		}
		return hooks.NewToolResultReceivedEvent(input.RunID, input.AgentID, input.SessionID, p.ToolName, p.ToolCallID, p.ParentToolCallID, p.Result, p.ResultPreview, p.Bounds, p.Artifacts, p.Duration, p.Telemetry, p.Error), nil

	case hooks.PolicyDecision:
		var p hooks.PolicyDecisionEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.PolicyDecision, err)
		}
		return hooks.NewPolicyDecisionEvent(input.RunID, input.AgentID, input.SessionID, p.AllowedTools, p.Caps, p.Labels, p.Metadata), nil

	case hooks.RetryHintIssued:
		var p hooks.RetryHintIssuedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.RetryHintIssued, err)
		}
		return hooks.NewRetryHintIssuedEvent(input.RunID, input.AgentID, input.SessionID, p.Reason, p.ToolName, p.Message), nil

	case hooks.MemoryAppended:
		var p hooks.MemoryAppendedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.MemoryAppended, err)
		}
		return hooks.NewMemoryAppendedEvent(input.RunID, input.AgentID, input.SessionID, p.EventCount), nil

	case hooks.Usage:
		var p hooks.UsageEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.Usage, err)
		}
		evt := hooks.NewUsageEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			p.InputTokens,
			p.OutputTokens,
			p.TotalTokens,
			p.CacheReadTokens,
			p.CacheWriteTokens,
		)
		evt.Model = p.Model
		return evt, nil

	case hooks.HardProtectionTriggered:
		var p hooks.HardProtectionEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", hooks.HardProtectionTriggered, err)
		}
		return hooks.NewHardProtectionEvent(input.RunID, input.AgentID, input.SessionID, p.Reason, p.ExecutedAgentTools, p.ChildrenTotal, p.ToolNames), nil

	default:
		return nil, fmt.Errorf("unsupported hook event type %q", input.Type)
	}
}

// stampHookEventTurnID sets the turn ID on a hook event. All hook events must
// implement turnIDSetter; this will panic if the event does not support it.
func stampHookEventTurnID(evt hooks.Event, turnID string) {
	evt.(turnIDSetter).SetTurnID(turnID)
}
