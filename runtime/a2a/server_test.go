package a2a

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"

	"goa.design/goa-ai/runtime/a2a/types"
	"goa.design/goa-ai/runtime/agent/tools"
)

// Test constants for task states and IDs.
const (
	testTaskID        = "task"
	testStateFailed   = "failed"
	testStateComplete = "completed"
)

type (
	testClient struct {
		out any
		err error
	}

	recordingStream struct {
		events []*types.TaskEvent
	}
)

func (c *testClient) Run(context.Context, []any) (any, error) {
	return c.out, c.err
}

func (s *recordingStream) Send(_ context.Context, ev *types.TaskEvent) error {
	s.events = append(s.events, ev)
	return nil
}

// TestTasksSendResponseProperty verifies Property 7: TasksSend response correctness.
// **Feature: a2a-architecture-redesign, Property 7: TasksSend Response Correctness**
// *For any* valid TasksSend request, the response should contain a task ID
// matching the request ID and a status reflecting the execution outcome.
// **Validates: Requirements 4.1**
func TestTasksSendResponseProperty(t *testing.T) {
	t.Helper()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50
	properties := gopter.NewProperties(parameters)

	properties.Property("TasksSend returns matching ID and state", prop.ForAll(
		func(taskID string, fail bool) bool {
			if taskID == "" {
				taskID = testTaskID
			}

			var out any = "ok"
			var err error
			if fail {
				err = errors.New("boom")
			}

			client := &testClient{out: out, err: err}
			srv, serr := NewServer(client, "http://example.com/a2a", ServerConfig{
				Suite:     "test.suite",
				AgentName: "agent",
				Skills: []SkillConfig{
					{
						ID:          "tools.echo",
						Description: "echo",
						Payload: tools.TypeSpec{
							Name:   "EchoPayload",
							Schema: []byte(`{"type":"object"}`),
						},
						Result: tools.TypeSpec{
							Name:   "EchoResult",
							Schema: []byte(`{"type":"object"}`),
						},
						ExampleArgs: `{}`,
					},
				},
			})
			if serr != nil {
				return false
			}

			text := "hello"
			payload := &types.SendTaskPayload{
				ID: taskID,
				Message: &types.TaskMessage{
					Role: "user",
					Parts: []*types.MessagePart{
						{
							Type: "text",
							Text: &text,
						},
					},
				},
			}

			resp, err := srv.TasksSend(context.Background(), payload)
			if err != nil {
				return false
			}
			if resp == nil || resp.Status == nil {
				return false
			}
			if resp.ID != taskID {
				return false
			}
			if fail {
				return resp.Status.State == testStateFailed
			}
			return resp.Status.State == testStateComplete
		},
		gen.AlphaString(),
		gen.Bool(),
	))

	properties.TestingRun(t)
}

// TestTasksSendSubscribeEventSequenceProperty verifies Property 8:
// TasksSendSubscribe event sequence.
// **Feature: a2a-architecture-redesign, Property 8: TasksSendSubscribe Event Sequence**
// *For any* TasksSendSubscribe request, the event stream should emit a "working"
// status event, followed by artifact events (if any), and end with a final
// status or error event.
// **Validates: Requirements 4.2**
func TestTasksSendSubscribeEventSequenceProperty(t *testing.T) {
	t.Helper()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50
	properties := gopter.NewProperties(parameters)

	properties.Property("TasksSendSubscribe emits expected event sequence", prop.ForAll(
		func(taskID string, fail bool) bool {
			if taskID == "" {
				taskID = testTaskID
			}

			var out any = "ok"
			var runErr error
			if fail {
				runErr = errors.New("boom")
			}

			client := &testClient{out: out, err: runErr}
			srv, serr := NewServer(client, "http://example.com/a2a", ServerConfig{
				Suite:     "test.suite",
				AgentName: "agent",
			})
			if serr != nil {
				return false
			}

			text := "hello"
			payload := &types.SendTaskPayload{
				ID: taskID,
				Message: &types.TaskMessage{
					Role: "user",
					Parts: []*types.MessagePart{
						{
							Type: "text",
							Text: &text,
						},
					},
				},
			}

			stream := &recordingStream{}
			err := srv.TasksSendSubscribe(context.Background(), payload, stream)
			if err != nil {
				return false
			}
			events := stream.events
			if len(events) == 0 {
				return false
			}
			// First event is always working status.
			if events[0].Type != "status" || events[0].Status == nil || events[0].Status.State != "working" {
				return false
			}

			if fail {
				if len(events) != 2 {
					return false
				}
				last := events[len(events)-1]
				if last.Type != "error" || last.Status == nil {
					return false
				}
				return last.Status.State == testStateFailed && last.Final
			}

			if len(events) != 3 {
				return false
			}
			if events[1].Type != "artifact" || events[1].Artifact == nil {
				return false
			}
			last := events[2]
			if last.Type != "status" || last.Status == nil {
				return false
			}
			return last.Status.State == testStateComplete && last.Final
		},
		gen.AlphaString(),
		gen.Bool(),
	))

	properties.TestingRun(t)
}

