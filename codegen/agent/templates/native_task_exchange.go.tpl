{{- define "native-task-poll" }}
{{- if .PollField }}
 var pollIntervalMs *int64
 {{- if .PollPointer }}
 if result.{{ .MetadataField }}.{{ .PollField }} != nil {
  value := int64(*result.{{ .MetadataField }}.{{ .PollField }})
  pollIntervalMs = &value
 }
 {{- else }}
 value := int64(result.{{ .MetadataField }}.{{ .PollField }})
 pollIntervalMs = &value
 {{- end }}
{{- end }}
{{- end }}
// {{ .Created }} validates the creator's first job observation and returns an
// unfinished job handle. Even a job that finished immediately is read through
// its declared observation method, so the creator's effects are never repeated.
func {{ .Created }}(result {{ .ObservationRef }}) (*{{ .API }}.PendingExecution, error) {
 if err := {{ .ObservationValidate }}(result); err != nil { return nil, err }
 {{ template "native-task-poll" . }}
 return {{ .API }}.NewPendingTaskWait(string(result.{{ .MetadataField }}.{{ .TaskIDField }}), {{ if .PollField }}pollIntervalMs{{ else }}nil{{ end }})
}

// {{ .Read }} validates one existing job's exact identity and state. It returns
// completed domain output, another unfinished operation, or the job's terminal
// error. Metadata and host input never enter the completed model result.
func {{ .Read }}(result {{ .ObservationRef }}, taskID string) ({{ .CompletedRef }}, *{{ .API }}.PendingExecution, error) {
 var completed {{ .CompletedRef }}
 if err := {{ .ObservationValidate }}(result); err != nil { return completed, nil, err }
 if string(result.{{ .MetadataField }}.{{ .TaskIDField }}) != taskID {
  return completed, nil, goa.PermanentError("invalid_result", "job observation changed its identifier")
 }
 {{ template "native-task-poll" . }}
 if value, ok := result.{{ .OutcomeField }}.AsComplete(); ok { return value, nil, nil }
 if input, ok := result.{{ .OutcomeField }}.AsInputRequired(); ok {
  questions, err := {{ .Pending }}(input)
  if err != nil { return completed, nil, err }
  pending, err := {{ .API }}.NewPendingTaskInput(taskID, {{ if .PollField }}pollIntervalMs{{ else }}nil{{ end }}, questions)
  return completed, pending, err
 }
 if failure, ok := result.{{ .OutcomeField }}.AsFailed(); ok {
  {{- if .FailureData }}
  var data json.RawMessage
  if failure.{{ .FailureData }} != nil {
   encoded, err := failure.{{ .FailureData }}.MarshalJSON()
   if err != nil { return completed, nil, err }
   data = json.RawMessage(encoded)
  }
  {{- end }}
  return completed, nil, &{{ .MCP }}.Error{Code: int(failure.{{ .FailureCode }}), Message: string(failure.{{ .FailureMessage }}){{ if .FailureData }}, Data: data{{ end }}}
 }
 if _, ok := result.{{ .OutcomeField }}.AsCancelled(); ok { return completed, nil, context.Canceled }
 // Full observation validation leaves only the working state here.
 pending, err := {{ .API }}.NewPendingTaskWait(taskID, {{ if .PollField }}pollIntervalMs{{ else }}nil{{ end }})
 return completed, pending, err
}

// {{ .Fill }} decodes host answers into the update method's authored map and
// validates its complete native payload. The service checks each submitted
// question identity and kind against the outstanding questions of that job.
func {{ .Fill }}(payload {{ .AnswerRef }}, taskID string, inputResponses map[string]json.RawMessage) error {
 payload.{{ .AnswerTaskIDField }} = {{ .AnswerTaskIDRef }}(taskID)
 {{ .Input.AnswerSource }}
 return {{ .AnswerValidate }}(payload)
}

// {{ .Pending }} returns typed host questions with opaque keys that preserve
// each native question identity and kind. The job owns how long those keys live.
func {{ .Pending }}(pending {{ .Input.PendingRef }}) (*{{ .MCP }}.InputRequired, error) {
 {{ .Input.PendingSource }}
}

{{- range .Roles }}
// {{ .Prepare }} copies the creator's shared context into the {{ .MethodName }}
// input and applies defaults for fields the creator does not supply. Application interceptors can fill additional context before the final
// filler assigns the saved job identity and validates the method input.
func {{ .Prepare }}({{ if .HasSource }}in {{ .SourceRef }}{{ end }}) {{ .PayloadRef }} {
 {{- if .Body }}
 var out {{ .PayloadRef }}
 {{ .Body }}
 {{- else }}
 out := &{{ .PayloadValueRef }}{}
 {{- end }}
 {{ .Defaults }}
 return out
}
{{- if not .Answer }}
// {{ .Fill }} assigns the workflow-owned job identifier after application input
// injection, then validates the complete {{ .MethodName }} payload.
func {{ .Fill }}(payload {{ .PayloadRef }}, taskID string) error {
 payload.{{ .TaskIDField }} = {{ .TaskIDRef }}(taskID)
 return {{ .Validate }}(payload)
}
{{- end }}
{{- end }}
