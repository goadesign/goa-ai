// These tests verify that the model stream boundary owns mutable provider data
// and applies its contract exactly once before exposing a clean EOF.
package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/internal/modelcall"
	"goa.design/goa-ai/runtime/agent/tools"
)

type validatedStreamFixture struct {
	chunks      []Chunk
	response    *Response
	next        int
	closed      bool
	recvErr     error
	closeErr    error
	closeCalled chan struct{}
	closeCalls  int
}

type recordingStreamObserver struct {
	chunks       []Chunk
	observations []StreamObservation
	closeErr     error
	closeCalls   int
}

type mutatingStreamObserver struct{}

type typedNilStreamFixture struct{}

type cancellationBlockedStreamFixture struct {
	ctx            context.Context
	recvStarted    chan struct{}
	cleanupStarted chan struct{}
	cleanupRelease chan struct{}
	closeCalled    chan struct{}
	closeCalls     int
	closeErr       error
}

type blockingCloseStreamFixture struct {
	recvErr      error
	closeErr     error
	closeStarted chan struct{}
	closeRelease chan struct{}
	closeCalls   int
}

type siblingStreamFixture struct {
	next       int
	secondRecv chan struct{}
}

type blockingRejectingStreamObserver struct {
	firstEntered chan struct{}
	releaseFirst chan struct{}
	rejectErr    error
	calls        atomic.Int32
}

type failingReceiveObserver struct {
	err error
}

func (*typedNilStreamFixture) Recv() (Chunk, error) {
	panic("typed-nil stream Recv called")
}

func (*typedNilStreamFixture) Response() *Response {
	panic("typed-nil stream Response called")
}

func (*typedNilStreamFixture) Close() error {
	panic("typed-nil stream Close called")
}

func (o *failingReceiveObserver) ObserveStreamRecv(StreamObservation) error {
	return o.err
}

func (*failingReceiveObserver) ObserveStreamClose(error) error {
	return nil
}

func TestValidatedStreamObserversReceiveFrozenSourceInEveryOrder(t *testing.T) {
	tests := []struct {
		name         string
		observerErr  error
		failingFirst bool
	}{
		{name: "ordinary error first", observerErr: errors.New("observer failed"), failingFirst: true},
		{name: "ordinary error last", observerErr: errors.New("observer failed")},
		{name: "EOF first", observerErr: io.EOF, failingFirst: true},
		{name: "EOF last", observerErr: io.EOF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := canonicalTextResponse()
			contract, err := NewRequestContract(&Request{})
			require.NoError(t, err)
			stream, err := contract.ValidateStream(&validatedStreamFixture{
				chunks:   []Chunk{TextChunk{Message: response.Content[0]}},
				response: response,
			})
			require.NoError(t, err)
			recording := &recordingStreamObserver{}
			failing := &failingReceiveObserver{err: test.observerErr}
			if test.failingFirst {
				stream, err = stream.Observe(failing)
				require.NoError(t, err)
				stream, err = stream.Observe(recording)
			} else {
				stream, err = stream.Observe(recording)
				require.NoError(t, err)
				stream, err = stream.Observe(failing)
			}
			require.NoError(t, err)

			_, err = stream.Recv()

			require.ErrorIs(t, err, test.observerErr)
			require.False(t, modelcall.Exact(err, io.EOF))
			require.Len(t, recording.observations, 1)
			require.NoError(t, recording.observations[0].Err)
			require.NoError(t, stream.Close())
		})
	}
}

func (s *cancellationBlockedStreamFixture) Recv() (Chunk, error) {
	close(s.recvStarted)
	<-s.ctx.Done()
	close(s.cleanupStarted)
	<-s.cleanupRelease
	return nil, s.ctx.Err()
}

func (*cancellationBlockedStreamFixture) Response() *Response {
	return nil
}

func (s *cancellationBlockedStreamFixture) Close() error {
	s.closeCalls++
	close(s.closeCalled)
	return s.closeErr
}

func (s *blockingCloseStreamFixture) Recv() (Chunk, error) {
	return nil, s.recvErr
}

func (*blockingCloseStreamFixture) Response() *Response {
	return nil
}

func (s *blockingCloseStreamFixture) Close() error {
	s.closeCalls++
	close(s.closeStarted)
	<-s.closeRelease
	return s.closeErr
}

func TestStreamBoundariesRejectTypedNilWithoutMethodCalls(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	var raw *typedNilStreamFixture
	var validated *ValidatedStream

	for name, stream := range map[string]Streamer{
		"provider pointer": raw,
		"validated stream": validated,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := contract.ValidateStream(stream)
			require.Nil(t, got)
			require.ErrorContains(t, outputValidationCause(t, err), "typed nil")
		})
	}
}

