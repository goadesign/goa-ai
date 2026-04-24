// RegistryClient defines the interface for fetching toolset data from a registry.
type RegistryClient interface {
	GetToolset(ctx context.Context, name string) (*ToolsetSchema, error)
}

// ToolsetSchema represents a toolset fetched from the registry.
type ToolsetSchema struct {
	Name        string
	Description string
	Tools       []ToolSchema
}

// ToolSchema represents a tool definition from the registry.
type ToolSchema struct {
	Name            string
	Title           string
	Description     string
	Tags            []string
	PayloadTypeName string
	PayloadSchema   []byte
	ResultTypeName  string
	ResultSchema    []byte
}

// RegistryToolsetID is the identifier for this registry-backed toolset.
const RegistryToolsetID = {{ printf "%q" .QualifiedName }}

// RegistryName is the name of the registry source.
const RegistryName = {{ printf "%q" .Registry.RegistryName }}

// ToolsetName is the name of the toolset in the registry.
const ToolsetName = {{ printf "%q" .Registry.ToolsetName }}

{{- if .Registry.Version }}

// Version is the pinned version for this toolset.
const Version = {{ printf "%q" .Registry.Version }}
{{- end }}

// Specs holds the tool specifications discovered from the registry.
// This slice is populated at runtime via DiscoverAndPopulate.
var Specs []tools.ToolSpec

var (
	specIndex = make(map[tools.Ident]*tools.ToolSpec)
	metadataIndex = make(map[tools.Ident]policy.ToolMetadata)
	metadata  []policy.ToolMetadata
	mu        sync.RWMutex
)

// DiscoverAndPopulate fetches tool schemas from the registry and populates
// the Specs slice. This function should be called during agent initialization
// before the agent starts processing requests.
//
// The function is safe to call multiple times; subsequent calls will refresh
// the cached specifications.
func DiscoverAndPopulate(ctx context.Context, client RegistryClient) error {
	toolset, err := client.GetToolset(ctx, ToolsetName)
	if err != nil {
		return fmt.Errorf("discover toolset %q from registry %q: %w", ToolsetName, RegistryName, err)
	}
	if toolset == nil {
		return fmt.Errorf("toolset %q not found in registry %q", ToolsetName, RegistryName)
	}

	mu.Lock()
	defer mu.Unlock()

	Specs = make([]tools.ToolSpec, 0, len(toolset.Tools))
	specIndex = make(map[tools.Ident]*tools.ToolSpec, len(toolset.Tools))
	metadataIndex = make(map[tools.Ident]policy.ToolMetadata, len(toolset.Tools))
	metadata = make([]policy.ToolMetadata, 0, len(toolset.Tools))

	for _, tool := range toolset.Tools {
		spec := tools.ToolSpec{
			Name:        tools.Ident(tool.Name),
			Service:     {{ printf "%q" .ServiceName }},
			Toolset:     ToolsetName,
			Description: tool.Description,
			Tags:        tool.Tags,
			Payload: tools.TypeSpec{
				Name:   tool.PayloadTypeName,
				Schema: tool.PayloadSchema,
				Codec:  tools.JSONCodec[any]{},
			},
			Result: tools.TypeSpec{
				Name:   tool.ResultTypeName,
				Schema: tool.ResultSchema,
				Codec:  tools.JSONCodec[any]{},
			},
		}
		Specs = append(Specs, spec)
		specIndex[spec.Name] = &Specs[len(Specs)-1]
		meta := policy.ToolMetadata{
			ID:          spec.Name,
			Title:       tool.Title,
			Description: tool.Description,
			Tags:        tool.Tags,
			BudgetClass: policy.ToolBudgetClassBudgeted,
		}
		metadata = append(metadata, meta)
		metadataIndex[spec.Name] = meta
	}

	return nil
}

// Names returns the identifiers of all discovered tools.
func Names() []tools.Ident {
	mu.RLock()
	defer mu.RUnlock()

	names := make([]tools.Ident, 0, len(specIndex))
	for name := range specIndex {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return string(names[i]) < string(names[j])
	})
	return names
}

// Spec returns the specification for the named tool if present.
func Spec(name tools.Ident) (*tools.ToolSpec, bool) {
	mu.RLock()
	defer mu.RUnlock()

	spec, ok := specIndex[name]
	return spec, ok
}

// PayloadSchema returns the JSON schema for the named tool payload.
func PayloadSchema(name tools.Ident) ([]byte, bool) {
	mu.RLock()
	defer mu.RUnlock()

	spec, ok := specIndex[name]
	if !ok {
		return nil, false
	}
	return spec.Payload.Schema, true
}

// ResultSchema returns the JSON schema for the named tool result.
func ResultSchema(name tools.Ident) ([]byte, bool) {
	mu.RLock()
	defer mu.RUnlock()

	spec, ok := specIndex[name]
	if !ok {
		return nil, false
	}
	return spec.Result.Schema, true
}

// Metadata exposes policy metadata for the discovered tools.
func Metadata() []policy.ToolMetadata {
	mu.RLock()
	defer mu.RUnlock()

	out := make([]policy.ToolMetadata, len(metadata))
	copy(out, metadata)
	return out
}

// MetadataByName returns policy metadata for the named tool if present.
func MetadataByName(name tools.Ident) (policy.ToolMetadata, bool) {
	mu.RLock()
	defer mu.RUnlock()

	meta, ok := metadataIndex[name]
	return meta, ok
}

// ValidatePayload validates a tool payload against its registry-provided schema.
// Returns nil if the payload is valid or if no schema is available.
func ValidatePayload(name tools.Ident, payload any) error {
	schema, ok := PayloadSchema(name)
	if !ok || len(schema) == 0 {
		return nil
	}
	return registryschema.Validate(schema, payload, "payload")
}

// ValidateResult validates a tool result against its registry-provided schema.
// Returns nil if the result is valid or if no schema is available.
func ValidateResult(name tools.Ident, result any) error {
	schema, ok := ResultSchema(name)
	if !ok || len(schema) == 0 {
		return nil
	}
	return registryschema.Validate(schema, result, "result")
}
