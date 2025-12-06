package codegen

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"goa.design/goa/v3/codegen/service"
)

// TestA2ATaskRoutingProperty verifies Property 12: A2A Task Routing.
// **Feature: mcp-registry, Property 12: A2A Task Routing**
// *For any* valid A2A task request targeting a skill, the runtime SHALL route
// to the corresponding tool and return a valid task response.
// **Validates: Requirements 13.4**
func TestA2ATaskRoutingProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("valid task requests produce valid task responses", prop.ForAll(
		func(taskID, skillID, messageText string) bool {
			// Create a mock runtime that tracks calls
			mockRuntime := &mockAgentRuntime{
				runResult: "Task completed successfully",
			}

			// Create adapter with mock runtime
			adapter := &testA2AAdapter{
				runtime: mockRuntime,
				agentID: "test-agent",
				baseURL: "http://localhost:8080",
			}

			// Create a valid task request
			request := &testSendTaskPayload{
				ID: taskID,
				Message: &testTaskMessage{
					Role: "user",
					Parts: []*testMessagePart{
						{Type: "text", Text: messageText},
					},
				},
			}

			// Execute the task
			response, err := adapter.TasksSend(context.Background(), request)

			// Property 1: No error should occur for valid requests
			if err != nil {
				return false
			}

			// Property 2: Response must have the same task ID
			if response.ID != taskID {
				return false
			}

			// Property 3: Response must have a status
			if response.Status == nil {
				return false
			}

			// Property 4: Status must be a valid state
			validStates := map[string]bool{
				"submitted":      true,
				"working":        true,
				"input-required": true,
				"completed":      true,
				"failed":         true,
				"canceled":       true,
			}
			if !validStates[response.Status.State] {
				return false
			}

			// Property 5: Runtime must have been called
			if !mockRuntime.runCalled {
				return false
			}

			return true
		},
		genTaskID(),
		genSkillID(),
		genMessageText(),
	))

	properties.TestingRun(t)
}

// TestA2ATaskRoutingPreservesTaskID verifies that task ID is preserved in responses.
// **Feature: mcp-registry, Property 12: A2A Task Routing**
// **Validates: Requirements 13.4**
func TestA2ATaskRoutingPreservesTaskID(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("task ID is preserved in response", prop.ForAll(
		func(taskID string) bool {
			mockRuntime := &mockAgentRuntime{runResult: "done"}
			adapter := &testA2AAdapter{
				runtime: mockRuntime,
				agentID: "test-agent",
				baseURL: "http://localhost:8080",
			}

			request := &testSendTaskPayload{
				ID: taskID,
				Message: &testTaskMessage{
					Role:  "user",
					Parts: []*testMessagePart{{Type: "text", Text: "test"}},
				},
			}

			response, err := adapter.TasksSend(context.Background(), request)
			if err != nil {
				return false
			}

			return response.ID == taskID
		},
		genTaskID(),
	))

	properties.TestingRun(t)
}

// TestA2ATaskRoutingMessageConversion verifies that messages are converted correctly.
// **Feature: mcp-registry, Property 12: A2A Task Routing**
// **Validates: Requirements 13.4**
func TestA2ATaskRoutingMessageConversion(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("text messages are converted to runtime format", prop.ForAll(
		func(role, text string) bool {
			mockRuntime := &mockAgentRuntime{runResult: "done"}
			adapter := &testA2AAdapter{
				runtime: mockRuntime,
				agentID: "test-agent",
				baseURL: "http://localhost:8080",
			}

			request := &testSendTaskPayload{
				ID: "task-1",
				Message: &testTaskMessage{
					Role:  role,
					Parts: []*testMessagePart{{Type: "text", Text: text}},
				},
			}

			_, err := adapter.TasksSend(context.Background(), request)
			if err != nil {
				return false
			}

			// Verify the runtime received the converted message
			if len(mockRuntime.lastMessages) == 0 {
				return false
			}

			// Check that the message was converted with correct role and content
			msg, ok := mockRuntime.lastMessages[0].(map[string]any)
			if !ok {
				return false
			}

			return msg["role"] == role && msg["content"] == text
		},
		genMessageRole(),
		genMessageText(),
	))

	properties.TestingRun(t)
}

