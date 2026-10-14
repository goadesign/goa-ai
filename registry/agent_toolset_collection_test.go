package registry

// These tests send complete native declarations through registration. Invalid
// groups must fail before storage changes; valid paging and ordinary tools must
// survive registration, consumer resolution and a Registry restart unchanged.

import (
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestAgentToolsetRejectsIncompleteCollections(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*genregistry.AgentToolsetDeclaration)
	}{
		{"unqualified name", func(p *genregistry.AgentToolsetDeclaration) { p.Tools[0].Name = "query" }},
		{"empty tool name", func(p *genregistry.AgentToolsetDeclaration) { p.Tools[0].Name = "records." }},
		{"missing continuation", func(p *genregistry.AgentToolsetDeclaration) { p.Tools = p.Tools[:1] }},
		{"missing source", func(p *genregistry.AgentToolsetDeclaration) { p.Tools = p.Tools[1:] }},
		{"different source", func(p *genregistry.AgentToolsetDeclaration) {
			other := "records.other"
			p.Tools[1].ConsumerContract.Bounds.Paging.SourceTool = &other
		}},
		{"duplicate name", func(p *genregistry.AgentToolsetDeclaration) { p.Tools = append(p.Tools, p.Tools[0]) }},
		{"service member", func(p *genregistry.AgentToolsetDeclaration) {
			p.Tools[1].ConsumerContract.Kind = "service"
			p.Tools[1].ConsumerContract.Agent = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
			invalid := testAgentPagingToolset()
			test.change(invalid)
			_, err := svc.RegisterAgentToolset(t.Context(), invalid)
			requireServiceErrorName(t, err, "validation_error")
			assert.Empty(t, store.content)
			assert.Empty(t, store.definitions)

			valid := testAgentPagingToolset()
			saved, err := svc.RegisterAgentToolset(t.Context(), valid)
			require.NoError(t, err)
			beforeState, beforeDefinition := maps.Clone(store.content), maps.Clone(store.definitions)
			_, err = svc.ReplaceAgentToolset(t.Context(), &genregistry.ReplaceAgentToolsetPayload{
				Name: invalid.Name, Tools: invalid.Tools, ExpectedRegistrationToken: saved.RegistrationToken,
			})
			requireServiceErrorName(t, err, "validation_error")
			assert.Equal(t, beforeState, store.content)
			assert.Equal(t, beforeDefinition, store.definitions)
		})
	}
}

func TestAgentToolsetPreservesCompleteVariedCollections(t *testing.T) {
	input := testAgentPagingToolset()
	ordinary := testAgentToolset("ordinary/1").Tools[0]
	ordinary.Name = "other.namespace.inspect"
	ordinary.ConsumerContract.Agent.Executor = "other.agent"
	ordinary.ConsumerContract.Agent.Workflow = "other.workflow"
	ordinary.ConsumerContract.Agent.Queue = "other.queue"
	ordinary.ConsumerContract.Meta = map[string][]string{"purpose": {"inspection"}}
	bounded := testAgentToolset("bounded/1").Tools[0]
	bounded.Name = "records.summary"
	bounded.ConsumerContract.Bounds = &genregistry.ToolBounds{}
	self := testAgentPagingToolset().Tools[0]
	self.Name = "records.scan"
	self.ConsumerContract.Bounds.Paging.ContinueTool = &self.Name
	implicit := testAgentPagingToolset().Tools[0]
	implicit.Name = "records.browse"
	implicit.ConsumerContract.Bounds.Paging.ContinueTool = nil
	input.Tools = append(input.Tools, ordinary, bounded, self, implicit)

	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	saved, err := svc.RegisterAgentToolset(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, input.Tools, saved.Toolset.Tools)
	resolved, err := registrycontract.Resolve(saved)
	require.NoError(t, err)
	assert.Len(t, resolved.Specs, len(input.Tools))
	same, err := svc.RegisterAgentToolset(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, saved, same)

	restarted := newToolsetCatalog(store, clock)
	require.NoError(t, restarted.validatePersistedEntries(t.Context()))
	loaded, err := restarted.GetToolset(t.Context(), input.Name)
	require.NoError(t, err)
	assert.Equal(t, saved.Toolset, loaded)
}

// testAgentPagingToolset describes a query and the native operation that reads
// its next page. Both have ordinary generated-compatible schemas and distinct
// configuration references, so collection checks cannot assume one target.
func testAgentPagingToolset() *genregistry.AgentToolsetDeclaration {
	query := testAgentToolset("query/1").Tools[0]
	next := testAgentToolset("next/1").Tools[0]
	query.Name, next.Name = "records.query", "records.next"
	payload := []byte(`{"type":"object","properties":{"cursor":{"type":"string"}},"additionalProperties":false}`)
	result := []byte(`{"type":"object","properties":{"next_cursor":{"type":"string"}},"additionalProperties":false}`)
	for _, tool := range []*genregistry.ToolSchema{query, next} {
		tool.PayloadSchema = payload
		tool.ExecutionPayloadSchema = payload
		tool.ResultSchema = result
		tool.ConsumerContract.Payload.SchemaWithoutRootExample = payload
		tool.ConsumerContract.Result.SchemaWithoutRootExample = result
		tool.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
			ContinueTool: &next.Name, RetainedFields: []string{}, CursorField: "cursor", NextCursorField: "next_cursor",
		}}
	}
	next.ConsumerContract.Bounds.Paging.SourceTool = &query.Name
	return &genregistry.AgentToolsetDeclaration{Name: "records", Tools: []*genregistry.ToolSchema{query, next}}
}
