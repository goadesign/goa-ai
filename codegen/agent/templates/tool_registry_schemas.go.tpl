// ToolSchemas returns complete registry declarations generated from this
// toolset's design. Each call owns its schemas, metadata, and optional values.
func ToolSchemas() []*genregistry.ToolSchema {
{{- range .Strings }}
    {{ .Name }} := {{ printf "%q" .Value }}
{{- end }}
    return []*genregistry.ToolSchema{
{{- range .Schemas }}
        {
            Name: {{ printf "%q" .Name }},
            Description: {{ pointer .Description }},
            {{- if .Tags }}
            Tags: []string{ {{ range .Tags }}{{ printf "%q" . }}, {{ end }} },
            {{- end }}
            PayloadSchema: []byte({{ printf "%q" .PayloadSchema }}),
            ExecutionPayloadSchema: []byte({{ printf "%q" .ExecutionPayloadSchema }}),
            ResultSchema: []byte({{ printf "%q" .ResultSchema }}),
            ConsumerContract: {{ template "consumer" .ConsumerContract }},
        },
{{- end }}
    }
}

{{- define "consumer" -}}
&genregistry.ConsumerContract{
    Kind: {{ printf "%q" .Kind }},
    Title: {{ printf "%q" .Title }},
    Search: &genregistry.ToolSearchDocument{
        Length: {{ .Search.Length }},
        Terms: map[string]int{ {{ range $word, $count := .Search.Terms }}{{ printf "%q" $word }}: {{ $count }}, {{ end }} },
    },
    Payload: {{ template "type" .Payload }},
    {{- if .Result }}
    Result: {{ template "type" .Result }},
    {{- end }}
    {{- if .Meta }}
    Meta: map[string][]string{
    {{- range $key, $values := .Meta }}
        {{ printf "%q" $key }}: { {{ range $values }}{{ printf "%q" . }}, {{ end }} },
    {{- end }}
    },
    {{- end }}
    {{- if .RequiredLabels }}
    RequiredLabels: []string{ {{ range .RequiredLabels }}{{ printf "%q" . }}, {{ end }} },
    {{- end }}
    {{- if .Bounds }}
    Bounds: &genregistry.ToolBounds{
        {{- with .Bounds.Paging }}
        Paging: &genregistry.ToolPaging{
            {{- if .ContinueTool }}
            ContinueTool: {{ pointer .ContinueTool }},
            {{- end }}
            {{- if .SourceTool }}
            SourceTool: {{ pointer .SourceTool }},
            {{- end }}
            ReplayPayload: {{ .ReplayPayload }},
            CursorField: {{ printf "%q" .CursorField }},
            NextCursorField: {{ printf "%q" .NextCursorField }},
        },
        {{- end }}
    },
    {{- end }}
    {{- with .Confirmation }}
    Confirmation: &genregistry.ToolConfirmation{
        {{- if .Title }}
        Title: {{ pointer .Title }},
        {{- end }}
        PromptTemplate: {{ printf "%q" .PromptTemplate }},
        DeniedResultTemplate: {{ printf "%q" .DeniedResultTemplate }},
    },
    {{- end }}
    {{- if .ServerData }}
    ServerData: []*genregistry.ToolServerData{
    {{- range .ServerData }}
        {
            Kind: {{ printf "%q" .Kind }},
            Audience: {{ printf "%q" .Audience }},
            {{- if .Description }}
            Description: {{ pointer .Description }},
            {{- end }}
            Schema: []byte({{ printf "%q" .Schema }}),
            Type: {{ template "type" .Type }},
        },
    {{- end }}
    },
    {{- end }}
    {{- if .ResultReminder }}
    ResultReminder: {{ pointer .ResultReminder }},
    {{- end }}
}
{{- end }}

{{- define "type" -}}
&genregistry.ToolTypeMetadata{
    {{- if .Name }}
    Name: {{ pointer .Name }},
    {{- end }}
    SchemaWithoutRootExample: []byte({{ printf "%q" .SchemaWithoutRootExample }}),
    {{- if .ExampleJSON }}
    ExampleJSON: []byte({{ printf "%q" .ExampleJSON }}),
    {{- end }}
    {{- if .Fields }}
    Fields: []*genregistry.ToolFieldMetadata{
    {{- range .Fields }}
        {
            {{- if .Path }}
            Path: {{ template "path" .Path }},
            {{- end }}
            {{- if .JSONType }}
            JSONType: {{ pointer .JSONType }},
            {{- end }}
            {{- if .Description }}
            Description: {{ pointer .Description }},
            {{- end }}
            {{- if .DiscriminatorValues }}
            DiscriminatorValues: []string{ {{ range .DiscriminatorValues }}{{ printf "%q" . }}, {{ end }} },
            {{- end }}
            {{- if .Branches }}
            Branches: []*genregistry.ToolUnionBranch{
            {{- range .Branches }}
                {
                    Discriminator: {{ template "path" .Discriminator }},
                    Value: {{ printf "%q" .Value }},
                },
            {{- end }}
            },
            {{- end }}
        },
    {{- end }}
    },
    {{- end }}
}
{{- end }}

{{- define "path" -}}
[]*genregistry.ToolFieldPathSegment{
{{- range . }}
    {Segment: {{ segment .Segment }}},
{{- end }}
}
{{- end }}
