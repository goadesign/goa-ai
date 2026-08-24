{{- if or .StaticPrompts .DynamicPrompts }}
// PromptProvider defines the interface for providing prompt content
// Users must implement this interface to provide actual prompt implementations
type PromptProvider interface {
{{- range .StaticPrompts }}
	// {{ .ProviderMethodName }} returns the content for the {{ .Name }} prompt
	{{ .ProviderMethodName }}(arguments json.RawMessage) (*PromptsGetResult, error)
{{- end }}
{{- range .DynamicPrompts }}
	// {{ .ProviderMethodName }} returns the dynamic content for the {{ .Name }} prompt
	{{ .ProviderMethodName }}(ctx context.Context, arguments json.RawMessage) (*PromptsGetResult, error)
{{- end }}
}
{{- end }}
