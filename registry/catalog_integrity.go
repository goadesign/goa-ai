// Package registry verifies every saved declaration and admission at startup.
// Historical services cannot execute with today's protocol, so their original
// consumer metadata is checked for integrity without decoding current types.
package registry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

// validatePersistedEntry reads one coherent pair and checks retirement history.
// Current services and all native Agents additionally require full executable
// validation; historical services retain their original declaration identity.
func (c *toolsetCatalog) validatePersistedEntry(ctx context.Context, name string) (err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.integrity",
		trace.WithAttributes(attribute.String("toolregistry.toolset", name)))
	defer finishCatalogSpan(ctx, span, &err)
	raw, definition, retired, exists, err := c.store.Snapshot(ctx, toolsetCatalogKey(name))
	if err != nil {
		return err
	}
	if !exists {
		return errToolsetNotFound
	}
	span.SetAttributes(
		attribute.Int("toolregistry.catalog.state_read_bytes", len(raw)),
		attribute.Int("toolregistry.catalog.definition_read_bytes", len(definition)),
	)
	state, err := parseCatalogState(name, raw)
	if err != nil {
		return err
	}
	if retired != (!state.NativeAgent && state.State == catalogEntryRetired) {
		return fmt.Errorf("toolset %q disagrees with permanent retirement history", name)
	}
	if !state.NativeAgent && state.WireProtocolVersion < toolregistry.WireProtocolVersion {
		_, err = c.savedSchemas(state, definition)
		return err
	}
	_, err = c.decodeSnapshot(ctx, name, raw, definition, retired)
	return err
}

// savedSchemas verifies original declaration identity and JSON schemas without
// treating its consumer metadata as a current executable contract. The returned
// fields support startup integrity and schema-only inspection.
func (c *toolsetCatalog) savedSchemas(state catalogState, raw string) (internaladmission.Schema, error) {
	schema, err := internaladmission.ValidateSavedToolsetIntegrity([]byte(raw), state.Info, state.SchemaFingerprint)
	if err != nil {
		return internaladmission.Schema{}, err
	}
	// These temporary validator inputs never become consumer declarations.
	tools := make([]*genregistry.ToolSchema, len(schema.Tools))
	for i, tool := range schema.Tools {
		tools[i] = &genregistry.ToolSchema{
			Name: tool.Name, PayloadSchema: tool.PayloadSchema,
			ExecutionPayloadSchema: tool.ExecutionPayloadSchema,
			ResultSchema:           tool.ResultSchema, SidecarSchema: tool.SidecarSchema,
		}
	}
	if err := c.validator.ValidateToolSchemas(tools); err != nil {
		return internaladmission.Schema{}, err
	}
	return schema, nil
}
