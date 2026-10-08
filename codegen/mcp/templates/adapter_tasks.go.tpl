{{- if .Tasks }}
// decodeTaskID accepts only a canonical tool key and native job identity. The
// operation's static dispatch selects the tool; service authorization decides
// whether this request may read, answer or cancel the identified native job.
func decodeTaskID(taskID string) (string, string, error) {
    tool, encodedID, found := strings.Cut(taskID, ".")
    if !found {
        return "", "", goa.PermanentError("invalid_params", "invalid task identifier")
    }
    id, err := base64.RawURLEncoding.DecodeString(encodedID)
    if err != nil || base64.RawURLEncoding.EncodeToString(id) != encodedID {
        return "", "", goa.PermanentError("invalid_params", "invalid task identifier")
    }
    return tool, string(id), nil
}

// validateTaskCapabilities rejects a task-only call before work starts. The
// shared MCP boundary owns capability parsing; Goa owns the typed error data.
func validateTaskCapabilities(meta json.RawMessage) error {
    if failure := mcpruntime.ValidateTaskCapabilities(meta); failure != nil {
        if failure.Code == mcpruntime.MissingRequiredClientCapability {
            return &MissingClientCapabilityError{RequiredCapabilities: &RequiredClientCapabilities{Extensions: map[string]*RequiredExtension{"io.modelcontextprotocol/tasks": {}}}}
        }
        return goa.PermanentError("invalid_params", "%s", failure.Message)
    }
    return nil
}

{{- range .Tasks }}
{{- $task := . }}
{{- range .Observations }}
// {{ .Name }}Metadata copies service-owned times and retention without
// inventing defaults. It replaces the native identifier with the opaque tool
// identity and derives the protocol status from the validated native outcome.
func {{ .Name }}Metadata(metadata {{ .MetadataRef }}, status string) *TaskCreated {
    {{ .MetadataConversion }}
    out.TaskID = {{ quote $task.Key }} + "." + base64.RawURLEncoding.EncodeToString([]byte(out.TaskID))
    if status == "complete" {
        status = "completed"
    }
    out.Status = status
    out.Meta = resultMeta()
    return out
}
{{- range .MetadataHelpers }}
// {{ .Name }} copies an authored metadata value into its protocol type.
func {{ .Name }}(v {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
    {{ .Code }}
}
{{- end }}
{{- end }}

// {{ .Prefix }}Input converts current job questions through the same typed
// form and URL plans used by native executors and registry providers.
func {{ .Prefix }}Input(pending {{ .Input.PendingRef }}, meta json.RawMessage) (*InputRequiredResult, error) {
    {{ .Input.PendingSource }}
}

// {{ .Prefix }}Answers fills only answers declared by the task contract.
// Unknown keys are ignored; the service decides which known questions remain
// outstanding and authorizes updates to the identified job.
func {{ .Prefix }}Answers(payload {{ .Answer.PayloadRef }}, inputResponses map[string]json.RawMessage) error {
    {{ .Input.AnswerSource }}
    return nil
}

// {{ .Prefix }}Observation validates the read result and exact job identity,
// then returns one full protocol state. Completed output uses the ordinary tool
// content and structured-result codecs; it does not expose job metadata.
func {{ .Prefix }}Observation(result {{ .Read.ResultRef }}, nativeID string, meta json.RawMessage) (*TasksGetResult, error) {
    value := {{ .Read.ResultValue }}
    if err := {{ .Observed.Validate }}(value); err != nil {
        return nil, goa.PermanentError("internal_error", "%s", err.Error())
    }
    if string({{ if .TaskIDPointer }}*{{ end }}value.{{ .Observed.MetadataField }}.{{ .TaskIDField }}) != nativeID {
        return nil, goa.PermanentError("internal_error", "read method returned a different task identifier")
    }
    state := {{ .Observed.Name }}Metadata(value.{{ .Observed.MetadataField }}, string(value.{{ .Observed.OutcomeField }}.Kind()))
    switch state.Status {
    case "working":
        return &TasksGetResult{Outcome: NewDetailedTaskWorking(&TaskWorkingResult{
            {{ template "task-fields" }}
        })}, nil
    case "cancelled":
        return &TasksGetResult{Outcome: NewDetailedTaskCancelled(&TaskCancelledResult{
            {{ template "task-fields" }}
        })}, nil
    case "input_required":
        pending, _ := value.{{ .Observed.OutcomeField }}.AsInputRequired()
        input, err := {{ .Prefix }}Input(pending, meta)
        if err != nil { return nil, err }
        return &TasksGetResult{Outcome: NewDetailedTaskInputRequired(&TaskInputRequiredResult{
            {{ template "task-fields" }}
            InputRequests: input.InputRequests,
        })}, nil
    case "failed":
        failed, _ := value.{{ .Observed.OutcomeField }}.AsFailed()
        failure := &TaskError{Code: int({{ if .FailureCodePointer }}*{{ end }}failed.{{ .FailureCode }}), Message: string({{ if .FailureMessagePointer }}*{{ end }}failed.{{ .FailureMessage }})}
        {{- if .FailureData }}
        if len(failed.{{ .FailureData }}) > 0 {
            raw, err := failed.{{ .FailureData }}.MarshalJSON()
            if err != nil { return nil, goa.PermanentError("internal_error", "%s", err.Error()) }
            failure.Data = json.RawMessage(raw)
        }
        {{- end }}
        return &TasksGetResult{Outcome: NewDetailedTaskFailed(&TaskFailedResult{
            {{ template "task-fields" }}
            Error: failure,
        })}, nil
    case "completed":
        {{- if not (and .Tool.ResultConversion .Read.ExecutionView) }}
        completedResult, _ := value.{{ .Observed.OutcomeField }}.AsComplete()
        {{- end }}
        {{- if .Tool.ResultConversion }}
        content, encoded, metadata, err := {{ .Tool.ResultConversion.Name }}({{ if .Read.ExecutionView }}result{{ else }}completedResult{{ end }})
        {{- else if .Tool.Codec.ResultViews }}
        var encoded []byte
        var err error
        switch result.View {
        {{- range .Tool.Codec.ResultViews }}
        case {{ quote .Name }}:
            encoded, err = {{ $.CodecPackage }}.{{ .Encode }}(completedResult)
            if err == nil {
                encoded, err = json.Marshal(struct{ Type string `json:"type"`; Value json.RawMessage `json:"value"` }{Type: {{ quote .Name }}, Value: json.RawMessage(encoded)})
            }
        {{- end }}
        default:
            return nil, goa.PermanentError("internal_error", "undeclared task result view %q", result.View)
        }
        {{- else }}
        encoded, err := {{ .Tool.Codec.ResultEncode }}(completedResult)
        {{- end }}
        if err != nil { return nil, goa.PermanentError("internal_error", "%s", err.Error()) }
        return &TasksGetResult{Outcome: NewDetailedTaskCompleted(&TaskCompletedResult{
            {{ template "task-fields" }}
            Result: &TaskToolResult{ResultType: "complete", Meta: {{ if .Tool.ResultConversion }}metadata{{ else }}resultMeta(){{ end }}, Content: {{ if .Tool.ResultConversion }}content{{ else }}[]*ContentItem{}{{ end }}, StructuredContent: json.RawMessage(encoded)},
        })}, nil
    default:
        panic("validated task observation has an undeclared status")
    }
}
{{- end }}

{{- range $index, $operation := taskOperations }}
// {{ $operation.Name }} selects the original task owner and invokes its
// configured endpoint. Native HTTP inputs are validated before authorization;
// only read returns state, while update and cancellation acknowledge acceptance.
func (a *MCPAdapter) {{ $operation.Name }}(ctx context.Context, p {{ index $.PayloadRefs $operation.Wire }}) (*{{ $operation.Result }}, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, {{ quote $operation.Wire }})
    defer span.End()
    result, err := a.{{ $operation.Invoke }}(ctx, p)
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
    }
    return result, err
}

