// Package admission checks historical declarations without interpreting their
// consumer contracts as current executable metadata. The original contract JSON
// remains part of the saved fingerprint, including its spacing and member order.
package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

type (
	// savedToolset contains the stable declaration fields used by discovery and
	// registration identity. Historical consumer metadata is retained as JSON.
	savedToolset struct {
		Name         string
		Description  *string
		Version      *genregistry.SemVer
		Tags         []string
		Tools        []*savedTool
		RegisteredAt string
	}

	// savedTool keeps schema bytes and the original consumer contract separate:
	// checking an accepted identity does not require executing that contract.
	savedTool struct {
		Name                   string
		Description            *string
		Tags                   []string
		PayloadSchema          []byte
		ExecutionPayloadSchema []byte
		ResultSchema           []byte
		SidecarSchema          []byte
		ConsumerContract       json.RawMessage
	}
)

// ValidateSavedToolsetIntegrity checks a historical definition against its saved
// summary and fingerprint without converting its consumer contracts. It grants
// no execution authority; Registry uses the returned stable schema fields for
// schema validation, never as a complete current consumer declaration.
func ValidateSavedToolsetIntegrity(raw []byte, info *genregistry.ToolsetInfo, fingerprint string) (Schema, error) {
	reader := savedDeclarationReader{raw: raw, decoder: json.NewDecoder(bytes.NewReader(raw))}
	if err := reader.readValue(reflect.TypeFor[savedToolset](), nil); err != nil {
		return Schema{}, err
	}
	if _, err := reader.decoder.Token(); !errors.Is(err, io.EOF) {
		return Schema{}, fmt.Errorf("trailing JSON value")
	}
	var saved *savedToolset
	if err := json.Unmarshal(raw, &saved); err != nil {
		return Schema{}, err
	}
	if saved == nil || saved.Name != info.Name || saved.RegisteredAt != "" {
		return Schema{}, fmt.Errorf("toolset %q has invalid static definition", info.Name)
	}
	summary := &genregistry.ToolsetInfo{
		Name: saved.Name, Description: saved.Description, Version: saved.Version,
		Tags: saved.Tags, ToolCount: len(saved.Tools), RegisteredAt: info.RegisteredAt,
	}
	if !reflect.DeepEqual(summary, info) {
		return Schema{}, fmt.Errorf("toolset %q discovery metadata does not match definition", info.Name)
	}
	schema := Schema{Name: saved.Name, Description: saved.Description, Tags: saved.Tags}
	if saved.Version != nil {
		schema.Version = new(string(*saved.Version))
	}
	for _, tool := range saved.Tools {
		if tool == nil {
			return Schema{}, fmt.Errorf("toolset %q contains a nil tool declaration", info.Name)
		}
		contract := tool.ConsumerContract
		if bytes.Equal(contract, []byte("null")) {
			contract = nil
		}
		schema.Tools = append(schema.Tools, ToolSchema{
			Name: tool.Name, Description: tool.Description, Tags: tool.Tags,
			PayloadSchema: tool.PayloadSchema, ExecutionPayloadSchema: tool.ExecutionPayloadSchema,
			ResultSchema: tool.ResultSchema, SidecarSchema: tool.SidecarSchema,
			ConsumerContract: contract,
		})
	}
	if SchemaFingerprint(schema) != fingerprint {
		return Schema{}, fmt.Errorf("toolset %q schema fingerprint does not match saved definition", info.Name)
	}
	return schema, nil
}
