// Prompt identifier constants.
const (
{{- range .Prompts }}
	// {{ .ConstantName }} is the canonical identifier for the {{ .Name }} prompt.
	{{ .ConstantName }} prompt.Ident = {{ printf "%q" .ID }}
{{- end }}
)

{{- range .Prompts }}
{{- if .HasArgs }}
// {{ .ArgsTypeName }} defines the typed input arguments for the {{ .Name }} prompt.
type {{ .ArgsTypeName }} {{ .ArgsDef }}
{{- end }}
{{- end }}

{{- range .Prompts }}
// {{ .SpecFuncName }} returns the baseline prompt specification for {{ .Name }}.
func {{ .SpecFuncName }}() prompt.PromptSpec {
	return prompt.PromptSpec{
		ID:          {{ .ConstantName }},
		AgentID:     {{ printf "%q" $.AgentID }},
		Role:        prompt.PromptRole{{ .Role | promptRoleTitle }},
		Description: {{ printf "%q" .Description }},
		Template:    {{ printf "%q" .Template }},
		Version:     {{ printf "%q" .Version }},
		Fields:      {{ renderPromptFields .Fields }},
	}
}
{{- end }}

// PromptSpecs returns all baseline prompt specifications declared for this agent.
func PromptSpecs() []prompt.PromptSpec {
	return []prompt.PromptSpec{
{{- range .Prompts }}
		{{ .SpecFuncName }}(),
{{- end }}
	}
}

{{- range .Prompts }}
{{- if not .HasArgs }}
// {{ .RenderFuncName }} resolves and renders the {{ .Name }} prompt using the run-scoped PlannerContext.
func {{ .RenderFuncName }}(ctx context.Context, pctx planner.PlannerContext) (*prompt.PromptContent, error) {
	if pctx == nil {
		return nil, errors.New("planner context is required")
	}
	return pctx.RenderPrompt(ctx, {{ .ConstantName }}, nil)
}
{{- else }}
// {{ .RenderFuncName }} resolves and renders the {{ .Name }} prompt using the run-scoped PlannerContext and typed arguments.
func {{ .RenderFuncName }}(ctx context.Context, pctx planner.PlannerContext, args {{ .ArgsTypeName }}) (*prompt.PromptContent, error) {
	if pctx == nil {
		return nil, errors.New("planner context is required")
	}
	return pctx.RenderPrompt(ctx, {{ .ConstantName }}, args)
}
{{- end }}
{{- end }}
