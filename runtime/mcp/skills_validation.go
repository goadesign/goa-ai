// Package mcp verifies Skill discovery records before servers publish them or
// hosts retain them. Generated decoders own field shapes and constraints; this
// file checks the relationships between frontmatter, directory and manifest.
// Acceptance here does not approve instructions or verify unread file content.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	genskillssrv "goa.design/goa-ai/internal/mcpskills/gen/http/skill_entries/server"
	genskills "goa.design/goa-ai/internal/mcpskills/gen/skill_entries"
	goahttp "goa.design/goa/v3/http"
)

// ValidateSkillEntry checks one complete Skills extension discovery record.
// It rejects malformed fields, invalid frontmatter, duplicate files, missing
// SKILL.md and files outside the skill directory. It performs no file reads,
// does not grant permissions and does not verify digests against unread bytes.
func ValidateSkillEntry(ctx context.Context, data json.RawMessage) error {
	_, err := decodeSkillEntry(ctx, data)
	return err
}

// decodeSkillEntry validates external JSON through the generated Goa decoder,
// then checks shared directory and frontmatter rules. Hosts retain this exact
// typed entry when verifying later reads instead of consulting a newer listing.
func decodeSkillEntry(ctx context.Context, data json.RawMessage) (*genskills.SkillEntry, error) {
	entry, err := genskills.DecodeSkillEntry(data)
	if err != nil {
		return nil, fmt.Errorf("mcp: invalid skill entry: %w", err)
	}
	decodeFrontmatter := genskillssrv.DecodeDecodeFrontmatterRequest(goahttp.NewMuxer(), func(request *http.Request) goahttp.Decoder {
		return generatedJSONDecoder(&http.Response{Body: request.Body})
	})
	frontmatter, err := decodeFrontmatter(skillDecodeRequest(ctx, entry.Frontmatter))
	if err != nil {
		return nil, fmt.Errorf("mcp: invalid skill frontmatter: %w", err)
	}
	if strings.ToLower(frontmatter.Name) != frontmatter.Name {
		return nil, errors.New("mcp: skill name must use lowercase characters")
	}
	address, err := skillFileAddress(entry.URI)
	if err != nil {
		return nil, err
	}
	if path.Base(address.Path) != "SKILL.md" {
		return nil, errors.New("mcp: skill URI must identify SKILL.md")
	}
	root := strings.TrimSuffix(address.Path, "/SKILL.md")
	name := path.Base(root)
	if root == "" {
		name = address.Host
	}
	if name != frontmatter.Name {
		return nil, errors.New("mcp: skill name must match its directory")
	}
	manifest, stable := entry.Resources.AsManifest()
	if !stable {
		return entry, nil
	}
	seen := make(map[string]struct{}, len(manifest))
	for _, file := range manifest {
		if _, duplicate := seen[file.URI]; duplicate {
			return nil, errors.New("mcp: skill manifest contains a duplicate URI")
		}
		seen[file.URI] = struct{}{}
		resource, err := skillFileAddress(file.URI)
		if err != nil {
			return nil, err
		}
		if resource.Scheme != address.Scheme || resource.Host != address.Host || resource.User.String() != address.User.String() || !strings.HasPrefix(resource.Path, root+"/") {
			return nil, errors.New("mcp: skill manifest file is outside its directory")
		}
	}
	if _, present := seen[entry.URI]; !present {
		return nil, errors.New("mcp: skill manifest must include its exact SKILL.md URI")
	}
	return entry, nil
}

// skillFileAddress parses a resource address without resolving its authority.
// Decoded path segments cannot escape the directory or become local separators
// on another operating system. The original URI remains the file's identity.
func skillFileAddress(uri string) (*url.URL, error) {
	address, err := resourceAddress(uri)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(address.Path, "/") {
		return nil, errors.New("mcp: skill file requires an absolute hierarchical resource URI")
	}
	return address, nil
}

// skillDecodeRequest supplies JSON to a generated decoder without issuing an
// HTTP request. The same generated validation runs for servers and consumers.
func skillDecodeRequest(ctx context.Context, data []byte) *http.Request {
	return (&http.Request{
		Method: http.MethodPost,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(bytes.NewReader(data)),
	}).WithContext(ctx)
}
