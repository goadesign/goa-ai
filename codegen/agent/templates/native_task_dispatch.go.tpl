{{- define "task-failure" }}
{{- if .Registry }}
return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "execution_failed", err.Error()), nil
{{- else }}
return runtime.Executed({{ .Failure }}(call.Name, err)), nil
{{- end }}
{{- end }}
{{- define "task-pending" }}
{{- if .Registry }}
return toolregistry.ToolResultMessage{RegistrationToken:msg.RegistrationToken, ToolUseID:msg.ToolUseID, PendingExecution:pending}, nil
{{- else }}
return runtime.Unfinished(pending)
{{- end }}
{{- end }}
{{- define "task-inject" }}
{{- if not .Registry }}
for _, injector := range cfg.injectors {
 if err := injector.Inject(ctx, roleIn, meta); err != nil { {{ template "task-failure" . }} }
}
{{- end }}
{{- end }}
{{- if .Registry }}
var methodOut {{ .Tool.Task.CompletedRef }}
{{- else }}
var methodOut any
{{- end }}
{{- if and (not .Registry) .Tool.HasMethodPayload (not .Tool.FillInputContinuation) }}
nativePayload, ok := methodIn.({{ .Tool.MethodPayloadTypeRef }})
if !ok {
 err := fmt.Errorf("unexpected creator payload type: %T", methodIn)
 {{ template "task-failure" . }}
}
{{- end }}
if {{ .Continuation }} != nil{{ if .Tool.FillInputContinuation }} && inputContinuation == nil{{ end }} {
 if taskID, query := {{ .Continuation }}.AsTaskGet(); query {
  {{- with index .Tool.Task.Roles 0 }}
  roleIn := {{ $.Specs }}{{ .Prepare }}({{ if .HasSource }}{{ if $.Registry }}methodIn{{ else }}nativePayload{{ end }}{{ end }})
  {{ template "task-inject" $ }}
  if err := {{ $.Specs }}{{ .Fill }}(roleIn, taskID); err != nil { {{ template "task-failure" $ }} }
  {{- if $.Registry }}
  observation, {{ if .ReturnsView }}_, {{ end }}err := p.svc.{{ .MethodName }}(ctx, roleIn)
  {{- else }}
  rawObservation, err := cfg.{{ .CallerField }}(ctx, roleIn)
  if err != nil { {{ template "task-failure" $ }} }
  observation, ok := rawObservation.({{ $.Tool.Task.ObservationRef }})
  if !ok { err := fmt.Errorf("unexpected job observation type: %T", rawObservation); {{ template "task-failure" $ }} }
  {{- end }}
  {{- end }}
  if err != nil { {{ template "task-failure" . }} }
  completed, pending, err := {{ .Specs }}{{ .Tool.Task.Read }}(observation, taskID)
  if err != nil { {{ template "task-failure" . }} }
  if pending != nil {
   if _, _, _, requiresInput := pending.AsTaskInput(); requiresInput && {{ .TextOnly }} {
    err := errors.New("text-only tool returned required host input")
    {{ template "task-failure" . }}
   }
   {{ template "task-pending" . }}
  }
  methodOut = completed
 } else if taskID, answers, update := {{ .Continuation }}.AsTaskUpdate(); update {
  {{- with index .Tool.Task.Roles 1 }}
  roleIn := {{ $.Specs }}{{ .Prepare }}({{ if .HasSource }}{{ if $.Registry }}methodIn{{ else }}nativePayload{{ end }}{{ end }})
  {{ template "task-inject" $ }}
  if err := {{ $.Specs }}{{ .Fill }}(roleIn, taskID, answers); err != nil { {{ template "task-failure" $ }} }
  {{- if $.Registry }}
  err := p.svc.{{ .MethodName }}(ctx, roleIn)
  {{- else }}
  _, err := cfg.{{ .CallerField }}(ctx, roleIn)
  {{- end }}
  {{- end }}
  if err != nil { {{ template "task-failure" . }} }
  pending, err := {{ .Tool.Task.API }}.NewPendingTaskWait(taskID, nil)
  if err != nil { {{ template "task-failure" . }} }
  {{ template "task-pending" . }}
 } else if taskID, cancel := {{ .Continuation }}.AsTaskCancel(); cancel {
  {{- with index .Tool.Task.Roles 2 }}
  roleIn := {{ $.Specs }}{{ .Prepare }}({{ if .HasSource }}{{ if $.Registry }}methodIn{{ else }}nativePayload{{ end }}{{ end }})
  {{ template "task-inject" $ }}
  if err := {{ $.Specs }}{{ .Fill }}(roleIn, taskID); err != nil { {{ template "task-failure" $ }} }
  {{- if $.Registry }}
  err := p.svc.{{ .MethodName }}(ctx, roleIn)
  {{- else }}
  _, err := cfg.{{ .CallerField }}(ctx, roleIn)
  {{- end }}
  {{- end }}
  if err != nil { {{ template "task-failure" . }} }
  pending, err := {{ .Tool.Task.API }}.NewPendingTaskWait(taskID, nil)
  if err != nil { {{ template "task-failure" . }} }
  {{ template "task-pending" . }}
 } else {
  err := errors.New("tool does not accept this continuation after job creation")
  {{ template "task-failure" . }}
 }
} else {
 {{- if .Registry }}
 nativeResult, {{ if .Tool.MethodReturnsView }}_, {{ end }}err := p.svc.{{ .Tool.MethodGoName }}(ctx{{ if .Tool.HasMethodPayload }}, methodIn{{ end }})
 {{- else }}
 rawResult, err := cfg.{{ .CreatorCaller }}(ctx, methodIn)
 if err != nil { {{ template "task-failure" . }} }
 nativeResult, ok := rawResult.({{ .Tool.NativeResultTypeRef }})
 if !ok { err := fmt.Errorf("unexpected creator result type: %T", rawResult); {{ template "task-failure" . }} }
 {{- end }}
 if err != nil { {{ template "task-failure" . }} }
 {{- if .Tool.ReadInputOutcome }}
 observation, input, err := {{ .Specs }}{{ .Tool.ReadInputOutcome }}(nativeResult)
 if err != nil { {{ template "task-failure" . }} }
 if input != nil {
  if {{ .TextOnly }} { err := errors.New("text-only tool returned required host input"); {{ template "task-failure" . }} }
  {{- if .Registry }}
  return toolregistry.NewInputRequiredResult(msg.RegistrationToken, msg.ToolUseID, input)
  {{- else }}
  return runtime.AwaitMCPInput(input)
  {{- end }}
 }
 pending, err := {{ .Specs }}{{ .Tool.Task.Created }}(observation)
 {{- else }}
 pending, err := {{ .Specs }}{{ .Tool.Task.Created }}(nativeResult)
 {{- end }}
 if err != nil { {{ template "task-failure" . }} }
 {{ template "task-pending" . }}
}
