// {{ .Runtime.Workflow.FuncName }} is the durable workflow entry point for the agent.
func {{ .Runtime.Workflow.FuncName }}(ctx engine.WorkflowContext, input any) (any, error) {
    runInput, ok := input.(runtime.RunInput)
    if !ok {
        return nil, errors.New("invalid run input")
    }
    if runtime.Default() == nil {
        return nil, errors.New("runtime not initialized")
    }
    return runtime.Default().ExecuteWorkflow(ctx, runInput)
}

// {{ .Runtime.Workflow.DefinitionVar }} describes the workflow for runtime registration.
var {{ .Runtime.Workflow.DefinitionVar }} = engine.WorkflowDefinition{
    Name:      {{ printf "%q" .Runtime.Workflow.Name }},
    TaskQueue: {{ printf "%q" .Runtime.Workflow.Queue }},
    Handler:   {{ .Runtime.Workflow.FuncName }},
}
