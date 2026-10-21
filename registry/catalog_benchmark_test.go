package registry

// This benchmark isolates the Go work in a warm health cycle. The large fixture
// differs only in schema size; Redis/network costs are measured separately.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func BenchmarkCatalogHealthLifecycle(b *testing.B) {
	for _, test := range []struct {
		name        string
		description string
	}{
		{name: "small"},
		{name: "large", description: strings.Repeat("schema evidence ", 32_768)},
	} {
		b.Run(test.name, func(b *testing.B) {
			ctx := b.Context()
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			catalog := newToolsetCatalog(newTestCatalogMap(clock), clock)
			schema, err := json.Marshal(map[string]any{
				"type":        "object",
				"description": test.description,
			})
			require.NoError(b, err)
			toolset := testCatalogToolset("benchmark.tools", "health fixture", nil)
			toolset.Tools = []*genregistry.ToolSchema{{
				Name:                   "lookup",
				PayloadSchema:          schema,
				ExecutionPayloadSchema: schema,
				ResultSchema:           schema,
			}}
			registration, err := catalog.Register(ctx, testCatalogDefinition(b, toolset),
				testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
			require.NoError(b, err)
			_, err = catalog.ActiveRegistration(ctx, toolset.Name)
			require.NoError(b, err)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_, _, err := catalog.healthEntry(ctx, toolset.Name)
				require.NoError(b, err)
				_, err = catalog.ListToolsets(ctx, nil)
				require.NoError(b, err)
				require.NoError(b, catalog.RecordPong(ctx, toolset.Name, "provider",
					testIncarnationA, registration.RegistrationToken, registration.HealthEpoch))
			}
		})
	}
}

// BenchmarkCatalogDefinitionRead measures the complete warm definition lookup,
// including exact-byte hashing, fresh state checks and validation reuse.
func BenchmarkCatalogDefinitionRead(b *testing.B) {
	for _, size := range []int{0, 32_768} {
		name := "small"
		if size != 0 {
			name = "large"
		}
		b.Run(name, func(b *testing.B) {
			ctx := b.Context()
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			catalog := newToolsetCatalog(store, clock)
			input := testDefinitionToolset()
			schema, err := json.Marshal(map[string]any{
				"type": "object", "description": strings.Repeat("schema evidence ", size),
			})
			require.NoError(b, err)
			input.Tools[0].PayloadSchema = schema
			input.Tools[0].ExecutionPayloadSchema = schema
			input.Tools[0].ResultSchema = schema
			definition := testCatalogDefinition(b, input)
			_, err = catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
			require.NoError(b, err)
			_, err = catalog.ActiveRegistration(ctx, input.Name)
			require.NoError(b, err)
			store.snapshotReads = 0
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				entry, err := catalog.ActiveRegistration(ctx, input.Name)
				require.NoError(b, err)
				require.NoError(b, validatePayload(entry.Toolset.executionSchemas["tools.lookup"], []byte(`{}`)))
			}
			b.ReportMetric(float64(store.snapshotReads*len(definition.raw))/float64(b.N), "definition-B/op")
		})
	}
}

// BenchmarkReadCatalogToolset measures a warm bounded read through the public
// service method, including fresh state checks, exact-byte hashing, and strict
// decoding into an independent result. The in-memory store excludes Redis and
// network time; all declarations and schema descriptions are synthetic.
func BenchmarkReadCatalogToolset(b *testing.B) {
	for _, test := range []struct {
		name             string
		tools            int
		descriptionWords int
	}{
		{name: "small", tools: 1},
		{name: "large", tools: 24, descriptionWords: 2048},
	} {
		b.Run(test.name, func(b *testing.B) {
			input := testDefinitionToolset()
			input.Tools = make([]*genregistry.ToolSchema, test.tools)
			schema, err := json.Marshal(map[string]any{
				"type": "object", "description": strings.Repeat("synthetic schema description ", test.descriptionWords),
			})
			require.NoError(b, err)
			for i := range input.Tools {
				tool := testDefinitionToolset().Tools[0]
				tool.Name = fmt.Sprintf("tools.lookup_%d", i)
				tool.PayloadSchema = schema
				tool.ExecutionPayloadSchema = schema
				tool.ResultSchema = schema
				input.Tools[i] = tool
			}
			service, store := testBoundedCatalogService(b, input)
			key := toolsetCatalogKey(input.Name)
			stateBytes, definitionBytes := len(store.content[key]), len(store.definitions[key])
			budget := int64(stateBytes + definitionBytes)
			_, err = service.ReadCatalogToolset(b.Context(), input.Name, budget)
			require.NoError(b, err)
			b.ReportAllocs()
			b.SetBytes(budget)
			b.ResetTimer()
			for b.Loop() {
				result, err := service.ReadCatalogToolset(b.Context(), input.Name, budget)
				require.NoError(b, err)
				require.Len(b, result.Registration.Toolset.Tools, test.tools)
			}
			b.ReportMetric(float64(stateBytes), "state-B/op")
			b.ReportMetric(float64(definitionBytes), "definition-B/op")
		})
	}
}
