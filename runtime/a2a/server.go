package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/a2a/types"
	agentruntime "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// ServerConfig contains static configuration for an A2A server. It is generated
	// from the agent design and remains constant for the lifetime of the server.
	ServerConfig struct {
		// Suite is the A2A suite identifier for this agent (for example,
		// "service.agent.toolset").
		Suite string
		// AgentName is the logical name of the agent.
		AgentName string
		// AgentDescription is a human-readable description of the agent.
		AgentDescription string
		// Version is the agent implementation version.
		Version string
		// DefaultInputModes lists the default input content modes used when the
		// caller does not specify explicit input modes.
		DefaultInputModes []string
		// DefaultOutputModes lists the default output content modes used when the
		// caller does not specify explicit output modes.
		DefaultOutputModes []string
		// Capabilities holds protocol-level or agent-specific capability flags.
		Capabilities map[string]any
		// Skills contains static metadata for each exported skill.
		Skills []SkillConfig
		// Security contains security scheme definitions and requirements for the
		// agent.
		Security SecurityConfig
	}

	// SkillConfig contains static metadata for a single skill. It is generated
	// from the agent design and used at runtime for encoding, decoding, and
	// retry-hint construction.
	SkillConfig struct {
		// ID is the canonical skill identifier (toolset.tool).
		ID string
		// Description is a human-readable description of the skill.
		Description string
		// Payload describes the payload schema and codec for the skill.
		Payload tools.TypeSpec
		// Result describes the result schema and codec for the skill.
		Result tools.TypeSpec
		// ExampleArgs contains an example JSON document for the payload used in
		// retry hints and documentation.
		ExampleArgs string
	}

	// SecurityConfig captures security schemes and requirements for the A2A agent.
	// It is intentionally minimal and aligned with the code generator's
	// A2ASecurityData.
	SecurityConfig struct {
		// Schemes maps scheme names to their definitions.
		Schemes map[string]*types.SecurityScheme
		// Requirements lists security requirements as in OpenAPI: each entry maps
		// a scheme name to a list of scopes.
		Requirements []map[string][]string
	}

	// TaskStore abstracts task state management for pluggability. The default
	// implementation is in-memory and process-bound.
	TaskStore interface {
		// Store saves or replaces the state for the given task ID.
		Store(id string, state *TaskState) error
		// Load returns the state for the given task ID if present.
		Load(id string) (*TaskState, bool)
		// Delete removes the state for the given task ID.
		Delete(id string)
	}

	// TaskState represents the state of an active task managed by the server.
	// It is safe for concurrent use by multiple goroutines.
	TaskState struct {
		mu sync.RWMutex
		// Status is the most recent task status snapshot.
		Status *types.TaskStatus
		// Cancel is the cancellation function for the underlying execution, if any.
		Cancel context.CancelFunc
	}

	// TaskStream is the minimal streaming interface used by TasksSendSubscribe.
	// Adapters generated for specific services wrap transport-specific stream
	// implementations to satisfy this interface.
	TaskStream interface {
		// Send streams a single task event to the client.
		Send(ctx context.Context, event *types.TaskEvent) error
	}

	// Server implements the A2A protocol surface by delegating execution to an
	// agent runtime Client and managing task lifecycle state.
	Server struct {
		rt      agentruntime.Client
		baseURL string
		config  ServerConfig
		store   TaskStore
	}

	// ServerOption configures optional aspects of the Server.
	ServerOption func(*Server)

	// inMemoryTaskStore is the default TaskStore implementation. It is safe for
	// concurrent use by multiple goroutines.
	inMemoryTaskStore struct {
		mu    sync.RWMutex
		tasks map[string]*TaskState
	}
)