func TestZeroValidatedStreamReturnsContractErrors(t *testing.T) {
	stream := &ValidatedStream{}

	chunk, err := stream.Recv()
	require.Nil(t, chunk)
	require.ErrorContains(t, err, "not an intact validated stream")
	require.Nil(t, stream.Response())
	require.ErrorContains(t, stream.Close(), "not an intact validated stream")
	require.ErrorContains(t, stream.Finalize(nil), "not an intact validated stream")
	observed, err := stream.Observe(&recordingStreamObserver{})
	require.Nil(t, observed)
	require.ErrorContains(t, err, "not an intact validated stream")
}

func TestValidatedStreamRejectsObserverAfterUseStarts(t *testing.T) {
	stream := mustValidateStream(t, &validatedStreamFixture{
		chunks: []Chunk{TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "visible"}},
		}}},
	}, &Request{})
	_, err := stream.Recv()
	require.NoError(t, err)

	observed, err := stream.Observe(&recordingStreamObserver{})

	require.Nil(t, observed)
	require.ErrorContains(t, err, "before receive or close")
}

func TestRequestContractRejectsInvalidRequestIdentity(t *testing.T) {
	contract, err := NewRequestContract(&Request{
		Model: string(make([]byte, maxTokenUsageModelBytes+1)),
	})

	require.Nil(t, contract)
	require.ErrorContains(t, err, "model request identity")
	require.ErrorContains(t, err, "token usage model exceeds")
}

func TestValidatedStreamRetainsImmutableToolCallEvidence(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			ToolCallChunk{ToolCall: ToolCall{
				Name:    "search",
				Payload: []byte(`{"query":"original"}`),
				ID:      "call-1",
			}},
			StopChunk{Reason: "tool_use"},
		},
		response: &Response{
			Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID:    "call-1",
					Name:  "search",
					Input: []byte(`{"query":"original"}`),
				}},
			}},
			StopReason: "tool_use",
		},
	}
	stream := mustValidateStream(t, raw, requestWithTool("search"))

	chunk, err := stream.Recv()
	require.NoError(t, err)
	toolCall := chunk.(ToolCallChunk)
	providerChunk := raw.chunks[0].(ToolCallChunk)
	providerChunk.ToolCall.Payload[10] = 'Y'
	require.JSONEq(t, `{"query":"original"}`, string(toolCall.ToolCall.Payload))
	toolCall.ToolCall.Payload[10] = 'X'

	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
	providerPart := raw.response.Content[0].Parts[0].(ToolUsePart)
	providerPart.Input[10] = 'Z'
	response := stream.Response()
	require.NotNil(t, response)
	require.JSONEq(t, `{"query":"original"}`, string(response.Content[0].Parts[0].(ToolUsePart).Input))
}

func TestNestedValidatedStreamGeneratedCorrectionRetainsUsageWithoutExposingResponse(t *testing.T) {
	call := ToolCall{
		Name:    "catalog.lookup",
		Payload: []byte(`{"query":42,"private":"submitted-secret"}`),
		ID:      "call-1",
	}
	usage := TokenUsage{
		Model:        "provider-model",
		InputTokens:  11,
		OutputTokens: 7,
		TotalTokens:  18,
	}
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			ToolCallDeltaChunk{Delta: ToolCallDelta{
				Name:  call.Name,
				ID:    call.ID,
				Delta: string(call.Payload),
			}},
			ToolCallChunk{ToolCall: call},
			UsageChunk{Usage: usage},
			StopChunk{Reason: "tool_use"},
		},
		response: &Response{
			Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID:    call.ID,
					Name:  call.Name.String(),
					Input: call.Payload,
				}},
			}},
			Usage:      usage,
			StopReason: "tool_use",
		},
	}
	validated := mustValidateStream(t, raw, &Request{
		Tools: []*ToolDefinition{generatedRejectingTool(&tools.FieldIssue{
			Field:            "query",
			Constraint:       "invalid_field_type",
			ExpectedJSONType: "string",
			ActualJSONType:   "number",
		})},
	})
	nested := mustValidateStream(t, validated, &Request{})
	observer := &recordingStreamObserver{}
	stream, err := nested.Observe(observer)
	require.NoError(t, err)

	chunk, err := stream.Recv()

	require.Nil(t, chunk)
	var outputErr *OutputValidationError
	require.ErrorAs(t, err, &outputErr)
	require.Contains(t, outputErr.RecoveryCorrection(), `Field "query" must contain a JSON string.`)
	require.NotContains(t, outputErr.RecoveryCorrection(), "submitted-secret")
	require.Equal(t, &usage, outputErr.Usage())
	require.Nil(t, stream.Response())
	require.Len(t, observer.observations, 1)
	observation := observer.observations[0]
	require.Nil(t, observation.Chunk)
	require.Nil(t, observation.Response)
	require.True(t, observation.ResponseEvidence.Present)
	require.NotEmpty(t, observation.ResponseEvidence.SHA256)
	require.Nil(t, observation.RejectedUsageDelta)
	require.Equal(t, &usage, observation.RejectedUsageTotal)
	require.NotContains(t, observation.Err.Error(), "submitted-secret")
	secondChunk, secondErr := stream.Recv()
	require.Nil(t, secondChunk)
	require.ErrorIs(t, secondErr, outputErr)
}

