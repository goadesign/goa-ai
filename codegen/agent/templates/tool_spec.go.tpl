// Tool IDs for this toolset.
const (
    // SchemaFingerprint identifies the exact generated schemas registered by
    // this toolset.
    SchemaFingerprint = {{ printf "%q" .SchemaFingerprint }}
{{- range .Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .Name }}
{{- end }}
)

// Specs returns fresh copies of every generated tool specification.
func Specs() []tools.ToolSpec {
    return []tools.ToolSpec{
{{- range .Tools }}
        newSpec{{ .ConstName }}(),
{{- end }}
    }
}

// RegistrationToken returns the exact registry admission token for these
// generated schemas and admissionRevision.
func RegistrationToken(admissionRevision string) (string, error) {
    return toolregistry.RegistrationToken(SchemaFingerprint, admissionRevision)
}

{{- range .Tools }}
// newSpec{{ .ConstName }} builds the immutable generated contract for
// {{ .Name }}. Each call owns all mutable slices and maps in the result.
func newSpec{{ .ConstName }}() tools.ToolSpec {
    return tools.ToolSpec{
        Name:        {{ .ConstName }},
        Service:     {{ printf "%q" .Service }},
        Toolset:     {{ printf "%q" .Toolset }},
        Description: {{ printf "%q" .Description }},
        Tags: []string{
        {{- range .Tags }}
            {{ printf "%q" . }},
        {{- end }}
        },
        {{- if .MetaPairs }}
        Meta: map[string][]string{
        {{- range .MetaPairs }}
            {{ printf "%q" .Key }}: []string{
            {{- range .Values }}
                {{ printf "%q" . }},
            {{- end }}
            },
        {{- end }}
        },
        {{- end }}
        {{- if .IsExportedByAgent }}
        IsAgentTool: true,
        AgentID:     {{ printf "%q" .ExportingAgentID }},
        {{- end }}
        {{- if .TerminalRun }}
        TerminalRun: true,
        {{- end }}
        {{- if .Bookkeeping }}
        Bookkeeping: true,
        {{- end }}
        {{- if .Bounds }}
        Bounds: &tools.BoundsSpec{
            {{- if .Bounds.Paging }}
            Paging: &tools.PagingSpec{
                ContinueTool: tools.Ident({{ printf "%q" .Bounds.Paging.ContinueTool }}),
                SourceTool: tools.Ident({{ printf "%q" .Bounds.Paging.SourceTool }}),
                ReplayPayload: {{ .Bounds.Paging.ReplayPayload }},
                CursorField: {{ printf "%q" .Bounds.Paging.CursorField }},
                NextCursorField: {{ printf "%q" .Bounds.Paging.NextCursorField }},
            },
            {{- end }}
        },
        {{- end }}
        {{- if .ServerData }}
        ServerData: []*tools.ServerDataSpec{
        {{- range .ServerData }}
            {
                Kind: {{ printf "%q" .Kind }},
                Audience: tools.ServerDataAudience({{ printf "%q" .Audience }}),
                Description: {{ printf "%q" .Description }},
                Type: tools.TypeSpec{
                    Name: {{ if .Type }}{{ printf "%q" .Type.TypeName }}{{ else }}""{{ end }},
                    {{- if .Type }}
                    Schema: {{- if gt (len .Type.SchemaJSON) 0 }}tools.RawJSON({{ printf "%q" .Type.SchemaJSON }}){{ else }}nil{{ end }},
                    SchemaWithoutRootExample: {{- if gt (len .Type.SchemaWithoutRootExampleJSON) 0 }}tools.RawJSON({{ printf "%q" .Type.SchemaWithoutRootExampleJSON }}){{ else }}nil{{ end }},
                    ExampleJSON: {{- if gt (len .Type.ExampleJSON) 0 }}tools.RawJSON({{ printf "%q" .Type.ExampleJSON }}){{ else }}nil{{ end }},
                    FieldDescriptions: {{- if .Type.FieldDescs }}cloneStringMap({{ goify .Type.TypeName false }}FieldDescs){{ else }}nil{{ end }},
                    FieldJSONTypes: {{- if .Type.FieldJSONTypes }}cloneStringMap({{ goify .Type.TypeName false }}FieldJSONTypes){{ else }}nil{{ end }},
                    Codec: {{ .Type.GenericCodec }},
                    {{- else }}
                    Schema: nil,
                    SchemaWithoutRootExample: nil,
                    ExampleJSON: nil,
                    FieldDescriptions: nil,
                    FieldJSONTypes: nil,
                    Codec: tools.JSONCodec[any]{},
                    {{- end }}
                },
            },
        {{- end }}
        },
        CanonicalizeServerData: canonicalize{{ .GoName }}ServerData,
        {{- end }}
        {{- if .ResultReminder }}
        ResultReminder: {{ printf "%q" .ResultReminder }},
        {{- end }}
        {{- if .Confirmation }}
        Confirmation: &tools.ConfirmationSpec{
            Title: {{ printf "%q" .Confirmation.Title }},
            PromptTemplate: {{ printf "%q" .Confirmation.PromptTemplate }},
            DeniedResultTemplate: {{ printf "%q" .Confirmation.DeniedResultTemplate }},
        },
        {{- end }}
        Payload: tools.TypeSpec{
            Name: {{ if .Payload }}{{ printf "%q" .Payload.TypeName }}{{ else }}""{{ end }},
            {{- if .Payload }}
            Schema: {{- if gt (len .Payload.SchemaJSON) 0 }}tools.RawJSON({{ printf "%q" .Payload.SchemaJSON }}){{ else }}nil{{ end }},
            SchemaWithoutRootExample: {{- if gt (len .Payload.SchemaWithoutRootExampleJSON) 0 }}tools.RawJSON({{ printf "%q" .Payload.SchemaWithoutRootExampleJSON }}){{ else }}nil{{ end }},
            ExampleJSON: {{- if gt (len .Payload.ExampleJSON) 0 }}tools.RawJSON({{ printf "%q" .Payload.ExampleJSON }}){{ else }}nil{{ end }},
            FieldDescriptions: {{- if .Payload.FieldDescs }}cloneStringMap({{ goify .Payload.TypeName false }}FieldDescs){{ else }}nil{{ end }},
            FieldJSONTypes: {{- if .Payload.FieldJSONTypes }}cloneStringMap({{ goify .Payload.TypeName false }}FieldJSONTypes){{ else }}nil{{ end }},
            Codec:  {{ .Payload.GenericCodec }},
            {{- else }}
            Schema: nil,
            SchemaWithoutRootExample: nil,
            ExampleJSON: nil,
            FieldDescriptions: nil,
            FieldJSONTypes: nil,
            Codec:  tools.JSONCodec[any]{},
            {{- end }}
        },
        Result: tools.TypeSpec{
            Name: {{ if .Result }}{{ printf "%q" .Result.TypeName }}{{ else }}""{{ end }},
            Schema: {{- if and .Result (gt (len .Result.SchemaJSON) 0) }}tools.RawJSON({{ printf "%q" .Result.SchemaJSON }}){{ else }}nil{{ end }},
            {{- if .Result }}
            SchemaWithoutRootExample: {{- if gt (len .Result.SchemaWithoutRootExampleJSON) 0 }}tools.RawJSON({{ printf "%q" .Result.SchemaWithoutRootExampleJSON }}){{ else }}nil{{ end }},
            FieldDescriptions: {{- if .Result.FieldDescs }}cloneStringMap({{ goify .Result.TypeName false }}FieldDescs){{ else }}nil{{ end }},
            FieldJSONTypes: {{- if .Result.FieldJSONTypes }}cloneStringMap({{ goify .Result.TypeName false }}FieldJSONTypes){{ else }}nil{{ end }},
            Codec:  {{ .Result.GenericCodec }},
            {{- else }}
            SchemaWithoutRootExample: nil,
            FieldDescriptions: nil,
            FieldJSONTypes: nil,
            Codec:  tools.JSONCodec[any]{},
            {{- end }}
        },
    }
}

