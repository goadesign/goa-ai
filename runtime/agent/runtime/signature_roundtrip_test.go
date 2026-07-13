package runtime

// signature_roundtrip_test.go contains the spanning test for tool-call
// thought signatures: it exercises the full chain from a provider stream
// through capture, planner-facing summarization, transcript commit, and durable
// message serialization, verifying the signature survives every hop even
// though it never touches a planner-facing type along the way.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
)

// TestToolCallThoughtSignatureRoundTripsProviderChunkToTranscript drives the
// entire path end to end:
//
//  1. A provider streams a tool-call presentation chunk and its canonical
//     response carrying the thought signature.
//  2. modelInvocationClient records the canonical response before any
//     planner-facing type exists.
//  3. planner.ConsumeStream produces a planner.ToolRequest with no signature
//     field at all (the type genuinely cannot carry one).
//  4. The runtime selects and commits the exact canonical response.
//  5. The canonical model messages survive a durable JSON serialization hop
//     byte for byte.
func TestToolCallThoughtSignatureRoundTripsProviderChunkToTranscript(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.tools.read", "svc.tools"))
	agentID := agent.Ident("agent-1")
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}

	// Step 1+2: provider chunk observed and captured at the client boundary.
	events := &runtimePlannerEvents{}
	invocations := &modelInvocationJournal{}
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.ToolCallChunk{
				ToolCall: model.ToolCall{
					ID:               "call-1",
					Name:             "svc.tools.read",
					Payload:          rawjson.Message(`{"z":9007199254740993,"q":"new"}`),
					ThoughtSignature: "opaque-provider-signature",
				},
			},
			model.StopChunk{Reason: "tool_use"},
		},
		response: testModelResponse(nil, model.ToolCall{
			ID:               "call-1",
			Name:             "svc.tools.read",
			Payload:          rawjson.Message(`{"z":9007199254740993,"q":"new"}`),
			ThoughtSignature: "opaque-provider-signature",
		}),
	}
	client := newModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return streamer, nil
		},
	}, invocations)

	st, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)

	// Step 3: the planner drains the wrapped stream via ConsumeStream, exactly
	// as a raw-client planner would. The resulting ToolRequest carries no
	// signature.
	summary, err := planner.ConsumeStream(context.Background(), st, &model.Request{}, events)
	require.NoError(t, err)
	require.Len(t, summary.ToolCalls, 1)

	// Step 4: the runtime identifies the invocation from the unchanged tool-call
	// ID/name/payload and commits its canonical response without rebuilding it.
	result := &planner.PlanResult{
		ToolCalls: summary.ToolCalls,
	}
	selected, err := invocations.exportModelInvocation(result)
	require.NoError(t, err)
	require.NoError(t, rt.appendSelectedModelResponse(context.Background(), agentID, base, "turn-1", result, selected))
	require.Len(t, base.Messages, 1)

	// Step 5: the workflow serialization hop must preserve the signature.
	data, err := json.Marshal(base.Messages)
	require.NoError(t, err)
	var replayed []*model.Message
	require.NoError(t, json.Unmarshal(data, &replayed))
	require.Len(t, replayed, 1)
	require.Len(t, replayed[0].Parts, 1)
	use, ok := replayed[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-1", use.ID)
	require.Equal(t, "opaque-provider-signature", use.ThoughtSignature)
	require.Equal(t, `{"z":9007199254740993,"q":"new"}`, string(use.Input)) //nolint:testifylint // Exact bytes are the contract.
}
