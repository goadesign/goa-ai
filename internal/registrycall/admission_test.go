// These tests reject malformed registry timestamps and preserve the independent
// execution and retention deadlines supplied by a valid generated response.
package registrycall

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestDecodeAdmissionRejectsMalformedRegistryResponses(t *testing.T) {
	for _, test := range []struct {
		name       string
		deadline   string
		expiration string
		want       string
	}{
		{"execution time", "not-a-time", "2030-01-02T03:04:05Z", "registry execution deadline"},
		{"expiration time", "2030-01-02T03:04:04Z", "not-a-time", "registry result expiration"},
		{"independent deadlines", "2030-01-02T03:04:04Z", "2030-01-02T03:04:05Z", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref, err := decodeAdmission(&genregistry.CallToolResult{
				ToolUseID:             "tool-use-1",
				RegistrationToken:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ExecutionDeadline:     test.deadline,
				ResultStreamExpiresAt: test.expiration,
			})
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
				assert.Empty(t, ref.ToolUseID)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, time.Date(2030, 1, 2, 3, 4, 4, 0, time.UTC), ref.ExecutionDeadline)
			assert.Equal(t, time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), ref.ResultStreamExpiresAt)
		})
	}
}
