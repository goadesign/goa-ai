package tooloutput

// These tests run controlled provider responses through the real private
// runtime. Formatter tests separately cover cause trees and missing metadata.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	shortError struct {
		cause error
	}
	panickingError   struct{}
	blockingProvider struct {
		started  chan struct{}
		release  chan struct{}
		finished chan struct{}
	}
	isolatedProvider struct {
		started chan struct{}
		release chan struct{}
	}
)

func (e shortError) Error() string {
	return "short summary"
}

func (e shortError) Unwrap() error {
	return e.cause
}

func (panickingError) Error() string {
	panic("broken custom error")
}

func TestRunRetainsDistinctMalformedAndSchemaRejections(t *testing.T) {
	provider := &recordingProvider{responses: []*model.Response{
		toolResponse(`{`),
		toolResponse(`{"value":7}`),
		toolResponse(`{"value":""}`),
		toolResponse(`{"value":"accepted","unexpected":true}`),
	}}
	_, err := Run(t.Context(), testClient(t, provider), outputRequest(), outputSpec())
	require.ErrorContains(t, err, "recovery_cap")
	assert.Len(t, provider.requests, 4)
	for _, cause := range []string{"not valid JSON", "want string", "minLength", "unexpected"} {
		assert.Contains(t, err.Error(), cause)
	}
}

func TestRunPreservesEveryRejectedCodecCause(t *testing.T) {
	provider := &recordingProvider{}
	causes := make(map[string]error)
	for i := range 4 {
		value := fmt.Sprintf("rejected-%d", i)
		causes[value] = tools.NewValidationError(value+strings.Repeat(" detailed cause", 300), []*tools.FieldIssue{{Field: "value", Constraint: "invalid_enum_value"}}, nil)
		provider.responses = append(provider.responses, toolResponse(fmt.Sprintf(`{"value":%q}`, value)))
	}
	spec := outputSpec()
	decode := spec.Codec.FromJSON
	spec.Codec.FromJSON = func(data []byte) (testOutput, error) {
		value, err := decode(data)
		if err != nil {
			return testOutput{}, err
		}
		return testOutput{}, causes[value.Value]
	}
	_, err := Run(t.Context(), testClient(t, provider), outputRequest(), spec)
	require.ErrorContains(t, err, "recovery_cap")
	assert.Len(t, provider.requests, 4)
	for _, cause := range causes {
		require.ErrorIs(t, err, cause)
		assert.Contains(t, err.Error(), cause.Error())
	}
	var rejected *model.OutputValidationError
	require.ErrorAs(t, err, &rejected)
	assert.Contains(t, err.Error(), "observed ")
}

func TestRunPreservesPostPlannerActivitySizeFailure(t *testing.T) {
	provider := &recordingProvider{responses: []*model.Response{
		toolResponse(`{"value":"` + strings.Repeat("x", 1200*1024) + `"}`),
	}}
	_, err := Run(t.Context(), testClient(t, provider), outputRequest(), outputSpec())
	require.Error(t, err)
	assert.Len(t, provider.requests, 1)
	var contract *planner.OutputContractError
	require.ErrorAs(t, err, &contract)
	assert.Contains(t, err.Error(), "1048576")
}

func TestRunRetainsRejectionBeforeResultEncoderFailure(t *testing.T) {
	encoderErr := errors.New("result encoder cannot encode this value")
	spec := outputSpec()
	spec.Codec.ToJSON = func(testOutput) ([]byte, error) {
		return nil, shortError{encoderErr}
	}
	provider := &recordingProvider{responses: []*model.Response{
		toolResponse(`{"value":7}`),
		toolResponse(`{"value":"accepted"}`),
	}}
	_, err := Run(t.Context(), testClient(t, provider), outputRequest(), spec)
	require.Error(t, err)
	assert.Len(t, provider.requests, 2)
	// Tool execution converts encoder errors to its serializable ToolError
	// chain before tracing. Preserve that received type and the full cause text.
	var toolErr *planner.ToolError
	require.ErrorAs(t, err, &toolErr)
	assert.Contains(t, err.Error(), encoderErr.Error())
	var rejected *model.OutputValidationError
	require.ErrorAs(t, err, &rejected)
}

