// Prompt identifier constants.
const (
{{- range .Prompts }}
	// {{ .ConstantName }} is the canonical identifier for the {{ .Name }} prompt.
	{{ .ConstantName }} prompt.Ident = {{ printf "%q" .ID }}
{{- end }}
)

{{- range .UserTypes }}
// {{ .Name }} defines the {{ .Name }} type referenced by prompt arguments.
type {{ .Name }} {{ .Def }}
{{- end }}

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
		AgentID:     {{ printf "%q" .AgentID }},
		Role:        prompt.PromptRole{{ .Role | promptRoleTitle }},
		Description: {{ printf "%q" .Description }},
		Template:    {{ printf "%q" .Template }},
		Version:     {{ printf "%q" .Version }},
{{- if .HasArgs }}
		ArgsType:    reflect.TypeFor[{{ .ArgsTypeName }}](),
{{- else }}
		ArgsType:    reflect.TypeFor[struct{}](),
{{- end }}
	}
}
{{- end }}

// PromptSpecs returns all baseline prompt specifications declared {{ if .IsService }}for this service{{ else }}for this agent{{ end }}.
func PromptSpecs() []prompt.PromptSpec {
	return []prompt.PromptSpec{
{{- range .Prompts }}
		{{ .SpecFuncName }}(),
{{- end }}
	}
}

// RegisterPrompts registers all baseline prompt specifications into the given PromptRegistry.
func RegisterPrompts(reg *prompt.Registry) error {
	if reg == nil {
		return errors.New("prompt registry is required")
	}
	for _, spec := range PromptSpecs() {
		if err := reg.Register(spec); err != nil {
			return err
		}
	}
	return nil
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

// {{ .RenderWithRegistryFuncName }} resolves and renders the {{ .Name }} prompt directly against a prompt registry.
func {{ .RenderWithRegistryFuncName }}(ctx context.Context, reg *prompt.Registry, scope prompt.Scope) (*prompt.PromptContent, error) {
	if reg == nil {
		return nil, errors.New("prompt registry is required")
	}
	return reg.Render(ctx, {{ .ConstantName }}, scope, nil)
}
{{- else }}
// {{ .RenderFuncName }} resolves and renders the {{ .Name }} prompt using the run-scoped PlannerContext and typed arguments.
func {{ .RenderFuncName }}(ctx context.Context, pctx planner.PlannerContext, args {{ .ArgsTypeName }}) (*prompt.PromptContent, error) {
	if pctx == nil {
		return nil, errors.New("planner context is required")
	}
	return pctx.RenderPrompt(ctx, {{ .ConstantName }}, args)
}

// {{ .RenderWithRegistryFuncName }} resolves and renders the {{ .Name }} prompt directly against a prompt registry with typed arguments.
func {{ .RenderWithRegistryFuncName }}(ctx context.Context, reg *prompt.Registry, scope prompt.Scope, args {{ .ArgsTypeName }}) (*prompt.PromptContent, error) {
	if reg == nil {
		return nil, errors.New("prompt registry is required")
	}
	return reg.Render(ctx, {{ .ConstantName }}, scope, args)
}
{{- end }}
{{- end }}
