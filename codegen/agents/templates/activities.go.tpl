{{- define "activityOptions" -}}
{{- $hasRetry := or (gt .RetryPolicy.MaxAttempts 0) (gt .RetryPolicy.InitialInterval 0) (ne .RetryPolicy.BackoffCoefficient 0.0) -}}
{{- if or (ne .Queue "") (gt .Timeout 0) $hasRetry }}
    Options: engine.ActivityOptions{
{{- if ne .Queue "" }}
        Queue: {{ printf "%q" .Queue }},
{{- end }}
{{- if gt .Timeout 0 }}
        Timeout: time.Duration({{ printf "%d" .Timeout }}),
{{- end }}
{{- if $hasRetry }}
        RetryPolicy: engine.RetryPolicy{
{{- if gt .RetryPolicy.MaxAttempts 0 }}
            MaxAttempts: {{ .RetryPolicy.MaxAttempts }},
{{- end }}
{{- if gt .RetryPolicy.InitialInterval 0 }}
            InitialInterval: time.Duration({{ printf "%d" .RetryPolicy.InitialInterval }}),
{{- end }}
{{- if ne .RetryPolicy.BackoffCoefficient 0.0 }}
            BackoffCoefficient: {{ printf "%g" .RetryPolicy.BackoffCoefficient }},
{{- end }}
        },
{{- end }}
    },
{{- end }}
{{- end }}

{{- range .Runtime.Activities }}
{{- if eq .Kind "plan" }}
// {{ .FuncName }} handles the {{ .Name }} activity.
func {{ .FuncName }}(ctx context.Context, input any) (any, error) {
    payload, ok := input.(agentsruntime.PlanActivityInput)
    if !ok {
        return nil, errors.New("invalid plan activity input")
    }
    rt := agentsruntime.Default()
    if rt == nil {
        return nil, errors.New("runtime not initialized")
    }
    return rt.PlanStartActivity(ctx, payload)
}

// {{ .DefinitionVar }} describes the activity for runtime registration.
var {{ .DefinitionVar }} = engine.ActivityDefinition{
    Name:    {{ printf "%q" .Name }},
    Handler: {{ .FuncName }},
{{ template "activityOptions" . -}}
}

{{- else if eq .Kind "resume" }}
// {{ .FuncName }} handles the {{ .Name }} activity.
func {{ .FuncName }}(ctx context.Context, input any) (any, error) {
    payload, ok := input.(agentsruntime.PlanActivityInput)
    if !ok {
        return nil, errors.New("invalid plan activity input")
    }
    rt := agentsruntime.Default()
    if rt == nil {
        return nil, errors.New("runtime not initialized")
    }
    return rt.PlanResumeActivity(ctx, payload)
}

// {{ .DefinitionVar }} describes the activity for runtime registration.
var {{ .DefinitionVar }} = engine.ActivityDefinition{
    Name:    {{ printf "%q" .Name }},
    Handler: {{ .FuncName }},
{{ template "activityOptions" . -}}
}

{{- else if eq .Kind "execute_tool" }}
// {{ .FuncName }} handles the {{ .Name }} activity.
func {{ .FuncName }}(ctx context.Context, input any) (any, error) {
    payload, ok := input.(agentsruntime.ToolInput)
    if !ok {
        return nil, errors.New("invalid tool activity input")
    }
    rt := agentsruntime.Default()
    if rt == nil {
        return nil, errors.New("runtime not initialized")
    }
    return rt.ExecuteToolActivity(ctx, payload)
}

// {{ .DefinitionVar }} describes the activity for runtime registration.
var {{ .DefinitionVar }} = engine.ActivityDefinition{
    Name:    {{ printf "%q" .Name }},
    Handler: {{ .FuncName }},
{{ template "activityOptions" . -}}
}

{{- else }}
// {{ .FuncName }} handles the {{ .Name }} activity.
func {{ .FuncName }}(ctx context.Context, input any) (any, error) {
    return nil, errors.New("activity not implemented")
}

// {{ .DefinitionVar }} describes the activity for runtime registration.
var {{ .DefinitionVar }} = engine.ActivityDefinition{
    Name:    {{ printf "%q" .Name }},
    Handler: {{ .FuncName }},
{{ template "activityOptions" . -}}
}

{{- end }}
{{ end }}
