package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	aistream "goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolserverdata"
	goa "goa.design/goa/v3/pkg"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

type correctableServiceError struct {
	error
	issues []*tools.FieldIssue
}

const (
	testRegistrationTokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testRegistrationTokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func (e *correctableServiceError) ToolFailure(tools.Ident) *planner.ToolFailure {
	return &planner.ToolFailure{
		Kind:  planner.FailureDomainRejection,
		Error: planner.ToolErrorFromError(e.error),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryCorrectCall,
		},
	}
}

func (e *correctableServiceError) Issues() []*tools.FieldIssue {
	return e.issues
}

func TestExecutorUsesOldestStartForResultStreamReader(t *testing.T) {
	t.Parallel()

	const (
		toolUseID       = "tooluse-123"
		resultStreamID  = "result:" + toolUseID
		resultEventName = toolregistry.ResultEventKey
	)

	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		},
	}

	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					RegistrationToken: testRegistrationTokenA,
					ToolUseID:         toolUseID,
					Result:            json.RawMessage(`{}`),
				}),
			},
		},
	}
	pc := fakePulseClient{
		streamID: resultStreamID,
		stream:   stream,
	}

	exec := New(fakeRegistryClient{
		toolUseID: toolUseID,
	}, pc, specs)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:     "run",
		SessionID: "sess",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})

	require.NoError(t, err)
	assert.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	assert.Equal(t, tools.Ident("todos.update_todos"), res.ToolResult.Name)
	assert.False(t, stream.destroyed)
}

func TestExecutorSequentialAndConcurrentWaitersReplayTerminalHistory(t *testing.T) {
	t.Parallel()

	const toolUseID = "replay-tool-use-id"
	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{{
			ID:        "1-0",
			EventName: toolregistry.ResultEventKey,
			Payload: mustJSON(t, toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Result:            json.RawMessage(`{}`),
			}),
		}},
	}
	exec := New(
		fakeRegistryClient{toolUseID: toolUseID},
		fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
		fakeSpecs{spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		}},
	)
	execute := func() (*agentsruntime.ToolExecutionResult, error) {
		return exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
			RunID:      "run",
			SessionID:  "session",
			ToolCallID: "call-1",
		}, &planner.ToolRequest{
			Name:    "todos.update_todos",
			Payload: []byte(`{}`),
		})
	}

	first, err := execute()
	require.NoError(t, err)
	require.NotNil(t, first)
	results := make(chan *agentsruntime.ToolExecutionResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			result, executeErr := execute()
			results <- result
			errs <- executeErr
		}()
	}
	for range 2 {
		require.NoError(t, <-errs)
		require.NotNil(t, <-results)
	}
	assert.Empty(t, stream.acked)
}

func TestExecutorRejectsMalformedTerminalHistoryImmediately(t *testing.T) {
	t.Parallel()

	const toolUseID = "malformed-tool-use-id"
	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{{
			ID:        "1-0",
			EventName: toolregistry.ResultEventKey,
			Payload:   []byte(`{"registration_token":`),
		}},
	}
	exec := New(
		fakeRegistryClient{toolUseID: toolUseID},
		fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
		fakeSpecs{spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		}},
	)

	_, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "session",
		ToolCallID: "call-1",
	}, &planner.ToolRequest{Name: "todos.update_todos", Payload: []byte(`{}`)})
	require.ErrorContains(t, err, "decode terminal tool result event 1-0")
}

func TestExecutorAsksRegistryToRetryTransientProviderOverload(t *testing.T) {
	t.Parallel()

	const toolUseID = "overload-tool-use-id"
	overloaded := toolregistry.NewToolResultRetryMessage(
		testRegistrationTokenA,
		toolUseID,
		toolregistry.ToolRetryReasonProviderOverloaded,
		toolregistry.ProviderOverloadRetryAfter,
	)
	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: toolregistry.ResultEventKey,
				Payload:   mustJSON(t, overloaded),
			},
			{
				ID:        "2-0",
				EventName: toolregistry.ResultEventKey,
				Payload: mustJSON(t, toolregistry.NewToolResultMessage(
					testRegistrationTokenA,
					toolUseID,
					json.RawMessage(`{}`),
				)),
			},
		},
	}
	var calls atomic.Int64
	var retryToken string
	exec := New(
		fakeRegistryClient{
			toolUseID:          toolUseID,
			calls:              &calls,
			retryExpectedToken: &retryToken,
		},
		fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
		fakeSpecs{spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		}},
	)

	result, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "session",
		ToolCallID: "call-1",
	}, &planner.ToolRequest{Name: "todos.update_todos", Payload: []byte(`{}`)})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, int64(2), calls.Load())
	assert.Equal(t, testRegistrationTokenA, retryToken)
}

