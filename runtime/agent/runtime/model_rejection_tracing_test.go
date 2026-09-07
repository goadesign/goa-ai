package runtime

// These tests use the model-owned client and stream lifecycle. Rejected tool
// arguments must stay diagnostic-only, on their own call's error event, without
// changing returned errors, cleanup, or successful-output capture.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// rejectionTracePayload stands in for a generated tool's typed payload.
	rejectionTracePayload struct {
		// Groups permits nested empty arrays but requires array-valued elements.
		Groups [][]string `json:"groups"`
		// Number exercises preservation of numeric argument spelling.
		Number float64 `json:"number"`
		// Label carries synthetic text with Unicode and escape sequences.
		Label string `json:"label"`
	}
)

func TestTracedRejectionRetainsExactArguments(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, capture := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/capture=%t", streaming, capture), func(t *testing.T) {
				request := rejectionTraceRequest(t)
				payloads := []string{
					" {\n\t\"groups\": null, \"number\": 1.00e+02, \"label\": \"雪\\u263a\\n\\\"\" } ",
					`{"groups":[[]],"number":-0,"label":"valid sibling"}`,
				}
				response := rejectionTraceResponse(payloads...)
				cleanupErr := errors.New("synthetic provider cleanup failed")
				tracer := &recordingTelemetryTracer{}
				client := newTracedClient(mustTestModelClient(stubModelClient{
					complete: func(context.Context, *model.Request) (*model.Response, error) {
						return response, nil
					},
					stream: func(context.Context, *model.Request) (model.Streamer, error) {
						stream := rejectionTraceStream(response)
						stream.closeErr = cleanupErr
						return stream, nil
					},
				}), tracer, telemetry.NewNoopLogger(), "synthetic", testGenAIContext(), capture)
				var rejection error
				if streaming {
					stream, err := client.Stream(t.Context(), request)
					require.NoError(t, err)
					for {
						chunk, err := stream.Recv()
						if err != nil {
							require.Nil(t, chunk)
							rejection = err
							break
						}
						_, exposed := chunk.(model.ToolCallChunk)
						require.False(t, exposed, "rejected calls must not escape validation")
					}
					_, repeated := stream.Recv()
					require.Same(t, rejection, repeated)
					require.ErrorIs(t, stream.Close(), cleanupErr)
					require.ErrorIs(t, stream.Close(), cleanupErr)
				} else {
					accepted, err := client.Complete(t.Context(), request)
					require.Nil(t, accepted)
					rejection = err
				}
				var outputErr *model.OutputValidationError
				require.ErrorAs(t, rejection, &outputErr)
				require.Contains(t, outputErr.Unwrap().Error(), "array")
				require.Len(t, tracer.spans, 1)
				span := tracer.spans[0]
				assert.Equal(t, codes.Error, span.statusCode)
				assert.Equal(t, 1, span.endCount)
				assert.Same(t, outputErr, span.errs[0])
				attrs := attrsByKey(span.errorAttrs[0])
				assert.Equal(t, outputErr.Unwrap().Error(), attrs["gen_ai.response.validation.cause"].AsString())
				assert.NotContains(t, attrsByKey(span.attrs), telemetry.AttrGenAIOutputMessages)
				if capture {
					assert.True(t, attrs["gen_ai.response.rejected.response_retained"].AsBool())
					body := attrs["gen_ai.response.rejected.tool_calls"].AsString()
					var calls []rejectedToolCall
					require.NoError(t, json.Unmarshal([]byte(body), &calls))
					require.Len(t, calls, len(payloads))
					for i, payload := range payloads {
						assert.Equal(t, rejectedToolCall{
							ID: fmt.Sprintf("call-%d", i), Name: "samples.inspect", ArgumentsJSON: payload,
						}, calls[i])
					}
					assert.NotContains(t, body, "synthetic reasoning")
					assert.NotContains(t, body, "synthetic signature")
					assert.NotContains(t, body, "unrelated assistant text")
				} else {
					assert.NotContains(t, attrs, "gen_ai.response.rejected.tool_calls")
					assert.NotContains(t, attrs, "gen_ai.response.rejected.response_retained")
				}
				if streaming {
					require.Len(t, span.errs, 2)
					assert.Same(t, cleanupErr, span.errs[1])
					assert.Empty(t, span.errorAttrs[1])
				} else {
					assert.Len(t, span.errs, 1)
				}
			})
		}
	}
}

