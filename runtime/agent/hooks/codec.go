package hooks

import (
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/run"
)

type (
	// runCompletedPayload is used to serialize RunCompletedEvent for transport.
	// It converts the error to a string since errors cannot be directly serialized.
	runCompletedPayload struct {
		Status         string    `json:"status"`
		Phase          run.Phase `json:"phase"`
		PublicError    string    `json:"public_error,omitempty"`
		Error          string    `json:"error,omitempty"`
		ErrorProvider  string    `json:"error_provider,omitempty"`
		ErrorOperation string    `json:"error_operation,omitempty"`
		ErrorKind      string    `json:"error_kind,omitempty"`
		ErrorCode      string    `json:"error_code,omitempty"`
		HTTPStatus     int       `json:"http_status,omitempty"`
		Retryable      bool      `json:"retryable"`
	}

	turnIDSetter interface {
		SetTurnID(string)
	}
)

// EncodeToHookInput creates a hook activity input envelope from a hook event for
// serialization and transport to the hook activity.
func EncodeToHookInput(evt Event, turnID string) (*ActivityInput, error) {
	var payload json.RawMessage
	switch e := evt.(type) {
	case *RunCompletedEvent:
		p := runCompletedPayload{
			Status:         e.Status,
			Phase:          e.Phase,
			PublicError:    e.PublicError,
			ErrorProvider:  e.ErrorProvider,
			ErrorOperation: e.ErrorOperation,
			ErrorKind:      e.ErrorKind,
			ErrorCode:      e.ErrorCode,
			HTTPStatus:     e.HTTPStatus,
			Retryable:      e.Retryable,
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

	return &ActivityInput{
		Type:      evt.Type(),
		RunID:     evt.RunID(),
		AgentID:   agent.Ident(evt.AgentID()),
		SessionID: evt.SessionID(),
		TurnID:    turnID,
		Payload:   payload,
	}, nil
}

// DecodeFromHookInput reconstructs a hooks.Event from the serialized hook input.
func DecodeFromHookInput(input *ActivityInput) (Event, error) {
	var evt Event
	switch input.Type {
	case RunStarted:
		var p RunStartedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", RunStarted, err)
		}
		evt = NewRunStartedEvent(input.RunID, input.AgentID, p.RunContext, p.Input)

	case RunPhaseChanged:
		var p RunPhaseChangedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", RunPhaseChanged, err)
		}
		evt = NewRunPhaseChangedEvent(input.RunID, input.AgentID, input.SessionID, p.Phase)

	case RunPaused:
		var p RunPausedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", RunPaused, err)
		}
		evt = NewRunPausedEvent(input.RunID, input.AgentID, input.SessionID, p.Reason, p.RequestedBy, p.Labels, p.Metadata)

	case RunResumed:
		var p RunResumedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", RunResumed, err)
		}
		evt = NewRunResumedEvent(input.RunID, input.AgentID, input.SessionID, p.Notes, p.RequestedBy, p.Labels, p.MessageCount)

	case RunCompleted:
		var p runCompletedPayload
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", RunCompleted, err)
		}
		var runErr error
		if p.Error != "" {
			runErr = errors.New(p.Error)
		}
		rc := NewRunCompletedEvent(input.RunID, input.AgentID, input.SessionID, p.Status, p.Phase, runErr)
		rc.PublicError = p.PublicError
		rc.ErrorProvider = p.ErrorProvider
		rc.ErrorOperation = p.ErrorOperation
		rc.ErrorKind = p.ErrorKind
		rc.ErrorCode = p.ErrorCode
		rc.HTTPStatus = p.HTTPStatus
		rc.Retryable = p.Retryable
		evt = rc

	case AgentRunStarted:
		var p AgentRunStartedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", AgentRunStarted, err)
		}
		evt = NewAgentRunStartedEvent(input.RunID, input.AgentID, input.SessionID, p.ToolName, p.ToolCallID, p.ChildRunID, p.ChildAgentID)

	case AwaitClarification:
		var p AwaitClarificationEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", AwaitClarification, err)
		}
		evt = NewAwaitClarificationEvent(input.RunID, input.AgentID, input.SessionID, p.ID, p.Question, p.MissingFields, p.RestrictToTool, p.ExampleInput)

	case AwaitQuestions:
		var p AwaitQuestionsEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", AwaitQuestions, err)
		}
		evt = NewAwaitQuestionsEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			p.ID,
			p.ToolName,
			p.ToolCallID,
			p.Payload,
			p.Title,
			p.Questions,
		)

	case AwaitConfirmation:
		var p AwaitConfirmationEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", AwaitConfirmation, err)
		}
		evt = NewAwaitConfirmationEvent(input.RunID, input.AgentID, input.SessionID, p.ID, p.Title, p.Prompt, p.ToolName, p.ToolCallID, p.Payload)

	case AwaitExternalTools:
		var p AwaitExternalToolsEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", AwaitExternalTools, err)
		}
		evt = NewAwaitExternalToolsEvent(input.RunID, input.AgentID, input.SessionID, p.ID, p.Items)

	case ToolAuthorization:
		var p ToolAuthorizationEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", ToolAuthorization, err)
		}
		evt = NewToolAuthorizationEvent(input.RunID, input.AgentID, input.SessionID, p.ToolName, p.ToolCallID, p.Approved, p.Summary, p.ApprovedBy)

	case AssistantMessage:
		var p AssistantMessageEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", AssistantMessage, err)
		}
		evt = NewAssistantMessageEvent(input.RunID, input.AgentID, input.SessionID, p.Message, p.Structured)

	case PlannerNote:
		var p PlannerNoteEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", PlannerNote, err)
		}
		evt = NewPlannerNoteEvent(input.RunID, input.AgentID, input.SessionID, p.Note, p.Labels)

	case ThinkingBlock:
		var p ThinkingBlockEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", ThinkingBlock, err)
		}
		evt = NewThinkingBlockEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			p.Text,
			p.Signature,
			p.Redacted,
			p.ContentIndex,
			p.Final,
		)

	case ToolCallScheduled:
		var p ToolCallScheduledEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", ToolCallScheduled, err)
		}
		evt = NewToolCallScheduledEvent(input.RunID, input.AgentID, input.SessionID, p.ToolName, p.ToolCallID, p.Payload, p.Queue, p.ParentToolCallID, p.ExpectedChildrenTotal)

	case ToolCallUpdated:
		var p ToolCallUpdatedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", ToolCallUpdated, err)
		}
		evt = NewToolCallUpdatedEvent(input.RunID, input.AgentID, input.SessionID, p.ToolCallID, p.ExpectedChildrenTotal)

	case ToolResultReceived:
		var p ToolResultReceivedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", ToolResultReceived, err)
		}
		evt = NewToolResultReceivedEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			p.ToolName,
			p.ToolCallID,
			p.ParentToolCallID,
			p.Result,
			p.ResultJSON,
			p.ResultPreview,
			p.Bounds,
			p.Artifacts,
			p.Duration,
			p.Telemetry,
			p.RetryHint,
			p.Error,
		)

	case PolicyDecision:
		var p PolicyDecisionEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", PolicyDecision, err)
		}
		evt = NewPolicyDecisionEvent(input.RunID, input.AgentID, input.SessionID, p.AllowedTools, p.Caps, p.Labels, p.Metadata)

	case RetryHintIssued:
		var p RetryHintIssuedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", RetryHintIssued, err)
		}
		evt = NewRetryHintIssuedEvent(input.RunID, input.AgentID, input.SessionID, p.Reason, p.ToolName, p.Message)

	case MemoryAppended:
		var p MemoryAppendedEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", MemoryAppended, err)
		}
		evt = NewMemoryAppendedEvent(input.RunID, input.AgentID, input.SessionID, p.EventCount)

	case Usage:
		var p UsageEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", Usage, err)
		}
		out := NewUsageEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			p.InputTokens,
			p.OutputTokens,
			p.TotalTokens,
			p.CacheReadTokens,
			p.CacheWriteTokens,
		)
		out.Model = p.Model
		evt = out

	case HardProtectionTriggered:
		var p HardProtectionEvent
		if err := json.Unmarshal(input.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", HardProtectionTriggered, err)
		}
		evt = NewHardProtectionEvent(input.RunID, input.AgentID, input.SessionID, p.Reason, p.ExecutedAgentTools, p.ChildrenTotal, p.ToolNames)

	default:
		return nil, fmt.Errorf("unsupported hook event type %q", input.Type)
	}

	if input.TurnID != "" {
		stampTurnID(evt, input.TurnID)
	}
	return evt, nil
}

func stampTurnID(evt Event, turnID string) {
	evt.(turnIDSetter).SetTurnID(turnID)
}
