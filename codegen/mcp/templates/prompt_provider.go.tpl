{{- if or .StaticPrompts .DynamicPrompts }}
// PromptProvider defines the interface for providing prompt content
// Users must implement this interface to provide actual prompt implementations
type PromptProvider interface {
{{- range .StaticPrompts }}
	// Get{{ goify .Name }}Prompt returns the content for the {{ .Name }} prompt
	Get{{ goify .Name }}Prompt(arguments json.RawMessage) (*PromptsGetResult, error)
{{- end }}
{{- range .DynamicPrompts }}
	// Get{{ goify .Name }}Prompt returns the dynamic content for the {{ .Name }} prompt
	Get{{ goify .Name }}Prompt(ctx context.Context, arguments json.RawMessage) (*PromptsGetResult, error)
{{- end }}
}
{{- end }}