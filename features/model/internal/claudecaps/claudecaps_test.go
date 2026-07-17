package claudecaps

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TemperatureSupported must classify every published Claude model ID shape
// (bare first-party, dated snapshot, Vertex "@" dated, Bedrock in-region /
// geo / global scopes) correctly for the generations Anthropic deprecated
// temperature on. A false positive sends a guaranteed-400 request; a false
// negative silently drops a caller's sampling configuration on a model that
// still honors it.
func TestTemperatureSupported(t *testing.T) {
	cases := []struct {
		name    string
		modelID string
		want    bool
	}{
		// Opus 4.x boundary: minor >= 7 rejects.
		{"opus-4-6 supports sampling", "claude-opus-4-6", true},
		{"opus-4-6 dated snapshot supports sampling", "claude-opus-4-6-20260201", true},
		{"opus-4-6 bedrock geo supports sampling", "global.anthropic.claude-opus-4-6-v1", true},
		{"opus-4-7 omits sampling", "claude-opus-4-7", false},
		{"opus-4-7 dated snapshot omits sampling", "claude-opus-4-7-20260315", false},
		{"opus-4-7 vertex dated omits sampling", "claude-opus-4-7@20260315", false},
		{"opus-4-7 bedrock geo omits sampling", "us.anthropic.claude-opus-4-7", false},
		{"opus-4-8 omits sampling", "claude-opus-4-8", false},
		{"future opus-4-9 omits sampling", "global.anthropic.claude-opus-4-9", false},
		{"future opus-4-10 omits sampling", "claude-opus-4-10", false},
		// Opus generation jump: a future "claude-opus-5" ID scheme omits.
		{"future opus-5 omits sampling", "claude-opus-5", false},
		{"future opus-5 dated omits sampling", "claude-opus-5@20270101", false},
		// Opus <= 4.6 legacy shapes keep sampling.
		{"opus-4-1 supports sampling", "claude-opus-4-1-20250805", true},
		{"opus-4-0 supports sampling", "claude-opus-4-0", true},
		{"opus-4-0 dated (date is not a minor) supports sampling", "claude-opus-4-20250514", true},
		{"legacy claude-3-opus supports sampling", "claude-3-opus-20240229", true},
		// Sonnet generation boundary: >= 5 rejects, 4.x keeps.
		{"sonnet-4-5 supports sampling", "claude-sonnet-4-5-20250929", true},
		{"sonnet-4-5 bedrock supports sampling", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", true},
		{"sonnet-4-6 supports sampling", "claude-sonnet-4-6", true},
		{"sonnet-5 omits sampling", "claude-sonnet-5", false},
		{"sonnet-5 vertex dated omits sampling", "claude-sonnet-5@20260201", false},
		{"sonnet-5 dated snapshot omits sampling", "claude-sonnet-5-20260201", false},
		{"sonnet-5 bedrock geo omits sampling", "us.anthropic.claude-sonnet-5", false},
		{"future sonnet-6 omits sampling", "claude-sonnet-6", false},
		{"legacy 3.5 sonnet supports sampling", "claude-3-5-sonnet-20241022", true},
		// Haiku: same newer-generation default; 4.5 and older keep sampling.
		{"haiku-4-5 supports sampling", "claude-haiku-4-5-20251001", true},
		{"haiku-4-5 bedrock supports sampling", "global.anthropic.claude-haiku-4-5-20251001-v1:0", true},
		{"future haiku-5 omits sampling", "claude-haiku-5", false},
		{"legacy haiku-3 supports sampling", "claude-3-haiku-20240307", true},
		// Fable/Mythos generation rejects unconditionally.
		{"fable-5 omits sampling", "claude-fable-5", false},
		{"fable-5 dated snapshot omits sampling", "claude-fable-5-20260315", false},
		{"fable-5 bedrock suffixed omits sampling", "global.anthropic.claude-fable-5-v1:0", false},
		{"mythos-5 omits sampling", "us.anthropic.claude-mythos-5", false},
		{"mythos-preview supports sampling", "claude-mythos-preview", true},
		// Unknown families and unparseable IDs keep sampling.
		{"non-claude model supports sampling", "amazon.nova-pro-v1:0", true},
		{"empty model id supports sampling", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, TemperatureSupported(tc.modelID))
		})
	}
}

