package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateResourceDirectory(t *testing.T) {
	for _, test := range []struct {
		name     string
		uri      string
		children []string
		want     string
	}{
		{name: "empty root", uri: "skill://review"},
		{name: "root children", uri: "skill://review", children: []string{"skill://review/SKILL.md", "skill://review/templates"}},
		{name: "nested", uri: "skill://team/review/templates", children: []string{"skill://team/review/templates/invoice.md"}},
		{name: "native scheme", uri: "github://owner/repository/review", children: []string{"github://owner/repository/review/SKILL.md"}},
		{name: "root slash", uri: "skill://review/", want: "trailing slash"},
		{name: "nested slash", uri: "skill://team/review/", want: "trailing slash"},
		{name: "relative", uri: "review/templates", want: "absolute hierarchical"},
		{name: "opaque", uri: "skill:review", want: "absolute hierarchical"},
		{name: "recursive", uri: "skill://review", children: []string{"skill://review/templates/invoice.md"}, want: "only direct children"},
		{name: "other authority", uri: "skill://review", children: []string{"skill://other/SKILL.md"}, want: "only direct children"},
		{name: "other scheme", uri: "skill://review", children: []string{"github://review/SKILL.md"}, want: "only direct children"},
		{name: "same resource", uri: "skill://team/review", children: []string{"skill://team/review"}, want: "only direct children"},
		{name: "prefix sibling", uri: "skill://team/review", children: []string{"skill://team/reviews/SKILL.md"}, want: "only direct children"},
		{name: "duplicate", uri: "skill://review", children: []string{"skill://review/SKILL.md", "skill://review/SKILL.md"}, want: "duplicate"},
		{name: "encoded traversal", uri: "skill://review", children: []string{"skill://review/%2e%2e"}, want: "unsafe segment"},
		{name: "encoded separator", uri: "skill://review", children: []string{"skill://review/templates%2finvoice.md"}, want: "only direct children"},
		{name: "backslash", uri: "skill://review", children: []string{"skill://review/templates%5cinvoice.md"}, want: "unsafe segment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateResourceDirectory(test.uri, test.children)
			if test.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.want)
		})
	}
}
