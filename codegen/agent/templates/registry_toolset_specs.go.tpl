// RegistryName is the name of the registry source.
const RegistryName = {{ printf "%q" .Registry.RegistryName }}

// ToolsetName is the name of the toolset in the registry.
const ToolsetName = {{ printf "%q" .Registry.ToolsetName }}

{{- if .Registry.Version }}

// Version is the pinned version for this toolset.
const Version = {{ printf "%q" .Registry.Version }}
{{- end }}

// Discover loads this toolset and validates its schemas before agent startup.
// Pass the returned immutable value to the generated agent's RegistryToolsets.
// Later discovery returns a new value without changing an existing agent.
func Discover(ctx context.Context, client registry.RegistryClient) (*registry.Toolset, error) {
	toolset, err := client.GetToolset(ctx, ToolsetName)
	if err != nil {
		return nil, fmt.Errorf("discover toolset %q from registry %q: %w", ToolsetName, RegistryName, err)
	}
	if toolset == nil {
		return nil, fmt.Errorf("toolset %q not found in registry %q", ToolsetName, RegistryName)
	}
	if toolset.Name != ToolsetName {
		return nil, fmt.Errorf("registry %q returned toolset %q for %q", RegistryName, toolset.Name, ToolsetName)
	}
{{- if .Registry.Version }}
	if toolset.Version != Version {
		return nil, fmt.Errorf("toolset %q requires version %q, registry returned %q", ToolsetName, Version, toolset.Version)
	}
{{- end }}
	return registry.NewToolset(toolset)
}
