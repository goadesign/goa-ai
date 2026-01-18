package registry

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/redis/go-redis/v9"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
)

// TestResultStreamCleanupOnSuccess verifies Property 10: Result stream cleanup on success.
// **Feature: internal-tool-registry, Property 10: Result stream cleanup on success**
// *For any* successful tool invocation, the temporary result stream should be destroyed
// after the result is returned.
// **Validates: Requirements 9.3, 12.3**
func TestResultStreamCleanupOnSuccess(t *testing.T) {
	rdb := getRedis(t)

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("result stream is destroyed after successful result", prop.ForAll(
		func(resultData map[string]any) bool {
			// Create a mock client that tracks stream destruction
			mockClient := newMockPulseClient()

			mgr, err := NewResultStreamManager(ResultStreamManagerOptions{
				Client: mockClient,
				Redis:  rdb,
			})
			if err != nil {
				return false
			}

			ctx := context.Background()

			// Create a result stream
			stream, toolUseID, _, err := mgr.CreateResultStream(ctx)
			if err != nil {
				return false
			}

			// Get the mock stream to control it
			mockStream := stream.(*mockPulseStream)

			// Marshal the result data
			resultJSON, err := json.Marshal(resultData)
			if err != nil {
				return false
			}

			// Prepare a result message to be delivered
			resultMsg := &ToolResultMessage{
				ToolUseID: toolUseID,
				Result:    resultJSON,
			}
			resultPayload, err := json.Marshal(resultMsg)
			if err != nil {
				return false
			}

			// Schedule the result to be delivered after a short delay
			go func() {
				time.Sleep(10 * time.Millisecond)
				mockStream.deliverEvent(&streaming.Event{
					EventName: MessageTypeResult,
					Payload:   resultPayload,
				})
			}()

			// Wait for the result
			result, err := mgr.WaitForResult(ctx, toolUseID, WaitForResultOptions{
				Timeout: 1 * time.Second,
			})
			if err != nil {
				return false
			}

			// Verify the result was received correctly
			if result.ToolUseID != toolUseID {
				return false
			}

			// Verify the stream was destroyed
			if !mockStream.destroyed {
				return false
			}

			// Verify the stream is no longer tracked locally
			impl := mgr.(*resultStreamManager)
			impl.mu.RLock()
			_, exists := impl.streams[toolUseID]
			impl.mu.RUnlock()
			if exists {
				return false
			}

			// Verify the mapping was deleted from Redis
			mappingKey := redisKeyForMapping(toolUseID)
			_, err = rdb.Get(ctx, mappingKey).Result()
			if !errors.Is(err, redis.Nil) {
				return false // Mapping should be deleted
			}

			return true
		},
		genResultData(),
	))

	properties.TestingRun(t)
}

// --- Mock implementations ---

// mockPulseClient is a mock implementation of clientspulse.Client for testing.
type mockPulseClient struct {
	mu      sync.Mutex
	streams map[string]*mockPulseStream
}

func newMockPulseClient() *mockPulseClient {
	return &mockPulseClient{
		streams: make(map[string]*mockPulseStream),
	}
}

func (c *mockPulseClient) Stream(name string, _ ...streamopts.Stream) (clientspulse.Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stream := &mockPulseStream{
		name:   name,
		events: make(chan *streaming.Event, 10),
	}
	c.streams[name] = stream
	return stream, nil
}

func (c *mockPulseClient) Close(ctx context.Context) error {
	return nil
}

// mockPulseStream is a mock implementation of clientspulse.Stream for testing.
type mockPulseStream struct {
	name      string
	events    chan *streaming.Event
	destroyed bool
	mu        sync.Mutex
}

func (s *mockPulseStream) Add(ctx context.Context, event string, payload []byte) (string, error) {
	return "0-0", nil
}

func (s *mockPulseStream) NewSink(ctx context.Context, name string, opts ...streamopts.Sink) (clientspulse.Sink, error) {
	return &mockPulseSink{stream: s}, nil
}

func (s *mockPulseStream) Destroy(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.destroyed = true
	return nil
}

func (s *mockPulseStream) deliverEvent(event *streaming.Event) {
	s.events <- event
}

// mockPulseSink is a mock implementation of clientspulse.Sink for testing.
type mockPulseSink struct {
	stream *mockPulseStream
}

func (s *mockPulseSink) Subscribe() <-chan *streaming.Event {
	return s.stream.events
}

func (s *mockPulseSink) Ack(ctx context.Context, event *streaming.Event) error {
	return nil
}

func (s *mockPulseSink) Close(ctx context.Context) {
}

// --- Generators ---

// genResultData generates result data for tool results.
func genResultData() gopter.Gen {
	return gen.OneConstOf(
		map[string]any{"status": "success"},
		map[string]any{"data": []string{"item1", "item2"}},
		map[string]any{"count": 42},
		map[string]any{"result": map[string]any{"key": "value"}},
		map[string]any{"items": []int{1, 2, 3}},
	)
}

