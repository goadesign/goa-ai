// TasksSend implements the tasks/send A2A method.
func (a *Adapter) TasksSend(ctx context.Context, p *{{ .A2APackage }}.SendTaskPayload) (*{{ .A2APackage }}.TaskResponse, error) {
	messages, err := convertMessage(p.Message)
	if err != nil {
		return errorResponse(p.ID, err), nil
	}

	taskCtx, cancel := context.WithCancel(ctx)
	a.tasks.Store(p.ID, &taskState{status: "working", cancel: cancel})
	defer a.tasks.Delete(p.ID)

	out, err := a.runtime.Run(taskCtx, messages)
	if err != nil {
		return errorResponse(p.ID, err), nil
	}
	return successResponse(p.ID, out), nil
}

// TasksSendSubscribe implements the tasks/sendSubscribe A2A method.
func (a *Adapter) TasksSendSubscribe(ctx context.Context, p *{{ .A2APackage }}.SendTaskPayload, stream {{ .A2APackage }}.TasksSendSubscribeServerStream) error {
	messages, err := convertMessage(p.Message)
	if err != nil {
		return stream.Send(errorEvent(p.ID, err))
	}

	taskCtx, cancel := context.WithCancel(ctx)
	a.tasks.Store(p.ID, &taskState{status: "working", cancel: cancel})
	defer a.tasks.Delete(p.ID)

	if err := stream.Send(statusEvent(p.ID, "working")); err != nil {
		return err
	}

	out, err := a.runtime.Run(taskCtx, messages)
	if err != nil {
		return stream.Send(errorEvent(p.ID, err))
	}

	if err := stream.Send(artifactEvent(p.ID, out)); err != nil {
		return err
	}
	return stream.Send(statusEvent(p.ID, "completed"))
}

// TasksGet implements the tasks/get A2A method.
func (a *Adapter) TasksGet(ctx context.Context, p *{{ .A2APackage }}.GetTaskPayload) (*{{ .A2APackage }}.TaskResponse, error) {
	v, ok := a.tasks.Load(p.ID)
	if !ok {
		return errorResponse(p.ID, fmt.Errorf("task not found")), nil
	}
	state := v.(*taskState)
	return &{{ .A2APackage }}.TaskResponse{
		ID:     p.ID,
		Status: &{{ .A2APackage }}.TaskStatus{State: state.status, Timestamp: time.Now().UTC().Format(time.RFC3339)},
	}, nil
}

// TasksCancel implements the tasks/cancel A2A method.
func (a *Adapter) TasksCancel(ctx context.Context, p *{{ .A2APackage }}.CancelTaskPayload) (*{{ .A2APackage }}.TaskResponse, error) {
	v, ok := a.tasks.Load(p.ID)
	if !ok {
		return errorResponse(p.ID, fmt.Errorf("task not found")), nil
	}
	state := v.(*taskState)
	if state.cancel != nil {
		state.cancel()
	}
	state.status = "canceled"
	return &{{ .A2APackage }}.TaskResponse{
		ID:     p.ID,
		Status: &{{ .A2APackage }}.TaskStatus{State: "canceled", Timestamp: time.Now().UTC().Format(time.RFC3339)},
	}, nil
}

// Helper functions for building responses and events.

func convertMessage(msg *{{ .A2APackage }}.TaskMessage) ([]any, error) {
	if msg == nil {
		return nil, fmt.Errorf("message is required")
	}
	var messages []any
	for _, part := range msg.Parts {
		switch part.Type {
		case "text":
			messages = append(messages, map[string]any{"role": msg.Role, "content": part.Text})
		case "data":
			data, err := json.Marshal(part.Data)
			if err != nil {
				return nil, fmt.Errorf("encoding data part: %w", err)
			}
			messages = append(messages, map[string]any{"role": msg.Role, "content": string(data)})
		}
	}
	return messages, nil
}

func errorResponse(taskID string, err error) *{{ .A2APackage }}.TaskResponse {
	return &{{ .A2APackage }}.TaskResponse{
		ID: taskID,
		Status: &{{ .A2APackage }}.TaskStatus{
			State:     "failed",
			Message:   &{{ .A2APackage }}.TaskMessage{Role: "system", Parts: []*{{ .A2APackage }}.MessagePart{{ "{{" }}Type: "text", Text: err.Error()}}},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
}

func successResponse(taskID string, out any) *{{ .A2APackage }}.TaskResponse {
	return &{{ .A2APackage }}.TaskResponse{
		ID:        taskID,
		Status:    &{{ .A2APackage }}.TaskStatus{State: "completed", Timestamp: time.Now().UTC().Format(time.RFC3339)},
		Artifacts: []*{{ .A2APackage }}.Artifact{convertArtifact(out)},
	}
}

func convertArtifact(out any) *{{ .A2APackage }}.Artifact {
	var parts []*{{ .A2APackage }}.MessagePart
	switch v := out.(type) {
	case string:
		parts = append(parts, &{{ .A2APackage }}.MessagePart{Type: "text", Text: v})
	default:
		data, _ := json.Marshal(v)
		parts = append(parts, &{{ .A2APackage }}.MessagePart{Type: "data", Data: json.RawMessage(data)})
	}
	return &{{ .A2APackage }}.Artifact{Name: "result", Parts: parts, LastChunk: true}
}

func statusEvent(taskID, state string) *{{ .A2APackage }}.TaskEvent {
	return &{{ .A2APackage }}.TaskEvent{
		Type:   "status",
		TaskID: taskID,
		Status: &{{ .A2APackage }}.TaskStatus{State: state, Timestamp: time.Now().UTC().Format(time.RFC3339)},
		Final:  state == "completed" || state == "failed" || state == "canceled",
	}
}

func errorEvent(taskID string, err error) *{{ .A2APackage }}.TaskEvent {
	return &{{ .A2APackage }}.TaskEvent{
		Type:   "error",
		TaskID: taskID,
		Status: &{{ .A2APackage }}.TaskStatus{
			State:   "failed",
			Message: &{{ .A2APackage }}.TaskMessage{Role: "system", Parts: []*{{ .A2APackage }}.MessagePart{{ "{{" }}Type: "text", Text: err.Error()}}},
		},
		Final: true,
	}
}

func artifactEvent(taskID string, out any) *{{ .A2APackage }}.TaskEvent {
	return &{{ .A2APackage }}.TaskEvent{
		Type:     "artifact",
		TaskID:   taskID,
		Artifact: convertArtifact(out),
	}
}
