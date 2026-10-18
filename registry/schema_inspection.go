// Package registry returns saved descriptive fields and JSON schemas without
// reconstructing executable consumer contracts. It grants no provider authority
// and always verifies one coherent state, definition, and retirement snapshot.
package registry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

// GetToolsetSchemas returns an active toolset's original descriptive fields and
// schemas. Historical consumer metadata remains saved but is not part of this
// inspection response; current services and native Agents remain fully checked.
func (s *Service) GetToolsetSchemas(ctx context.Context, p *genregistry.GetToolsetPayload) (result *genregistry.ToolsetSchemas, err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.schemas",
		trace.WithAttributes(attribute.String("toolregistry.toolset", p.Name)))
	defer finishCatalogSpan(ctx, span, &err)
	raw, definition, retired, exists, err := s.catalog.store.Snapshot(ctx, toolsetCatalogKey(p.Name))
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("read toolset schemas: %w", err))
	}
	if !exists {
		return nil, genregistry.MakeNotFound(fmt.Errorf("toolset %q not found", p.Name))
	}
	state, err := parseCatalogState(p.Name, raw)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	if retired != (!state.NativeAgent && state.State == catalogEntryRetired) {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("toolset %q disagrees with permanent retirement history", p.Name))
	}
	if state.State != catalogEntryActive {
		return nil, genregistry.MakeNotFound(fmt.Errorf("toolset %q not found", p.Name))
	}
	if state.NativeAgent || state.WireProtocolVersion == toolregistry.WireProtocolVersion {
		if _, err := s.catalog.decodeSnapshot(ctx, p.Name, raw, definition, retired); err != nil {
			return nil, genregistry.MakeServiceUnavailable(err)
		}
	}
	schema, err := s.catalog.savedSchemas(state, definition)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	info := state.Info
	result = &genregistry.ToolsetSchemas{
		Name: info.Name, Description: info.Description, Version: info.Version,
		Tags: info.Tags, RegisteredAt: info.RegisteredAt,
		Tools: make([]*genregistry.ToolSchemas, len(schema.Tools)),
	}
	for i, tool := range schema.Tools {
		result.Tools[i] = &genregistry.ToolSchemas{
			Name: tool.Name, Description: tool.Description, Tags: tool.Tags,
			PayloadSchema: tool.PayloadSchema, ResultSchema: tool.ResultSchema,
			SidecarSchema: tool.SidecarSchema,
		}
	}
	return result, nil
}