// TestTaskStateConcurrencySafetyProperty verifies Property 9: task state
// concurrency safety.
// **Feature: a2a-architecture-redesign, Property 9: Task State Concurrency Safety**
// *For any* number of concurrent TasksGet and TasksCancel operations on the same
// task ID, the operations should not cause inconsistent observable state.
// **Validates: Requirements 4.3**
func TestTaskStateConcurrencySafetyProperty(t *testing.T) {
	t.Helper()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 25
	properties := gopter.NewProperties(parameters)

	properties.Property("concurrent TasksGet and TasksCancel are safe", prop.ForAll(
		func(ops int) bool {
			if ops < 1 {
				ops = 1
			}
			if ops > 32 {
				ops = 32
			}

			client := &testClient{out: "ok"}
			srv, err := NewServer(client, "http://example.com/a2a", ServerConfig{
				Suite:     "test.suite",
				AgentName: "agent",
			})
			if err != nil {
				return false
			}

			taskID := "task"
			state := &TaskState{
				Status: &types.TaskStatus{State: "working"},
				Cancel: func() {},
			}
			if err := srv.store.Store(taskID, state); err != nil {
				return false
			}

			var wg sync.WaitGroup
			for i := 0; i < ops; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					if i%2 == 0 {
						_, _ = srv.TasksGet(context.Background(), &types.GetTaskPayload{ID: taskID})
					} else {
						_, _ = srv.TasksCancel(context.Background(), &types.CancelTaskPayload{ID: taskID})
					}
				}(i)
			}
			wg.Wait()

			final, ok := srv.store.Load(taskID)
			if !ok || final == nil || final.Status == nil {
				return false
			}
			switch final.Status.State {
			case "working", "canceled":
				return true
			default:
				return false
			}
		},
		gen.IntRange(1, 32),
	))

	properties.TestingRun(t)
}

// TestAgentCardFromServerConfigProperty verifies Property 10: AgentCard from
// ServerConfig.
// **Feature: a2a-architecture-redesign, Property 10: AgentCard from ServerConfig**
// *For any* ServerConfig, the AgentCard response should contain all skills from
// ServerConfig.Skills with matching IDs and descriptions.
// **Validates: Requirements 4.4**
func TestAgentCardFromServerConfigProperty(t *testing.T) {
	t.Helper()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50
	properties := gopter.NewProperties(parameters)

	properties.Property("AgentCard reflects ServerConfig", prop.ForAll(
		func(agentName, agentDesc, version, baseURL string, skillIDs []string) bool {
			if agentName == "" {
				agentName = "agent"
			}
			if version == "" {
				version = "1.0.0"
			}
			if baseURL == "" {
				baseURL = "http://example.com/a2a"
			}

			skills := make([]SkillConfig, 0, len(skillIDs))
			for i, id := range skillIDs {
				if id == "" {
					id = "toolset.tool"
				}
				skills = append(skills, SkillConfig{
					ID:          id,
					Description: agentDesc,
					Payload: tools.TypeSpec{
						Name:   "Payload",
						Schema: []byte(`{"type":"object"}`),
					},
					Result: tools.TypeSpec{
						Name:   "Result",
						Schema: []byte(`{"type":"object"}`),
					},
					ExampleArgs: `{}`,
				})
				// avoid unused variable warning for index in property
				_ = i
			}

			client := &testClient{out: "ok"}
			cfg := ServerConfig{
				Suite:            "test.suite",
				AgentName:        agentName,
				AgentDescription: agentDesc,
				Version:          version,
				Skills:           skills,
				Security:         SecurityConfig{},
			}
			srv, err := NewServer(client, baseURL, cfg)
			if err != nil {
				return false
			}

			card, err := srv.AgentCard(context.Background())
			if err != nil {
				return false
			}
			if card == nil {
				return false
			}

			if card.Name != cfg.AgentName || card.Description != cfg.AgentDescription {
				return false
			}
			if card.URL != baseURL || card.Version != cfg.Version {
				return false
			}
			if len(card.Skills) != len(cfg.Skills) {
				return false
			}
			for i, sc := range cfg.Skills {
				cs := card.Skills[i]
				if cs == nil {
					return false
				}
				if cs.ID != sc.ID || cs.Description != sc.Description || cs.Name != sc.ID {
					return false
				}
			}
			return true
		},
		gen.AlphaString(),
		gen.AlphaString(),
		gen.AlphaString(),
		gen.AlphaString(),
		gen.SliceOf(gen.AlphaString()),
	))

	properties.TestingRun(t)
}