// NewServer creates an A2A server with the given configuration. By default it
// uses an in-memory TaskStore; use WithTaskStore to provide a different
// implementation.
//
//nolint:unparam // error return reserved for future validation
func NewServer(rt agentruntime.Client, baseURL string, cfg ServerConfig, opts ...ServerOption) (*Server, error) {
	s := &Server{
		rt:      rt,
		baseURL: baseURL,
		config:  cfg,
		store:   newInMemoryTaskStore(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s, nil
}

// WithTaskStore configures the server to use the given TaskStore instead of
// the default in-memory implementation.
func WithTaskStore(store TaskStore) ServerOption {
	return func(s *Server) {
		s.store = store
	}
}

// TasksSend implements the tasks/send A2A method.
func (s *Server) TasksSend(ctx context.Context, p *types.SendTaskPayload) (*types.TaskResponse, error) {
	messages, err := convertMessage(p.Message)
	if err != nil {
		return errorResponse(p.ID, err), nil
	}

	taskCtx, cancel := context.WithCancel(ctx)
	state := &TaskState{
		Status: &types.TaskStatus{
			State:     "working",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
		Cancel: cancel,
	}
	if err := s.store.Store(p.ID, state); err != nil {
		cancel()
		return errorResponse(p.ID, err), nil
	}
	defer s.store.Delete(p.ID)

	out, err := s.rt.Run(taskCtx, messages)
	if err != nil {
		state.mu.Lock()
		state.Status = &types.TaskStatus{
			State:     "failed",
			Message:   errorMessage(err),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		state.mu.Unlock()
		return errorResponse(p.ID, err), nil
	}

	state.mu.Lock()
	state.Status = &types.TaskStatus{
		State:     "completed",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	state.mu.Unlock()
	return successResponse(p.ID, out), nil
}

// TasksSendSubscribe implements the tasks/sendSubscribe A2A method.
func (s *Server) TasksSendSubscribe(ctx context.Context, p *types.SendTaskPayload, stream TaskStream) error {
	messages, err := convertMessage(p.Message)
	if err != nil {
		return stream.Send(ctx, errorEvent(p.ID, err))
	}

	taskCtx, cancel := context.WithCancel(ctx)
	state := &TaskState{
		Status: &types.TaskStatus{
			State:     "working",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
		Cancel: cancel,
	}
	if err := s.store.Store(p.ID, state); err != nil {
		cancel()
		return stream.Send(ctx, errorEvent(p.ID, err))
	}
	defer s.store.Delete(p.ID)

	state.mu.RLock()
	initialStatus := state.Status
	state.mu.RUnlock()
	if err := stream.Send(ctx, statusEvent(p.ID, initialStatus)); err != nil {
		return err
	}

	out, err := s.rt.Run(taskCtx, messages)
	if err != nil {
		state.mu.Lock()
		state.Status = &types.TaskStatus{
			State:     "failed",
			Message:   errorMessage(err),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		state.mu.Unlock()
		return stream.Send(ctx, errorEvent(p.ID, err))
	}

	if err := stream.Send(ctx, artifactEvent(p.ID, out)); err != nil {
		return err
	}

	state.mu.Lock()
	state.Status = &types.TaskStatus{
		State:     "completed",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	finalStatus := state.Status
	state.mu.Unlock()
	return stream.Send(ctx, statusEvent(p.ID, finalStatus))
}

// TasksGet implements the tasks/get A2A method.
//
//nolint:unparam // error return is part of the A2A interface contract
func (s *Server) TasksGet(_ context.Context, p *types.GetTaskPayload) (*types.TaskResponse, error) {
	state, ok := s.store.Load(p.ID)
	if !ok {
		return errorResponse(p.ID, fmt.Errorf("task not found")), nil
	}
	state.mu.RLock()
	status := copyTaskStatus(state.Status)
	state.mu.RUnlock()
	return &types.TaskResponse{
		ID:     p.ID,
		Status: status,
	}, nil
}

// TasksCancel implements the tasks/cancel A2A method.
//
//nolint:unparam // error return is part of the A2A interface contract
func (s *Server) TasksCancel(_ context.Context, p *types.CancelTaskPayload) (*types.TaskResponse, error) {
	state, ok := s.store.Load(p.ID)
	if !ok {
		return errorResponse(p.ID, fmt.Errorf("task not found")), nil
	}
	state.mu.Lock()
	if state.Cancel != nil {
		state.Cancel()
	}
	state.Status = &types.TaskStatus{
		State:     "canceled",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	status := copyTaskStatus(state.Status)
	state.mu.Unlock()
	return &types.TaskResponse{
		ID:     p.ID,
		Status: status,
	}, nil
}

// AgentCard implements the agent/card A2A method.
//
//nolint:unparam // error return is part of the A2A interface contract
func (s *Server) AgentCard(_ context.Context) (*types.AgentCardResponse, error) {
	skills := make([]*types.Skill, 0, len(s.config.Skills))
	for _, sk := range s.config.Skills {
		skills = append(skills, &types.Skill{
			ID:          sk.ID,
			Name:        sk.ID,
			Description: sk.Description,
		})
	}

	card := &types.AgentCard{
		ProtocolVersion:    "1.0",
		Name:               s.config.AgentName,
		Description:        s.config.AgentDescription,
		URL:                s.baseURL,
		Version:            s.config.Version,
		Capabilities:       s.config.Capabilities,
		DefaultInputModes:  s.config.DefaultInputModes,
		DefaultOutputModes: s.config.DefaultOutputModes,
		Skills:             skills,
		SecuritySchemes:    s.config.Security.Schemes,
	}

	return card, nil
}

// newInMemoryTaskStore creates a new in-memory TaskStore implementation.
func newInMemoryTaskStore() *inMemoryTaskStore {
	return &inMemoryTaskStore{
		tasks: make(map[string]*TaskState),
	}
}

// Store saves or replaces the state for the given task ID.
func (s *inMemoryTaskStore) Store(id string, state *TaskState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[id] = state
	return nil
}

// Load returns the state for the given task ID if present.
func (s *inMemoryTaskStore) Load(id string) (*TaskState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.tasks[id]
	return state, ok
}

// Delete removes the state for the given task ID.
func (s *inMemoryTaskStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
}

func convertMessage(msg *types.TaskMessage) ([]any, error) {
	messages := make([]any, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		switch part.Type {
		case "text":
			messages = append(messages, map[string]any{
				"role":    msg.Role,
				"content": part.Text,
			})
		case "data":
			if len(part.Data) == 0 {
				continue
			}
			var v any
			if err := json.Unmarshal(part.Data, &v); err != nil {
				return nil, fmt.Errorf("decoding data part: %w", err)
			}
			messages = append(messages, map[string]any{
				"role":    msg.Role,
				"content": v,
			})
		}
	}
	return messages, nil
}

func convertArtifact(out any) *types.Artifact {
	var parts []*types.MessagePart
	switch v := out.(type) {
	case string:
		parts = append(parts, &types.MessagePart{
			Type: "text",
			Text: ptrString(v),
		})
	default:
		data, _ := json.Marshal(v) // Best-effort encoding; errors surface via caller.
		parts = append(parts, &types.MessagePart{
			Type: "data",
			Data: data,
		})
	}
	last := true
	return &types.Artifact{
		Name:      ptrString("result"),
		Parts:     parts,
		LastChunk: &last,
	}
}

func statusEvent(taskID string, status *types.TaskStatus) *types.TaskEvent {
	final := status.State == "completed" || status.State == "failed" || status.State == "canceled"
	return &types.TaskEvent{
		Type:   "status",
		TaskID: taskID,
		Status: status,
		Final:  final,
	}
}

func errorEvent(taskID string, err error) *types.TaskEvent {
	msg := errorMessage(err)
	status := &types.TaskStatus{
		State:     "failed",
		Message:   msg,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	return &types.TaskEvent{
		Type:   "error",
		TaskID: taskID,
		Status: status,
		Final:  true,
	}
}

func artifactEvent(taskID string, out any) *types.TaskEvent {
	return &types.TaskEvent{
		Type:     "artifact",
		TaskID:   taskID,
		Artifact: convertArtifact(out),
	}
}

func errorResponse(taskID string, err error) *types.TaskResponse {
	status := &types.TaskStatus{
		State:     "failed",
		Message:   errorMessage(err),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	return &types.TaskResponse{
		ID:     taskID,
		Status: status,
	}
}

func successResponse(taskID string, out any) *types.TaskResponse {
	status := &types.TaskStatus{
		State:     "completed",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	return &types.TaskResponse{
		ID:        taskID,
		Status:    status,
		Artifacts: []*types.Artifact{convertArtifact(out)},
	}
}

func errorMessage(err error) *types.TaskMessage {
	if err == nil {
		return nil
	}
	return &types.TaskMessage{
		Role: "system",
		Parts: []*types.MessagePart{
			{
				Type: "text",
				Text: ptrString(err.Error()),
			},
		},
	}
}

func ptrString(s string) *string { return &s }

// copyTaskStatus creates a deep copy of a TaskStatus to avoid races when
// returning status snapshots.
func copyTaskStatus(s *types.TaskStatus) *types.TaskStatus {
	if s == nil {
		return nil
	}
	cp := &types.TaskStatus{
		State:     s.State,
		Timestamp: s.Timestamp,
	}
	if s.Message != nil {
		cp.Message = copyTaskMessage(s.Message)
	}
	return cp
}

// copyTaskMessage creates a deep copy of a TaskMessage.
func copyTaskMessage(m *types.TaskMessage) *types.TaskMessage {
	if m == nil {
		return nil
	}
	cp := &types.TaskMessage{
		Role:  m.Role,
		Parts: make([]*types.MessagePart, len(m.Parts)),
	}
	for i, p := range m.Parts {
		cp.Parts[i] = copyMessagePart(p)
	}
	return cp
}

// copyMessagePart creates a deep copy of a MessagePart.
func copyMessagePart(p *types.MessagePart) *types.MessagePart {
	if p == nil {
		return nil
	}
	cp := &types.MessagePart{
		Type: p.Type,
	}
	if p.Text != nil {
		cp.Text = ptrString(*p.Text)
	}
	if len(p.Data) > 0 {
		cp.Data = make([]byte, len(p.Data))
		copy(cp.Data, p.Data)
	}
	if p.MIMEType != nil {
		cp.MIMEType = ptrString(*p.MIMEType)
	}
	if p.URI != nil {
		cp.URI = ptrString(*p.URI)
	}
	return cp
}
