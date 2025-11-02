// {{ .Runtime.Workflow.FuncName }} is a placeholder workflow handler. The
// concrete handler is registered by Register{{ .StructName }} using a closure
// that captures the runtime. This function is not invoked at runtime.
func {{ .Runtime.Workflow.FuncName }}(ctx engine.WorkflowContext, input any) (any, error) {
    return nil, errors.New("unreachable workflow handler")
}

// {{ .Runtime.Workflow.DefinitionVar }} describes the workflow for runtime registration.
var {{ .Runtime.Workflow.DefinitionVar }} = engine.WorkflowDefinition{
    Name:      {{ printf "%q" .Runtime.Workflow.Name }},
    TaskQueue: {{ printf "%q" .Runtime.Workflow.Queue }},
    Handler:   {{ .Runtime.Workflow.FuncName }},
}