func TestExecutorRejectsMalformedRetryControl(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "malformed-retry-tool-use-id"
		toolCallID = "malformed-retry-tool-call-id"
	)
	validRetry := &toolregistry.ToolRetry{
		Reason:           toolregistry.ToolRetryReasonProviderOverloaded,
		RetryAfterMillis: toolregistry.ProviderOverloadRetryAfter.Milliseconds(),
	}
	terminalError := toolregistry.NewToolResultErrorMessage(
		testRegistrationTokenA,
		toolUseID,
		"execution_failed",
		"failed",
	).Error
	tests := []struct {
		name string
		msg  toolregistry.ToolResultMessage
		want string
	}{
		{
			name: "retry and result",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Result:            json.RawMessage(`{"ok":true}`),
				Retry:             validRetry,
			},
			want: "retry and result are both set",
		},
		{
			name: "retry and error",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Error:             terminalError,
				Retry:             validRetry,
			},
			want: "retry and error are both set",
		},
		{
			name: "retry and bounds",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Bounds:            &agent.Bounds{},
				Retry:             validRetry,
			},
			want: "retry and bounds are both set",
		},
		{
			name: "retry and server data",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				ServerData: []*toolregistry.ServerDataItem{{
					Kind: "test",
					Data: json.RawMessage(`{}`),
				}},
				Retry: validRetry,
			},
			want: "retry and server data are both set",
		},
		{
			name: "unknown reason",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Retry: &toolregistry.ToolRetry{
					Reason:           "try_again",
					RetryAfterMillis: toolregistry.ProviderOverloadRetryAfter.Milliseconds(),
				},
			},
			want: "unsupported tool retry reason",
		},
		{
			name: "missing delay",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Retry: &toolregistry.ToolRetry{
					Reason: toolregistry.ToolRetryReasonProviderOverloaded,
				},
			},
			want: "tool retry delay 0ms is outside",
		},
		{
			name: "excessive delay",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Retry: &toolregistry.ToolRetry{
					Reason: toolregistry.ToolRetryReasonProviderOverloaded,
					RetryAfterMillis: toolregistry.MaxProviderOverloadRetryAfter.Milliseconds() +
						1,
				},
			},
			want: "tool retry delay",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := executeRegistryResultMessage(
				t,
				toolUseID,
				toolCallID,
				test.msg,
				&tools.ToolSpec{
					Name:    "todos.update_todos",
					Toolset: "todos.todos",
					Result:  tools.TypeSpec{},
					Payload: tools.TypeSpec{},
				},
			)
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), test.want)
			assert.Contains(t, err.Error(), "tool_call_id="+toolCallID)
			assert.Contains(t, err.Error(), "tool_use_id="+toolUseID)
		})
	}
}

func TestExecutorIgnoresLateResultFromReusedToolUseID(t *testing.T) {
	t.Parallel()

	const toolUseID = "reused-tool-use-id"
	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: toolregistry.ResultEventKey,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					RegistrationToken: testRegistrationTokenB,
					ToolUseID:         toolUseID,
					Result:            json.RawMessage(`{}`),
				}),
			},
			{
				ID:        "2-0",
				EventName: toolregistry.ResultEventKey,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					RegistrationToken: testRegistrationTokenA,
					ToolUseID:         toolUseID,
					Result:            json.RawMessage(`{}`),
				}),
			},
		},
	}
	exec := New(
		fakeRegistryClient{toolUseID: toolUseID},
		fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
		fakeSpecs{spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		}},
	)

	result, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:     "run",
		SessionID: "session",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, stream.acked)
}

func TestExecutorRegistryErrorsCarryCanonicalRecovery(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "tooluse-example"
		toolCallID = "toolcall-example"
	)
	example := tools.RawJSON(`{"query":"latency"}`)

	cases := []struct {
		name        string
		code        string
		wantKind    planner.FailureKind
		wantAction  planner.RecoveryAction
		wantExample bool
	}{
		{"timeout finishes", "timeout", planner.FailureTimeout, planner.RecoveryFinish, false},
		{"domain rejection replans", "invalid_input", planner.FailureDomainRejection, planner.RecoveryReplan, false},
		{"invalid call carries correction data", "invalid_arguments", planner.FailureInvalidCall, planner.RecoveryCorrectCall, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := &tools.ToolSpec{
				Name:    "atlas.read.get_time_series",
				Toolset: "atlas.read",
				Result:  tools.TypeSpec{},
				Payload: tools.TypeSpec{ExampleJSON: example},
			}
			message := toolregistry.NewToolResultErrorMessage(
				testRegistrationTokenA,
				toolUseID,
				tc.code,
				"boom",
			)
			res, err := executeRegistryResultMessage(t, toolUseID, toolCallID, message, spec)
			require.NoError(t, err)
			require.NotNil(t, res)
			require.NotNil(t, res.ToolResult)
			require.NotNil(t, res.ToolResult.Failure)
			assert.Equal(t, tc.wantKind, res.ToolResult.Failure.Kind)
			assert.Equal(t, tc.wantAction, res.ToolResult.Failure.Recovery.Action)
			if tc.wantExample {
				assert.NotEmpty(t, res.ToolResult.Failure.Recovery.ExampleJSON)
			} else {
				assert.Empty(t, res.ToolResult.Failure.Recovery.ExampleJSON)
			}
		})
	}
}

func TestExecutorPreservesProviderOwnedRecovery(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "tooluse-provider-recovery"
		toolCallID = "toolcall-provider-recovery"
	)
	message := toolregistry.NewToolResultErrorMessage(
		testRegistrationTokenA,
		toolUseID,
		"invalid_input",
		"requested history is unavailable",
	)
	message.Error.Failure.Recovery.Action = planner.RecoveryFinish

	result, err := executeRegistryResultMessage(t, toolUseID, toolCallID, message, &tools.ToolSpec{
		Name:    "atlas.read.get_time_series",
		Toolset: "atlas.read",
		Result:  tools.TypeSpec{},
		Payload: tools.TypeSpec{},
	})

	require.NoError(t, err)
	require.NotNil(t, result.ToolResult)
	require.NotNil(t, result.ToolResult.Failure)
	assert.Equal(t, planner.FailureDomainRejection, result.ToolResult.Failure.Kind)
	assert.Equal(t, planner.RecoveryFinish, result.ToolResult.Failure.Recovery.Action)
}