func TestValidatedStreamDiscardsCompletedToolCallAfterLaterProviderRejection(t *testing.T) {
	contract, err := NewRequestContract(requestWithTool("search"))
	require.NoError(t, err)
	rejection := contract.RejectProviderOutput(
		&TokenUsage{InputTokens: 4, OutputTokens: 3, TotalTokens: 7},
		NewUnadvertisedToolNameError("near_search"),
	)
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: "preview"}},
			}},
			ToolCallChunk{ToolCall: ToolCall{
				Name:    "search",
				Payload: []byte(`{"query":"discarded"}`),
				ID:      "call-1",
			}},
			UsageChunk{Usage: TokenUsage{InputTokens: 4, OutputTokens: 3, TotalTokens: 7}},
		},
		recvErr: rejection,
	}
	stream, err := contract.ValidateStream(raw)
	require.NoError(t, err)

	chunk, err := stream.Recv()
	require.NoError(t, err)
	require.IsType(t, TextChunk{}, chunk)
	chunk, err = stream.Recv()

	require.Nil(t, chunk)
	require.ErrorIs(t, err, rejection)
	require.Nil(t, stream.Response())
	name, ok := UnadvertisedToolName(err)
	require.True(t, ok)
	require.Equal(t, "near_search", name)
}

func TestValidatedStreamSharesBudgetAcrossChunks(t *testing.T) {
	text := strings.Repeat("x", maxDynamicValueBytes/2)
	raw := &validatedStreamFixture{chunks: []Chunk{
		TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: text}},
		}},
		TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: text}},
		}},
	}}
	stream := mustValidateStream(t, raw, &Request{})

	_, err := stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()

	require.ErrorContains(t, outputValidationCause(t, err), "exceeds maximum byte size")
}

func TestValidatedStreamBoundsToolDeltaAccumulator(t *testing.T) {
	delta := strings.Repeat("x", maxDynamicValueBytes/2)
	raw := &validatedStreamFixture{chunks: []Chunk{
		ToolCallDeltaChunk{Delta: ToolCallDelta{Name: "search", ID: "call-1", Delta: delta}},
		ToolCallDeltaChunk{Delta: ToolCallDelta{Name: "search", ID: "call-1", Delta: delta}},
	}}
	stream := mustValidateStream(t, raw, requestWithTool("search"))
	inner := stream.core.inner.(*validatedStreamer)

	_, err := stream.Recv()

	require.ErrorContains(t, outputValidationCause(t, err), "exceeds maximum byte size")
	require.Equal(t, len(delta), inner.validator.toolDeltaPayloads["call-1"].Len())
}

func TestValidatedStreamDoesNotDoubleChargeFinalizedToolDeltas(t *testing.T) {
	payload := []byte(`{"value":"` + strings.Repeat("x", maxDynamicValueBytes/2) + `"}`)
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			ToolCallDeltaChunk{Delta: ToolCallDelta{
				Name:  "search",
				ID:    "call-1",
				Delta: string(payload),
			}},
			ToolCallChunk{ToolCall: ToolCall{
				Name:    "search",
				ID:      "call-1",
				Payload: payload,
			}},
			StopChunk{Reason: "tool_use"},
		},
		response: &Response{
			Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID:    "call-1",
					Name:  "search",
					Input: payload,
				}},
			}},
			StopReason: "tool_use",
		},
	}
	stream := mustValidateStream(t, raw, requestWithTool("search"))

	for _, expected := range raw.chunks {
		chunk, err := stream.Recv()
		require.NoError(t, err)
		require.Equal(t, expected, chunk)
	}
	_, err := stream.Recv()

	require.ErrorIs(t, err, io.EOF)
}

func TestValidatedStreamSharesVisitBudgetAcrossChunks(t *testing.T) {
	chunks := make([]Chunk, maxDynamicValueVisits+1)
	for index := range chunks {
		chunks[index] = UsageChunk{}
	}
	stream := mustValidateStream(t, &validatedStreamFixture{chunks: chunks}, &Request{})

	for range maxDynamicValueVisits {
		_, err := stream.Recv()
		require.NoError(t, err)
	}
	_, err := stream.Recv()

	require.ErrorContains(t, outputValidationCause(t, err), "exceeds maximum visited values")
}

func TestValidatedStreamDoesNotDoubleChargeTerminalResponse(t *testing.T) {
	text := strings.Repeat("x", maxDynamicValueBytes/2)
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: text}},
			}},
			StopChunk{Reason: "stop"},
		},
		response: &Response{
			Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: text}},
			}},
			StopReason: "stop",
		},
	}
	stream := mustValidateStream(t, raw, &Request{})

	_, err := stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()

	require.ErrorIs(t, err, io.EOF)
}

