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
	"goa.design/goa-ai/runtime/agent/telemetry"
	aitools "goa.design/goa-ai/runtime/agent/tools"
)

type (
	// agentJSONPayloadConverter wraps Temporal's JSON payload converter and
	// rehydrates planner.ToolResult.Result using the tool's generated result codec.
	//
	// Temporal's default JSON converter decodes `any` fields as JSON-shaped values
	// (map[string]any, []any, float64, ...). This violates the goa-ai contract that
	// planner.ToolResult.Result contains the concrete generated result type produced
	// by the tool's result codec.
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
		ToolResults []toolResultWire
		Finalize    *planner.Termination
	}

	runOutputWire struct {
		// See planActivityInputWire: these names match Temporal's default JSON encoding.
		AgentID    agent.Ident
		RunID      string
		Final      *model.Message
		ToolEvents []toolResultWire
		Notes      []*planner.PlannerAnnotation
		Usage      *model.TokenUsage
	}

	toolResultWire struct {
		// See planActivityInputWire: these names match Temporal's default JSON encoding.
		Name          aitools.Ident
		Result        json.RawMessage
		Artifacts     []*planner.Artifact
		Bounds        *agent.Bounds
		Error         *planner.ToolError
		RetryHint     *planner.RetryHint
		Telemetry     *telemetry.ToolTelemetry
		ToolCallID    string
		ChildrenCount int
		RunLink       *run.Handle
	}

	toolResultsSetWire struct {
		// See planActivityInputWire: these names match Temporal's default JSON encoding.
		RunID      string
		ID         string
		Results    []toolResultWire
		RetryHints []*planner.RetryHint
	}
)

// NewAgentDataConverter returns a Temporal data converter that preserves concrete
// tool result types across activity/workflow boundaries.
//
// Temporal's default JSON payload converter decodes `any` fields as JSON-shaped
// values (map[string]any, []any, float64, ...). This breaks the goa-ai contract
// that planner.ToolResult.Result contains the concrete generated result type
// produced by the tool's result codec.
//
// The returned converter installs custom payload converters for goa-ai API
// payloads that carry planner.ToolResult values (notably api.PlanActivityInput
// and api.RunOutput). These converters decode ToolResult.Result back into the
// concrete generated Go type using the tool's generated result codec.
//
// spec must return the ToolSpec for a tool name known to the agent runtime.
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
		w, err := encodeRunOutputWire(c.spec, v)
		if err != nil {
			return nil, err
		}
		return c.JSONPayloadConverter.ToPayload(w)
	case api.RunOutput:
		return c.ToPayload(&v)
	case *api.PlanActivityInput:
		w, err := encodePlanActivityInputWire(c.spec, v)
		if err != nil {
			return nil, err
		}
		return c.JSONPayloadConverter.ToPayload(w)
	case api.PlanActivityInput:
		return c.ToPayload(&v)
	case *api.ToolResultsSet:
		w, err := encodeToolResultsSetWire(c.spec, v)
		if err != nil {
			return nil, err
		}
		return c.JSONPayloadConverter.ToPayload(w)
	case api.ToolResultsSet:
		return c.ToPayload(&v)
	case *planner.ToolResult:
		w, err := encodeToolResultWire(c.spec, v)
		if err != nil {
			return nil, err
		}
		return c.JSONPayloadConverter.ToPayload(w)
	case planner.ToolResult:
		return c.ToPayload(&v)
	default:
		return c.JSONPayloadConverter.ToPayload(value)
	}
}