// {{ $operation.Invoke }} decodes the task ID and dispatches its known owner.
func (a *MCPAdapter) {{ $operation.Invoke }}(ctx context.Context, p {{ index $.PayloadRefs $operation.Wire }}) (*{{ $operation.Result }}, error) {
    key, nativeID, err := decodeTaskID(p.TaskID)
    if err != nil { return nil, err }
    switch key {
    {{- range $.Tasks }}
    {{- $task := . }}
    {{- $role := index .Roles $index }}
    case {{ quote .Key }}:
        body := &{{ $role.PayloadTransportRef }}{}
        identity := {{ $role.TaskID.ValueTypeRef }}(nativeID)
        body.{{ $role.TaskID.Selector }} = {{ if $role.TaskID.Pointer }}&{{ end }}identity
        {{- if $role.Answer }}
        body.{{ $role.Responses.Selector }} = make({{ $role.Responses.TypeRef }})
        {{- end }}
        {{- range $role.Defaults }}
        {
            {{- range .Default.Declarations }}
            {{ . }}
            {{- end }}
            body.{{ .Selector }} = {{ .Default.Expression }}
        }
        {{- end }}
        payload, err := {{ $.CodecPackage }}.{{ $role.PayloadConstructor }}(body)
        if err != nil { return nil, goa.PermanentError("invalid_params", "%s", err.Error()) }
        {{- if $role.Answer }}
        if err := {{ .Prefix }}Answers(payload, p.InputResponses); err != nil { return nil, err }
        {{- end }}
        {{- if or $role.Endpoint.Credentials $role.Endpoint.Paths }}
        if err := fill{{ $role.Endpoint.CallName }}Inputs(payload{{ range $role.Endpoint.Credentials }}, p.{{ index .Sources $operation.Wire }}{{ end }}{{ range $role.Endpoint.Paths }}, p.{{ index .Sources $operation.Wire }}{{ end }}); err != nil { return nil, err }
        {{- else }}
        if err := {{ $role.Endpoint.InputValidate }}(payload); err != nil { return nil, goa.PermanentError("invalid_params", "%s", err.Error()) }
        {{- end }}
        {{ if $operation.Read }}value, err := {{ else }}err = {{ end }}a.{{ $role.Endpoint.CallName }}(ctx, payload)
        if err != nil { return nil, a.mapError(err, {{ if $role.Endpoint.FaultNames }}is{{ $role.Endpoint.CallName }}Fault{{ else }}isEndpointFault{{ end }}(err)).err }
        {{- if $operation.Read }}
        return {{ .Prefix }}Observation(value, nativeID, p.Meta)
        {{- else }}
        return &{{ $operation.Result }}{ResultType: "complete", Meta: resultMeta()}, nil
        {{- end }}
    {{- end }}
    default:
        return nil, goa.PermanentError("invalid_params", "unknown task owner")
    }
}
{{- end }}
{{- end }}

{{- define "task-fields" }}
TaskID: state.TaskID,
CreatedAt: state.CreatedAt,
LastUpdatedAt: state.LastUpdatedAt,
StatusMessage: state.StatusMessage,
TTLMs: state.TTLMs,
PollIntervalMs: state.PollIntervalMs,
ResultType: "complete",
Meta: resultMeta(),
{{- end }}
