package temporal

import (
	"encoding/json"
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	aitools "goa.design/goa-ai/runtime/agent/tools"
)

type (
	// agentJSONPayloadConverter wraps Temporal's JSON payload converter for goa-ai
	// workflow payloads.
	//
	// Temporal's default JSON converter decodes `any` fields as JSON-shaped values
	// (map[string]any, []any, float64, ...). goa-ai forbids `any` across
	// workflow/activity/signal boundaries: tool results and artifacts must cross
	// boundaries as canonical JSON bytes (`api.ToolEvent` / `api.ToolArtifact`).
	//
	// This converter exists to:
	//   - Preserve encoding/json's default field-name behavior for existing workflow
	//     histories (no JSON tags in wire structs).
	//   - Fail fast when code attempts to send `planner.ToolResult` across a Temporal
	//     boundary (it must be wrapped/encoded as `api.ToolEvent`).
	//
	// This converter operates under the same encoding as the default JSON payload
	// converter so that existing workflow history continues to decode correctly.
	agentJSONPayloadConverter struct {
		*converter.JSONPayloadConverter
		spec func(aitools.Ident) (*aitools.ToolSpec, bool)
	}

	planActivityInputWire struct {
		// NOTE: These fields intentionally do not use JSON tags.
		//
		// Temporal's default JSON payload converter marshals goa-ai runtime API types
		// (e.g. api.PlanActivityInput) using encoding/json defaults, which emit the
		// Go field names ("AgentID", "RunID", ...). We must decode that payload
		// verbatim to preserve correctness for existing workflow histories.
		AgentID     agent.Ident
		RunID       string
		Messages    []*model.Message
		RunContext  run.Context
		ToolResults []*api.ToolEvent
		Finalize    *planner.Termination
	}

	runOutputWire struct {
		// See planActivityInputWire: these names match Temporal's default JSON encoding.
		AgentID    agent.Ident
		RunID      string
		Final      *model.Message
		ToolEvents []*api.ToolEvent
		Notes      []*planner.PlannerAnnotation
		Usage      *model.TokenUsage
	}

	toolResultsSetWire struct {
		// See planActivityInputWire: these names match Temporal's default JSON encoding.
		RunID      string
		ID         string
		Results    []*api.ToolEvent
		RetryHints []*planner.RetryHint
	}
)

// NewAgentDataConverter returns a Temporal data converter that enforces goa-ai
// workflow boundary contracts.
//
// Temporal's default JSON payload converter decodes `any` fields as JSON-shaped
// values (map[string]any, []any, float64, ...). goa-ai forbids `any` across
// workflow/activity/signal boundaries: it must be represented as canonical JSON
// bytes (json.RawMessage) and decoded back into typed Go values using the tool's
// generated codecs at the execution boundary (activities), not inside workflow
// serialization.
//
// This converter:
//   - Provides stable encoding/decoding for goa-ai API envelopes.
//   - Fails fast if planner.ToolResult crosses a Temporal boundary (use
//     api.ToolEvent instead).
//
// spec is accepted for API compatibility; the current boundary-safe envelopes
// carry JSON bytes directly and do not require spec lookup during conversion.
func NewAgentDataConverter(spec func(aitools.Ident) (*aitools.ToolSpec, bool)) converter.DataConverter {
	base := converter.NewJSONPayloadConverter()
	return converter.NewCompositeDataConverter(
		converter.NewNilPayloadConverter(),
		converter.NewByteSlicePayloadConverter(),
		converter.NewProtoPayloadConverter(),
		converter.NewProtoJSONPayloadConverter(),
		&agentJSONPayloadConverter{
			JSONPayloadConverter: base,
			spec:                 spec,
		},
	)
}

func (c *agentJSONPayloadConverter) ToPayload(value any) (*commonpb.Payload, error) {
	switch v := value.(type) {
	case *api.RunOutput:
		w, err := encodeRunOutputWire(v)
		if err != nil {
			return nil, err
		}
		return c.JSONPayloadConverter.ToPayload(w)
	case api.RunOutput:
		return c.ToPayload(&v)
	case *api.PlanActivityInput:
		w, err := encodePlanActivityInputWire(v)
		if err != nil {
			return nil, err
		}
		return c.JSONPayloadConverter.ToPayload(w)
	case api.PlanActivityInput:
		return c.ToPayload(&v)
	case *api.ToolResultsSet:
		w, err := encodeToolResultsSetWire(v)
		if err != nil {
			return nil, err
		}
		return c.JSONPayloadConverter.ToPayload(w)
	case api.ToolResultsSet:
		return c.ToPayload(&v)
	case *planner.ToolResult, planner.ToolResult:
		return nil, fmt.Errorf("temporal: planner.ToolResult must not cross workflow boundaries (use api.ToolEvent)")
	default:
		return c.JSONPayloadConverter.ToPayload(value)
	}
}