// TestResultDeliveryCorrectness verifies Property 14: Result delivery correctness.
// **Feature: internal-tool-registry, Property 14: Result delivery correctness**
// *For any* EmitToolResult call, the result should be published to the correct
// result stream (keyed by tool use ID).
// **Validates: Requirements 4.1, 4.2**
func TestResultDeliveryCorrectness(t *testing.T) {
	rdb := getRedis(t)

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("EmitToolResult publishes to correct result stream", prop.ForAll(
		func(tc resultDeliveryTestCase) bool {
			ctx := context.Background()

			// Create a mock client that tracks published messages.
			mockClient := newMockPulseClientWithCapture()

			mgr, err := NewResultStreamManager(ResultStreamManagerOptions{
				Client: mockClient,
				Redis:  rdb,
			})
			if err != nil {
				return false
			}

			// Create a result stream to get a valid tool_use_id.
			_, toolUseID, streamID, err := mgr.CreateResultStream(ctx)
			if err != nil {
				return false
			}

			// Build the result message based on test case.
			var msg *ToolResultMessage
			if tc.isError {
				msg = NewToolResultErrorMessage(toolUseID, tc.errorCode, tc.errorMessage)
			} else {
				resultJSON, err := json.Marshal(tc.resultData)
				if err != nil {
					return false
				}
				msg = NewToolResultMessage(toolUseID, resultJSON)
			}

			// Publish the result.
			if err := mgr.PublishResult(ctx, toolUseID, msg); err != nil {
				return false
			}

			// Verify the message was published to the correct stream.
			mockClient.mu.Lock()
			publishedMsgs := mockClient.publishedMessages[streamID]
			mockClient.mu.Unlock()

			if len(publishedMsgs) != 1 {
				return false
			}

			// Verify the published message content.
			var publishedMsg ToolResultMessage
			if err := json.Unmarshal(publishedMsgs[0].payload, &publishedMsg); err != nil {
				return false
			}

			// Verify tool_use_id matches.
			if publishedMsg.ToolUseID != toolUseID {
				return false
			}

			// Verify result or error content.
			if tc.isError {
				if publishedMsg.Error == nil {
					return false
				}
				if publishedMsg.Error.Code != tc.errorCode {
					return false
				}
				if publishedMsg.Error.Message != tc.errorMessage {
					return false
				}
			} else {
				if publishedMsg.Error != nil {
					return false
				}
				// Verify result data by comparing JSON representations.
				expectedJSON, err := json.Marshal(tc.resultData)
				if err != nil {
					return false
				}
				// Compare JSON strings (normalized comparison).
				var expected, actual any
				if err := json.Unmarshal(expectedJSON, &expected); err != nil {
					return false
				}
				if err := json.Unmarshal(publishedMsg.Result, &actual); err != nil {
					return false
				}
				// Re-marshal both to get canonical JSON for comparison.
				expectedCanonical, _ := json.Marshal(expected)
				actualCanonical, _ := json.Marshal(actual)
				if string(expectedCanonical) != string(actualCanonical) {
					return false
				}
			}

			return true
		},
		genResultDeliveryTestCase(),
	))

	properties.TestingRun(t)
}

// resultDeliveryTestCase represents a test case for result delivery.
type resultDeliveryTestCase struct {
	resultData   map[string]any
	isError      bool
	errorCode    string
	errorMessage string
}

// genResultDeliveryTestCase generates test cases for result delivery.
func genResultDeliveryTestCase() gopter.Gen {
	// Generate success cases and error cases with equal probability.
	return gopter.CombineGens(
		gen.Bool(),
		genResultData(),
		genErrorCode(),
		genErrorMessage(),
	).Map(func(vals []any) resultDeliveryTestCase {
		isError := vals[0].(bool)
		if isError {
			return resultDeliveryTestCase{
				isError:      true,
				errorCode:    vals[2].(string),
				errorMessage: vals[3].(string),
			}
		}
		return resultDeliveryTestCase{
			resultData: vals[1].(map[string]any),
			isError:    false,
		}
	})
}

// genErrorCode generates error codes for tool errors.
func genErrorCode() gopter.Gen {
	return gen.OneConstOf(
		"execution_failed",
		"invalid_input",
		"timeout",
		"internal_error",
		"not_found",
	)
}

// genErrorMessage generates error messages for tool errors.
func genErrorMessage() gopter.Gen {
	return gen.OneConstOf(
		"Failed to connect to database",
		"Invalid parameter value",
		"Operation timed out",
		"Internal server error",
		"Resource not found",
	)
}

// mockPulseClientWithCapture is a mock that captures published messages.
type mockPulseClientWithCapture struct {
	mu                sync.Mutex
	streams           map[string]*mockPulseStreamWithCapture
	publishedMessages map[string][]capturedMessage
}

type capturedMessage struct {
	eventType string
	payload   []byte
}

func newMockPulseClientWithCapture() *mockPulseClientWithCapture {
	return &mockPulseClientWithCapture{
		streams:           make(map[string]*mockPulseStreamWithCapture),
		publishedMessages: make(map[string][]capturedMessage),
	}
}

func (c *mockPulseClientWithCapture) Stream(name string, _ ...streamopts.Stream) (clientspulse.Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stream := &mockPulseStreamWithCapture{
		name:   name,
		client: c,
		events: make(chan *streaming.Event, 10),
	}
	c.streams[name] = stream
	return stream, nil
}

func (c *mockPulseClientWithCapture) Close(ctx context.Context) error {
	return nil
}

// mockPulseStreamWithCapture is a mock stream that captures published messages.
type mockPulseStreamWithCapture struct {
	name      string
	client    *mockPulseClientWithCapture
	events    chan *streaming.Event
	destroyed bool
	mu        sync.Mutex
}

func (s *mockPulseStreamWithCapture) Add(ctx context.Context, event string, payload []byte) (string, error) {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	s.client.publishedMessages[s.name] = append(s.client.publishedMessages[s.name], capturedMessage{
		eventType: event,
		payload:   payload,
	})
	return "0-0", nil
}

func (s *mockPulseStreamWithCapture) NewSink(ctx context.Context, name string, opts ...streamopts.Sink) (clientspulse.Sink, error) {
	return &mockPulseSink{stream: &mockPulseStream{name: s.name, events: s.events}}, nil
}

func (s *mockPulseStreamWithCapture) Destroy(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.destroyed = true
	return nil
}
