// Package workflowcodec owns the saved RunOutput format. New completions have an explicit
// encoding tag; old json/plain completions use one frozen schema and never a
// shape guess. Other payload types keep their existing converters.
package workflowcodec

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/toolstats"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

type (
	runOutputPayloadConverter struct{}

	// legacyRunOutput freezes the unversioned top-level result. Its nested
	// contracts, including suspension v7, are unchanged by this migration.
	legacyRunOutput struct {
		AgentID         agent.Ident
		RunID           string
		Final           *model.Message
		FinalToolResult *api.ToolEvent
		ToolEvents      []*api.ToolEvent
		Notes           []*planner.PlannerAnnotation
		Usage           *model.TokenUsage
		Suspension      *api.RunSuspension
	}
)

const runOutputEncoding = "json/goa-ai-run-output-v2"

func (c *runOutputPayloadConverter) Encoding() string {
	return runOutputEncoding
}

func (c *runOutputPayloadConverter) ToPayload(value any) (*commonpb.Payload, error) {
	if !isRunOutputType(reflect.TypeOf(value)) {
		return nil, nil
	}
	if err := validateRunOutputStats(reflect.ValueOf(value)); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode workflow output: %w", err)
	}
	return &commonpb.Payload{
		Metadata: map[string][]byte{converter.MetadataEncoding: []byte(runOutputEncoding)},
		Data:     data,
	}, nil
}

func (c *runOutputPayloadConverter) FromPayload(payload *commonpb.Payload, valuePtr any) error {
	if !isRunOutputType(reflect.TypeOf(valuePtr)) {
		return fmt.Errorf("workflow output encoding requires a RunOutput destination, got %T", valuePtr)
	}
	if err := new(strictJSONPayloadConverter).FromPayload(payload, valuePtr); err != nil {
		return err
	}
	return validateRunOutputStats(reflect.ValueOf(valuePtr))
}

func (c *runOutputPayloadConverter) ToString(payload *commonpb.Payload) string {
	return string(payload.Data)
}

// decodeLegacyRunOutput is selected only by json/plain and a RunOutput
// destination. Unknown fields or invalid old values fail this single decode.
func decodeLegacyRunOutput(payload *commonpb.Payload, valuePtr any) error {
	destination := reflect.ValueOf(valuePtr)
	if destination.Kind() != reflect.Pointer || destination.IsNil() {
		return errors.New("workflow output destination must be a nonnil pointer")
	}
	var old *legacyRunOutput
	if err := new(strictJSONPayloadConverter).FromPayload(payload, &old); err != nil {
		return err
	}
	var output *api.RunOutput
	if old != nil {
		var stats toolstats.Accumulator
		for _, event := range old.ToolEvents {
			if event == nil {
				return errors.New("legacy workflow output contains a nil tool event")
			}
			if err := stats.Add(event.Telemetry); err != nil {
				return fmt.Errorf("legacy workflow output: %w", err)
			}
		}
		count, combined := stats.Result()
		output = &api.RunOutput{
			AgentID: old.AgentID, RunID: old.RunID,
			Final: old.Final, FinalToolResult: old.FinalToolResult,
			Notes: old.Notes, Usage: old.Usage, Suspension: old.Suspension,
			ToolCount: count, ToolTelemetry: combined,
		}
	}
	assignRunOutput(destination.Elem(), output)
	return nil
}

// isRunOutputType recognizes only the top-level result and its pointer forms.
// A struct containing a RunOutput is not a versioned workflow completion.
func isRunOutputType(t reflect.Type) bool {
	if t == nil {
		return false
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t == reflect.TypeFor[api.RunOutput]()
}

// assignRunOutput preserves JSON null semantics: pointer results become nil,
// while a null decoded into a value leaves that value unchanged.
func assignRunOutput(destination reflect.Value, output *api.RunOutput) {
	if output == nil {
		if destination.Kind() == reflect.Pointer {
			destination.SetZero()
		}
		return
	}
	for destination.Kind() == reflect.Pointer {
		if destination.IsNil() {
			destination.Set(reflect.New(destination.Type().Elem()))
		}
		destination = destination.Elem()
	}
	destination.Set(reflect.ValueOf(*output))
}

// validateRunOutputStats rejects impossible statistics at the saved-result
// boundary. The runtime owns aggregation; decoding must not repair bad totals.
func validateRunOutputStats(value reflect.Value) error {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	output := value.Interface().(api.RunOutput)
	if output.ToolCount < 0 {
		return errors.New("workflow output tool count is negative")
	}
	if t := output.ToolTelemetry; t != nil && (t.TokensUsed < 0 || t.DurationMs < 0) {
		return errors.New("workflow output tool telemetry is negative")
	}
	return nil
}
