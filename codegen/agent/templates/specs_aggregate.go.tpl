var (
    // Specs aggregates tool specs from all toolset packages.
    Specs = make([]tools.ToolSpec, 0)
    // metadata aggregates tool metadata from all toolset packages.
    metadata = make([]policy.ToolMetadata, 0)
)

func init() {
{{- range .Toolsets }}
    // {{ .Name }} toolset
    Specs = append(Specs, {{ .SpecsPackageName }}.Specs...)
    metadata = append(metadata, {{ .SpecsPackageName }}.Metadata()...)
{{- end }}
    sort.Slice(Specs, func(i, j int) bool {
        return string(Specs[i].Name) < string(Specs[j].Name)
    })
}

// Names returns sorted tool identifiers for all aggregated toolsets.
func Names() []tools.Ident {
    names := make([]tools.Ident, 0, len(Specs))
    for _, s := range Specs {
        names = append(names, s.Name)
    }
    // Sort using string values for stability
    strs := make([]string, len(names))
    for i, n := range names { strs[i] = string(n) }
    sort.Strings(strs)
    out := make([]tools.Ident, len(strs))
    for i, s := range strs { out[i] = tools.Ident(s) }
    return out
}

// Spec returns the specification for the named tool if present.
func Spec(name tools.Ident) (*tools.ToolSpec, bool) {
    for i := range Specs {
        if Specs[i].Name == name {
            return &Specs[i], true
        }
    }
    return nil, false
}

// PayloadSchema returns the JSON schema for the named tool payload.
func PayloadSchema(name tools.Ident) ([]byte, bool) {
    if s, ok := Spec(name); ok {
        return s.Payload.Schema, true
    }
    return nil, false
}

// ResultSchema returns the JSON schema for the named tool result.
func ResultSchema(name tools.Ident) ([]byte, bool) {
    if s, ok := Spec(name); ok {
        return s.Result.Schema, true
    }
    return nil, false
}

// Metadata exposes policy metadata for the aggregated tools.
func Metadata() []policy.ToolMetadata {
    out := make([]policy.ToolMetadata, len(metadata))
    copy(out, metadata)
    return out
}
