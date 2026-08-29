package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutputValidationKindsRestoreWithoutExposingPrivateCause(t *testing.T) {
	const privateCause = "private-response-cause-sentinel"
	kinds := []OutputValidationKind{
		OutputValidationResponseShape,
		OutputValidationOutputBounds,
		OutputValidationToolIdentity,
		OutputValidationToolArguments,
		OutputValidationToolChoice,
		OutputValidationStructuredOutput,
		OutputValidationStreamProtocol,
		OutputValidationUsage,
	}

	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			restored, err := RestoreOutputValidationError(
				kind,
				errors.New(privateCause),
				ResponseEvidence{Present: true},
				nil,
			)

			require.NoError(t, err)
			require.Equal(t, kind, restored.Kind())
			require.EqualError(t, restored, "model output does not meet its request contract")
			require.NotContains(t, restored.Error(), privateCause)
		})
	}
}

func TestRestoreOutputValidationErrorRejectsEmptyAndUnrecognizedKinds(t *testing.T) {
	for _, kind := range []OutputValidationKind{"", "other"} {
		t.Run(string(kind), func(t *testing.T) {
			restored, err := RestoreOutputValidationError(
				kind,
				errors.New("private cause"),
				ResponseEvidence{Present: true},
				nil,
			)

			require.Nil(t, restored)
			require.ErrorContains(t, err, "invalid kind")
		})
	}
}

func TestRequestContractRejectsEmptyAndUnrecognizedKinds(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	for _, kind := range []OutputValidationKind{"", "other"} {
		t.Run(string(kind), func(t *testing.T) {
			require.PanicsWithValue(
				t,
				`model: invalid output validation kind "`+string(kind)+`"`,
				func() {
					rejected := contract.RejectProviderOutput(kind, nil, errors.New("private cause"))
					require.Nil(t, rejected)
				},
			)
		})
	}
}
