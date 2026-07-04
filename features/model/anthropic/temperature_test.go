package anthropic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// temperatureSupported must match every published Claude model ID shape
// (bare first-party, dated snapshot, Vertex "@" dated) for the generations
// Anthropic has deprecated temperature on. Misclassifying a model here either
// sends a guaranteed-400 request (false positive) or silently drops a caller's
// sampling configuration on a model that still honors it (false negative).
func TestTemperatureSupported(t *testing.T) {
	cases := []struct {
		name    string
		modelID string
		want    bool
	}{
		{"opus-4-6 supports sampling", "claude-opus-4-6", true},
		{"opus-4-6 dated snapshot supports sampling", "claude-opus-4-6-20260201", true},
		{"opus-4-7 omits sampling", "claude-opus-4-7", false},
		{"opus-4-7 dated snapshot omits sampling", "claude-opus-4-7-20260315", false},
		{"opus-4-7 vertex dated omits sampling", "claude-opus-4-7@20260315", false},
		{"opus-4-8 omits sampling", "claude-opus-4-8", false},
		{"future opus-4-9 omits sampling", "claude-opus-4-9", false},
		{"opus-4-1 supports sampling", "claude-opus-4-1-20250805", true},
		{"opus-4-0 supports sampling", "claude-opus-4-0", true},
		{"sonnet-4-5 supports sampling", "claude-sonnet-4-5-20250929", true},
		{"sonnet-4-6 supports sampling", "claude-sonnet-4-6", true},
		{"sonnet-5 omits sampling", "claude-sonnet-5", false},
		{"sonnet-5 vertex dated omits sampling", "claude-sonnet-5@20260201", false},
		{"sonnet-5 dated snapshot omits sampling", "claude-sonnet-5-20260201", false},
		{"fable-5 omits sampling", "claude-fable-5", false},
		{"fable-5 dated snapshot omits sampling", "claude-fable-5-20260315", false},
		{"mythos-5 omits sampling", "claude-mythos-5", false},
		{"mythos-preview supports sampling", "claude-mythos-preview", true},
		{"haiku-4-5 supports sampling", "claude-haiku-4-5-20251001", true},
		{"legacy 3.5 sonnet supports sampling", "claude-3-5-sonnet-20241022", true},
		{"empty model id supports sampling", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, temperatureSupported(tc.modelID))
		})
	}
}

func TestOpusMinorVersion(t *testing.T) {
	cases := []struct {
		name      string
		modelID   string
		wantMinor int
		wantOK    bool
	}{
		{"bare", "claude-opus-4-7", 7, true},
		{"dated snapshot", "claude-opus-4-7-20260315", 7, true},
		{"vertex dated", "claude-opus-4-7@20260315", 7, true},
		{"double digit minor", "claude-opus-4-10", 10, true},
		{"not opus", "claude-sonnet-4-6", 0, false},
		{"opus without minor digits", "claude-opus-4-", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			minor, ok := opusMinorVersion(tc.modelID)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantMinor, minor)
			}
		})
	}
}