func (c *agentJSONPayloadConverter) FromPayload(p *commonpb.Payload, valuePtr any) error {
	switch valuePtr.(type) {
	case **api.RunOutput:
		return decodeRunOutput(p, valuePtr)
	case **api.PlanActivityInput:
		return decodePlanActivityInput(p, valuePtr)
	case **api.ToolResultsSet:
		return decodeToolResultsSet(p, valuePtr)
	default:
		return c.JSONPayloadConverter.FromPayload(p, valuePtr)
	}
}

func decodeJSONPayload(p *commonpb.Payload, dst any) error {
	if p == nil {
		return fmt.Errorf("temporal: payload is nil")
	}
	return json.Unmarshal(p.Data, dst)
}

func decodeTarget[T any](valuePtr any, label, typeName string) (*T, error) {
	switch v := valuePtr.(type) {
	case **T:
		if v == nil {
			return nil, fmt.Errorf("temporal: %s decoder got nil **%s", label, typeName)
		}
		if *v == nil {
			*v = new(T)
		}
		if *v == nil {
			return nil, fmt.Errorf("temporal: %s is nil", label)
		}
		return *v, nil
	default:
		return nil, fmt.Errorf("temporal: %s decoder requires **%s, got %T", label, typeName, valuePtr)
	}
}

func decodeRunOutput(p *commonpb.Payload, valuePtr any) error {
	var w runOutputWire
	if err := decodeJSONPayload(p, &w); err != nil {
		return err
	}

	dst, err := decodeTarget[api.RunOutput](valuePtr, "run output", "api.RunOutput")
	if err != nil {
		return err
	}

	dst.AgentID = w.AgentID
	dst.RunID = w.RunID
	dst.Final = w.Final
	dst.ToolEvents = w.ToolEvents
	dst.Notes = w.Notes
	dst.Usage = w.Usage
	return nil
}

func decodePlanActivityInput(p *commonpb.Payload, valuePtr any) error {
	var w planActivityInputWire
	if err := decodeJSONPayload(p, &w); err != nil {
		return err
	}

	dst, err := decodeTarget[api.PlanActivityInput](valuePtr, "plan activity input", "api.PlanActivityInput")
	if err != nil {
		return err
	}

	dst.AgentID = w.AgentID
	dst.RunID = w.RunID
	dst.Messages = w.Messages
	dst.RunContext = w.RunContext
	dst.ToolResults = w.ToolResults
	dst.Finalize = w.Finalize
	return nil
}

func decodeToolResultsSet(p *commonpb.Payload, valuePtr any) error {
	var w toolResultsSetWire
	if err := decodeJSONPayload(p, &w); err != nil {
		return err
	}

	dst, err := decodeTarget[api.ToolResultsSet](valuePtr, "tool results set", "api.ToolResultsSet")
	if err != nil {
		return err
	}

	dst.RunID = w.RunID
	dst.ID = w.ID
	dst.Results = w.Results
	dst.RetryHints = w.RetryHints
	return nil
}

func encodeRunOutputWire(in *api.RunOutput) (*runOutputWire, error) {
	if in == nil {
		return nil, fmt.Errorf("temporal: run output is nil")
	}
	return &runOutputWire{
		AgentID:    in.AgentID,
		RunID:      in.RunID,
		Final:      in.Final,
		ToolEvents: in.ToolEvents,
		Notes:      in.Notes,
		Usage:      in.Usage,
	}, nil
}

func encodePlanActivityInputWire(in *api.PlanActivityInput) (*planActivityInputWire, error) {
	if in == nil {
		return nil, fmt.Errorf("temporal: plan activity input is nil")
	}
	return &planActivityInputWire{
		AgentID:     in.AgentID,
		RunID:       in.RunID,
		Messages:    in.Messages,
		RunContext:  in.RunContext,
		ToolResults: in.ToolResults,
		Finalize:    in.Finalize,
	}, nil
}

func encodeToolResultsSetWire(in *api.ToolResultsSet) (*toolResultsSetWire, error) {
	if in == nil {
		return nil, fmt.Errorf("temporal: tool results set is nil")
	}
	return &toolResultsSetWire{
		RunID:      in.RunID,
		ID:         in.ID,
		Results:    in.Results,
		RetryHints: in.RetryHints,
	}, nil
}
