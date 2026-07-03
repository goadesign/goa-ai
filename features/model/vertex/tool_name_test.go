package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestSanitizeToolName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"extraction.emit.emit_event", "extraction.emit.emit_event"}, // dots allowed
		{"toolset/tool", "toolset_tool"},                             // slash rewritten
		{"9starts_with_digit", "_9starts_with_digit"},                // must start with letter/_
		{"has spaces here", "has_spaces_here"},
		{"", "_"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeToolName(tc.in))
		})
	}
}

func TestSanitizeToolNameTruncatesTo64(t *testing.T) {
	long := make([]byte, 100)
	for i := range long {
		long[i] = 'a'
	}
	got := sanitizeToolName(string(long))
	assert.Len(t, got, 64)
}

func TestBuildToolNameMapsRoundTrip(t *testing.T) {
	defs := []*model.ToolDefinition{
		{Name: "feed/find_duplicates"},
		{Name: "extraction.emit.emit_event"},
	}
	canonToProv, provToCanon := buildToolNameMaps(defs)
	assert.Equal(t, "feed_find_duplicates", canonToProv["feed/find_duplicates"])
	assert.Equal(t, tools.Ident("feed/find_duplicates"), tools.Ident(provToCanon["feed_find_duplicates"]))
	assert.Equal(t, "extraction.emit.emit_event", provToCanon["extraction.emit.emit_event"])
}
