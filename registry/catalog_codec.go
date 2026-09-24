// Package registry validates saved definitions together with their current state.
// Definition-dependent reads decode a fresh snapshot and reuse compiled schemas.
// Lease and health operations read only compact state.
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

type (
	// catalogToolset owns one validated definition and its compiled execution
	// schemas for registration or the caller that selected this snapshot.
	catalogToolset struct {
		raw              json.RawMessage
		info             *genregistry.ToolsetInfo
		fingerprint      string
		executionSchemas map[string]*jsonschema.Schema
	}
)

// newCatalogToolset serializes registration inputs and copies their compact
// information so later input changes cannot affect the saved definition.
func newCatalogToolset(toolset *genregistry.Toolset, fingerprint string, validator *schemaValidator) (*catalogToolset, error) {
	definition := *toolset
	definition.RegisteredAt = ""
	raw, err := json.Marshal(&definition)
	if err != nil {
		return nil, fmt.Errorf("marshal toolset %q: %w", toolset.Name, err)
	}
	schemas := make(map[string]*jsonschema.Schema, len(toolset.Tools))
	for _, tool := range toolset.Tools {
		schema, err := validator.compiledSchema(tool.ExecutionPayloadSchema)
		if err != nil {
			return nil, err
		}
		schemas[tool.Name] = schema
	}
	return &catalogToolset{raw: raw, info: toolsetToInfo(&definition), fingerprint: fingerprint, executionSchemas: schemas}, nil
}

// toolsetToInfo retains only the fields needed for discovery and health.
func toolsetToInfo(toolset *genregistry.Toolset) *genregistry.ToolsetInfo {
	return copyToolsetInfo(&genregistry.ToolsetInfo{
		Name:         toolset.Name,
		Description:  toolset.Description,
		Version:      toolset.Version,
		Tags:         toolset.Tags,
		ToolCount:    len(toolset.Tools),
		RegisteredAt: toolset.RegisteredAt,
	})
}

// copyToolsetInfo gives the receiver ownership of every mutable summary field.
func copyToolsetInfo(info *genregistry.ToolsetInfo) *genregistry.ToolsetInfo {
	owned := *info
	if info.Description != nil {
		value := *info.Description
		owned.Description = &value
	}
	if info.Version != nil {
		value := *info.Version
		owned.Version = &value
	}
	owned.Tags = slices.Clone(info.Tags)
	return &owned
}

// decodeCatalogToolset applies the same strict persisted-data checks on a new
// definition and on independent results returned to callers.
func decodeCatalogToolset(raw []byte, toolset **genregistry.Toolset) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(toolset); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

// decode gives a caller its own complete definition for schema-dependent work.
func (t *catalogToolset) decode(registeredAt string) (*genregistry.Toolset, error) {
	var result *genregistry.Toolset
	if err := decodeCatalogToolset(t.raw, &result); err != nil {
		return nil, fmt.Errorf("decode toolset %q: %w", t.info.Name, err)
	}
	result.RegisteredAt = registeredAt
	return result, nil
}

// marshalCatalogState encodes only provider state and compact discovery fields.
func marshalCatalogState(entry catalogState) (string, error) {
	body, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal toolset %q state: %w", entry.Info.Name, err)
	}
	return string(body), nil
}