func TestExecutorPreservesServiceFailureThroughJSONAndEnrichesCorrection(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "tooluse-correctable-service-failure"
		toolCallID = "toolcall-correctable-service-failure"
	)
	issue := &tools.FieldIssue{
		Field:      "query",
		Constraint: "invalid_length",
	}
	message := toolregistry.NewToolResultServiceErrorMessage(
		testRegistrationTokenA,
		toolUseID,
		"atlas.read.get_time_series",
		"invalid_input",
		&correctableServiceError{
			error:  errors.New("query must identify a source"),
			issues: []*tools.FieldIssue{issue},
		},
	)
	result, err := executeRegistryResultMessage(t, toolUseID, toolCallID, message, &tools.ToolSpec{
		Name:    "atlas.read.get_time_series",
		Toolset: "atlas.read",
		Result:  tools.TypeSpec{},
		Payload: tools.TypeSpec{
			ExampleJSON: tools.RawJSON(`{"query":"compressor temperature"}`),
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result.ToolResult)
	failure := result.ToolResult.Failure
	require.NotNil(t, failure)
	assert.Equal(t, planner.FailureDomainRejection, failure.Kind)
	assert.Equal(t, planner.RecoveryCorrectCall, failure.Recovery.Action)
	assert.Equal(t, []*tools.FieldIssue{issue}, failure.Recovery.Issues)
	assert.JSONEq(t, `{}`, string(failure.Recovery.PriorInput))
	assert.JSONEq(t, `{"query":"compressor temperature"}`, string(failure.Recovery.ExampleJSON))
}

func TestExecutorTransportFailureClassifiesToolUnavailable(t *testing.T) {
	t.Parallel()

	spec := &tools.ToolSpec{
		Name:    "atlas.read.get_time_series",
		Toolset: "atlas.read",
		Result:  tools.TypeSpec{},
		Payload: tools.TypeSpec{ExampleJSON: tools.RawJSON(`{"query":"latency"}`)},
	}
	exec := New(
		fakeRegistryClient{err: errors.New("dial registry gateway: connection refused")},
		fakePulseClient{},
		fakeSpecs{spec: spec},
	)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "sess",
		ToolCallID: "toolcall-transport",
	}, &planner.ToolRequest{
		Name:    "atlas.read.get_time_series",
		Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	require.NotNil(t, res.ToolResult.Failure)
	assert.Equal(t, planner.FailureInternal, res.ToolResult.Failure.Kind)
	assert.Equal(t, planner.RecoveryFinish, res.ToolResult.Failure.Recovery.Action)
	assert.Contains(t, res.ToolResult.Failure.Error.Error(), toolregistry.ToolErrorCodeOutcomeUnknown)
	assert.Empty(t, res.ToolResult.Failure.Recovery.ExampleJSON)
	assert.Equal(t, "toolcall-transport", res.ToolResult.ToolCallID)
}

func TestExecutorAllowsRegistryAdmissionDecisionToReachCaller(t *testing.T) {
	t.Parallel()

	var callDeadline time.Time
	before := time.Now()
	exec := New(
		fakeRegistryClient{
			err:          errors.New("registry unavailable"),
			callDeadline: &callDeadline,
		},
		fakePulseClient{},
		fakeSpecs{spec: &tools.ToolSpec{
			Name:    "atlas.read.get_time_series",
			Toolset: "atlas.read",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		}},
	)

	_, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "session",
		ToolCallID: "call",
	}, &planner.ToolRequest{
		Name:    "atlas.read.get_time_series",
		Payload: []byte(`{}`),
	})
	after := time.Now()

	require.NoError(t, err)
	wait := toolregistry.MaxToolCallWait + toolregistry.ResultStreamTransportBudget
	assert.False(t, callDeadline.Before(before.Add(wait)))
	assert.False(t, callDeadline.After(after.Add(wait)))
}

func TestExecutorClassifiesTypedPreAdmissionFailures(t *testing.T) {
	t.Parallel()

	type classificationTest struct {
		name       string
		registry   string
		wantKind   planner.FailureKind
		wantAction planner.RecoveryAction
	}
	transportValidationNames := []string{
		goa.InvalidFieldType,
		goa.MissingField,
		goa.InvalidEnumValue,
		goa.InvalidFormat,
		goa.InvalidPattern,
		goa.InvalidRange,
		goa.InvalidLength,
		goa.UnsupportedMediaType,
		goa.DecodePayload,
		goa.MissingPayload,
	}
	tests := make([]classificationTest, 0, 3+len(transportValidationNames))
	tests = append(tests,
		classificationTest{
			name:       "not admitted replans",
			registry:   "call_not_admitted",
			wantKind:   planner.FailureUnavailable,
			wantAction: planner.RecoveryReplan,
		},
		classificationTest{
			name:       "missing toolset replans",
			registry:   "not_found",
			wantKind:   planner.FailureUnavailable,
			wantAction: planner.RecoveryReplan,
		},
		classificationTest{
			name:       "registry validation is internal",
			registry:   "validation_error",
			wantKind:   planner.FailureInternal,
			wantAction: planner.RecoveryFinish,
		},
	)
	for _, name := range transportValidationNames {
		tests = append(tests, classificationTest{
			name:       name + " is internal",
			registry:   name,
			wantKind:   planner.FailureInternal,
			wantAction: planner.RecoveryFinish,
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			spec := &tools.ToolSpec{
				Name:    "atlas.read.get_time_series",
				Toolset: "atlas.read",
				Result:  tools.TypeSpec{},
				Payload: tools.TypeSpec{},
			}
			exec := New(
				fakeRegistryClient{err: goa.NewServiceError(
					errors.New("registry rejected call"),
					test.registry,
					false,
					false,
					false,
				)},
				fakePulseClient{},
				fakeSpecs{spec: spec},
			)

			result, err := exec.Execute(
				context.Background(),
				&agentsruntime.ToolCallMeta{ToolCallID: "call-1"},
				&planner.ToolRequest{Name: spec.Name, Payload: []byte(`{}`)},
			)

			require.NoError(t, err)
			require.NotNil(t, result.ToolResult.Failure)
			assert.Equal(t, test.wantKind, result.ToolResult.Failure.Kind)
			assert.Equal(t, test.wantAction, result.ToolResult.Failure.Recovery.Action)
			assert.NotContains(t, result.ToolResult.Failure.Error.Error(), toolregistry.ToolErrorCodeOutcomeUnknown)
		})
	}
}

