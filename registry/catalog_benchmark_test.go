package registry

// This benchmark isolates the Go work in a warm health cycle. The large fixture
// differs only in schema size; Redis/network costs are measured separately.

import (
	"encoding/json"
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