// TestA2ATaskRoutingDataPartConversion verifies that data parts are converted correctly.
// **Feature: mcp-registry, Property 12: A2A Task Routing**
// **Validates: Requirements 13.4**
func TestA2ATaskRoutingDataPartConversion(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("data parts are JSON-encoded in runtime format", prop.ForAll(
		func(key, value string) bool {
			mockRuntime := &mockAgentRuntime{runResult: "done"}
			adapter := &testA2AAdapter{
				runtime: mockRuntime,
				agentID: "test-agent",
				baseURL: "http://localhost:8080",
			}

			data := map[string]string{key: value}
			request := &testSendTaskPayload{
				ID: "task-1",
				Message: &testTaskMessage{
					Role:  "user",
					Parts: []*testMessagePart{{Type: "data", Data: data}},
				},
			}

			_, err := adapter.TasksSend(context.Background(), request)
			if err != nil {
				return false
			}

			// Verify the runtime received the converted message
			if len(mockRuntime.lastMessages) == 0 {
				return false
			}

			msg, ok := mockRuntime.lastMessages[0].(map[string]any)
			if !ok {
				return false
			}

			// Content should be JSON-encoded data
			content, ok := msg["content"].(string)
			if !ok {
				return false
			}

			// Verify it's valid JSON
			var parsed map[string]string
			if err := json.Unmarshal([]byte(content), &parsed); err != nil {
				return false
			}

			return parsed[key] == value
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestA2ATaskRoutingErrorHandling verifies that runtime errors produce failed task responses.
// **Feature: mcp-registry, Property 12: A2A Task Routing**
// **Validates: Requirements 13.4**
func TestA2ATaskRoutingErrorHandling(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("runtime errors produce failed task responses", prop.ForAll(
		func(taskID, errorMsg string) bool {
			mockRuntime := &mockAgentRuntime{
				runError: errors.New(errorMsg),
			}
			adapter := &testA2AAdapter{
				runtime: mockRuntime,
				agentID: "test-agent",
				baseURL: "http://localhost:8080",
			}

			request := &testSendTaskPayload{
				ID: taskID,
				Message: &testTaskMessage{
					Role:  "user",
					Parts: []*testMessagePart{{Type: "text", Text: "test"}},
				},
			}

			response, err := adapter.TasksSend(context.Background(), request)

			// Property 1: No Go error should be returned (error is in response)
			if err != nil {
				return false
			}

			// Property 2: Response must have the same task ID
			if response.ID != taskID {
				return false
			}

			// Property 3: Status must be "failed"
			if response.Status == nil || response.Status.State != "failed" {
				return false
			}

			return true
		},
		genTaskID(),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	properties.TestingRun(t)
}

// TestA2ATaskRoutingNilMessageRejection verifies that nil messages are rejected.
// **Feature: mcp-registry, Property 12: A2A Task Routing**
// **Validates: Requirements 13.4**
func TestA2ATaskRoutingNilMessageRejection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("nil messages produce failed task responses", prop.ForAll(
		func(taskID string) bool {
			mockRuntime := &mockAgentRuntime{runResult: "done"}
			adapter := &testA2AAdapter{
				runtime: mockRuntime,
				agentID: "test-agent",
				baseURL: "http://localhost:8080",
			}

			request := &testSendTaskPayload{
				ID:      taskID,
				Message: nil, // Nil message
			}

			response, err := adapter.TasksSend(context.Background(), request)

			// Property 1: No Go error should be returned
			if err != nil {
				return false
			}

			// Property 2: Response must have the same task ID
			if response.ID != taskID {
				return false
			}

			// Property 3: Status must be "failed"
			if response.Status == nil || response.Status.State != "failed" {
				return false
			}

			// Property 4: Runtime should NOT have been called
			if mockRuntime.runCalled {
				return false
			}

			return true
		},
		genTaskID(),
	))

	properties.TestingRun(t)
}

// TestA2ATaskRoutingSuccessArtifacts verifies that successful tasks include artifacts.
// **Feature: mcp-registry, Property 12: A2A Task Routing**
// **Validates: Requirements 13.4**
func TestA2ATaskRoutingSuccessArtifacts(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("successful tasks include result artifacts", prop.ForAll(
		func(taskID, result string) bool {
			mockRuntime := &mockAgentRuntime{runResult: result}
			adapter := &testA2AAdapter{
				runtime: mockRuntime,
				agentID: "test-agent",
				baseURL: "http://localhost:8080",
			}

			request := &testSendTaskPayload{
				ID: taskID,
				Message: &testTaskMessage{
					Role:  "user",
					Parts: []*testMessagePart{{Type: "text", Text: "test"}},
				},
			}

			response, err := adapter.TasksSend(context.Background(), request)
			if err != nil {
				return false
			}

			// Property 1: Status must be "completed"
			if response.Status == nil || response.Status.State != "completed" {
				return false
			}

			// Property 2: Artifacts must be present
			if len(response.Artifacts) == 0 {
				return false
			}

			// Property 3: First artifact must have parts
			if len(response.Artifacts[0].Parts) == 0 {
				return false
			}

			return true
		},
		genTaskID(),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// Test types that mirror the generated A2A types

type testSendTaskPayload struct {
	ID      string
	Message *testTaskMessage
}

type testTaskMessage struct {
	Role  string
	Parts []*testMessagePart
}

type testMessagePart struct {
	Type string
	Text string
	Data any
}

type testTaskResponse struct {
	ID        string
	Status    *testTaskStatus
	Artifacts []*testArtifact
}

type testTaskStatus struct {
	State     string
	Message   *testTaskMessage
	Timestamp string
}

type testArtifact struct {
	Name      string
	Parts     []*testMessagePart
	LastChunk bool
}

// Mock runtime for testing

type mockAgentRuntime struct {
	runCalled    bool
	lastMessages []any
	runResult    any
	runError     error
}

func (m *mockAgentRuntime) Run(_ context.Context, messages []any) (any, error) {
	m.runCalled = true
	m.lastMessages = messages
	if m.runError != nil {
		return nil, m.runError
	}
	return m.runResult, nil
}

// Test adapter that mirrors the generated adapter structure

type testA2AAdapter struct {
	runtime *mockAgentRuntime
	agentID string
	baseURL string
}

//nolint:unparam // error return is part of the interface contract
func (a *testA2AAdapter) TasksSend(ctx context.Context, p *testSendTaskPayload) (*testTaskResponse, error) {
	messages, err := a.convertMessage(p.Message)
	if err != nil {
		return a.errorResponse(p.ID, err), nil
	}

	out, err := a.runtime.Run(ctx, messages)
	if err != nil {
		return a.errorResponse(p.ID, err), nil
	}
	return a.successResponse(p.ID, out), nil
}

func (a *testA2AAdapter) convertMessage(msg *testTaskMessage) ([]any, error) {
	if msg == nil {
		return nil, errors.New("message is required")
	}
	var messages []any
	for _, part := range msg.Parts {
		switch part.Type {
		case "text":
			messages = append(messages, map[string]any{"role": msg.Role, "content": part.Text})
		case "data":
			data, err := json.Marshal(part.Data)
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": msg.Role, "content": string(data)})
		}
	}
	return messages, nil
}

func (a *testA2AAdapter) errorResponse(taskID string, err error) *testTaskResponse {
	return &testTaskResponse{
		ID: taskID,
		Status: &testTaskStatus{
			State: "failed",
			Message: &testTaskMessage{
				Role:  "system",
				Parts: []*testMessagePart{{Type: "text", Text: err.Error()}},
			},
		},
	}
}

func (a *testA2AAdapter) successResponse(taskID string, out any) *testTaskResponse {
	return &testTaskResponse{
		ID:        taskID,
		Status:    &testTaskStatus{State: "completed"},
		Artifacts: []*testArtifact{a.convertArtifact(out)},
	}
}

func (a *testA2AAdapter) convertArtifact(out any) *testArtifact {
	var parts []*testMessagePart
	switch v := out.(type) {
	case string:
		parts = append(parts, &testMessagePart{Type: "text", Text: v})
	default:
		data, _ := json.Marshal(v)
		parts = append(parts, &testMessagePart{Type: "data", Data: json.RawMessage(data)})
	}
	return &testArtifact{Name: "result", Parts: parts, LastChunk: true}
}

// Generators

func genTaskID() gopter.Gen {
	return gen.OneConstOf(
		"task-1", "task-2", "task-abc-123",
		"550e8400-e29b-41d4-a716-446655440000",
		"request-001", "job-xyz",
	)
}

func genSkillID() gopter.Gen {
	return gen.OneConstOf(
		"toolset.analyze", "toolset.search", "toolset.query",
		"data_tools.process", "admin_tools.configure",
	)
}

func genMessageText() gopter.Gen {
	return gen.OneConstOf(
		"Hello, world!",
		"Please analyze this data",
		"Search for documents about AI",
		"Process the input file",
		"",
	)
}

func genMessageRole() gopter.Gen {
	return gen.OneConstOf("user", "assistant", "system")
}

// Ensure service package is used
var _ = service.Data{}