func (c *agentJSONPayloadConverter) FromPayload(p *commonpb.Payload, valuePtr any) error {
	switch valuePtr.(type) {
	case **api.RunOutput:
		return decodeRunOutput(c.spec, p, valuePtr)
	case **api.PlanActivityInput:
		return decodePlanActivityInput(c.spec, p, valuePtr)
	case **api.ToolResultsSet:
		return decodeToolResultsSet(c.spec, p, valuePtr)
	case **planner.ToolResult:
		return decodeToolResult(c.spec, p, valuePtr)
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

func decodeRunOutput(specFn func(aitools.Ident) (*aitools.ToolSpec, bool), p *commonpb.Payload, valuePtr any) error {
	var w runOutputWire
	if err := decodeJSONPayload(p, &w); err != nil {
		return err
	}

	events := make([]*planner.ToolResult, 0, len(w.ToolEvents))
	for _, trw := range w.ToolEvents {
		tr, err := decodeToolResultWire(specFn, trw)
		if err != nil {
			return err
		}
		events = append(events, tr)
	}

	var dst *api.RunOutput
	switch v := valuePtr.(type) {
	case **api.RunOutput:
		if v == nil {
			return fmt.Errorf("temporal: run output decoder got nil **api.RunOutput")
		}
		if *v == nil {
			*v = &api.RunOutput{}
		}
		dst = *v
	default:
		return fmt.Errorf("temporal: run output decoder requires **api.RunOutput, got %T", valuePtr)
	}
	if dst == nil {
		return fmt.Errorf("temporal: run output is nil")
	}

	dst.AgentID = w.AgentID
	dst.RunID = w.RunID
	dst.Final = w.Final
	dst.ToolEvents = events
	dst.Notes = w.Notes
	dst.Usage = w.Usage
	return nil
}

func decodePlanActivityInput(specFn func(aitools.Ident) (*aitools.ToolSpec, bool), p *commonpb.Payload, valuePtr any) error {
	var w planActivityInputWire
	if err := decodeJSONPayload(p, &w); err != nil {
		return err
	}

	results := make([]*planner.ToolResult, 0, len(w.ToolResults))
	for _, trw := range w.ToolResults {
		tr, err := decodeToolResultWire(specFn, trw)
		if err != nil {
			return err
		}
		results = append(results, tr)
	}

	var dst *api.PlanActivityInput
	switch v := valuePtr.(type) {
	case **api.PlanActivityInput:
		if v == nil {
			return fmt.Errorf("temporal: plan activity input decoder got nil **api.PlanActivityInput")
		}
		if *v == nil {
			*v = &api.PlanActivityInput{}
		}
		dst = *v
	default:
		return fmt.Errorf("temporal: plan activity input decoder requires **api.PlanActivityInput, got %T", valuePtr)
	}
	if dst == nil {
		return fmt.Errorf("temporal: plan activity input is nil")
	}

	dst.AgentID = w.AgentID
	dst.RunID = w.RunID
	dst.Messages = w.Messages
	dst.RunContext = w.RunContext
	dst.ToolResults = results
	dst.Finalize = w.Finalize
	return nil
}

func decodeToolResultsSet(specFn func(aitools.Ident) (*aitools.ToolSpec, bool), p *commonpb.Payload, valuePtr any) error {
	var w toolResultsSetWire
	if err := decodeJSONPayload(p, &w); err != nil {
		return err
	}

	results := make([]*planner.ToolResult, 0, len(w.Results))
	for _, trw := range w.Results {
		tr, err := decodeToolResultWire(specFn, trw)
		if err != nil {
			return err
		}
		results = append(results, tr)
	}

	var dst *api.ToolResultsSet
	switch v := valuePtr.(type) {
	case **api.ToolResultsSet:
		if v == nil {
			return fmt.Errorf("temporal: tool results set decoder got nil **api.ToolResultsSet")
		}
		if *v == nil {
			*v = &api.ToolResultsSet{}
		}
		dst = *v
	default:
		return fmt.Errorf("temporal: tool results set decoder requires **api.ToolResultsSet, got %T", valuePtr)
	}
	if dst == nil {
		return fmt.Errorf("temporal: tool results set is nil")
	}

	dst.RunID = w.RunID
	dst.ID = w.ID
	dst.Results = results
	dst.RetryHints = w.RetryHints
	return nil
}

func decodeToolResult(specFn func(aitools.Ident) (*aitools.ToolSpec, bool), p *commonpb.Payload, valuePtr any) error {
	var w toolResultWire
	if err := decodeJSONPayload(p, &w); err != nil {
		return err
	}

	tr, err := decodeToolResultWire(specFn, w)
	if err != nil {
		return err
	}

	var dst *planner.ToolResult
	switch v := valuePtr.(type) {
	case **planner.ToolResult:
		if v == nil {
			return fmt.Errorf("temporal: tool result decoder got nil **planner.ToolResult")
		}
		if *v == nil {
			*v = &planner.ToolResult{}
		}
		dst = *v
	default:
		return fmt.Errorf("temporal: tool result decoder requires **planner.ToolResult, got %T", valuePtr)
	}
	if dst == nil {
		return fmt.Errorf("temporal: tool result is nil")
	}

	*dst = *tr
	return nil
}

func decodeToolResultWire(specFn func(aitools.Ident) (*aitools.ToolSpec, bool), w toolResultWire) (*planner.ToolResult, error) {
	var decoded any
	if w.Error == nil && len(w.Result) > 0 {
		spec, ok := specFn(w.Name)
		if !ok || spec == nil {
			return nil, fmt.Errorf("temporal: unknown tool spec for result %s", w.Name)
		}
		res, err := spec.Result.Codec.FromJSON(w.Result)
		if err != nil {
			return nil, fmt.Errorf("temporal: decode %s tool result: %w", w.Name, err)
		}
		decoded = res
	}

	return &planner.ToolResult{
		Name:          w.Name,
		Result:        decoded,
		Artifacts:     w.Artifacts,
		Bounds:        w.Bounds,
		Error:         w.Error,
		RetryHint:     w.RetryHint,
		Telemetry:     w.Telemetry,
		ToolCallID:    w.ToolCallID,
		ChildrenCount: w.ChildrenCount,
		RunLink:       w.RunLink,
	}, nil
}

func encodeRunOutputWire(specFn func(aitools.Ident) (*aitools.ToolSpec, bool), in *api.RunOutput) (*runOutputWire, error) {
	if in == nil {
		return nil, fmt.Errorf("temporal: run output is nil")
	}
	events := make([]toolResultWire, 0, len(in.ToolEvents))
	for _, tr := range in.ToolEvents {
		w, err := encodeToolResultWire(specFn, tr)
		if err != nil {
			return nil, err
		}
		events = append(events, *w)
	}
	return &runOutputWire{
		AgentID:    in.AgentID,
		RunID:      in.RunID,
		Final:      in.Final,
		ToolEvents: events,
		Notes:      in.Notes,
		Usage:      in.Usage,
	}, nil
}

func encodePlanActivityInputWire(specFn func(aitools.Ident) (*aitools.ToolSpec, bool), in *api.PlanActivityInput) (*planActivityInputWire, error) {
	if in == nil {
		return nil, fmt.Errorf("temporal: plan activity input is nil")
	}
	results := make([]toolResultWire, 0, len(in.ToolResults))
	for _, tr := range in.ToolResults {
		w, err := encodeToolResultWire(specFn, tr)
		if err != nil {
			return nil, err
		}
		results = append(results, *w)
	}
	return &planActivityInputWire{
		AgentID:     in.AgentID,
		RunID:       in.RunID,
		Messages:    in.Messages,
		RunContext:  in.RunContext,
		ToolResults: results,
		Finalize:    in.Finalize,
	}, nil
}

func encodeToolResultsSetWire(specFn func(aitools.Ident) (*aitools.ToolSpec, bool), in *api.ToolResultsSet) (*toolResultsSetWire, error) {
	if in == nil {
		return nil, fmt.Errorf("temporal: tool results set is nil")
	}
	results := make([]toolResultWire, 0, len(in.Results))
	for _, tr := range in.Results {
		w, err := encodeToolResultWire(specFn, tr)
		if err != nil {
			return nil, err
		}
		results = append(results, *w)
	}
	return &toolResultsSetWire{
		RunID:      in.RunID,
		ID:         in.ID,
		Results:    results,
		RetryHints: in.RetryHints,
	}, nil
}

func encodeToolResultWire(specFn func(aitools.Ident) (*aitools.ToolSpec, bool), tr *planner.ToolResult) (*toolResultWire, error) {
	if tr == nil {
		return &toolResultWire{}, nil
	}
	w := &toolResultWire{
		Name:          tr.Name,
		Artifacts:     tr.Artifacts,
		Bounds:        tr.Bounds,
		Error:         tr.Error,
		RetryHint:     tr.RetryHint,
		Telemetry:     tr.Telemetry,
		ToolCallID:    tr.ToolCallID,
		ChildrenCount: tr.ChildrenCount,
		RunLink:       tr.RunLink,
	}
	if tr.Result == nil {
		return w, nil
	}
	spec, ok := specFn(tr.Name)
	if !ok || spec == nil {
		return nil, fmt.Errorf("temporal: unknown tool spec for result %s", tr.Name)
	}
	if spec.Result.Codec.ToJSON == nil {
		return nil, fmt.Errorf("temporal: missing result codec for %s", tr.Name)
	}
	raw, err := spec.Result.Codec.ToJSON(tr.Result)
	if err != nil {
		return nil, fmt.Errorf("temporal: encode %s tool result: %w", tr.Name, err)
	}
	w.Result = json.RawMessage(raw)
	return w, nil
}
