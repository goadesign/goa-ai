// Package workflowcodec owns the saved RunOutput format. New completions have an explicit
// encoding tag and a required generated run subject. Earlier Agent-only
// completions must stay with their matching worker. Other payload types retain
// their existing converters.
package workflowcodec

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/api"
)

type (
	runOutputPayloadConverter struct{}
)

const runOutputEncoding = "json/goa-ai-run-output-v3"

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