func TestExecutorRejectsNoncanonicalRegistrationToken(t *testing.T) {
	t.Parallel()

	exec := New(
		fakeRegistryClient{
			toolUseID:         "tooluse-invalid-token",
			registrationToken: "ABC123",
		},
		fakePulseClient{},
		fakeSpecs{spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		}},
	)

	result, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:     "run",
		SessionID: "session",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.ToolResult.Failure)
	assert.Equal(t, planner.RecoveryFinish, result.ToolResult.Failure.Recovery.Action)
	assert.Contains(t, result.ToolResult.Failure.Error.Error(), toolregistry.ToolErrorCodeOutcomeUnknown)
}

func TestExecutorDerivesResultStreamIDFromToolUseID(t *testing.T) {
	t.Parallel()

	const (
		toolUseID       = "tooluse-derive-123"
		resultEventName = toolregistry.ResultEventKey
	)
	expectedResultStreamID := toolregistry.ResultStreamID(toolUseID)

	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		},
	}

	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					RegistrationToken: testRegistrationTokenA,
					ToolUseID:         toolUseID,
					Result:            json.RawMessage(`{}`),
				}),
			},
		},
	}
	pc := fakePulseClient{
		streamID: expectedResultStreamID,
		stream:   stream,
	}

	exec := New(fakeRegistryClient{
		toolUseID: toolUseID,
	}, pc, specs)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:     "run",
		SessionID: "sess",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})

	require.NoError(t, err)
	assert.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	assert.Equal(t, tools.Ident("todos.update_todos"), res.ToolResult.Name)
}

func TestExecutorWaitsOnlyThroughExecutionDeadline(t *testing.T) {
	t.Parallel()

	const toolUseID = "tooluse-deadline"
	deadline := time.Now().Add(30 * time.Millisecond).UTC().Truncate(time.Millisecond)
	stream := &fakeStream{t: t, requiredStart: "0", keepOpen: true}
	exec := New(
		fakeRegistryClient{
			toolUseID:         toolUseID,
			executionDeadline: deadline,
		},
		fakePulseClient{
			streamID: toolregistry.ResultStreamID(toolUseID),
			stream:   stream,
		},
		fakeSpecs{spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		}},
	)

	result, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "session",
		ToolCallID: "call",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.ToolResult.Failure)
	assert.Equal(t, planner.RecoveryFinish, result.ToolResult.Failure.Recovery.Action)
	assert.Contains(t, result.ToolResult.Failure.Error.Error(), toolregistry.ToolErrorCodeOutcomeUnknown)
}

func TestExecutorEmitsRegistrySpan(t *testing.T) {
	tracer := &recordingTracer{}
	const (
		toolUseID       = "tooluse-genai-123"
		resultEventName = toolregistry.ResultEventKey
	)
	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		},
	}
	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					RegistrationToken: testRegistrationTokenA,
					ToolUseID:         toolUseID,
					Result:            json.RawMessage(`{}`),
				}),
			},
		},
	}
	exec := New(
		fakeRegistryClient{toolUseID: toolUseID},
		fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
		specs,
		WithTracer(tracer),
	)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "sess",
		TurnID:     "turn",
		ToolCallID: "toolcall-1",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, tracer.spans, 1)
	assert.Equal(t, "toolregistry.execute", tracer.spans[0].name)
	attrs := attrsByKey(tracer.spans[0].attrs)
	assert.Equal(t, "todos.update_todos", attrs[attribute.Key("toolregistry.tool")].AsString())
	assert.Equal(t, "todos.todos", attrs[attribute.Key("toolregistry.toolset")].AsString())
	assert.Equal(t, "toolcall-1", attrs[attribute.Key("toolregistry.tool_call_id")].AsString())
	_, hasConsumerGroup := attrs[attribute.Key("toolregistry.sink")]
	assert.False(t, hasConsumerGroup)
}

type captureSink struct {
	events []aistream.Event
}

type recordingTracer struct {
	spans []*recordingSpan
}

type recordingSpan struct {
	name  string
	attrs []attribute.KeyValue
}

func (s *captureSink) Send(ctx context.Context, event aistream.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *captureSink) Close(ctx context.Context) error {
	return nil
}