func TestValidatedStreamChargesMismatchedTerminalDataBeforeCopy(t *testing.T) {
	chunkText := strings.Repeat("x", maxDynamicValueBytes/2)
	responseText := strings.Repeat("y", maxDynamicValueBytes/2)
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: chunkText}},
			}},
			StopChunk{Reason: "stop"},
		},
		response: &Response{
			Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: responseText}},
			}},
			StopReason: "stop",
		},
	}
	stream := mustValidateStream(t, raw, &Request{})
	_, err := stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()

	require.ErrorContains(t, outputValidationCause(t, err), "exceeds maximum byte size")
	require.Nil(t, stream.Response())
}

func TestValidatedStreamRejectsPrematureCompletionStop(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{StopChunk{Reason: "stop"}},
	}
	stream := mustValidateStream(t, raw, &Request{
		StructuredOutput: &StructuredOutput{
			Name:   "answer",
			Schema: []byte(`{"type":"object"}`),
		},
	})

	_, firstErr := stream.Recv()
	_, secondErr := stream.Recv()

	var validationErr *OutputValidationError
	require.ErrorAs(t, firstErr, &validationErr)
	require.ErrorContains(t, errors.Unwrap(validationErr), "structured output stream stopped before a completion")
	require.Same(t, firstErr, secondErr)
	require.True(t, IsStreamValidationError(firstErr))
}

func TestIsStreamValidationErrorReportsValidationInMixedTree(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))

	require.True(t, IsStreamValidationError(errors.Join(
		fmt.Errorf("wrapped validation: %w", validationErr),
		errors.New("independent failure"),
	)))
}

func TestValidatedStreamObserveUsesExactSource(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: "validated"}},
			}},
			StopChunk{Reason: "stop"},
		},
		response: &Response{
			Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: "validated"}},
			}},
			StopReason: "stop",
		},
	}
	validated := mustValidateStream(t, raw, &Request{})
	observer := &recordingStreamObserver{}
	observed, err := validated.Observe(observer)
	require.NoError(t, err)

	chunk, err := observed.Recv()

	require.NoError(t, err)
	require.Equal(t, "validated", chunk.(TextChunk).Message.Parts[0].(TextPart).Text)
	require.Equal(t, []Chunk{chunk}, observer.chunks)
}

func TestValidatedStreamObserveDoesNotExposeMutableOutput(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			ToolCallChunk{ToolCall: ToolCall{
				Name:    "search",
				Payload: []byte(`{"query":"original"}`),
				ID:      "call-1",
			}},
			StopChunk{Reason: "tool_use"},
		},
		response: &Response{
			Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID:    "call-1",
					Name:  "search",
					Input: []byte(`{"query":"original"}`),
				}},
			}},
			StopReason: "tool_use",
		},
	}
	validated := mustValidateStream(t, raw, requestWithTool("search"))
	observed, err := validated.Observe(&mutatingStreamObserver{})
	require.NoError(t, err)

	chunk, err := observed.Recv()
	require.NoError(t, err)
	require.JSONEq(t, `{"query":"original"}`, string(chunk.(ToolCallChunk).ToolCall.Payload))
	_, err = observed.Recv()
	require.NoError(t, err)
	_, err = observed.Recv()
	require.ErrorIs(t, err, io.EOF)
	require.JSONEq(
		t,
		`{"query":"original"}`,
		string(observed.Response().Content[0].Parts[0].(ToolUsePart).Input),
	)
}

func TestValidatedStreamObserveBoundsRejectedUsageEvidence(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{UsageChunk{Usage: TokenUsage{
			Model:            string(make([]byte, maxTokenUsageModelBytes+1)),
			InputTokens:      1,
			OutputTokens:     2,
			TotalTokens:      3,
			CacheReadTokens:  4,
			CacheWriteTokens: 5,
		}}},
	}
	validated := mustValidateStream(t, raw, &Request{})
	observer := &recordingStreamObserver{}
	observed, err := validated.Observe(observer)
	require.NoError(t, err)

	chunk, err := observed.Recv()

	require.Nil(t, chunk)
	require.ErrorContains(t, outputValidationCause(t, err), "token usage model exceeds")
	require.Len(t, observer.observations, 1)
	require.Nil(t, observer.observations[0].Chunk)
	require.Equal(t, &TokenUsage{
		InputTokens:      1,
		OutputTokens:     2,
		TotalTokens:      3,
		CacheReadTokens:  4,
		CacheWriteTokens: 5,
	}, observer.observations[0].RejectedUsageDelta)
	require.Nil(t, observer.observations[0].RejectedUsageTotal)
}

