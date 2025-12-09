// TasksSend implements the tasks/send A2A method.
func (a *Adapter) TasksSend(ctx context.Context, p *SendTaskPayload) (*TaskResponse, error) {
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
func (a *Adapter) TasksSendSubscribe(ctx context.Context, p *SendTaskPayload, stream TasksSendSubscribeServerStream) error {
	messages, err := convertMessage(p.Message)
	if err != nil {
		return stream.Send(ctx, errorEvent(p.ID, err))
	}

	taskCtx, cancel := context.WithCancel(ctx)
	a.tasks.Store(p.ID, &taskState{status: "working", cancel: cancel})
	defer a.tasks.Delete(p.ID)

	if err := stream.Send(ctx, statusEvent(p.ID, "working")); err != nil {
		return err
	}

	out, err := a.runtime.Run(taskCtx, messages)
	if err != nil {
		return stream.Send(ctx, errorEvent(p.ID, err))
	}

	if err := stream.Send(ctx, artifactEvent(p.ID, out)); err != nil {
		return err
	}
	return stream.Send(ctx, statusEvent(p.ID, "completed"))
}

// TasksGet implements the tasks/get A2A method.
func (a *Adapter) TasksGet(ctx context.Context, p *GetTaskPayload) (*TaskResponse, error) {
	v, ok := a.tasks.Load(p.ID)
	if !ok {
		return errorResponse(p.ID, fmt.Errorf("task not found")), nil
	}
	state := v.(*taskState)
	return &TaskResponse{
		ID:     p.ID,
		Status: &TaskStatus{State: state.status, Timestamp: ptrString(time.Now().UTC().Format(time.RFC3339))},
	}, nil
}

// TasksCancel implements the tasks/cancel A2A method.
func (a *Adapter) TasksCancel(ctx context.Context, p *CancelTaskPayload) (*TaskResponse, error) {
	v, ok := a.tasks.Load(p.ID)
	if !ok {
		return errorResponse(p.ID, fmt.Errorf("task not found")), nil
	}
	state := v.(*taskState)
	if state.cancel != nil {
		state.cancel()
	}
	state.status = "canceled"
	return &TaskResponse{
		ID:     p.ID,
		Status: &TaskStatus{State: "canceled", Timestamp: ptrString(time.Now().UTC().Format(time.RFC3339))},
	}, nil
}

// Helper functions for building responses and events.

func ptrString(s string) *string { return &s }
func ptrBool(b bool) *bool       { return &b }

func convertMessage(msg *TaskMessage) ([]any, error) {
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

func errorResponse(taskID string, err error) *TaskResponse {
	return &TaskResponse{
		ID: taskID,
		Status: &TaskStatus{
			State:     "failed",
			Message:   &TaskMessage{Role: "system", Parts: []*MessagePart{{"{{" }}Type: "text", Text: ptrString(err.Error())}}},
			Timestamp: ptrString(time.Now().UTC().Format(time.RFC3339)),
		},
	}
}

func successResponse(taskID string, out any) *TaskResponse {
	return &TaskResponse{
		ID:        taskID,
		Status:    &TaskStatus{State: "completed", Timestamp: ptrString(time.Now().UTC().Format(time.RFC3339))},
		Artifacts: []*Artifact{convertArtifact(out)},
	}
}

func convertArtifact(out any) *Artifact {
	var parts []*MessagePart
	switch v := out.(type) {
	case string:
		parts = append(parts, &MessagePart{Type: "text", Text: ptrString(v)})
	default:
		data, _ := json.Marshal(v)
		parts = append(parts, &MessagePart{Type: "data", Data: json.RawMessage(data)})
	}
	return &Artifact{Name: ptrString("result"), Parts: parts, LastChunk: ptrBool(true)}
}

func statusEvent(taskID, state string) *TaskEvent {
	return &TaskEvent{
		Type:   "status",
		TaskID: taskID,
		Status: &TaskStatus{State: state, Timestamp: ptrString(time.Now().UTC().Format(time.RFC3339))},
		Final:  ptrBool(state == "completed" || state == "failed" || state == "canceled"),
	}
}

func errorEvent(taskID string, err error) *TaskEvent {
	return &TaskEvent{
		Type:   "error",
		TaskID: taskID,
		Status: &TaskStatus{
			State:   "failed",
			Message: &TaskMessage{Role: "system", Parts: []*MessagePart{{"{{" }}Type: "text", Text: ptrString(err.Error())}}},
		},
		Final: ptrBool(true),
	}
}

func artifactEvent(taskID string, out any) *TaskEvent {
	return &TaskEvent{
		Type:     "artifact",
		TaskID:   taskID,
		Artifact: convertArtifact(out),
	}
}
