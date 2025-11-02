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
// {{ .FuncName }} is a placeholder for the {{ .Name }} activity. The concrete
// handler is registered by Register{{ $.StructName }} using a closure that captures
// the runtime. This function is not invoked at runtime.
func {{ .FuncName }}(ctx context.Context, input any) (any, error) {
    return nil, errors.New("unreachable activity handler")
}

// {{ .DefinitionVar }} describes the activity for runtime registration.
var {{ .DefinitionVar }} = engine.ActivityDefinition{
    Name:    {{ printf "%q" .Name }},
    Handler: {{ .FuncName }},
{{ template "activityOptions" . -}}
}

{{- else if eq .Kind "resume" }}
// {{ .FuncName }} is a placeholder for the {{ .Name }} activity. The concrete
// handler is registered by Register{{ $.StructName }} using a closure that captures
// the runtime. This function is not invoked at runtime.
func {{ .FuncName }}(ctx context.Context, input any) (any, error) {
    return nil, errors.New("unreachable activity handler")
}

// {{ .DefinitionVar }} describes the activity for runtime registration.
var {{ .DefinitionVar }} = engine.ActivityDefinition{
    Name:    {{ printf "%q" .Name }},
    Handler: {{ .FuncName }},
{{ template "activityOptions" . -}}
}

{{- else if eq .Kind "execute_tool" }}
// {{ .FuncName }} is a placeholder for the {{ .Name }} activity. The concrete
// handler is registered by Register{{ $.StructName }} using a closure that captures
// the runtime. This function is not invoked at runtime.
func {{ .FuncName }}(ctx context.Context, input any) (any, error) {
    return nil, errors.New("unreachable activity handler")
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