func TestValidatedStreamObservePreservesProviderRejectionEvidence(t *testing.T) {
	contract, err := NewRequestContract(&Request{ModelClass: ModelClassDefault})
	require.NoError(t, err)
	rejection := contract.RejectProviderOutput(&TokenUsage{
		Model:        "provider-model",
		InputTokens:  7,
		OutputTokens: 3,
		TotalTokens:  10,
	}, errors.New("translate provider stream"))
	raw := &validatedStreamFixture{recvErr: rejection}
	validated, err := contract.ValidateStream(raw)
	require.NoError(t, err)
	observer := &recordingStreamObserver{}
	observed, err := validated.Observe(observer)
	require.NoError(t, err)

	chunk, err := observed.Recv()

	require.Nil(t, chunk)
	require.ErrorIs(t, err, rejection)
	require.Len(t, observer.observations, 1)
	require.Equal(t, rejection.Usage(), observer.observations[0].RejectedUsageTotal)
	require.Equal(t, rejection.Evidence(), observer.observations[0].ResponseEvidence)
}

func TestObservedStreamCloseJoinsProviderAndObserverFailures(t *testing.T) {
	providerErr := errors.New("provider close failed")
	observerErr := errors.New("observer close failed")
	raw := &validatedStreamFixture{closeErr: providerErr}
	validated := mustValidateStream(t, raw, &Request{})
	observed, err := validated.Observe(
		&recordingStreamObserver{closeErr: observerErr},
	)
	require.NoError(t, err)

	err = observed.Close()

	require.ErrorIs(t, err, providerErr)
	require.ErrorIs(t, err, observerErr)
	require.Equal(t, err, observed.Close())
	require.True(t, raw.closed)
}

func TestValidatedStreamFinalizeOperationResults(t *testing.T) {
	providerReceiveErr := errors.New("provider receive failed")
	providerCloseErr := errors.New("provider close failed")
	unrelatedErr := errors.New("unrelated receive failure")
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))
	wrappedDuplicateValidation := fmt.Errorf(
		"validation wrapper: %w",
		errors.Join(validationErr, validationErr),
	)

	tests := []struct {
		name        string
		recvErr     error
		closeErr    error
		primary     func(error) error
		wantExact   error
		wantErrors  []error
		absentError error
	}{
		{
			name:      "validation only",
			recvErr:   validationErr,
			primary:   func(recvErr error) error { return recvErr },
			wantExact: validationErr,
		},
		{
			name:        "validation and provider cleanup",
			recvErr:     validationErr,
			closeErr:    providerCloseErr,
			primary:     func(recvErr error) error { return recvErr },
			wantExact:   validationErr,
			absentError: providerCloseErr,
		},
		{
			name:        "wrapped duplicate validation and provider cleanup",
			recvErr:     wrappedDuplicateValidation,
			closeErr:    providerCloseErr,
			primary:     func(recvErr error) error { return recvErr },
			wantExact:   wrappedDuplicateValidation,
			absentError: providerCloseErr,
		},
		{
			name:       "ordinary receive and provider cleanup",
			recvErr:    providerReceiveErr,
			closeErr:   providerCloseErr,
			primary:    func(recvErr error) error { return recvErr },
			wantErrors: []error{providerReceiveErr, providerCloseErr},
		},
		{
			name:       "validation mixed with unrelated receive failure",
			recvErr:    errors.Join(validationErr, unrelatedErr),
			closeErr:   providerCloseErr,
			primary:    func(recvErr error) error { return recvErr },
			wantErrors: []error{validationErr, unrelatedErr, providerCloseErr},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := &validatedStreamFixture{recvErr: test.recvErr, closeErr: test.closeErr}
			stream := mustValidateStream(t, raw, &Request{})
			_, recvErr := stream.Recv()
			require.Error(t, recvErr)

			got := stream.Finalize(test.primary(recvErr))

			if test.wantExact != nil {
				require.Same(t, test.wantExact, got)
			}
			for _, wantErr := range test.wantErrors {
				require.ErrorIs(t, got, wantErr)
			}
			if test.absentError != nil {
				require.NotErrorIs(t, got, test.absentError)
			}
			require.Equal(t, 1, raw.closeCalls)
		})
	}
}

func TestValidatedStreamFinalizeRetainsObserverAndIncompleteFailures(t *testing.T) {
	observerErr := errors.New("observer close failed")
	raw := &validatedStreamFixture{}
	stream := mustValidateStream(t, raw, &Request{})
	stream, err := stream.Observe(&recordingStreamObserver{closeErr: observerErr})
	require.NoError(t, err)

	operationErr := stream.Finalize(nil)

	require.ErrorIs(t, operationErr, observerErr)
	require.ErrorContains(t, operationErr, "model stream was not completely consumed")
	require.Equal(t, 1, raw.closeCalls)
	require.ErrorIs(t, stream.Close(), observerErr)
	_, recvErr := stream.Recv()
	require.ErrorContains(t, recvErr, "model stream was not completely consumed")
}