func TestTracedAcceptedNestedEmptyArraysRemainAccepted(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			response := rejectionTraceResponse(`{"groups":[[]],"number":1,"label":"accepted"}`)
			tracer := &recordingTelemetryTracer{}
			client := newTracedClient(mustTestModelClient(stubModelClient{
				complete: func(context.Context, *model.Request) (*model.Response, error) {
					return response, nil
				},
				stream: func(context.Context, *model.Request) (model.Streamer, error) {
					return rejectionTraceStream(response), nil
				},
			}), tracer, telemetry.NewNoopLogger(), "synthetic", testGenAIContext(), true)
			if streaming {
				stream, err := client.Stream(t.Context(), rejectionTraceRequest(t))
				require.NoError(t, err)
				for {
					_, err = stream.Recv()
					if err != nil {
						require.ErrorIs(t, err, io.EOF)
						break
					}
				}
				require.NoError(t, stream.Close())
				require.Equal(t, response.ToolCalls(), stream.Response().ToolCalls())
			} else {
				accepted, err := client.Complete(t.Context(), rejectionTraceRequest(t))
				require.NoError(t, err)
				require.Equal(t, response.ToolCalls(), accepted.ToolCalls())
			}
			span := tracer.spans[0]
			assert.Empty(t, span.errs)
			assert.Equal(t, codes.Ok, span.statusCode)
			assert.Contains(t, attrsByKey(span.attrs), telemetry.AttrGenAIOutputMessages)
		})
	}
}

func TestTracedProviderRejectionEvidence(t *testing.T) {
	const streamReceive = "stream receive"
	for _, stage := range []string{"complete", "stream setup", streamReceive, "observer setup"} {
		for _, evidence := range []struct {
			name     string
			response *model.Response
		}{
			{name: "unavailable"},
			{name: "no calls", response: &model.Response{}},
			{name: "malformed arguments", response: rejectionTraceResponse(" {\"groups\": [ ")},
		} {
			t.Run(stage+"/"+evidence.name, func(t *testing.T) {
				contract, err := model.NewRequestContract(&model.Request{})
				require.NoError(t, err)
				cause := errors.New("synthetic provider rejected arguments: unexpected end")
				rejected := contract.RejectResponse(model.OutputValidationToolArguments, evidence.response, cause)
				wrapped := fmt.Errorf("synthetic provider: %w", rejected)
				provider := model.Provider(stubModelClient{
					complete: func(context.Context, *model.Request) (*model.Response, error) { return nil, wrapped },
					stream: func(context.Context, *model.Request) (model.Streamer, error) {
						if stage == streamReceive {
							return &stubStreamer{recvErr: wrapped}, nil
						}
						return nil, wrapped
					},
				})
				if stage == "observer setup" {
					provider = &failingCallPreparationProvider{err: wrapped}
				}
				tracer := &recordingTelemetryTracer{}
				client := newTracedClient(mustTestModelClient(provider), tracer, telemetry.NewNoopLogger(), "synthetic", testGenAIContext(), true)
				var returned error
				if stage == "stream setup" || stage == streamReceive {
					stream, err := client.Stream(t.Context(), &model.Request{Model: "synthetic"})
					returned = err
					if stage == streamReceive {
						require.NoError(t, err)
						_, returned = stream.Recv()
						require.NoError(t, stream.Close())
					}
				} else {
					_, returned = client.Complete(t.Context(), &model.Request{Model: "synthetic"})
				}
				require.ErrorIs(t, returned, wrapped)
				span := tracer.spans[0]
				require.Len(t, span.errs, 1)
				assert.Same(t, wrapped, span.errs[0])
				assert.Equal(t, 1, span.endCount)
				attrs := attrsByKey(span.errorAttrs[0])
				assert.Equal(t, cause.Error(), attrs["gen_ai.response.validation.cause"].AsString())
				assert.Equal(t, evidence.response != nil, attrs["gen_ai.response.rejected.response_retained"].AsBool())
				switch {
				case evidence.response == nil:
					assert.NotContains(t, attrs, "gen_ai.response.rejected.tool_calls")
				case len(evidence.response.ToolCalls()) == 0:
					assert.Equal(t, "[]", attrs["gen_ai.response.rejected.tool_calls"].AsString())
				default:
					var calls []rejectedToolCall
					require.NoError(t, json.Unmarshal([]byte(attrs["gen_ai.response.rejected.tool_calls"].AsString()), &calls))
					require.Len(t, calls, 1)
					assert.Equal(t, string(evidence.response.ToolCalls()[0].Payload), calls[0].ArgumentsJSON)
				}
			})
		}
	}
}

func TestTracedConcurrentRejectionsStayOnOwnCalls(t *testing.T) {
	tracer := &recordingTelemetryTracer{}
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	client := newTracedClient(mustTestModelClient(stubModelClient{
		complete: func(_ context.Context, request *model.Request) (*model.Response, error) {
			arrived <- struct{}{}
			<-release
			return rejectionTraceResponse(fmt.Sprintf(`{"groups":null,"label":%q}`, request.Model)), nil
		},
	}), tracer, telemetry.NewNoopLogger(), "synthetic", testGenAIContext(), true)
	var completed sync.WaitGroup
	for _, modelName := range []string{"synthetic-first", "synthetic-second"} {
		request := rejectionTraceRequest(t)
		request.Model = modelName
		completed.Go(func() {
			_, err := client.Complete(t.Context(), request)
			assert.Error(t, err)
		})
	}
	<-arrived
	<-arrived
	close(release)
	completed.Wait()
	require.Len(t, tracer.spans, 2)
	for _, span := range tracer.spans {
		require.Len(t, span.errorAttrs, 1)
		name := attrsByKey(span.attrs)[telemetry.AttrGenAIRequestModel].AsString()
		var calls []rejectedToolCall
		require.NoError(t, json.Unmarshal([]byte(attrsByKey(span.errorAttrs[0])["gen_ai.response.rejected.tool_calls"].AsString()), &calls))
		require.Len(t, calls, 1)
		assert.Equal(t, fmt.Sprintf(`{"groups":null,"label":%q}`, name), calls[0].ArgumentsJSON)
		assert.Equal(t, 1, span.endCount)
	}
}