// Spec{{ .ConstName }} returns a fresh {{ .Name }} specification.
func Spec{{ .ConstName }}() tools.ToolSpec {
    return newSpec{{ .ConstName }}()
}
{{- end }}

{{- range .Tools }}
{{- if .TypedToolVar }}

// {{ .TypedToolVar }} pairs the {{ .Name }} identifier with its generated
// typed payload and result codecs so consumers decode tool JSON without
// restating the name-to-codec pairing fixed by the design.
func {{ .TypedToolVar }}() tools.TypedTool[{{ if .Payload.Pointer }}*{{ end }}{{ .Payload.FullRef }}, {{ if .Result }}{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}{{ else }}any{{ end }}] {
    return tools.TypedTool[{{ if .Payload.Pointer }}*{{ end }}{{ .Payload.FullRef }}, {{ if .Result }}{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}{{ else }}any{{ end }}]{
        Name:    {{ .ConstName }},
        Payload: tools.JSONCodec[{{ if .Payload.Pointer }}*{{ end }}{{ .Payload.FullRef }}]{
            ToJSON:   {{ .Payload.MarshalFunc }},
            FromJSON: {{ .Payload.UnmarshalFunc }},
        },
        {{- if .Result }}
        Result: tools.JSONCodec[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}]{
            ToJSON:   {{ .Result.MarshalFunc }},
            FromJSON: {{ .Result.UnmarshalFunc }},
        },
        {{- else }}
        Result:  tools.JSONCodec[any]{},
        {{- end }}
    }
}
{{- end }}
{{- end }}

