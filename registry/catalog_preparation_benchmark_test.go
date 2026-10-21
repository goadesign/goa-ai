// These benchmarks measure independent catalog results and the preparation
// retained for one unchanged synthetic declaration. The in-memory store leaves
// network and Redis time outside both cold and warm measurements.
package registry

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// BenchmarkCatalogDeclarationPreparation measures a complete bounded catalog
// read with either fresh preparation or the same already checked declaration.
func BenchmarkCatalogDeclarationPreparation(b *testing.B) {
	for _, large := range []bool{false, true} {
		name := "small"
		if large {
			name = "large"
		}
		b.Run(name, func(b *testing.B) {
			input := preparationBenchmarkToolset(b, large)
			for _, cold := range []bool{false, true} {
				mode := "warm"
				if cold {
					mode = "cold"
				}
				b.Run(mode, func(b *testing.B) {
					service, store := testBoundedCatalogService(b, input)
					key := toolsetCatalogKey(input.Name)
					budget := int64(len(store.content[key]) + len(store.definitions[key]))
					_, err := service.ReadCatalogToolset(b.Context(), input.Name, budget)
					require.NoError(b, err)
					clock := store.clock
					b.ReportAllocs()
					b.SetBytes(budget)
					b.ResetTimer()
					for b.Loop() {
						if cold {
							service.catalog = newToolsetCatalog(store, clock)
						}
						result, err := service.ReadCatalogToolset(b.Context(), input.Name, budget)
						require.NoError(b, err)
						require.Len(b, result.Registration.Toolset.Tools, len(input.Tools))
					}
				})
			}
		})
	}
}

// BenchmarkCatalogRegistrationPreparation measures saving one already validated
// registration. Unlike a cold catalog read, this includes writing JSON before
// preparing the owned declaration; storage and fingerprint calculation are outside it.
func BenchmarkCatalogRegistrationPreparation(b *testing.B) {
	for _, large := range []bool{false, true} {
		name := "small"
		if large {
			name = "large"
		}
		b.Run(name, func(b *testing.B) {
			input := preparationBenchmarkToolset(b, large)
			validator := newSchemaValidator()
			require.NoError(b, validator.ValidateToolSchemas(input.Tools))
			raw, err := json.Marshal(input)
			require.NoError(b, err)
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for b.Loop() {
				prepared, err := newCatalogToolset(input, "synthetic", validator)
				require.NoError(b, err)
				require.Equal(b, len(input.Tools), prepared.info.ToolCount)
			}
		})
	}
}

// BenchmarkCatalogDeclarationRetention measures heap retained by independent
// Registry instances after preparing the same checked bytes. The source bytes
// and registration store are allocated before the first heap measurement.
func BenchmarkCatalogDeclarationRetention(b *testing.B) {
	for _, large := range []bool{false, true} {
		name := "small"
		if large {
			name = "large"
		}
		b.Run(name, func(b *testing.B) {
			input := preparationBenchmarkToolset(b, large)
			service, store := testBoundedCatalogService(b, input)
			key := toolsetCatalogKey(input.Name)
			raw := store.definitions[key]
			clock := store.clock
			_, err := service.catalog.validatedDefinition(b.Context(), input.Name, raw, false)
			require.NoError(b, err)
			const instances = 8
			b.ReportAllocs()
			for b.Loop() {
				catalogs := make([]*toolsetCatalog, instances)
				for index := range catalogs {
					catalogs[index] = newToolsetCatalog(store, clock)
				}
				runtime.GC()
				runtime.GC()
				var before runtime.MemStats
				runtime.ReadMemStats(&before)
				for index := range catalogs {
					_, err := catalogs[index].validatedDefinition(b.Context(), input.Name, raw, false)
					require.NoError(b, err)
				}
				runtime.GC()
				runtime.GC()
				var after runtime.MemStats
				runtime.ReadMemStats(&after)
				retained := float64(after.HeapAlloc) - float64(before.HeapAlloc)
				b.ReportMetric(retained/instances, "retained-B/definition")
				runtime.KeepAlive(catalogs)
				runtime.KeepAlive(service)
				runtime.KeepAlive(input)
				runtime.KeepAlive(raw)
				runtime.KeepAlive(store)
			}
		})
	}
}

// preparationBenchmarkToolset builds complete declarations with distinct
// operation names and shared synthetic schema text for reproducible size cases.
func preparationBenchmarkToolset(b *testing.B, large bool) *genregistry.Toolset {
	b.Helper()
	count, words := 1, 0
	if large {
		count, words = 24, 2048
	}
	schema, err := json.Marshal(map[string]any{
		"type": "object", "description": strings.Repeat("synthetic schema description ", words),
	})
	require.NoError(b, err)
	input := testDefinitionToolset()
	input.Tools = make([]*genregistry.ToolSchema, count)
	for index := range input.Tools {
		tool := testDefinitionToolset().Tools[0]
		tool.Name = fmt.Sprintf("tools.lookup_%d", index)
		tool.PayloadSchema = schema
		tool.ExecutionPayloadSchema = schema
		tool.ResultSchema = schema
		input.Tools[index] = tool
	}
	return input
}
