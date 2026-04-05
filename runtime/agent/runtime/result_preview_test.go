package runtime

import (
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestFormatResultPreviewUsesExplicitResultAndBoundsShape(t *testing.T) {
	toolName := tools.Ident("svc.tools.preview_nested")
	rthints.RegisterResultHint(toolName, template.Must(template.New("preview_nested").Parse(
		`{{ index .Result.Results 0 }} / {{ .Bounds.Returned }} / {{ .Bounds.Total }}`,
	)))

	total := 9
	preview := formatResultPreview(toolName, nil, &projectedRuntimeResult{
		Results: []string{"alpha"},
	}, &agent.Bounds{
		Returned:  1,
		Total:     &total,
		Truncated: true,
	})

	require.Equal(t, "alpha / 1 / 9", preview)
}

func TestFormatResultPreviewLeavesBoundsNilWhenAbsent(t *testing.T) {
	toolName := tools.Ident("svc.tools.preview_nil_bounds")
	rthints.RegisterResultHint(toolName, template.Must(template.New("preview_nil_bounds").Parse(
		`{{ if .Bounds }}has-bounds{{ else }}{{ len .Result.Results }} result{{ end }}`,
	)))

	preview := formatResultPreview(toolName, nil, &projectedRuntimeResult{
		Results: []string{"alpha"},
	}, nil)

	require.Equal(t, "1 result", preview)
}

func TestFormatResultPreviewIncludesTypedArgsWhenAvailable(t *testing.T) {
	toolName := tools.Ident("svc.tools.preview_args")
	rthints.RegisterResultHint(toolName, template.Must(template.New("preview_args").Parse(
		`{{ .Args.Query }} -> {{ index .Result.Results 0 }}`,
	)))

	preview := formatResultPreview(toolName, &projectedRuntimePayload{
		Query: "status",
	}, &projectedRuntimeResult{
		Results: []string{"alpha"},
	}, nil)

	require.Equal(t, "status -> alpha", preview)
}
