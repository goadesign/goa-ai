// Tool IDs for this toolset.
const (
{{- range .Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .Name }}
{{- end }}
)

var Specs = []tools.ToolSpec{
{{- range .Tools }}
    {{ .SpecVar }},
{{- end }}
}

var (
{{- range .Tools }}
    {{ .SpecVar }} = tools.ToolSpec{
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
                    FieldDescriptions: {{- if .Type.FieldDescs }}{{ .Type.TypeName }}FieldDescs{{ else }}nil{{ end }},
                    FieldJSONTypes: {{- if .Type.FieldJSONTypes }}{{ .Type.TypeName }}FieldJSONTypes{{ else }}nil{{ end }},
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
        CanonicalizeServerData: {{ .CanonicalizeServerDataFunc }},
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
            FieldDescriptions: {{- if .Payload.FieldDescs }}{{ .Payload.TypeName }}FieldDescs{{ else }}nil{{ end }},
            FieldJSONTypes: {{- if .Payload.FieldJSONTypes }}{{ .Payload.TypeName }}FieldJSONTypes{{ else }}nil{{ end }},
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
            FieldDescriptions: {{- if .Result.FieldDescs }}{{ .Result.TypeName }}FieldDescs{{ else }}nil{{ end }},
            FieldJSONTypes: {{- if .Result.FieldJSONTypes }}{{ .Result.TypeName }}FieldJSONTypes{{ else }}nil{{ end }},
            Codec:  {{ .Result.GenericCodec }},
            {{- else }}
            SchemaWithoutRootExample: nil,
            FieldDescriptions: nil,
            FieldJSONTypes: nil,
            Codec:  tools.JSONCodec[any]{},
            {{- end }}
        },
    }
{{- end }}
)

{{- range .Tools }}
{{- if .TypedToolVar }}

// {{ .TypedToolVar }} pairs the {{ .Name }} identifier with its generated
// typed payload and result codecs so consumers decode tool JSON without
// restating the name-to-codec pairing fixed by the design.
var {{ .TypedToolVar }} = tools.TypedTool[{{ if .Payload.Pointer }}*{{ end }}{{ .Payload.FullRef }}, {{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}]{
    Name:    {{ .ConstName }},
    Payload: {{ .Payload.ExportedCodec }},
    Result:  {{ .Result.ExportedCodec }},
}
{{- end }}
{{- end }}

{{- range .Tools }}
{{- if .ServerData }}

// canonicalize{{ .GoName }}ServerData validates the server-only payloads
// declared by {{ .Name }} and returns their canonical envelope.
func {{ .CanonicalizeServerDataFunc }}(data tools.RawJSON) (tools.RawJSON, error) {
    return toolserverdata.Canonicalize(data, {{ .CanonicalizeServerDataItemFunc }})
}

// canonicalize{{ .GoName }}ServerDataItem validates one kind-specific payload
// and returns the audience and JSON declared by {{ .Name }}.
func {{ .CanonicalizeServerDataItemFunc }}(kind, audience string, data tools.RawJSON) (string, tools.RawJSON, error) {
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

var (
    metadata   = []policy.ToolMetadata{
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
    names = []tools.Ident{
    {{- range .Tools }}
        {{ .ConstName }},
    {{- end }}
    }
)

// RequiredLabels lists the run label keys this toolset's Inject-populated
// tools require to be present via WithLabels(...) at run start. The runtime
// validates coverage across every toolset an agent uses before starting a
// run, so a missing label fails fast instead of surfacing mid-run as a tool
// call error.
var RequiredLabels = []string{
{{- range .RequiredLabels }}
    {{ printf "%q" . }},
{{- end }}
}

// Names returns the identifiers of all generated tools.
func Names() []tools.Ident {
    return names
}

// Spec returns the specification for the named tool if present.
func Spec(name tools.Ident) (*tools.ToolSpec, bool) {
    switch name {
    {{- range .Tools }}
    case {{ .ConstName }}:
        return &{{ .SpecVar }}, true
    {{- end }}
    default:
        return nil, false
    }
}

// PayloadSchema returns the JSON schema for the named tool payload.
func PayloadSchema(name tools.Ident) ([]byte, bool) {
    switch name {
    {{- range .Tools }}
    case {{ .ConstName }}:
        return {{ .SpecVar }}.Payload.Schema, true
    {{- end }}
    default:
        return nil, false
    }
}

// ResultSchema returns the JSON schema for the named tool result.
func ResultSchema(name tools.Ident) ([]byte, bool) {
    switch name {
    {{- range .Tools }}
    case {{ .ConstName }}:
        return {{ .SpecVar }}.Result.Schema, true
    {{- end }}
    default:
        return nil, false
    }
}

// Metadata exposes policy metadata for the generated tools.
func Metadata() []policy.ToolMetadata {
    return metadata
}

// MetadataByName returns policy metadata for the named tool if present.
func MetadataByName(name tools.Ident) (policy.ToolMetadata, bool) {
    switch name {
    {{- range $i, $tool := .Tools }}
    case {{ $tool.ConstName }}:
        return metadata[{{ $i }}], true
    {{- end }}
    default:
        return policy.ToolMetadata{}, false
    }
}
