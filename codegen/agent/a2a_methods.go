package codegen

import "goa.design/goa/v3/expr"

// buildMethods creates all A2A protocol methods
func (b *a2aExprBuilder) buildMethods() []*expr.MethodExpr {
	methods := make([]*expr.MethodExpr, 0, 6)

	// Core A2A methods
	methods = append(methods,
		b.buildSendTaskMethod(),
		b.buildSendTaskSubscribeMethod(),
		b.buildGetTaskMethod(),
		b.buildCancelTaskMethod(),
		b.buildGetAgentCardMethod(),
	)

	return methods
}

// buildSendTaskMethod creates the tasks/send method for non-streaming task execution.
func (b *a2aExprBuilder) buildSendTaskMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "tasks/send",
		Description: "Send a task to the agent and wait for completion",
		Payload:     b.userTypeAttr("SendTaskPayload", b.buildSendTaskPayloadType),
		Result:      b.userTypeAttr("TaskResponse", b.buildTaskResponseType),
	}
}

// buildSendTaskSubscribeMethod creates the tasks/sendSubscribe method for streaming task execution.
func (b *a2aExprBuilder) buildSendTaskSubscribeMethod() *expr.MethodExpr {
	m := &expr.MethodExpr{
		Name:        "tasks/sendSubscribe",
		Description: "Send a task to the agent and stream events",
		Payload:     b.userTypeAttr("SendTaskPayload", b.buildSendTaskPayloadType),
		Result:      b.userTypeAttr("TaskEvent", b.buildTaskEventType),
	}
	// Enable server streaming for SSE
	m.Stream = expr.ServerStreamKind
	m.StreamingResult = b.userTypeAttr("TaskEvent", b.buildTaskEventType)
	return m
}

// buildGetTaskMethod creates the tasks/get method for retrieving task status.
func (b *a2aExprBuilder) buildGetTaskMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "tasks/get",
		Description: "Get the status of a task",
		Payload:     b.userTypeAttr("GetTaskPayload", b.buildGetTaskPayloadType),
		Result:      b.userTypeAttr("TaskResponse", b.buildTaskResponseType),
	}
}

// buildCancelTaskMethod creates the tasks/cancel method for canceling a task.
func (b *a2aExprBuilder) buildCancelTaskMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "tasks/cancel",
		Description: "Cancel a running task",
		Payload:     b.userTypeAttr("CancelTaskPayload", b.buildCancelTaskPayloadType),
		Result:      b.userTypeAttr("TaskResponse", b.buildTaskResponseType),
	}
}

// buildGetAgentCardMethod creates the agent/card method for retrieving the agent card.
func (b *a2aExprBuilder) buildGetAgentCardMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "agent/card",
		Description: "Get the agent card describing capabilities and skills",
		Result:      b.userTypeAttr("AgentCardResponse", b.buildAgentCardResponseType),
	}
}
