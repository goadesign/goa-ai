// These checks keep factual context separate from assessed content and make
// decision variants distinguishable in serialized reports.
package eval

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecisionJSONDoesNotInventCalibratedRationales(t *testing.T) {
	for _, test := range []struct {
		decision Decision
		kind     string
	}{
		{Calibrated{Model: "classifier-1", QualificationID: "qualification", Prediction: predictedLabels([]Claim{{ID: "requirement", Text: "Complete."}}, .97).Predictions[0]}, "calibrated"},
		{Reasoned{Judgment: Judgment{ClaimID: "requirement", Label: Indeterminate, Rationale: "The answer contradicts itself."}}, "reasoned"},
		{Unresolved{Reason: "The captured evidence cannot resolve the disagreement."}, "unresolved"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			data, err := json.Marshal(Assessment{ID: "requirement", Decision: test.decision})
			require.NoError(t, err)
			assert.Contains(t, string(data), `"kind":"`+test.kind+`"`)
			if test.kind != "reasoned" {
				assert.NotContains(t, string(data), "rationale")
			}
		})
	}
}