func TestValidatedStreamFinalizeAfterEOF(t *testing.T) {
	providerCloseErr := errors.New("provider close failed")
	observerErr := errors.New("observer close failed")
	tests := []struct {
		name       string
		closeErr   error
		observer   error
		wantErrors []error
	}{
		{name: "success"},
		{
			name:       "provider cleanup only",
			closeErr:   providerCloseErr,
			wantErrors: []error{providerCloseErr},
		},
		{
			name:       "observer cleanup only",
			observer:   observerErr,
			wantErrors: []error{observerErr},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := canonicalTextResponse()
			raw := &validatedStreamFixture{
				chunks: []Chunk{
					TextChunk{Message: response.Content[0]},
					StopChunk{Reason: response.StopReason},
				},
				response: response,
				closeErr: test.closeErr,
			}
			stream := mustValidateStream(t, raw, &Request{})
			stream, err := stream.Observe(&recordingStreamObserver{closeErr: test.observer})
			require.NoError(t, err)
			_, err = stream.Recv()
			require.NoError(t, err)
			_, err = stream.Recv()
			require.NoError(t, err)
			_, err = stream.Recv()
			require.ErrorIs(t, err, io.EOF)

			operationErr := stream.Finalize(nil)

			if len(test.wantErrors) == 0 {
				require.NoError(t, operationErr)
			}
			for _, wantErr := range test.wantErrors {
				require.ErrorIs(t, operationErr, wantErr)
			}
			require.Equal(t, 1, raw.closeCalls)
		})
	}
}

func TestValidatedStreamFinalizeCachesOnePrimary(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))
	providerCloseErr := errors.New("provider close failed")
	raw := &validatedStreamFixture{recvErr: validationErr, closeErr: providerCloseErr}
	stream := mustValidateStream(t, raw, &Request{})
	_, primaryErr := stream.Recv()
	require.Same(t, validationErr, primaryErr)

	first := stream.Finalize(primaryErr)
	second := stream.Finalize(primaryErr)
	mismatch := stream.Finalize(errors.New("different primary"))

	require.Same(t, validationErr, first)
	require.Same(t, first, second)
	require.ErrorContains(t, mismatch, "different primary error")
	require.ErrorIs(t, stream.Close(), providerCloseErr)
	require.Equal(t, 1, raw.closeCalls)
}

func TestValidatedStreamFinalizeUsesPriorCloseResult(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))
	providerCloseErr := errors.New("provider close failed")
	raw := &validatedStreamFixture{recvErr: validationErr, closeErr: providerCloseErr}
	stream := mustValidateStream(t, raw, &Request{})
	_, primaryErr := stream.Recv()
	require.Same(t, validationErr, primaryErr)

	require.ErrorIs(t, stream.Close(), providerCloseErr)
	operationErr := stream.Finalize(primaryErr)

	require.Same(t, validationErr, operationErr)
	require.Equal(t, 1, raw.closeCalls)
}

func TestObservedStreamCloseDiscardsBufferedToolChunks(t *testing.T) {
	call := ToolCall{
		ID:      "call-1",
		Name:    "search",
		Payload: []byte(`{"query":"accepted"}`),
	}
	providerErr := errors.New("provider close failed")
	observerErr := errors.New("observer rejected delivery")
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			ToolCallDeltaChunk{Delta: ToolCallDelta{
				ID:    call.ID,
				Name:  call.Name,
				Delta: string(call.Payload),
			}},
			ToolCallChunk{ToolCall: call},
			StopChunk{Reason: "tool_use"},
		},
		response: responseWithToolCall(call),
		closeErr: providerErr,
	}
	base := mustValidateStream(t, raw, requestWithTool("search"))
	inner := base.core.inner.(*validatedStreamer)
	observed, err := base.Observe(&failingReceiveObserver{err: observerErr})
	require.NoError(t, err)

	chunk, err := observed.Recv()
	require.IsType(t, ToolCallDeltaChunk{}, chunk)
	require.ErrorIs(t, err, observerErr)
	require.NotEmpty(t, inner.pending)

	err = observed.Close()

	require.ErrorIs(t, err, providerErr)
	require.Empty(t, inner.pending)
	require.True(t, raw.closed)
}

func TestValidatedStreamObserverConcurrentOperations(t *testing.T) {
	const chunkCount = 100
	chunks := make([]Chunk, chunkCount)
	for index := range chunks {
		chunks[index] = TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "x"}},
		}}
	}
	raw := &validatedStreamFixture{chunks: chunks}
	validated := mustValidateStream(t, raw, &Request{})
	observer := &recordingStreamObserver{}
	observed, err := validated.Observe(observer)
	require.NoError(t, err)

	var operations sync.WaitGroup
	recvResult := make(chan error, 1)
	closeResult := make(chan error, 1)
	operations.Add(3)
	go func() {
		defer operations.Done()
		for range chunkCount {
			_, recvErr := observed.Recv()
			if recvErr != nil {
				recvResult <- recvErr
				return
			}
		}
		recvResult <- nil
	}()
	go func() {
		defer operations.Done()
		for range chunkCount {
			_ = observed.Response()
		}
	}()
	go func() {
		defer operations.Done()
		for range chunkCount {
			if closeErr := observed.Close(); closeErr != nil {
				closeResult <- closeErr
				return
			}
		}
		closeResult <- nil
	}()
	operations.Wait()

	require.NoError(t, <-recvResult)
	require.NoError(t, <-closeResult)
	require.Len(t, observer.observations, chunkCount)
	require.True(t, raw.closed)
	require.Equal(t, 1, raw.closeCalls)
}