func TestTracedRejectionUsesExistingClueErrorEvent(t *testing.T) {
	// This non-parallel test configures the same global tracer provider used by
	// application bootstrap, then restores it before parallel tests resume.
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	for _, capture := range []bool{false, true} {
		client := newTracedClient(mustTestModelClient(stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				return rejectionTraceResponse(`{"groups":null}`), nil
			},
		}), telemetry.NewClueTracer(), telemetry.NewNoopLogger(), "synthetic", testGenAIContext(), capture)
		_, err := client.Complete(t.Context(), rejectionTraceRequest(t))
		var rejection *model.OutputValidationError
		require.ErrorAs(t, err, &rejection)
		spans := recorder.Ended()
		span := spans[len(spans)-1]
		events := span.Events()
		require.Len(t, events, 1, "capture must enrich the existing error, not add another rejection event")
		assert.Equal(t, "exception", events[0].Name)
		attrs := attrsByKey(events[0].Attributes)
		assert.Equal(t, rejection.Error(), attrs["exception.message"].AsString())
		assert.Equal(t, rejection.Unwrap().Error(), attrs["gen_ai.response.validation.cause"].AsString())
		if capture {
			assert.JSONEq(t, `[{"id":"call-0","name":"samples.inspect","arguments_json":"{\"groups\":null}"}]`, attrs["gen_ai.response.rejected.tool_calls"].AsString())
		} else {
			assert.NotContains(t, attrs, "gen_ai.response.rejected.tool_calls")
		}
	}
	require.Len(t, recorder.Ended(), 2)
}

func rejectionTraceRequest(t *testing.T) *model.Request {
	t.Helper()
	schema := rawjson.Message(`{
		"type":"object","properties":{
			"groups":{"type":"array","items":{"type":"array","items":{"type":"string"}}},
			"number":{"type":"number"},"label":{"type":"string"}
		},"required":["groups"]
	}`)
	definition, err := model.NewToolDefinitionFromSpec(tools.ToolSpec{
		Name: "samples.inspect",
		Payload: tools.TypeSpec{
			Schema:                   schema,
			SchemaWithoutRootExample: schema,
			Codec:                    tools.JSONCodec[any]{FromJSON: decodeRejectionTracePayload},
		},
	})
	require.NoError(t, err)
	return &model.Request{
		Model: "synthetic",
		Tools: []*model.ToolDefinition{definition},
	}
}

// decodeRejectionTracePayload exercises the typed codec path used by generated
// tool specs after advertised-schema validation, without importing an example.
func decodeRejectionTracePayload(data []byte) (any, error) {
	var payload rejectionTracePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func rejectionTraceResponse(payloads ...string) *model.Response {
	parts := make([]model.Part, 0, 2+len(payloads))
	parts = append(parts,
		model.ThinkingPart{Text: "synthetic reasoning", Signature: "synthetic signature", Final: true},
		model.TextPart{Text: "unrelated assistant text"},
	)
	for i, payload := range payloads {
		parts = append(parts, model.ToolUsePart{
			ID: fmt.Sprintf("call-%d", i), Name: "samples.inspect", Input: rawjson.Message(payload),
			ThoughtSignature: "synthetic signature",
		})
	}
	return &model.Response{
		Content:    []model.Message{{Role: model.ConversationRoleAssistant, Parts: parts}},
		StopReason: "tool_use",
	}
}

// rejectionTraceStream supplies chunks that exactly match the canonical
// synthetic response, so failures come from argument validation, not fixtures.
func rejectionTraceStream(response *model.Response) *stubStreamer {
	calls := response.ToolCalls()
	chunks := make([]model.Chunk, 0, 3+len(calls))
	chunks = append(chunks,
		model.ThinkingChunk{Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{response.Content[0].Parts[0]},
		}},
		model.TextChunk{Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{response.Content[0].Parts[1]},
		}},
	)
	for _, call := range calls {
		chunks = append(chunks, model.ToolCallChunk{ToolCall: call})
	}
	chunks = append(chunks, model.StopChunk{Reason: response.StopReason})
	return &stubStreamer{chunks: chunks, response: response}
}