// parseCatalogState validates the current persistence contract. Definition bytes
// and retirement history are not read or copied by health/lifecycle operations.
func parseCatalogState(name, body string) (catalogState, error) {
	var entry catalogState
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entry); err != nil {
		return catalogState{}, fmt.Errorf("unmarshal toolset %q: %w", name, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return catalogState{}, fmt.Errorf("unmarshal toolset %q: trailing JSON value", name)
	}
	if entry.Info == nil || entry.Info.Name != name || entry.Info.ToolCount < 0 {
		return catalogState{}, fmt.Errorf("toolset %q has invalid discovery metadata", name)
	}
	if entry.RegisteredAt == "" || entry.Info.RegisteredAt != entry.RegisteredAt {
		return catalogState{}, fmt.Errorf("toolset %q has invalid registered_at", name)
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.RegisteredAt); err != nil {
		return catalogState{}, fmt.Errorf("toolset %q invalid registered_at: %w", name, err)
	}
	switch entry.State {
	case catalogEntryActive, catalogEntryRetired:
	default:
		return catalogState{}, fmt.Errorf("toolset %q has invalid catalog state %q", name, entry.State)
	}
	if entry.NativeAgent {
		if err := toolregistry.ValidateRegistrationToken(entry.SchemaFingerprint); err != nil {
			return catalogState{}, err
		}
		if entry.RegistrationToken != nativeAgentToken(entry.SchemaFingerprint) ||
			entry.AdmissionRevision != "" || entry.WireProtocolVersion != 0 ||
			len(entry.ProviderLeases) != 0 || entry.HealthEpoch != 0 || entry.LastPongUnixNano != 0 {
			return catalogState{}, fmt.Errorf("toolset %q has invalid native Agent state", name)
		}
		return entry, nil
	}
	if err := toolregistry.ValidateAdmissionRevision(entry.AdmissionRevision); err != nil {
		return catalogState{}, fmt.Errorf("toolset %q invalid admission revision: %w", name, err)
	}
	if err := toolregistry.ValidateWireProtocolVersion(entry.WireProtocolVersion); err != nil {
		return catalogState{}, fmt.Errorf("toolset %q invalid wire protocol version: %w", name, err)
	}
	if err := toolregistry.ValidateRegistrationToken(entry.SchemaFingerprint); err != nil {
		return catalogState{}, fmt.Errorf("toolset %q invalid schema fingerprint: %w", name, err)
	}
	token, err := admissionRegistrationToken(
		entry.SchemaFingerprint,
		entry.AdmissionRevision,
		entry.WireProtocolVersion,
	)
	if err != nil {
		return catalogState{}, fmt.Errorf("derive toolset %q admission token: %w", name, err)
	}
	if entry.RegistrationToken != token {
		return catalogState{}, fmt.Errorf("toolset %q registration token does not match admission identity", name)
	}
	if entry.ProviderLeases == nil {
		return catalogState{}, fmt.Errorf("toolset %q missing provider lease map", name)
	}
	if entry.HealthEpoch == 0 {
		return catalogState{}, fmt.Errorf("toolset %q has invalid health epoch", name)
	}
	if entry.LastPongUnixNano < 0 {
		return catalogState{}, fmt.Errorf("toolset %q has invalid last pong timestamp", name)
	}
	for leaseKey, lease := range entry.ProviderLeases {
		if _, _, err := parseProviderLeaseKey(leaseKey); err != nil {
			return catalogState{}, fmt.Errorf("toolset %q has invalid provider lease key: %w", name, err)
		}
		if lease.ExpiresAtUnixMilli <= 0 {
			return catalogState{}, fmt.Errorf("toolset %q has invalid provider lease deadline", name)
		}
	}
	return entry, nil
}

// snapshot reads state and its definition at one Redis instant. A replacement
// cannot combine one registration's token with another registration's schemas.
func (c *toolsetCatalog) snapshot(ctx context.Context, name string) (entry catalogEntry, err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.definition.load",
		trace.WithAttributes(attribute.String("toolregistry.toolset", name)))
	defer finishCatalogSpan(ctx, span, &err)
	raw, definitionRaw, tokenRetired, exists, err := c.store.Snapshot(ctx, toolsetCatalogKey(name))
	if err != nil {
		return catalogEntry{}, err
	}
	if !exists {
		return catalogEntry{}, errToolsetNotFound
	}
	span.SetAttributes(
		attribute.Int("toolregistry.catalog.state_read_bytes", len(raw)),
		attribute.Int("toolregistry.catalog.definition_read_bytes", len(definitionRaw)),
	)
	state, err := parseCatalogState(name, raw)
	if err != nil {
		return catalogEntry{}, err
	}
	if tokenRetired != (!state.NativeAgent && state.State == catalogEntryRetired) {
		return catalogEntry{}, fmt.Errorf("toolset %q disagrees with permanent retirement history", name)
	}
	var toolset *genregistry.Toolset
	if err := decodeCatalogToolset([]byte(definitionRaw), &toolset); err != nil {
		return catalogEntry{}, fmt.Errorf("decode definition %q: %w", name, err)
	}
	if toolset == nil || toolset.Name != name || toolset.RegisteredAt != "" {
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid static definition", name)
	}
	if err := c.validator.ValidateToolSchemas(toolset.Tools); err != nil {
		return catalogEntry{}, fmt.Errorf("toolset %q invalid persisted schemas: %w", name, err)
	}
	if state.NativeAgent {
		if err := validateAgentTools(toolset.Tools); err != nil {
			return catalogEntry{}, err
		}
	}
	fingerprint, err := toolcontract.Fingerprint(toolset)
	if err != nil {
		return catalogEntry{}, err
	}
	if fingerprint != state.SchemaFingerprint {
		return catalogEntry{}, fmt.Errorf("toolset %q schema fingerprint does not match canonical schema", name)
	}
	definition, err := newCatalogToolset(toolset, fingerprint, c.validator)
	if err != nil {
		return catalogEntry{}, err
	}
	info := copyToolsetInfo(definition.info)
	info.RegisteredAt = state.RegisteredAt
	if !reflect.DeepEqual(info, state.Info) {
		return catalogEntry{}, fmt.Errorf("toolset %q discovery metadata does not match definition", name)
	}
	return catalogEntry{catalogState: state, Toolset: definition}, nil
}