func TestValidatedStreamDoesNotReceiveSiblingChunkBeforeObserverDecision(t *testing.T) {
	rejected := errors.New("first chunk rejected")
	raw := &siblingStreamFixture{secondRecv: make(chan struct{})}
	base := mustValidateStream(t, raw, &Request{})
	shared := &blockingRejectingStreamObserver{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		rejectErr:    rejected,
	}
	observed, err := base.Observe(shared)
	require.NoError(t, err)

	results := make(chan error, 2)
	go func() {
		_, recvErr := observed.Recv()
		results <- recvErr
	}()
	<-shared.firstEntered
	go func() {
		_, recvErr := base.Recv()
		results <- recvErr
	}()

	select {
	case <-raw.secondRecv:
		t.Fatal("sibling view consumed chunk 2 before observers decided chunk 1")
	default:
	}
	close(shared.releaseFirst)
	require.ErrorIs(t, <-results, rejected)
	require.ErrorIs(t, <-results, rejected)
	select {
	case <-raw.secondRecv:
		t.Fatal("sibling view consumed chunk 2 after chunk 1 was rejected")
	default:
	}
	require.Equal(t, 1, raw.next)
	require.EqualValues(t, 1, shared.calls.Load())
}

func TestValidatedStreamCloseWaitsForReceiveObservation(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "visible"}},
		}}},
		closeCalled: make(chan struct{}),
	}
	base := mustValidateStream(t, raw, &Request{})
	observer := &blockingRejectingStreamObserver{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	observed, err := base.Observe(observer)
	require.NoError(t, err)

	recvResult := make(chan error, 1)
	go func() {
		_, recvErr := observed.Recv()
		recvResult <- recvErr
	}()
	<-observer.firstEntered
	closeResult := make(chan error, 1)
	go func() {
		closeResult <- observed.Close()
	}()

	select {
	case <-raw.closeCalled:
		t.Fatal("Close overtook the in-flight receive observer")
	default:
	}
	close(observer.releaseFirst)
	require.NoError(t, <-recvResult)
	<-raw.closeCalled
	require.NoError(t, <-closeResult)
	require.NoError(t, observed.Close())
}

func TestValidatedStreamSerializesCancellationAndIdempotentClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	providerCloseErr := errors.New("provider close failed")
	raw := &cancellationBlockedStreamFixture{
		ctx:            ctx,
		recvStarted:    make(chan struct{}),
		cleanupStarted: make(chan struct{}),
		cleanupRelease: make(chan struct{}),
		closeCalled:    make(chan struct{}),
		closeErr:       providerCloseErr,
	}
	stream := mustValidateStream(t, raw, &Request{})
	recvErr := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		recvErr <- err
	}()
	<-raw.recvStarted

	closeErr := make(chan error, 1)
	go func() {
		closeErr <- stream.Close()
	}()
	select {
	case <-raw.closeCalled:
		t.Fatal("Close operated on the provider while Recv was active")
	default:
	}

	cancel()
	<-raw.cleanupStarted
	select {
	case <-raw.closeCalled:
		t.Fatal("Close ran before provider receive cleanup finished")
	default:
	}
	select {
	case <-closeErr:
		t.Fatal("Close returned before provider receive cleanup finished")
	default:
	}
	close(raw.cleanupRelease)

	require.ErrorIs(t, <-recvErr, context.Canceled)
	require.ErrorIs(t, <-closeErr, providerCloseErr)
	require.ErrorIs(t, stream.Close(), providerCloseErr)
	require.Equal(t, 1, raw.closeCalls)
}

func TestValidatedStreamSerializesConcurrentReceiveCloseAndFinalize(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	providerCloseErr := errors.New("provider close failed")
	processingErr := errors.New("caller processing failed")
	raw := &cancellationBlockedStreamFixture{
		ctx:            ctx,
		recvStarted:    make(chan struct{}),
		cleanupStarted: make(chan struct{}),
		cleanupRelease: make(chan struct{}),
		closeCalled:    make(chan struct{}),
		closeErr:       providerCloseErr,
	}
	stream := mustValidateStream(t, raw, &Request{})
	observer := &recordingStreamObserver{}
	stream, err := stream.Observe(observer)
	require.NoError(t, err)
	recvResult := make(chan error, 1)
	go func() {
		_, recvErr := stream.Recv()
		recvResult <- recvErr
	}()
	<-raw.recvStarted
	finalizeResult := make(chan error, 1)
	closeResult := make(chan error, 1)
	go func() {
		finalizeResult <- stream.Finalize(processingErr)
	}()
	go func() {
		closeResult <- stream.Close()
	}()

	cancel()
	<-raw.cleanupStarted
	close(raw.cleanupRelease)

	require.ErrorIs(t, <-recvResult, context.Canceled)
	operationErr := <-finalizeResult
	require.ErrorIs(t, operationErr, processingErr)
	require.ErrorIs(t, operationErr, context.Canceled)
	require.ErrorIs(t, operationErr, providerCloseErr)
	require.ErrorIs(t, <-closeResult, providerCloseErr)
	require.Equal(t, 1, raw.closeCalls)
	require.Equal(t, 1, observer.closeCalls)
}

