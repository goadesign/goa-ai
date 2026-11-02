// Prompt identifier constants.
const (
{{- range .Prompts }}
	// {{ .ConstantName }} is the canonical identifier for the {{ .Name }} prompt.
	{{ .ConstantName }} {{ $.PromptAlias }}.Ident = {{ printf "%q" .ID }}
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
func {{ .SpecFuncName }}() {{ $.PromptAlias }}.PromptSpec {
	return {{ $.PromptAlias }}.PromptSpec{
		ID:          {{ .ConstantName }},
		AgentID:     {{ printf "%q" .AgentID }},
		Role:        {{ $.PromptAlias }}.PromptRole{{ .Role | promptRoleTitle }},
		Description: {{ printf "%q" .Description }},
		Template:    {{ printf "%q" .Template }},
		Version:     {{ printf "%q" .Version }},
{{- if .HasArgs }}
		ArgsType:    {{ $.ReflectAlias }}.TypeFor[{{ .ArgsTypeName }}](),
{{- else }}
		ArgsType:    {{ $.ReflectAlias }}.TypeFor[struct{}](),
{{- end }}
	}
}
{{- end }}

// PromptSpecs returns all baseline prompt specifications declared {{ if .IsService }}for this service{{ else }}for this agent{{ end }}.
func PromptSpecs() []{{ $.PromptAlias }}.PromptSpec {
	return []{{ $.PromptAlias }}.PromptSpec{
{{- range .Prompts }}
		{{ .SpecFuncName }}(),
{{- end }}
	}
}

// RegisterPrompts registers all baseline prompt specifications into the given PromptRegistry.
func RegisterPrompts(reg *{{ $.PromptAlias }}.Registry) error {
	if reg == nil {
		return {{ $.ErrorsAlias }}.New("prompt registry is required")
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
func {{ .RenderFuncName }}(ctx {{ $.ContextAlias }}.Context, pctx {{ $.PlannerAlias }}.PlannerContext) (*{{ $.PromptAlias }}.PromptContent, error) {
	if pctx == nil {
		return nil, {{ $.ErrorsAlias }}.New("planner context is required")
	}
	return pctx.RenderPrompt(ctx, {{ .ConstantName }}, struct{}{})
}

// {{ .RenderWithRegistryFuncName }} resolves and renders the {{ .Name }} prompt directly against a prompt registry.
func {{ .RenderWithRegistryFuncName }}(ctx {{ $.ContextAlias }}.Context, reg *{{ $.PromptAlias }}.Registry, scope {{ $.PromptAlias }}.Scope) (*{{ $.PromptAlias }}.PromptContent, error) {
	if reg == nil {
		return nil, {{ $.ErrorsAlias }}.New("prompt registry is required")
	}
	return reg.Render(ctx, {{ .ConstantName }}, scope, struct{}{})
}
{{- else }}
// {{ .RenderFuncName }} resolves and renders the {{ .Name }} prompt using the run-scoped PlannerContext and typed arguments.
func {{ .RenderFuncName }}(ctx {{ $.ContextAlias }}.Context, pctx {{ $.PlannerAlias }}.PlannerContext, args {{ .ArgsTypeName }}) (*{{ $.PromptAlias }}.PromptContent, error) {
	if pctx == nil {
		return nil, {{ $.ErrorsAlias }}.New("planner context is required")
	}
	return pctx.RenderPrompt(ctx, {{ .ConstantName }}, args)
}

// {{ .RenderWithRegistryFuncName }} resolves and renders the {{ .Name }} prompt directly against a prompt registry with typed arguments.
func {{ .RenderWithRegistryFuncName }}(ctx {{ $.ContextAlias }}.Context, reg *{{ $.PromptAlias }}.Registry, scope {{ $.PromptAlias }}.Scope, args {{ .ArgsTypeName }}) (*{{ $.PromptAlias }}.PromptContent, error) {
	if reg == nil {
		return nil, {{ $.ErrorsAlias }}.New("prompt registry is required")
	}
	return reg.Render(ctx, {{ .ConstantName }}, scope, args)
}
{{- end }}
{{- end }}