func TestExecutorForwardsOnlyExactAdmissionOutputDeltaForReusedToolUseID(t *testing.T) {
	t.Parallel()

	const (
		toolUseID       = "tooluse-123"
		resultStreamID  = "result:" + toolUseID
		resultEventName = toolregistry.ResultEventKey
	)

	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		},
	}

	malformedDelta := toolregistry.NewToolOutputDeltaMessage(
		"ABC123",
		toolUseID,
		"stdout",
		"malformed\n",
	)
	staleDelta := toolregistry.NewToolOutputDeltaMessage(
		testRegistrationTokenB,
		toolUseID,
		"stdout",
		"stale\n",
	)
	delta := toolregistry.NewToolOutputDeltaMessage(
		testRegistrationTokenA,
		toolUseID,
		"stdout",
		"hi\n",
	)
	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: toolregistry.OutputDeltaEventKey,
				Payload:   mustJSON(t, malformedDelta),
			},
			{
				ID:        "2-0",
				EventName: toolregistry.OutputDeltaEventKey,
				Payload:   mustJSON(t, staleDelta),
			},
			{
				ID:        "3-0",
				EventName: toolregistry.OutputDeltaEventKey,
				Payload:   mustJSON(t, delta),
			},
			{
				ID:        "4-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					RegistrationToken: testRegistrationTokenA,
					ToolUseID:         toolUseID,
					Result:            json.RawMessage(`{}`),
				}),
			},
		},
	}
	pc := fakePulseClient{
		streamID: resultStreamID,
		stream:   stream,
	}

	sink := &captureSink{}
	exec := New(
		fakeRegistryClient{
			toolUseID: toolUseID,
		},
		pc,
		specs,
		WithStreamSink(sink),
	)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "sess",
		ToolCallID: "toolcall-1",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, sink.events, 1)
	assert.Empty(t, stream.acked)
	ev, ok := sink.events[0].(aistream.ToolOutputDelta)
	require.True(t, ok)
	assert.Equal(t, aistream.EventToolOutputDelta, ev.Type())
	assert.Equal(t, "run", ev.RunID())
	assert.Equal(t, "sess", ev.SessionID())
	assert.Equal(t, "toolcall-1", ev.Data.ToolCallID)
	assert.Equal(t, "stdout", ev.Data.Stream)
	assert.Equal(t, "hi\n", ev.Data.Delta)
}

func TestExecutorRestoresBoundsFromRegistryMessage(t *testing.T) {
	t.Parallel()

	const (
		toolUseID       = "tooluse-123"
		resultStreamID  = "result:" + toolUseID
		resultEventName = toolregistry.ResultEventKey
	)
	nextCursor := "cursor-2"

	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "atlas.read.list_devices",
			Toolset: "atlas.read",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
			Bounds: &tools.BoundsSpec{
				Paging: &tools.PagingSpec{
					CursorField:     "cursor",
					NextCursorField: "next_cursor",
				},
			},
		},
	}

	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					RegistrationToken: testRegistrationTokenA,
					ToolUseID:         toolUseID,
					Result:            json.RawMessage(`{}`),
					Bounds: &agent.Bounds{
						Returned:       1,
						Truncated:      true,
						NextCursor:     &nextCursor,
						RefinementHint: "narrow by device",
					},
				}),
			},
		},
	}
	pc := fakePulseClient{
		streamID: resultStreamID,
		stream:   stream,
	}

	exec := New(
		fakeRegistryClient{
			toolUseID: toolUseID,
		},
		pc,
		specs,
	)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:     "run",
		SessionID: "sess",
	}, &planner.ToolRequest{
		Name:    "atlas.read.list_devices",
		Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	require.NotNil(t, res.ToolResult.Bounds)
	assert.Equal(t, 1, res.ToolResult.Bounds.Returned)
	assert.True(t, res.ToolResult.Bounds.Truncated)
	require.NotNil(t, res.ToolResult.Bounds.NextCursor)
	assert.Equal(t, nextCursor, *res.ToolResult.Bounds.NextCursor)
	assert.Equal(t, "narrow by device", res.ToolResult.Bounds.RefinementHint)
}

func TestExecutorRejectsInvalidRegistryErrorResults(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "tooluse-invalid"
		toolCallID = "toolcall-invalid"
	)
	cases := []struct {
		name string
		msg  toolregistry.ToolResultMessage
		want string
	}{
		{
			name: "error and bounds",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Error: toolregistry.NewToolResultErrorMessage(
					testRegistrationTokenA,
					toolUseID,
					"execution_failed",
					"failed",
				).Error,
				Bounds: &agent.Bounds{Returned: 1},
			},
			want: "error and bounds are both set",
		},
		{
			name: "error and result",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Error: toolregistry.NewToolResultErrorMessage(
					testRegistrationTokenA,
					toolUseID,
					"execution_failed",
					"failed",
				).Error,
				Result: json.RawMessage(`{"ok":true}`),
			},
			want: "error and result are both set",
		},
		{
			name: "error missing failure",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Error:             &toolregistry.ToolError{},
			},
			want: "error is missing failure contract",
		},
		{
			name: "failure missing error chain",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Error: &toolregistry.ToolError{
					Code: "execution_failed",
					Failure: &planner.ToolFailure{
						Kind:     planner.FailureInternal,
						Recovery: planner.RecoveryDirective{Action: planner.RecoveryFinish},
					},
				},
			},
			want: "failure error is invalid",
		},
		{
			name: "failure unknown kind",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Error: &toolregistry.ToolError{
					Code: "execution_failed",
					Failure: &planner.ToolFailure{
						Kind:  "unknown",
						Error: planner.NewToolError("failed"),
						Recovery: planner.RecoveryDirective{
							Action: planner.RecoveryFinish,
						},
					},
				},
			},
			want: `unknown failure kind "unknown"`,
		},
		{
			name: "failure issues on replan",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Error: &toolregistry.ToolError{
					Code: "execution_failed",
					Failure: &planner.ToolFailure{
						Kind:  planner.FailureUnavailable,
						Error: planner.NewToolError("failed"),
						Recovery: planner.RecoveryDirective{
							Action: planner.RecoveryReplan,
							Issues: []*tools.FieldIssue{{
								Field:      "query",
								Constraint: "missing_field",
							}},
						},
					},
				},
			},
			want: `recovery "replan" cannot carry correction data`,
		},
		{
			name: "error and server data",
			msg: toolregistry.ToolResultMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         toolUseID,
				Error: toolregistry.NewToolResultErrorMessage(
					testRegistrationTokenA,
					toolUseID,
					"execution_failed",
					"failed",
				).Error,
				ServerData: []*toolregistry.ServerDataItem{{
					Kind: "test",
					Data: json.RawMessage(`{}`),
				}},
			},
			want: "error and server data are both set",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := executeRegistryResultMessage(t, toolUseID, toolCallID, tc.msg, &tools.ToolSpec{
				Name:    "atlas.read.list_devices",
				Toolset: "atlas.read",
				Result:  tools.TypeSpec{},
				Payload: tools.TypeSpec{},
			})

			require.Error(t, err)
			assert.Nil(t, res)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "tool_call_id="+toolCallID)
			assert.Contains(t, err.Error(), "tool_use_id="+toolUseID)
		})
	}
}

