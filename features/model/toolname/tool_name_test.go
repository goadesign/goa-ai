package toolname

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestSanitizePreservesNamespaces(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "plain name passthrough",
			in:   "lookup",
			want: "lookup",
		},
		{
			name: "toolset namespace preserved",
			in:   "ada.get_application_status",
			want: "ada_get_application_status",
		},
		{
			name: "multi segment canonical id preserved",
			in:   "atlas.read.chat.chat_get_user_details",
			want: "atlas_read_chat_chat_get_user_details",
		},
		{
			name: "repeated segment preserved",
			in:   "todos.todos.update_todos",
			want: "todos_todos_update_todos",
		},
		{
			name: "disallowed runes replaced",
			in:   "analytics.analyze/v2",
			want: "analytics_analyze_v2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, Sanitize(tc.in))
		})
	}
}

// Namespace preservation is what makes the projection injective: two tools
// sharing a leaf name in different toolsets must not collapse onto one
// provider-visible name, because the adapter maps the echoed name back to a
// canonical identifier.
func TestSanitizeDistinguishesSharedLeafNames(t *testing.T) {
	t.Parallel()

	assert.NotEqual(
		t,
		Sanitize("atlas.read.explain_control_logic"),
		Sanitize("ada.explain_control_logic"),
	)
}

func TestSanitizeTruncatesWithStableHashSuffix(t *testing.T) {
	t.Parallel()

	in := "atlas.read.chat." + strings.Repeat("very_long_segment_", 10) + "tool"
	got := Sanitize(in)

	assert.LessOrEqual(t, len(got), 64)
	assert.Regexp(t, `_[0-9a-f]{8}$`, got)
	assert.Equal(t, got, Sanitize(in), "mapping must be deterministic")
}

func TestSanitizeTruncationDistinguishesLongNames(t *testing.T) {
	t.Parallel()

	prefix := "atlas.read.chat." + strings.Repeat("very_long_segment_", 10)
	assert.NotEqual(t, Sanitize(prefix+"alpha"), Sanitize(prefix+"beta"))
}

func TestBuildMapsIsBijective(t *testing.T) {
	t.Parallel()

	canonToProv, provToCanon, err := BuildMaps([]*model.ToolDefinition{
		{Name: "ada.lookup"},
		{Name: "atlas.read.lookup"},
	})

	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"ada.lookup":        "ada_lookup",
		"atlas.read.lookup": "atlas_read_lookup",
	}, canonToProv)
	assert.Equal(t, map[string]string{
		"ada_lookup":        "ada.lookup",
		"atlas_read_lookup": "atlas.read.lookup",
	}, provToCanon)
}

func TestBuildMapsRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		defs    []*model.ToolDefinition
		wantErr string
	}{
		{
			name:    "sanitization collision",
			defs:    []*model.ToolDefinition{{Name: "ada.lookup"}, {Name: "ada_lookup"}},
			wantErr: `tool name "ada_lookup" sanitizes to "ada_lookup" which collides with "ada.lookup"`,
		},
		{
			name:    "nil definition",
			defs:    []*model.ToolDefinition{{Name: "ada.lookup"}, nil},
			wantErr: "tool[1] is nil",
		},
		{
			name:    "empty name",
			defs:    []*model.ToolDefinition{{Name: ""}},
			wantErr: "tool[0] is missing name",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := BuildMaps(tc.defs)
			assert.EqualError(t, err, tc.wantErr)
		})
	}
}
