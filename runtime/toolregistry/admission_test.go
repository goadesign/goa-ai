package toolregistry

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateAdmissionRevision(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		revision string
		valid    bool
	}{
		{name: "release revision", revision: "2026-07-23.4+441534ae50f6", valid: true},
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
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
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
			token: "270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c",
			valid: true,
		},
		{
			name:  "uppercase",
			token: "270A659D38FF331401280AD7B0C8FDBA673FD02E7114B856A2F12E1C49EEC34C",
		},
		{
			name:  "short",
			token: "270a659d",
		},
		{
			name:  "non-hex",
			token: "z70a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c",
		},
		{
			name: "empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRegistrationToken(tc.token)
			if tc.valid {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
		})
	}
}