func TestExecutorResultDecodeFailureReturnsModelVisibleErrorWithoutBounds(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "tooluse-decode"
		toolCallID = "toolcall-decode"
	)
	nextCursor := "cursor-2"
	spec := &tools.ToolSpec{
		Name:    "atlas.read.list_devices",
		Toolset: "atlas.read",
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				FromJSON: func(data []byte) (any, error) {
					return nil, errors.New("invalid enum value \"retired\"")
				},
			},
		},
		Payload: tools.TypeSpec{},
		Bounds: &tools.BoundsSpec{
			Paging: &tools.PagingSpec{
				CursorField:     "cursor",
				NextCursorField: "next_cursor",
			},
		},
	}

	res, err := executeRegistryResultMessage(t, toolUseID, toolCallID, toolregistry.ToolResultMessage{
		RegistrationToken: testRegistrationTokenA,
		ToolUseID:         toolUseID,
		Result:            json.RawMessage(`{"status":"retired"}`),
		Bounds: &agent.Bounds{
			Returned:   1,
			Truncated:  true,
			NextCursor: &nextCursor,
		},
		ServerData: []*toolregistry.ServerDataItem{{
			Kind:     "atlas.devices",
			Audience: "internal",
			Data:     json.RawMessage(`{"count":1}`),
		}},
	}, spec)

	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	assert.Nil(t, res.ToolResult.Bounds)
	assert.Nil(t, res.ToolResult.ServerData)
	require.NotNil(t, res.ToolResult.Failure)
	assert.Contains(t, res.ToolResult.Failure.Error.Error(), "invalid enum value \"retired\"")
	assert.Equal(t, planner.FailureMalformedResult, res.ToolResult.Failure.Kind)
	assert.Equal(t, planner.RecoveryFinish, res.ToolResult.Failure.Recovery.Action)
}

func TestExecutorInvalidServerDataFailsWholeResult(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "tooluse-invalid-server-data"
		toolCallID = "toolcall-invalid-server-data"
	)
	spec := &tools.ToolSpec{
		Name:    "atlas.read.get_diagram",
		Toolset: "atlas.read",
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				FromJSON: func(data []byte) (any, error) {
					var result map[string]any
					err := json.Unmarshal(data, &result)
					return result, err
				},
			},
		},
		CanonicalizeServerData: func(rawjson.Message) (rawjson.Message, error) {
			return nil, errors.New("diagram server data is invalid")
		},
	}

	res, err := executeRegistryResultMessage(t, toolUseID, toolCallID, toolregistry.ToolResultMessage{
		RegistrationToken: testRegistrationTokenA,
		ToolUseID:         toolUseID,
		Result:            json.RawMessage(`{"status":"ready"}`),
		Bounds: &agent.Bounds{
			Returned: 1,
		},
		ServerData: []*toolregistry.ServerDataItem{{
			Kind:     "atlas.diagram",
			Audience: "timeline",
			Data:     json.RawMessage(`{"legacy":true}`),
		}},
	}, spec)

	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	assert.Nil(t, res.ToolResult.Result)
	assert.Nil(t, res.ToolResult.Bounds)
	assert.Nil(t, res.ToolResult.ServerData)
	require.NotNil(t, res.ToolResult.Failure)
	assert.Equal(t, planner.FailureMalformedResult, res.ToolResult.Failure.Kind)
	assert.Equal(t, planner.RecoveryFinish, res.ToolResult.Failure.Recovery.Action)
	assert.ErrorContains(t, res.ToolResult.Failure.Error, "diagram server data is invalid")
}