func TestValidatedStreamConcurrentFinalizationIsFirstCallWinsAcrossViews(t *testing.T) {
	receiveErr := errors.New("provider receive failed")
	closeErr := errors.New("provider close failed")
	firstPrimary := errors.New("caller processing failed")
	differentPrimary := errors.New("different caller failure")
	raw := &blockingCloseStreamFixture{
		recvErr:      receiveErr,
		closeErr:     closeErr,
		closeStarted: make(chan struct{}),
		closeRelease: make(chan struct{}),
	}
	base := mustValidateStream(t, raw, &Request{})
	observed, err := base.Observe(&recordingStreamObserver{})
	require.NoError(t, err)
	_, err = observed.Recv()
	require.ErrorIs(t, err, receiveErr)
	firstResult := make(chan error, 1)
	sameResult := make(chan error, 1)
	differentResult := make(chan error, 1)
	go func() {
		firstResult <- base.Finalize(firstPrimary)
	}()
	<-raw.closeStarted
	go func() {
		sameResult <- observed.Finalize(firstPrimary)
	}()
	go func() {
		differentResult <- observed.Finalize(differentPrimary)
	}()

	close(raw.closeRelease)
	firstErr := <-firstResult
	sameErr := <-sameResult
	mismatchErr := <-differentResult

	require.ErrorIs(t, firstErr, firstPrimary)
	require.ErrorIs(t, firstErr, receiveErr)
	require.ErrorIs(t, firstErr, closeErr)
	require.Same(t, firstErr, sameErr)
	require.ErrorContains(t, mismatchErr, "different primary error")
	require.ErrorIs(t, observed.Close(), closeErr)
	require.Equal(t, 1, raw.closeCalls)
}

func (s *validatedStreamFixture) Recv() (Chunk, error) {
	if s.next == len(s.chunks) {
		if s.recvErr != nil {
			return nil, s.recvErr
		}
		return nil, io.EOF
	}
	chunk := s.chunks[s.next]
	s.next++
	return chunk, nil
}

func (s *validatedStreamFixture) Response() *Response {
	return s.response
}

func (s *validatedStreamFixture) Close() error {
	s.closed = true
	s.closeCalls++
	if s.closeCalled != nil {
		close(s.closeCalled)
	}
	return s.closeErr
}

func (s *siblingStreamFixture) Recv() (Chunk, error) {
	s.next++
	if s.next == 2 {
		close(s.secondRecv)
	}
	return TextChunk{Message: Message{
		Role:  ConversationRoleAssistant,
		Parts: []Part{TextPart{Text: "visible"}},
	}}, nil
}

func (*siblingStreamFixture) Response() *Response {
	return nil
}

func (*siblingStreamFixture) Close() error {
	return nil
}

func mustValidateStream(t *testing.T, raw Streamer, request *Request) *ValidatedStream {
	t.Helper()
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	stream, err := contract.ValidateStream(raw)
	require.NoError(t, err)
	return stream
}

func (o *recordingStreamObserver) ObserveStreamRecv(observation StreamObservation) error {
	o.observations = append(o.observations, observation)
	if observation.Err == nil {
		o.chunks = append(o.chunks, observation.Chunk)
	}
	return nil
}

func (o *recordingStreamObserver) ObserveStreamClose(error) error {
	o.closeCalls++
	return o.closeErr
}

func (*mutatingStreamObserver) ObserveStreamRecv(observation StreamObservation) error {
	if tool, ok := observation.Chunk.(ToolCallChunk); ok {
		tool.ToolCall.Payload[10] = 'X'
	}
	if observation.Response != nil {
		tool := observation.Response.Content[0].Parts[0].(ToolUsePart)
		tool.Input[10] = 'Y'
	}
	return nil
}

func (*mutatingStreamObserver) ObserveStreamClose(error) error {
	return nil
}

func (o *blockingRejectingStreamObserver) ObserveStreamRecv(StreamObservation) error {
	if o.calls.Add(1) == 1 {
		close(o.firstEntered)
		<-o.releaseFirst
		return o.rejectErr
	}
	return nil
}

func (*blockingRejectingStreamObserver) ObserveStreamClose(error) error {
	return nil
}
