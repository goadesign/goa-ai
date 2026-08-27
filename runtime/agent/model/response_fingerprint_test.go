// Model response fingerprint tests protect the durable identity recorded when
// the runtime rejects provider output without retaining the response body.
package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/internal/responseevidence"
)

func TestResponseFingerprintIncludesOutputLimitState(t *testing.T) {
	response := &Response{
		Content: []Message{{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "partial answer"}},
		}},
		StopReason: "max_tokens",
	}
	complete, err := fingerprintResponse(response)
	require.NoError(t, err)

	response.OutputLimited = true
	limited, err := fingerprintResponse(response)

	require.NoError(t, err)
	require.NotEqual(t, complete.sha256, limited.sha256)
	require.Equal(t, complete.size, limited.size)
	evidence := responseEvidencePreflighted(response)
	require.Equal(t, responseevidence.VersionV2, evidence.Version)
	require.Equal(t, limited.sha256, evidence.SHA256)
	require.NoError(t, validateResponseEvidence(evidence))
}

func TestValidateResponseEvidenceAcceptsPreviousVersion(t *testing.T) {
	require.NoError(t, validateResponseEvidence(ResponseEvidence{
		Present: true,
		Version: responseevidence.VersionV1,
		SHA256:  strings.Repeat("a", 64),
		Size:    1,
	}))
}
