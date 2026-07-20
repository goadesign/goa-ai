package bedrock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFoundationModelID pins the profile→foundation translation the Runtime
// CountTokens path relies on: geo scopes are stripped, bare foundation IDs pass
// through, and an unrecognized leading namespace fails fast instead of being
// forwarded to Bedrock as a guaranteed ValidationException.
func TestFoundationModelID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		want    string
		wantErr bool
	}{
		{name: "us geo scope", id: "us.anthropic.claude-opus-4-8", want: "anthropic.claude-opus-4-8"},
		{name: "global geo scope", id: "global.anthropic.claude-sonnet-5", want: "anthropic.claude-sonnet-5"},
		{name: "eu geo scope", id: "eu.anthropic.claude-sonnet-5", want: "anthropic.claude-sonnet-5"},
		{name: "au geo scope", id: "au.anthropic.claude-sonnet-5", want: "anthropic.claude-sonnet-5"},
		{name: "jp geo scope", id: "jp.anthropic.claude-sonnet-5", want: "anthropic.claude-sonnet-5"},
		{name: "apac geo scope", id: "apac.anthropic.claude-sonnet-5", want: "anthropic.claude-sonnet-5"},
		{name: "us-gov geo scope", id: "us-gov.anthropic.claude-sonnet-5", want: "anthropic.claude-sonnet-5"},
		{name: "anthropic foundation passthrough", id: "anthropic.claude-opus-4-8", want: "anthropic.claude-opus-4-8"},
		{name: "amazon foundation passthrough", id: "amazon.nova-pro-v1:0", want: "amazon.nova-pro-v1:0"},
		{name: "meta foundation passthrough", id: "meta.llama3-70b-instruct-v1:0", want: "meta.llama3-70b-instruct-v1:0"},
		{name: "unknown geo prefix", id: "usa.anthropic.claude-opus-4-8", wantErr: true},
		{name: "unknown vendor prefix", id: "acme.some-model-v1", wantErr: true},
		{name: "no dotted namespace", id: "claude-opus-4-8", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FoundationModelID(tc.id)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