func TestRunPreservesInternalDecoderErrorWithoutRetry(t *testing.T) {
	want := errors.New("decoder internal configuration failure")
	spec := outputSpec()
	spec.Codec.FromJSON = func([]byte) (testOutput, error) {
		return testOutput{}, want
	}
	provider := &recordingProvider{responses: []*model.Response{toolResponse(`{"value":"accepted"}`)}}
	_, err := Run(t.Context(), testClient(t, provider), outputRequest(), spec)
	require.ErrorIs(t, err, want)
	assert.Contains(t, err.Error(), want.Error())
	assert.Len(t, provider.requests, 1)
}

func TestRunReportsMissingCallIDAtModelBoundary(t *testing.T) {
	response := toolResponse(`{"value":"accepted"}`)
	part := response.Content[0].Parts[0].(model.ToolUsePart)
	part.ID = ""
	response.Content[0].Parts[0] = part
	provider := &recordingProvider{responses: []*model.Response{response}}
	_, err := Run(t.Context(), testClient(t, provider), outputRequest(), outputSpec())
	require.Error(t, err)
	var rejected *model.OutputValidationError
	require.ErrorAs(t, err, &rejected)
	assert.Contains(t, err.Error(), "id")
	assert.Len(t, provider.requests, 1)
}

func TestDiagnosticRendersTerminalAndJoinedCauses(t *testing.T) {
	first := errors.New("first full cause")
	second := errors.New("second full cause")
	contract, err := model.NewRequestContract(outputRequest())
	require.NoError(t, err)
	left := contract.RejectProviderOutput(model.OutputValidationResponseShape, nil, first)
	right := contract.RejectProviderOutput(model.OutputValidationUsage, nil, second)
	terminal := shortError{errors.Join(left, right)}
	err = (&runDiagnostics{}).failure(terminal)
	require.ErrorIs(t, err, first)
	require.ErrorIs(t, err, second)
	assert.True(t, strings.HasPrefix(err.Error(), "short summary"))
	assert.Contains(t, err.Error(), first.Error())
	assert.Contains(t, err.Error(), second.Error())
	assert.Equal(t, 2, strings.Count(err.Error(), "validation_kind="))
	assert.Equal(t, "error message unavailable", renderDiagnostic("", panickingError{}).Error())
}

func TestDiagnosticReportsOnlyKnownValidationMetadata(t *testing.T) {
	contract, err := model.NewRequestContract(outputRequest())
	require.NoError(t, err)
	usage := &model.TokenUsage{Model: "test-model", ModelClass: model.ModelClassDefault, InputTokens: 17, OutputTokens: 5, TotalTokens: 22}
	for _, test := range []struct {
		name string
		err  error
		want []string
	}{
		{"no response or usage", contract.RejectProviderOutput(model.OutputValidationResponseShape, nil, errors.New("shape")), []string{"usage=unknown", "retained_response=false", "output_limited=unknown"}},
		{"usage only", contract.RejectProviderOutput(model.OutputValidationResponseShape, usage, errors.New("shape")), []string{"InputTokens:17", "OutputTokens:5", "TotalTokens:22", "retained_response=false", "output_limited=unknown"}},
		{"limited response", contract.RejectResponse(model.OutputValidationResponseShape, &model.Response{StopReason: "max_tokens", OutputLimited: true, Usage: *usage}, errors.New("shape")), []string{`stop_reason="max_tokens"`, "output_limited=true", "retained_response=true", "SHA256:"}},
		{"ordinary stop", contract.RejectResponse(model.OutputValidationResponseShape, &model.Response{StopReason: "end_turn", Usage: *usage}, errors.New("shape")), []string{`stop_reason="end_turn"`, "output_limited=false"}},
		{"unreported stop", contract.RejectResponse(model.OutputValidationResponseShape, &model.Response{}, errors.New("shape")), []string{`stop_reason=unknown`, "retained_response=true"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := renderDiagnostic("", test.err).Error()
			for _, want := range test.want {
				assert.Contains(t, text, want)
			}
		})
	}
}