// AdaptiveThinkingRequired must match every Bedrock inference profile scope
// (in-region, geo cross-region, global cross-region) for the Opus versions
// that require adaptive thinking. Misclassifying these models causes an
// adapter to send the legacy type:"enabled" + budget_tokens config, which
// produces unreliable signatures on Opus 4.6 and a 400 error on Opus 4.7+.
func TestAdaptiveThinkingRequired(t *testing.T) {
	cases := []struct {
		name    string
		modelID string
		want    bool
	}{
		{"opus-4-6 in-region", "anthropic.claude-opus-4-6-v1", true},
		{"opus-4-6 us geo", "us.anthropic.claude-opus-4-6-v1", true},
		{"opus-4-6 eu geo", "eu.anthropic.claude-opus-4-6-v1", true},
		{"opus-4-6 global", "global.anthropic.claude-opus-4-6-v1", true},
		{"opus-4-7 in-region", "anthropic.claude-opus-4-7", true},
		{"opus-4-7 us geo", "us.anthropic.claude-opus-4-7", true},
		{"opus-4-8", "us.anthropic.claude-opus-4-8", true},
		{"future opus-5", "claude-opus-5", true},
		{"fable-5 in-region", "anthropic.claude-fable-5", true},
		{"fable-5 us geo", "us.anthropic.claude-fable-5", true},
		{"fable-5 global", "global.anthropic.claude-fable-5", true},
		{"fable-5 suffixed", "us.anthropic.claude-fable-5-v1:0", true},
		{"mythos-5", "us.anthropic.claude-mythos-5", true},
		{"opus-4-5 legacy config", "anthropic.claude-opus-4-5-20251101-v1", false},
		{"opus-4-0 dated (date is not a minor)", "claude-opus-4-20250514", false},
		{"sonnet-4-5", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", false},
		{"sonnet-4-6", "global.anthropic.claude-sonnet-4-6", false},
		{"sonnet-5 bare", "claude-sonnet-5", true},
		{"sonnet-5 global", "global.anthropic.claude-sonnet-5", true},
		{"sonnet-5 suffixed", "us.anthropic.claude-sonnet-5-v1:0", true},
		{"haiku-4-5", "global.anthropic.claude-haiku-4-5-20251001-v1:0", false},
		{"future haiku-5", "global.anthropic.claude-haiku-5", true},
		{"mythos-preview", "claude-mythos-preview", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AdaptiveThinkingRequired(tc.modelID))
		})
	}
}

func TestIsFableGeneration(t *testing.T) {
	cases := []struct {
		name    string
		modelID string
		want    bool
	}{
		{"fable-5 bare", "claude-fable-5", true},
		{"fable-5 bedrock suffixed", "global.anthropic.claude-fable-5-v1:0", true},
		{"mythos-5", "claude-mythos-5", true},
		{"future fable-6", "claude-fable-6", true},
		{"mythos-preview is not claude 5", "claude-mythos-preview", false},
		{"opus", "claude-opus-4-8", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsFableGeneration(tc.modelID))
		})
	}
}

func TestFamilyVersion(t *testing.T) {
	cases := []struct {
		name       string
		modelID    string
		marker     string
		wantGen    int
		wantMinor  int
		wantHasMin bool
		wantOK     bool
	}{
		{"bare gen+minor", "claude-opus-4-7", "claude-opus-", 4, 7, true, true},
		{"dated snapshot after minor", "claude-opus-4-7-20260315", "claude-opus-", 4, 7, true, true},
		{"vertex dated", "claude-opus-4-7@20260315", "claude-opus-", 4, 7, true, true},
		{"date segment is not a minor", "claude-opus-4-20250514", "claude-opus-", 4, 0, false, true},
		{"double digit minor", "claude-opus-4-10", "claude-opus-", 4, 10, true, true},
		{"generation only", "claude-sonnet-5", "claude-sonnet-", 5, 0, false, true},
		{"bedrock scoped", "us.anthropic.claude-sonnet-5", "claude-sonnet-", 5, 0, false, true},
		{"marker absent", "claude-3-5-sonnet-20241022", "claude-sonnet-", 0, 0, false, false},
		{"no version after marker", "claude-mythos-preview", "claude-mythos-", 0, 0, false, false},
		{"empty", "", "claude-opus-", 0, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gen, minor, hasMinor, ok := familyVersion(tc.modelID, tc.marker)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			assert.Equal(t, tc.wantGen, gen)
			assert.Equal(t, tc.wantHasMin, hasMinor)
			if tc.wantHasMin {
				assert.Equal(t, tc.wantMinor, minor)
			}
		})
	}
}
