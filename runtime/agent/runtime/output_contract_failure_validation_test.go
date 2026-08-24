// Package runtime tests the complete activity-to-workflow rejection envelope.
// Corrupt combinations must fail before any durable rejection event is written.
package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
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
			name: "model rejection with response fingerprint",
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
			name: "planner rejection",
			failure: func() *OutputContractFailure {
				failure := validModel()
				failure.Origin = planner.OutputContractOriginPlanner
				return failure
			},
			valid: true,
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
