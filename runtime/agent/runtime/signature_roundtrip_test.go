package runtime

// signature_roundtrip_test.go contains the spanning test for tool-call
// thought signatures: it exercises the full chain from a raw provider chunk
// through capture, planner-facing summarization, transcript rebuild, and
// ledger replay, verifying the signature survives every hop even though it
// never touches a planner-facing type along the way.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// TestToolCallThoughtSignatureRoundTripsProviderChunkToLedger drives the
// entire path end to end:
//
//  1. A provider streams a ChunkTypeToolCall chunk carrying a thought
//     signature.
//  2. signatureCapturingClient observes and records it, keyed by tool-call ID,
//     before any planner-facing type exists.
//  3. planner.ConsumeStream produces a planner.ToolRequest with no signature
//     field at all (the type genuinely cannot carry one).
//  4. recordAssistantTurn reattaches the signature by ID lookup when
//     rebuilding the canonical ToolUsePart.
//  5. transcript.FromModelMessages + Ledger.BuildMessages (a full
//     serialize/replay hop through the provider-precise ledger) preserves it
//     byte for byte.
func TestToolCallThoughtSignatureRoundTripsProviderChunkToLedger(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.tools.read", "svc.tools"))
	agentID := agent.Ident("agent-1")
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}

	// Step 1+2: provider chunk observed and captured at the client boundary.
	events := &runtimePlannerEvents{}
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			{
				Type: model.ChunkTypeToolCall,
				ToolCall: &model.ToolCall{
					ID:               "call-1",
					Name:             "svc.tools.read",
					Payload:          rawjson.Message(`{"q":"new"}`),
					ThoughtSignature: "opaque-provider-signature",
				},
			},
		},
	}
	client := newSignatureCapturingClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return streamer, nil
		},
	}, events)

	st, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)

	// Step 3: the planner drains the wrapped stream via ConsumeStream, exactly
	// as a raw-client planner would. The resulting ToolRequest carries no
	// signature.
	summary, err := planner.ConsumeStream(context.Background(), st, &model.Request{}, events)
	require.NoError(t, err)
	require.Len(t, summary.ToolCalls, 1)

	// Step 4: the runtime reattaches the signature by ID lookup.
	signatures := events.exportToolCallSignatures()
	require.Equal(t, map[string]string{"call-1": "opaque-provider-signature"}, signatures)
	require.NoError(t, rt.recordAssistantTurn(context.Background(), agentID, base, nil, summary.ToolCalls, signatures, "turn-1"))
	require.Len(t, base.Messages, 1)

	// Step 5: a full ledger serialize/replay hop must preserve the signature.
	replayed := transcript.FromModelMessages(base.Messages).BuildMessages()
	require.Len(t, replayed, 1)
	require.Len(t, replayed[0].Parts, 1)
	use, ok := replayed[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-1", use.ID)
	require.Equal(t, "opaque-provider-signature", use.ThoughtSignature)
}
