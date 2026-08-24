package tests

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGolden_BoundedResult_UsesBoundsSpecAndProjection(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceToolsetBindSelfBoundedResult())

	specs := generatedContentBySuffix(t, files, "toolsets/lookup/specs.go")
	require.Contains(t, specs, "Bounds: &tools.BoundsSpec{")
	require.Contains(t, specs, "Paging: &tools.PagingSpec{")
	require.NotContains(t, specs, "BoundedResult: true")

	schemas := generatedContentBySuffix(t, files, "agents/scribe/specs/tool_schemas.json")
	require.Contains(t, schemas, `"returned"`)
	require.Contains(t, schemas, `"truncated"`)
	require.Contains(t, schemas, `"total"`)
	require.Contains(t, schemas, `"refinement_hint"`)
	require.Contains(t, schemas, `"next_cursor"`)

	executor := generatedContentBySuffix(t, files, "agents/scribe/lookup/service_executor.go")
	require.Contains(t, executor, "SearchPayloadCodec().FromJSON(call.Payload)")
	require.Contains(t, executor, "bounds := initSearchBounds(mr)")
	require.Contains(t, executor, "Bounds: bounds,")
	require.Contains(t, executor, "func initSearchBounds(")
	require.Contains(t, executor, "bounds.Returned = mr.Returned")
	require.Contains(t, executor, "bounds.Truncated = mr.Truncated")
	require.Contains(t, executor, "bounds.NextCursor = mr.NextCursor")
	require.NotContains(t, executor, `requires method result field "returned"`)
	require.NotContains(t, executor, `requires method result field "truncated"`)

	provider := generatedContentBySuffix(t, files, "toolsets/lookup/provider.go")
	require.Contains(t, provider, "bounds := initSearchBounds(methodOut)")
	require.Contains(t, provider, "Bounds:    bounds,")
	require.Contains(t, provider, "func initSearchBounds(")
	require.Contains(t, provider, "bounds.Returned = mr.Returned")
	require.Contains(t, provider, "bounds.Truncated = mr.Truncated")
	require.Contains(t, provider, "bounds.NextCursor = mr.NextCursor")
}

func TestGolden_BoundedResult_ProjectsRequiredExactTotal(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceToolsetBindSelfBoundedResultExactTotal())

	specs := generatedContentBySuffix(t, files, "toolsets/lookup/specs.go")
	require.NotContains(t, specs, "TotalRequired")
	schemas := generatedContentBySuffix(t, files, "agents/scribe/specs/tool_schemas.json")
	var document struct {
		Tools []struct {
			Result struct {
				Schema struct {
					Required []string `json:"required"`
				} `json:"schema"`
			} `json:"result"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemas), &document))
	require.Len(t, document.Tools, 1)
	require.ElementsMatch(t, []string{"results", "returned", "total", "truncated"}, document.Tools[0].Result.Schema.Required)

	executor := generatedContentBySuffix(t, files, "agents/scribe/lookup/service_executor.go")
	require.Contains(t, executor, "total := mr.Total")
	require.Contains(t, executor, "bounds.Total = &total")

	provider := generatedContentBySuffix(t, files, "toolsets/lookup/provider.go")
	require.Contains(t, provider, "total := mr.Total")
	require.Contains(t, provider, "bounds.Total = &total")
}
