package toolregistry

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateWireProtocolVersion(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateWireProtocolVersion(WireProtocolVersion))
	require.Error(t, ValidateWireProtocolVersion(0))
	require.Error(t, ValidateWireProtocolVersion(WireProtocolVersion+1))
}

func TestValidateAdmissionRevision(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		revision string
		valid    bool
	}{
		{name: "release revision", revision: "example-release+schema-v1", valid: true},
		{name: "image and release", revision: "release/42@sha256:abc", valid: true},
		{name: "maximum length", revision: "r" + strings.Repeat("a", 255), valid: true},
		{name: "empty", revision: "", valid: false},
		{name: "leading punctuation", revision: ".release", valid: false},
		{name: "whitespace", revision: "release 42", valid: false},
		{name: "too long", revision: "r" + strings.Repeat("a", 256), valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAdmissionRevision(tc.revision)
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
		})
	}
}

func TestValidateRegistrationToken(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		token string
		valid bool
	}{
		{
			name:  "canonical lowercase SHA-256",
			token: "1111111111111111111111111111111111111111111111111111111111111111",
			valid: true,
		},
		{
			name:  "uppercase",
			token: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		{
			name:  "short",
			token: "11111111",
		},
		{
			name:  "non-hex",
			token: "z111111111111111111111111111111111111111111111111111111111111111",
		},
		{
			name: "empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRegistrationToken(tc.token)
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
		})
	}
}

func TestRegistrationTokenBindsFingerprintAndRevision(t *testing.T) {
	t.Parallel()

	fingerprintA := strings.Repeat("a", 64)
	fingerprintB := strings.Repeat("b", 64)
	tokenA, err := RegistrationToken(fingerprintA, "release-a")
	require.NoError(t, err)
	assert.Regexp(t, RegistrationTokenPattern, tokenA)

	differentSchema, err := RegistrationToken(fingerprintB, "release-a")
	require.NoError(t, err)
	differentRevision, err := RegistrationToken(fingerprintA, "release-b")
	require.NoError(t, err)
	assert.NotEqual(t, tokenA, differentSchema)
	assert.NotEqual(t, tokenA, differentRevision)

	_, err = RegistrationToken("not-a-fingerprint", "release-a")
	require.Error(t, err)
	_, err = RegistrationToken(fingerprintA, "contains whitespace")
	require.Error(t, err)
}
