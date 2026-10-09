// These checks give external discovery JSON to the generated entry codec and
// shared verifier. They cover complete manifests, open frontmatter, exact byte
// counts and resource confinement without loading or executing instructions.
package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSkillEntryValidation(t *testing.T) {
	valid := skillEntryJSON("skill://review/SKILL.md", `{"name":"review","description":"Review changes","future":{"exact":9007199254740993,"nullable":null}}`, "")
	manifest := "[" + skillFileJSON("skill://review/SKILL.md") + "]"
	for _, test := range []struct {
		name, entry, want string
	}{
		{"complete entry", valid, ""},
		{"dynamic", strings.Replace(valid, manifest, `"dynamic"`, 1), ""},
		{"unlisted native scheme", skillEntryJSON("github://owner/skills/review/SKILL.md", `{"name":"review","description":"Review changes"}`, ""), ""},
		{"nested skill", skillEntryJSON("skill://team/parent/review/SKILL.md", `{"name":"review","description":"Review changes"}`, ""), ""},
		{"unicode name", skillEntryJSON("skill://评审/SKILL.md", `{"name":"评审","description":"Review changes"}`, ""), ""},
		{"missing markdown", strings.Replace(valid, manifest, `[]`, 1), "must include"},
		{"different name", strings.Replace(valid, `"name":"review"`, `"name":"other"`, 1), "match its directory"},
		{"uppercase name", strings.Replace(valid, `"name":"review"`, `"name":"Review"`, 1), "lowercase"},
		{"invalid frontmatter", strings.Replace(valid, `"frontmatter":{`, `"frontmatter":{"name":"duplicated",`, 1), "duplicate"},
		{"unknown entry member", strings.Replace(valid, `"frontmatter":`, `"unexpected":true,"frontmatter":`, 1), "unknown JSON field"},
		{"unknown file member", strings.Replace(valid, `"digest":`, `"unexpected":true,"digest":`, 1), "unknown JSON field"},
		{"duplicate file member", strings.Replace(valid, `"size":`, `"size":1,"size":`, 1), "duplicate"},
		{"null file", strings.Replace(valid, manifest, `[null]`, 1), "null"},
		{"invalid digest", strings.Replace(valid, "sha256:", "sha512:", 1), "digest"},
		{"negative byte count", strings.Replace(valid, `"size":9007199254740993`, `"size":-1`, 1), "size"},
		{"null byte count", strings.Replace(valid, `"size":9007199254740993`, `"size":null`, 1), "null"},
		{"wrong dynamic", strings.Replace(valid, manifest, `"unknown"`, 1), "dynamic"},
		{"outside directory", skillEntryJSON("skill://review/SKILL.md", `{"name":"review","description":"Review changes"}`, ","+skillFileJSON("skill://other/script.sh")), "outside"},
		{"encoded traversal", skillEntryJSON("skill://review/SKILL.md", `{"name":"review","description":"Review changes"}`, ","+skillFileJSON("skill://review/%2e%2e/script.sh")), "unsafe"},
		{"duplicate file URI", skillEntryJSON("skill://review/SKILL.md", `{"name":"review","description":"Review changes"}`, ","+skillFileJSON("skill://review/SKILL.md")), "duplicate URI"},
		{"future fields retain null", skillEntryJSON("skill://review/SKILL.md", `{"name":"review","description":"Review","future":null}`, ""), ""},
		{"known optional null", skillEntryJSON("skill://review/SKILL.md", `{"name":"review","description":"Review","license":null}`, ""), "cannot be null"},
		{"metadata string values", skillEntryJSON("skill://review/SKILL.md", `{"name":"review","description":"Review","metadata":{"version":1}}`, ""), "frontmatter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSkillEntry(t.Context(), json.RawMessage(test.entry))
			if test.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestSkillFrontmatterCharacterLimits(t *testing.T) {
	for _, test := range []struct {
		field string
		limit int
	}{
		{"name", 64}, {"description", 1024}, {"compatibility", 500},
	} {
		for _, length := range []int{test.limit - 1, test.limit, test.limit + 1} {
			t.Run(fmt.Sprintf("%s/%d", test.field, length), func(t *testing.T) {
				value := strings.Repeat("评", length)
				name := "review"
				frontmatter := `{"name":"review","description":"Review"`
				switch test.field {
				case "name":
					name = value
					frontmatter = fmt.Sprintf(`{"name":%q,"description":"Review"`, value)
				case "description":
					frontmatter = fmt.Sprintf(`{"name":"review","description":%q`, value)
				default:
					frontmatter += fmt.Sprintf(`,"compatibility":%q`, value)
				}
				err := ValidateSkillEntry(t.Context(), json.RawMessage(skillEntryJSON("skill://"+name+"/SKILL.md", frontmatter+"}", "")))
				if length > test.limit {
					assert.ErrorContains(t, err, test.field)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	}
}

func skillEntryJSON(uri, frontmatter, extraFiles string) string {
	return fmt.Sprintf(`{"uri":%q,"frontmatter":%s,"resources":[%s%s]}`, uri, frontmatter, skillFileJSON(uri), extraFiles)
}

func skillFileJSON(uri string) string {
	return fmt.Sprintf(`{"uri":%q,"digest":"sha256:%s","size":9007199254740993}`, uri, strings.Repeat("a", 64))
}
