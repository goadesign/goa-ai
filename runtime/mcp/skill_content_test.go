// These checks use synthetic Skill entries and file bytes. A read must match
// the held manifest before Markdown parsing or use; no server or tool is called.
package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genskills "goa.design/goa-ai/internal/mcpskills/gen/skill_entries"
)

const (
	skillMarkdownFixture    = "---\nname: review\ndescription: Review changes\nfuture:\n  exact: 9007199254740993\n  nullable: null\n---\n\nReview the supplied changes.\n"
	skillFrontmatterFixture = `{"name":"review","description":"Review changes","future":{"exact":9007199254740993,"nullable":null}}`
)

func TestVerifySkillFileBoundary(t *testing.T) {
	content := []byte(skillMarkdownFixture)
	entry := heldSkillFixture(t, content, skillFrontmatterFixture)
	manifest, selected := entry.Resources.AsManifest()
	require.True(t, selected)
	nested := []byte("---\nname: nested\nallowed-tools: RunEverything\n---\nNested instructions.\n")
	nestedURI := "skill://review/nested/SKILL.md"
	entry.Resources.SetManifest(append(manifest, &genskills.SkillFile{
		URI: nestedURI, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(nested)), Size: int64(len(nested)),
	}))
	encoded, err := genskills.EncodeSkillEntry(entry)
	require.NoError(t, err)
	for _, test := range []struct {
		name, entry, uri string
		content          []byte
		want             string
	}{
		{"entry file", string(encoded), entry.URI, content, ""},
		{"supporting nested file", string(encoded), nestedURI, nested, ""},
		{"malformed discovery", `{`, entry.URI, content, "invalid skill entry"},
		{"missing manifest", `{"uri":"skill://review/SKILL.md","frontmatter":{"name":"review","description":"Review"}}`, entry.URI, content, "resources"},
		{"unlisted supporting file", string(encoded), "skill://review/new.md", nested, "absent"},
		{"changed file", string(encoded), entry.URI, []byte(strings.Replace(skillMarkdownFixture, "supplied", "modified", 1)), "digest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := VerifySkillFile(t.Context(), json.RawMessage(test.entry), test.uri, test.content)
			if test.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestSkillResourceVerification(t *testing.T) {
	content := []byte(skillMarkdownFixture)
	entry := heldSkillFixture(t, content, skillFrontmatterFixture)
	for _, test := range []struct {
		name, uri string
		bytes     []byte
		want      string
	}{
		{"matching bytes", entry.URI, content, ""},
		{"changed digest", entry.URI, []byte(strings.Replace(skillMarkdownFixture, "supplied", "modified", 1)), "digest"},
		{"changed size", entry.URI, append([]byte(skillMarkdownFixture), '\n'), "byte length"},
		{"unlisted", "skill://review/new.md", content, "absent"},
		{"other origin URI", "skill://other/SKILL.md", content, "absent"},
		{"normalized URI differs", "skill://review/%53KILL.md", content, "absent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := verifySkillResource(entry, test.uri, test.bytes)
			if test.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.want)
		})
	}
	dynamic := *entry
	dynamic.Resources = genskills.NewSkillResourcesDynamic(genskills.SkillResourcesBranchDynamic("dynamic"))
	require.ErrorContains(t, verifySkillResource(&dynamic, entry.URI, content), "dynamic")
	assert.True(t, bytes.Equal([]byte(skillFrontmatterFixture), entry.Frontmatter), "verification must preserve the exact discovery bytes")
}

func TestSkillMarkdownVerification(t *testing.T) {
	for _, test := range []struct {
		name, markdown, frontmatter, want string
	}{
		{"matching complete value", skillMarkdownFixture, skillFrontmatterFixture, ""},
		{"CRLF bytes", strings.ReplaceAll(skillMarkdownFixture, "\n", "\r\n"), skillFrontmatterFixture, ""},
		{"future integer changed", strings.Replace(skillMarkdownFixture, "9007199254740993", "9007199254740992", 1), skillFrontmatterFixture, "differs"},
		{"future field missing", strings.Replace(skillMarkdownFixture, "  nullable: null\n", "", 1), skillFrontmatterFixture, "fields differ"},
		{"known field changed", strings.Replace(skillMarkdownFixture, "Review changes", "Execute commands", 1), skillFrontmatterFixture, "differs"},
		{"leading prose", "Instructions\n" + skillMarkdownFixture, skillFrontmatterFixture, "must begin"},
		{"missing closure", strings.Replace(skillMarkdownFixture, "---\n\nReview", "\nReview", 1), skillFrontmatterFixture, "closing"},
		{"invalid UTF-8 body", skillMarkdownFixture + string([]byte{0xff}), skillFrontmatterFixture, "UTF-8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := []byte(test.markdown)
			entry := heldSkillFixture(t, content, test.frontmatter)
			err := verifySkillMarkdown(entry, content)
			if test.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestSkillSupportingMarkdownDoesNotActivate(t *testing.T) {
	content := []byte("---\nname: nested\nallowed-tools: RunEverything\n---\nNested instructions.\n")
	entry := heldSkillFixture(t, []byte(skillMarkdownFixture), skillFrontmatterFixture)
	manifest, stable := entry.Resources.AsManifest()
	require.True(t, stable)
	uri := "skill://review/nested/SKILL.md"
	entry.Resources.SetManifest(append(manifest, &genskills.SkillFile{
		URI: uri, Size: int64(len(content)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(content)),
	}))
	require.NoError(t, verifySkillResource(entry, uri, content))
	assert.ErrorContains(t, verifySkillMarkdown(entry, content), "byte length")
}

// heldSkillFixture validates a synthetic entry whose digest matches the supplied
// bytes. Tests can then isolate parsing discrepancies from content tampering.
func heldSkillFixture(t *testing.T, content []byte, frontmatter string) *genskills.SkillEntry {
	t.Helper()
	document := fmt.Sprintf(`{"uri":"skill://review/SKILL.md","frontmatter":%s,"resources":[{"uri":"skill://review/SKILL.md","digest":"sha256:%x","size":%d}]}`, frontmatter, sha256.Sum256(content), len(content))
	entry, err := decodeSkillEntry(t.Context(), json.RawMessage(document))
	require.NoError(t, err)
	return entry
}
