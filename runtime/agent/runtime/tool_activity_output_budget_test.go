package runtime

// This file verifies that tool activities reject oversized results before the
// workflow engine encodes them. Tool implementations store large domain data
// themselves and return a small typed reference.

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// privateBudgetFields verifies that the activity-output walk sees exported
	// values promoted by an embedded private struct.
	privateBudgetFields struct {
		Raw    rawjson.Message
		Number float64
		Text   string
	}

	budgetEnvelope struct {
		privateBudgetFields
	}
)

func TestValidateToolActivityOutputBudget(t *testing.T) {
	require.NoError(t, validateToolActivityOutputBudget(&ToolOutput{Payload: []byte(`{"ok":true}`)}))

	err := validateToolActivityOutputBudget(&ToolOutput{
		Payload: []byte(`"` + strings.Repeat("x", engine.MaxPayloadBytes) + `"`),
	})
	require.ErrorContains(t, err, "store the result and return its typed reference")

	contractErr := outputcontract.NewWithOrigin(err, outputcontract.OriginTool)
	require.Equal(t, outputcontract.OriginTool, contractErr.Origin())
}

func TestPlanActivityOutputBudgetChecksEmbeddedPrivateStructFields(t *testing.T) {
	tests := []struct {
		name    string
		value   budgetEnvelope
		wantErr string
	}{
		{
			name: "invalid raw JSON",
			value: budgetEnvelope{privateBudgetFields: privateBudgetFields{
				Raw: rawjson.Message(`{"broken"`),
			}},
			wantErr: "invalid raw JSON",
		},
		{
			name: "non-finite number",
			value: budgetEnvelope{privateBudgetFields: privateBudgetFields{
				Number: math.Inf(-1),
			}},
			wantErr: "non-finite number",
		},
		{
			name: "oversized string",
			value: budgetEnvelope{privateBudgetFields: privateBudgetFields{
				Text: strings.Repeat("x", maxPlanActivityOutputBytes+1),
			}},
			wantErr: "encoded-size bound",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (&planActivityOutputBudget{}).add(test.value)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}