func TestDiagnosticSpanContextAndFrozenSnapshot(t *testing.T) {
	receiver := &runDiagnostics{}
	ctx, span := receiver.Start(t.Context(), "first operation")
	assert.Same(t, span, receiver.Span(ctx))
	first := errors.New("first observation")
	span.RecordError(first)
	err := receiver.failure(context.Canceled)
	before := err.Error()
	receiver.Span(t.Context()).RecordError(errors.New("unscoped error"))
	span.RecordError(errors.New("late observation"))
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, first)
	assert.Equal(t, before, err.Error())
	assert.NotContains(t, before, "late observation")
	assert.NotContains(t, receiver.failure(context.Canceled).Error(), "unscoped error")
}

func TestRunCancellationReturnsBeforeLateProviderFailure(t *testing.T) {
	provider := &blockingProvider{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	release := sync.OnceFunc(func() {
		close(provider.release)
	})
	result := make(chan error, 1)
	done := make(chan struct{})
	client := testClient(t, provider)
	go func() {
		defer close(done)
		_, err := Run(ctx, client, outputRequest(), outputSpec())
		result <- err
	}()
	t.Cleanup(func() {
		cancel()
		release()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run test worker did not finish")
		}
	})
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	cancel()
	var err error
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return on cancellation")
	}
	require.ErrorIs(t, err, context.Canceled)
	before := err.Error()
	unwrap := append([]error(nil), err.(interface{ Unwrap() []error }).Unwrap()...)
	release()
	select {
	case <-provider.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not finish")
	}
	// The provider is unblocked, but the private runtime may still be handling
	// its return. The direct snapshot test proves append-after-return isolation;
	// this test proves prompt cancellation without changing engine lifetime.
	assert.Equal(t, before, err.Error())
	assert.Equal(t, unwrap, err.(interface{ Unwrap() []error }).Unwrap())
}

func TestConcurrentRunsKeepErrorsPrivate(t *testing.T) {
	provider := isolatedProvider{started: make(chan struct{}, 3), release: make(chan struct{})}
	release := sync.OnceFunc(func() {
		close(provider.release)
	})
	client := testClient(t, provider)
	var wait sync.WaitGroup
	for _, name := range []string{"failed-one", "failed-two", "success"} {
		wait.Go(func() {
			request := outputRequest()
			request.Messages = userMessages(name)
			value, err := Run(t.Context(), client, request, outputSpec())
			if name == "success" {
				if assert.NoError(t, err) {
					assert.Equal(t, "accepted", value.Value)
				}
				return
			}
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), name)
				other := "failed-one"
				if name == other {
					other = "failed-two"
				}
				assert.NotContains(t, err.Error(), other)
			}
		})
	}
	done := make(chan struct{})
	go func() {
		wait.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("concurrent Run test workers did not finish")
		}
	})
	for range 3 {
		select {
		case <-provider.started:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent provider call did not start")
		}
	}
	release()
	<-done
}

func (p *blockingProvider) Complete(context.Context, *model.Request) (*model.Response, error) {
	close(p.started)
	<-p.release
	defer close(p.finished)
	return nil, errors.New("late provider failure")
}

func (*blockingProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, errors.New("unexpected stream")
}

func (p isolatedProvider) Complete(_ context.Context, request *model.Request) (*model.Response, error) {
	p.started <- struct{}{}
	<-p.release
	name := request.Messages[0].Parts[0].(model.TextPart).Text
	if name == "success" {
		return toolResponseBytes(rawjson.Message(`{"value":"accepted"}`)), nil
	}
	return nil, errors.New(name)
}

func (isolatedProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, errors.New("unexpected stream")
}
