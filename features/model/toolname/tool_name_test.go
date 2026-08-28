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
			in:   "records.get_status",
			want: "records_get_status",
		},
		{
			name: "multi segment canonical id preserved",
			in:   "catalog.read.assistant.assistant_get_request_details",
			want: "catalog_read_assistant_assistant_get_request_details",
		},
		{
			name: "repeated segment preserved",
			in:   "queue.queue.update_items",
			want: "queue_queue_update_items",
		},
		{
			name: "disallowed runes replaced",
			in:   "reports.analyze/v2",
			want: "reports_analyze_v2",
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
		Sanitize("catalog.read.explain_record"),
		Sanitize("records.explain_record"),
	)
}

func TestSanitizeTruncatesWithStableHashSuffix(t *testing.T) {
	t.Parallel()

	in := "catalog.read.assistant." + strings.Repeat("very_long_segment_", 10) + "tool"
	got := Sanitize(in)

	assert.LessOrEqual(t, len(got), 64)
	assert.Regexp(t, `_[0-9a-f]{8}$`, got)
	assert.Equal(t, got, Sanitize(in), "mapping must be deterministic")
}

func TestSanitizeTruncationDistinguishesLongNames(t *testing.T) {
	t.Parallel()

	prefix := "catalog.read.assistant." + strings.Repeat("very_long_segment_", 10)
	assert.NotEqual(t, Sanitize(prefix+"alpha"), Sanitize(prefix+"beta"))
}

func TestBuildMapsIsBijective(t *testing.T) {
	t.Parallel()

	canonToProv, provToCanon, err := BuildMaps([]*model.ToolDefinition{
		{Name: "records.lookup"},
		{Name: "catalog.read.lookup"},
	})

	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"records.lookup":      "records_lookup",
		"catalog.read.lookup": "catalog_read_lookup",
	}, canonToProv)
	assert.Equal(t, map[string]string{
		"records_lookup":      "records.lookup",
		"catalog_read_lookup": "catalog.read.lookup",
	}, provToCanon)
}

func TestProviderNameProjectsHistoryWithoutAdvertisingIt(t *testing.T) {
	t.Parallel()

	active := map[string]string{"catalog.read.lookup": "catalog_read_lookup"}
	name, err := ProviderName("records.lookup", active)
	require.NoError(t, err)
	assert.Equal(t, "records_lookup", name)

	_, err = ProviderName("catalog_read.lookup", active)
	require.ErrorContains(t, err, `collides with active tool "catalog.read.lookup"`)
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
			defs:    []*model.ToolDefinition{{Name: "records.lookup"}, {Name: "records_lookup"}},
			wantErr: `tool name "records_lookup" sanitizes to "records_lookup" which collides with "records.lookup"`,
		},
		{
			name:    "nil definition",
			defs:    []*model.ToolDefinition{{Name: "records.lookup"}, nil},
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
