package runtime

import (
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestFormatResultPreviewUsesExplicitResultAndBoundsShape(t *testing.T) {
	toolName := tools.Ident("svc.tools.preview_nested")
	rthints.RegisterResultHint(toolName, template.Must(template.New("preview_nested").Parse(
		`{{ index .Result.Results 0 }} / {{ .Bounds.Returned }} / {{ .Bounds.Total }}`,
	)))

	total := 9
	preview, err := formatResultPreview(toolName, nil, &projectedRuntimeResult{
		Results: []string{"alpha"},
	}, &agent.Bounds{
		Returned:  1,
		Total:     &total,
		Truncated: true,
	})

	require.NoError(t, err)
	require.Equal(t, "alpha / 1 / 9", preview)
}

func TestFormatResultPreviewLeavesBoundsNilWhenAbsent(t *testing.T) {
	toolName := tools.Ident("svc.tools.preview_nil_bounds")
	rthints.RegisterResultHint(toolName, template.Must(template.New("preview_nil_bounds").Parse(
		`{{ if .Bounds }}has-bounds{{ else }}{{ len .Result.Results }} result{{ end }}`,
	)))

	preview, err := formatResultPreview(toolName, nil, &projectedRuntimeResult{
		Results: []string{"alpha"},
	}, nil)

	require.NoError(t, err)
	require.Equal(t, "1 result", preview)
}

func TestFormatResultPreviewIncludesTypedArgsWhenAvailable(t *testing.T) {
	toolName := tools.Ident("svc.tools.preview_args")
	rthints.RegisterResultHint(toolName, template.Must(template.New("preview_args").Parse(
		`{{ .Args.Query }} -> {{ index .Result.Results 0 }}`,
	)))

	preview, err := formatResultPreview(toolName, &projectedRuntimePayload{
		Query: "status",
	}, &projectedRuntimeResult{
		Results: []string{"alpha"},
	}, nil)

	require.NoError(t, err)
	require.Equal(t, "status -> alpha", preview)
}

func TestFormatToolResultPreviewSkipsErrorResults(t *testing.T) {
	toolName := tools.Ident("svc.tools.preview_error")
	rthints.RegisterResultHint(toolName, template.Must(template.New("preview_error").Parse(
		`{{ .Result.Summary }}`,
	)))

	preview, err := formatToolResultPreviewForCall(t.Context(), nil, &planner.ToolRequest{
		Name: toolName,
	}, &planner.ToolResult{
		Name:  toolName,
		Error: planner.NewToolError("tool failed"),
	})

	require.NoError(t, err)
	require.Empty(t, preview)
}
