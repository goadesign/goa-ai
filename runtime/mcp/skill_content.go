// Package mcp verifies fetched Skill files against the entry retained by the
// consuming host. Byte verification precedes parsing or use. These checks never
// fetch content, approve instructions, or add files from a newer directory page.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	genskills "goa.design/goa-ai/internal/mcpskills/gen/skill_entries"
)

// VerifySkillFile checks fetched bytes against the complete discovery entry
// retained by the host. It validates the entry, requires exact manifest membership,
// and checks byte length and SHA-256. For the entry's own SKILL.md it also compares
// every YAML frontmatter field with discovery. A nested SKILL.md remains supporting
// content. Dynamic entries are declined because their bytes cannot be verified.
// This check grants no permissions; the host retains the entry while its Skill
// instructions remain in model context and obtains any required user consent.
func VerifySkillFile(ctx context.Context, entryJSON json.RawMessage, uri string, content []byte) error {
	entry, err := decodeSkillEntry(ctx, entryJSON)
	if err != nil {
		return err
	}
	if uri == entry.URI {
		return verifySkillMarkdown(entry, content)
	}
	return verifySkillResource(entry, uri, content)
}

// verifySkillResource accepts bytes only for an exact file in the held manifest.
// Dynamic entries have no stable digest, so this host declines their contents.
func verifySkillResource(entry *genskills.SkillEntry, uri string, content []byte) error {
	manifest, stable := entry.Resources.AsManifest()
	if !stable {
		return errors.New("mcp: dynamic Skill content cannot be verified against a manifest")
	}
	for _, file := range manifest {
		if file.URI != uri {
			continue
		}
		if int64(len(content)) != file.Size {
			return errors.New("mcp: Skill resource byte length differs from the retained manifest")
		}
		if fmt.Sprintf("sha256:%x", sha256.Sum256(content)) != file.Digest {
			return errors.New("mcp: Skill resource digest differs from the retained manifest")
		}
		return nil
	}
	return errors.New("mcp: Skill resource is absent from the retained manifest")
}

// verifySkillMarkdown checks the original bytes before parsing the entry file.
// Every YAML field must agree with discovery, including fields unknown to Goa.
// A supporting file named SKILL.md does not enter this activation check.
func verifySkillMarkdown(entry *genskills.SkillEntry, content []byte) error {
	if err := verifySkillResource(entry, entry.URI, content); err != nil {
		return err
	}
	if !utf8.Valid(content) {
		return errors.New("mcp: Skill instructions must contain valid UTF-8")
	}
	frontmatter, err := skillYAMLFrontmatter(content)
	if err != nil {
		return err
	}
	return compareSkillFrontmatter(frontmatter, entry.Frontmatter)
}

// skillYAMLFrontmatter finds the leading YAML block without rewriting file bytes.
// LF and CRLF delimiters are accepted; the digest always covers the original file.
func skillYAMLFrontmatter(content []byte) ([]byte, error) {
	first, rest, found := bytes.Cut(content, []byte("\n"))
	if !found || !bytes.Equal(bytes.TrimSuffix(first, []byte("\r")), []byte("---")) {
		return nil, errors.New("mcp: SKILL.md must begin with YAML frontmatter")
	}
	start := len(first) + 1
	position := start
	for {
		line, remaining, newline := bytes.Cut(rest, []byte("\n"))
		if bytes.Equal(bytes.TrimSuffix(line, []byte("\r")), []byte("---")) {
			return content[start:position], nil
		}
		if !newline {
			return nil, errors.New("mcp: SKILL.md has no closing frontmatter delimiter")
		}
		position += len(line) + 1
		rest = remaining
	}
}
