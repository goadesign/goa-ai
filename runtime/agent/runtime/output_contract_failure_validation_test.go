// Package runtime tests the complete activity-to-workflow rejection envelope.
// Corrupt combinations must fail before any durable rejection event is written.
package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestValidateOutputContractFailureStateMatrix(t *testing.T) {
	reasonDigest, reasonSize := fingerprintBytes([]byte("reason"))
	emptyDigest, _ := fingerprintBytes(nil)
	responseDigest, responseSize := fingerprintBytes([]byte("response"))
	validModel := func() *OutputContractFailure {
		return &OutputContractFailure{
			Origin:       planner.OutputContractOriginModel,
			ReasonSHA256: reasonDigest,
			ReasonSize:   reasonSize,
		}
	}

	tests := []struct {
		name    string
		failure func() *OutputContractFailure
		valid   bool
	}{
		{
			name:    "model rejection without complete response",
			failure: validModel,
			valid:   true,
		},
		{
			name: "model rejection with unencodable complete response",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				return failure
			},
			valid: true,
		},
		{
			name: "model rejection with previous response fingerprint",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelResponseSHA256 = responseDigest
				failure.ModelResponseSize = responseSize
				failure.ModelResponseFingerprintVersion = api.ModelResponseFingerprintVersionV1
				return failure
			},
			valid: true,
		},
		{
			name: "model rejection with current response fingerprint",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelResponseSHA256 = responseDigest
				failure.ModelResponseSize = responseSize
				failure.ModelResponseFingerprintVersion = api.ModelResponseFingerprintVersionV2
				return failure
			},
			valid: true,
		},
		{
			name: "planner rejection",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.Origin = planner.OutputContractOriginPlanner
				return failure
			},
			valid: true,
		},
		{
			name: "recoverable model rejection",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelResponseFingerprintVersion = api.ModelResponseFingerprintVersionV1
				failure.ModelResponseSHA256 = responseDigest
				failure.ModelResponseSize = responseSize
				failure.ModelOutputRecovery = &ModelOutputRecovery{
					Kind:       planner.ModelOutputRecoveryAnswer,
					Correction: "Use at most eight references.",
				}
				return failure
			},
			valid: true,
		},
		{
			name: "recoverable model rejection without response fingerprint",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelOutputRecovery = &ModelOutputRecovery{
					Kind:       planner.ModelOutputRecoveryAnswer,
					Correction: "Use at most eight references.",
				}
				return failure
			},
		},
		{
			name: "planner rejection with correction",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.Origin = planner.OutputContractOriginPlanner
				failure.ModelOutputRecovery = &ModelOutputRecovery{
					Kind:       planner.ModelOutputRecoveryAnswer,
					Correction: "Try again.",
				}
				return failure
			},
		},
		{
			name: "blank correction",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelOutputRecovery = &ModelOutputRecovery{
					Kind:       planner.ModelOutputRecoveryAnswer,
					Correction: " ",
				}
				return failure
			},
		},
		{
			name: "oversized correction",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelOutputRecovery = &ModelOutputRecovery{
					Kind:       planner.ModelOutputRecoveryAnswer,
					Correction: strings.Repeat("x", outputcontract.MaxCorrectionBytes+1),
				}
				return failure
			},
		},
		{
			name: "recovery kind without correction",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelOutputRecovery = &ModelOutputRecovery{
					Kind: planner.ModelOutputRecoveryAnswer,
				}
				return failure
			},
		},
		{
			name: "unsupported recovery kind",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelResponseFingerprintVersion = api.ModelResponseFingerprintVersionV1
				failure.ModelResponseSHA256 = responseDigest
				failure.ModelResponseSize = responseSize
				failure.ModelOutputRecovery = &ModelOutputRecovery{
					Kind:       "invalid",
					Correction: "Try again.",
				}
				return failure
			},
		},
		{
			name:    "missing envelope",
			failure: func() *OutputContractFailure { return nil },
		},
		{
			name: "negative reason size",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ReasonSize = -1
				return failure
			},
		},
		{
			name: "malformed reason digest",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ReasonSHA256 = "invalid"
				return failure
			},
		},
		{
			name: "false empty reason digest",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ReasonSHA256 = strings.Repeat("0", 64)
				failure.ReasonSize = 0
				return failure
			},
		},
		{
			name: "canonical empty reason",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ReasonSHA256 = emptyDigest
				failure.ReasonSize = 0
				return failure
			},
			valid: true,
		},
		{
			name: "negative response size",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelResponseSHA256 = responseDigest
				failure.ModelResponseSize = -1
				failure.ModelResponseFingerprintVersion = api.ModelResponseFingerprintVersionV1
				return failure
			},
		},
		{
			name: "malformed response digest",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelResponseSHA256 = "invalid"
				failure.ModelResponseSize = responseSize
				failure.ModelResponseFingerprintVersion = api.ModelResponseFingerprintVersionV1
				return failure
			},
		},
		{
			name: "response digest without complete response",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponseSHA256 = responseDigest
				failure.ModelResponseSize = responseSize
				failure.ModelResponseFingerprintVersion = api.ModelResponseFingerprintVersionV1
				return failure
			},
		},
		{
			name: "response digest without version",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelResponseSHA256 = responseDigest
				failure.ModelResponseSize = responseSize
				return failure
			},
		},
		{
			name: "version without response digest",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelResponseFingerprintVersion = api.ModelResponseFingerprintVersionV1
				return failure
			},
		},
		{
			name: "unsupported response fingerprint version",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.ModelResponsePresent = true
				failure.ModelResponseSHA256 = responseDigest
				failure.ModelResponseSize = responseSize
				failure.ModelResponseFingerprintVersion = "unsupported"
				return failure
			},
		},
		{
			name: "planner rejection with model response",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.Origin = planner.OutputContractOriginPlanner
				failure.ModelResponsePresent = true
				return failure
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateOutputContractFailure(test.failure())
			if test.valid {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
		})
	}
}

func TestBoundedPlanActivityOutputFailureRetainsCorrection(t *testing.T) {
	reasonDigest, reasonSize := fingerprintBytes([]byte("reason"))
	responseDigest, responseSize := fingerprintBytes([]byte("response"))
	failure := &OutputContractFailure{
		Origin:                          planner.OutputContractOriginModel,
		ReasonSHA256:                    reasonDigest,
		ReasonSize:                      reasonSize,
		ModelResponsePresent:            true,
		ModelResponseFingerprintVersion: api.ModelResponseFingerprintVersionV1,
		ModelResponseSHA256:             responseDigest,
		ModelResponseSize:               responseSize,
		ModelOutputRecovery: &ModelOutputRecovery{
			Kind:       planner.ModelOutputRecoveryAnswer,
			Correction: "Use at most eight references.",
		},
	}

	output := boundedPlanActivityOutputFailure(
		"batch-1",
		"already published",
		model.TokenUsage{TotalTokens: 12},
		failure,
		errors.New("planner events exceed activity output budget"),
	)

	require.Equal(t, failure.ModelOutputRecovery, output.OutputContractFailure.ModelOutputRecovery)
	require.Equal(t, "already published", output.PublishedAssistantText)
	require.NoError(t, validateOutputContractFailure(output.OutputContractFailure))
}