func TestValidateServerDataEnforcesGeneratedContract(t *testing.T) {
	t.Parallel()

	type sidecar struct {
		Value string `json:"value"`
	}
	codec := tools.JSONCodec[any]{
		FromJSON: func(data []byte) (any, error) {
			var result sidecar
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&result); err != nil {
				return nil, err
			}
			if result.Value == "" {
				return nil, errors.New("value is required")
			}
			return result, nil
		},
		ToJSON: json.Marshal,
	}
	canonicalize := func(data rawjson.Message) (rawjson.Message, error) {
		return toolserverdata.Canonicalize(
			data,
			func(kind, audience string, data rawjson.Message) (string, rawjson.Message, error) {
				if kind != "atlas.diagram" {
					return "", nil, fmt.Errorf("server data kind %q is not declared by tool", kind)
				}
				if audience != string(tools.AudienceTimeline) {
					return "", nil, fmt.Errorf(
						"server data kind %q has audience %q, expected %q",
						kind,
						audience,
						tools.AudienceTimeline,
					)
				}
				value, err := codec.FromJSON(data)
				if err != nil {
					return "", nil, err
				}
				canonical, err := codec.ToJSON(value)
				if err != nil {
					return "", nil, err
				}
				return string(tools.AudienceTimeline), rawjson.Message(canonical), nil
			},
		)
	}
	apply := func(items []*toolregistry.ServerDataItem) (rawjson.Message, error) {
		data, err := toolregistry.EncodeServerData(items)
		if err != nil {
			return nil, err
		}
		return toolserverdata.Apply(canonicalize, rawjson.Message(data))
	}

	raw, err := apply([]*toolregistry.ServerDataItem{{
		Kind:     "atlas.diagram",
		Audience: string(tools.AudienceTimeline),
		Data:     json.RawMessage(`{ "value": "ready" }`),
	}})
	require.NoError(t, err)
	assert.JSONEq(t, `[{"kind":"atlas.diagram","audience":"timeline","data":{"value":"ready"}}]`, string(raw))

	tests := []struct {
		name  string
		items []*toolregistry.ServerDataItem
		want  string
	}{
		{
			name: "unknown kind",
			items: []*toolregistry.ServerDataItem{{
				Kind:     "atlas.unknown",
				Audience: "timeline",
				Data:     json.RawMessage(`{"value":"ready"}`),
			}},
			want: "is not declared",
		},
		{
			name: "wrong audience",
			items: []*toolregistry.ServerDataItem{{
				Kind:     "atlas.diagram",
				Audience: "internal",
				Data:     json.RawMessage(`{"value":"ready"}`),
			}},
			want: `audience "internal", expected "timeline"`,
		},
		{
			name: "unknown field",
			items: []*toolregistry.ServerDataItem{{
				Kind:     "atlas.diagram",
				Audience: "timeline",
				Data:     json.RawMessage(`{"value":"ready","legacy":true}`),
			}},
			want: `unknown field "legacy"`,
		},
		{
			name: "duplicate kind",
			items: []*toolregistry.ServerDataItem{
				{
					Kind:     "atlas.diagram",
					Audience: "timeline",
					Data:     json.RawMessage(`{"value":"ready"}`),
				},
				{
					Kind:     "atlas.diagram",
					Audience: "timeline",
					Data:     json.RawMessage(`{"value":"ready"}`),
				},
			},
			want: "appears more than once",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := apply(test.items)
			require.Error(t, err)
			assert.ErrorContains(t, err, test.want)
		})
	}
}

type fakeRegistryClient struct {
	toolUseID          string
	registrationToken  string
	executionDeadline  time.Time
	resultStreamTTL    time.Duration
	err                error
	callDeadline       *time.Time
	calls              *atomic.Int64
	retryExpectedToken *string
}

func (c fakeRegistryClient) CallTool(
	ctx context.Context,
	_ string,
	_ tools.Ident,
	_ []byte,
	_ toolregistry.ToolCallMeta,
) (toolregistry.ToolCallRef, error) {
	if c.callDeadline != nil {
		*c.callDeadline, _ = ctx.Deadline()
	}
	if c.calls != nil {
		c.calls.Add(1)
	}
	if c.err != nil {
		return toolregistry.ToolCallRef{}, c.err
	}
	registrationToken := c.registrationToken
	if registrationToken == "" {
		registrationToken = testRegistrationTokenA
	}
	executionDeadline := c.executionDeadline
	if executionDeadline.IsZero() {
		executionDeadline = testResultStreamExpiration(toolregistry.MaxToolCallWait)
	}
	resultStreamTTL := c.resultStreamTTL
	if resultStreamTTL == 0 {
		resultStreamTTL = toolregistry.DefaultResultStreamTTL
	}
	return toolregistry.ToolCallRef{
		ToolUseID:             c.toolUseID,
		RegistrationToken:     registrationToken,
		ExecutionDeadline:     executionDeadline,
		ResultStreamExpiresAt: testResultStreamExpiration(resultStreamTTL),
	}, nil
}

func (c fakeRegistryClient) RetryTool(
	_ context.Context,
	_ string,
	_ tools.Ident,
	_ []byte,
	_ toolregistry.ToolCallMeta,
	expectedRegistrationToken string,
) (toolregistry.ToolCallRef, error) {
	if c.calls != nil {
		c.calls.Add(1)
	}
	if c.retryExpectedToken != nil {
		*c.retryExpectedToken = expectedRegistrationToken
	}
	if c.err != nil {
		return toolregistry.ToolCallRef{}, c.err
	}
	resultStreamTTL := c.resultStreamTTL
	if resultStreamTTL == 0 {
		resultStreamTTL = toolregistry.DefaultResultStreamTTL
	}
	executionDeadline := c.executionDeadline
	if executionDeadline.IsZero() {
		executionDeadline = testResultStreamExpiration(toolregistry.MaxToolCallWait)
	}
	return toolregistry.ToolCallRef{
		ToolUseID:             c.toolUseID,
		RegistrationToken:     expectedRegistrationToken,
		ExecutionDeadline:     executionDeadline,
		ResultStreamExpiresAt: testResultStreamExpiration(resultStreamTTL),
	}, nil
}