{{- range .Tools }}
{{- if .ServerData }}

// canonicalize{{ .GoName }}ServerData validates the server-only payloads
// declared by {{ .Name }} and returns their canonical envelope.
func canonicalize{{ .GoName }}ServerData(data tools.RawJSON) (tools.RawJSON, error) {
    return toolserverdata.Canonicalize(data, canonicalize{{ .GoName }}ServerDataItem)
}

// canonicalize{{ .GoName }}ServerDataItem validates one kind-specific payload
// and returns the audience and JSON declared by {{ .Name }}.
func canonicalize{{ .GoName }}ServerDataItem(kind, audience string, data tools.RawJSON) (string, tools.RawJSON, error) {
    switch kind {
    {{- range .ServerData }}
    case {{ printf "%q" .Kind }}:
        if audience != {{ printf "%q" .Audience }} {
            return "", nil, fmt.Errorf("server data kind %q has audience %q; expected %q", kind, audience, {{ printf "%q" .Audience }})
        }
        value, err := {{ .Type.GenericCodec }}.FromJSON(data)
        if err != nil {
            return "", nil, fmt.Errorf("decode server data kind %q: %w", kind, err)
        }
        canonical, err := {{ .Type.GenericCodec }}.ToJSON(value)
        if err != nil {
            return "", nil, fmt.Errorf("encode server data kind %q: %w", kind, err)
        }
        return {{ printf "%q" .Audience }}, canonical, nil
    {{- end }}
    default:
        return "", nil, fmt.Errorf("server data kind %q is not declared by tool {{ .Name }}", kind)
    }
}
{{- end }}
{{- end }}

// Metadata returns fresh policy metadata for every generated tool.
func Metadata() []policy.ToolMetadata {
    return []policy.ToolMetadata{
{{- range .Tools }}
        {
            ID:          {{ .ConstName }},
            Title:       {{ printf "%q" .Title }},
            Description: {{ printf "%q" .Description }},
            Tags: []string{
            {{- range .Tags }}
                {{ printf "%q" . }},
            {{- end }}
            },
            BudgetClass: policy.ToolBudgetClass{{ if .Bookkeeping }}Bookkeeping{{ else }}Budgeted{{ end }},
        },
{{- end }}
    }
}

// Names returns the identifiers of all generated tools.
func Names() []tools.Ident {
    return []tools.Ident{
{{- range .Tools }}
        {{ .ConstName }},
{{- end }}
    }
}

// RequiredLabels lists the run label keys this toolset's Inject-populated
// tools require to be present via WithLabels(...) at run start. The runtime
// validates coverage across every toolset an agent uses before starting a
// run, so a missing label fails fast instead of surfacing mid-run as a tool
// call error.
func RequiredLabels() []string {
    return []string{
{{- range .RequiredLabels }}
        {{ printf "%q" . }},
{{- end }}
    }
}

// Spec returns the specification for the named tool if present.
func Spec(name tools.Ident) (tools.ToolSpec, bool) {
    switch name {
    {{- range .Tools }}
    case {{ .ConstName }}:
        return newSpec{{ .ConstName }}(), true
    {{- end }}
    default:
        return tools.ToolSpec{}, false
    }
}

// MetadataByName returns policy metadata for the named tool if present.
func MetadataByName(name tools.Ident) (policy.ToolMetadata, bool) {
    switch name {
    {{- range $tool := .Tools }}
    case {{ $tool.ConstName }}:
        return policy.ToolMetadata{
            ID:          {{ $tool.ConstName }},
            Title:       {{ printf "%q" $tool.Title }},
            Description: {{ printf "%q" $tool.Description }},
            Tags: []string{
            {{- range $tool.Tags }}
                {{ printf "%q" . }},
            {{- end }}
            },
            BudgetClass: policy.ToolBudgetClass{{ if $tool.Bookkeeping }}Bookkeeping{{ else }}Budgeted{{ end }},
        }, true
    {{- end }}
    default:
        return policy.ToolMetadata{}, false
    }
}

// cloneStringMap gives each returned specification ownership of its generated
// field metadata.
func cloneStringMap(source map[string]string) map[string]string {
    if source == nil {
        return nil
    }
    cloned := make(map[string]string, len(source))
    for key, value := range source {
        cloned[key] = value
    }
    return cloned
}