func testResultStreamExpiration(retention time.Duration) time.Time {
	return time.Now().UTC().Truncate(time.Minute).Add(retention)
}

type fakeSpecs struct {
	spec *tools.ToolSpec
}

func (s fakeSpecs) Spec(name tools.Ident) (*tools.ToolSpec, bool) {
	if s.spec == nil {
		return nil, false
	}
	if s.spec.Name != name {
		return nil, false
	}
	return s.spec, true
}

type fakePulseClient struct {
	streamID string
	stream   pulse.Stream
}

func (c fakePulseClient) Stream(name string, opts ...streamopts.Stream) (pulse.Stream, error) {
	if name != c.streamID {
		return nil, assert.AnError
	}
	parsed := streamopts.ParseStreamOptions(opts...)
	assert.True(c.stream.(*fakeStream).t, parsed.DeadlineSet)
	assert.True(c.stream.(*fakeStream).t, parsed.MaxLenSet)
	assert.Equal(c.stream.(*fakeStream).t, toolregistry.ResultStreamMaxLen, parsed.MaxLen)
	assert.False(c.stream.(*fakeStream).t, parsed.Unbounded)
	assert.WithinDuration(
		c.stream.(*fakeStream).t,
		testResultStreamExpiration(toolregistry.DefaultResultStreamTTL),
		parsed.Deadline,
		time.Second,
	)
	return c.stream, nil
}

func (c fakePulseClient) Close(ctx context.Context) error {
	return nil
}

type fakeStream struct {
	t             *testing.T
	requiredStart string
	events        []*streaming.Event
	acked         []*streaming.Event
	destroyed     bool
	keepOpen      bool
}

func (s *fakeStream) Add(ctx context.Context, event string, payload []byte) (string, error) {
	return "", assert.AnError
}

func (s *fakeStream) Snapshot(context.Context) ([]pulse.SnapshotEvent, error) {
	return nil, nil
}

func (s *fakeStream) Open(context.Context) error {
	s.t.Helper()
	s.t.Error("executor must not establish registry-owned result streams")
	return assert.AnError
}

func (s *fakeStream) NewSink(ctx context.Context, name string, opts ...streamopts.Sink) (pulse.Sink, error) {
	o := streamopts.ParseSinkOptions(opts...)
	assert.Equal(s.t, s.requiredStart, o.LastEventID)
	return &fakeSink{events: s.events, acked: &s.acked}, nil
}

func (s *fakeStream) NewReader(ctx context.Context, opts ...streamopts.Reader) (pulse.Reader, error) {
	o := streamopts.ParseReaderOptions(opts...)
	assert.Equal(s.t, s.requiredStart, o.LastEventID)
	return &fakeReader{events: s.events, keepOpen: s.keepOpen}, nil
}

func (s *fakeStream) EnsureGroup(context.Context, string) error { return nil }

func (s *fakeStream) Destroy(ctx context.Context) error {
	s.destroyed = true
	return nil
}

type fakeSink struct {
	events []*streaming.Event
	acked  *[]*streaming.Event
}

type fakeReader struct {
	events   []*streaming.Event
	keepOpen bool
}

func (r *fakeReader) Subscribe() <-chan *streaming.Event {
	ch := make(chan *streaming.Event, len(r.events))
	for _, event := range r.events {
		ch <- event
	}
	if !r.keepOpen {
		close(ch)
	}
	return ch
}

func (r *fakeReader) Close() {}

func (s *fakeSink) Subscribe() <-chan *streaming.Event {
	ch := make(chan *streaming.Event, len(s.events))
	for _, ev := range s.events {
		ch <- ev
	}
	close(ch)
	return ch
}

func (s *fakeSink) Ack(ctx context.Context, ev *streaming.Event) error {
	*s.acked = append(*s.acked, ev)
	return nil
}

func (s *fakeSink) Close(ctx context.Context) error { return nil }

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func executeRegistryResultMessage(
	t *testing.T,
	toolUseID string,
	toolCallID string,
	msg toolregistry.ToolResultMessage,
	spec *tools.ToolSpec,
) (*agentsruntime.ToolExecutionResult, error) {
	t.Helper()

	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: toolregistry.ResultEventKey,
				Payload:   mustJSON(t, msg),
			},
		},
	}
	exec := New(
		fakeRegistryClient{toolUseID: toolUseID},
		fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
		fakeSpecs{spec: spec},
	)
	return exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "sess",
		ToolCallID: toolCallID,
	}, &planner.ToolRequest{
		Name:    spec.Name,
		Payload: []byte(`{}`),
	})
}

func (t *recordingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	span := &recordingSpan{
		name:  name,
		attrs: cfg.Attributes(),
	}
	t.spans = append(t.spans, span)
	return ctx, span
}

func (t *recordingTracer) Span(context.Context) telemetry.Span {
	if len(t.spans) == 0 {
		return &recordingSpan{}
	}
	return t.spans[len(t.spans)-1]
}

func (s *recordingSpan) End(...trace.SpanEndOption) {}

func (s *recordingSpan) AddEvent(string, ...any) {}

func (s *recordingSpan) SetAttributes(attrs ...attribute.KeyValue) {
	s.attrs = append(s.attrs, attrs...)
}

func (s *recordingSpan) SetStatus(codes.Code, string) {}

func (s *recordingSpan) RecordError(error, ...trace.EventOption) {}

func attrsByKey(attrs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	out := make(map[attribute.Key]attribute.Value, len(attrs))
	for _, attr := range attrs {
		out[attr.Key] = attr.Value
	}
	return out
}
