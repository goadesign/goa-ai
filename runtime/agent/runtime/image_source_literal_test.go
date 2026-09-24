// Mixed image history keeps inline pixels and generated source descriptors in
// the same literal. Only an actual model request reads the retained image.
package runtime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestNativeImageLiteralPreparationAndClosedReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pixels := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	random := rand.NewPCG(10, 20)
	for offset := 0; offset < len(pixels.Pix); offset += 4 {
		pixels.Pix[offset] = byte((random.Uint64() >> 32) & 255)
		pixels.Pix[offset+1] = byte((random.Uint64() >> 32) & 255)
		pixels.Pix[offset+2] = byte((random.Uint64() >> 32) & 255)
		pixels.Pix[offset+3] = 255
	}
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, pixels))
	inline := model.ImagePart{Format: model.ImageFormatPNG, Bytes: buffer.Bytes()}
	require.Equal(t, "d95ee95c69a6b1d4e0fda54eed97446d36f1ee3f3c0bb202b43d2c96786b25b4",
		fmt.Sprintf("%x", sha256.Sum256(inline.Bytes)))
	owner := newImageFixtureOwner(t)
	retained := owner.bodies["photo-2"]
	data, err := genpictures.ViewFixtureImageV1ServerDataCodec().ToJSON(&genpictures.ViewFixtureImageV1ServerData{
		ID: "photo-2", Format: "png", Size: int64(len(retained)), Sha256: fmt.Sprintf("%x", sha256.Sum256(retained)),
	})
	require.NoError(t, err)
	messages := []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{
			model.TextPart{Text: "Compare these images."},
			inline,
			model.ImageSourcePart{SourceKind: "fixture.image.v1", Data: data},
		},
		Meta: map[string]any{"source": "original"},
	}}
	canonical, err := transcript.EncodeRunLogDelta(messages)
	require.NoError(t, err)
	require.Greater(t, len(canonical), storage.MaxSeedCommandBytes)
	store := inmem.New()
	_, err = store.CreateSession(ctx, "holder", time.Now())
	require.NoError(t, err)
	provider := &imageFixtureProvider{}
	var calls atomic.Int32
	plan := &imageFixturePlanner{
		start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			calls.Add(1)
			encoded, err := transcript.EncodeRunLogDelta(input.Messages)
			require.NoError(t, err)
			assert.Equal(t, canonical, encoded)
			assert.Empty(t, owner.reads, "preparation and history expansion cannot read retained images")
			assert.Empty(t, input.Agent.AdvertisedToolDefinitions(), "historical codecs need no executable producer")
			client, ok := input.Agent.ModelClient("fixture")
			require.True(t, ok)
			result, err := client.Complete(ctx, &model.Request{Model: "fixture", Messages: input.Messages})
			if err != nil {
				return nil, err
			}
			return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &result.Content[0]}}, nil
		},
	}
	client := newMixedImageClient(t, ctx, store, owner, provider, plan)
	prepared, err := client.Prepare(ctx, "holder", messages, runtime.WithRunID("mixed-run"))
	require.NoError(t, err)
	prepared = assertNativeImagePreparationRecovery(t, ctx, client, prepared, "holder", "mixed-run")
	assert.Empty(t, owner.reads)
	accepted, found, err := store.FindRunPreparation(ctx, storage.PreparationOperation{
		AgentID: "mixed.reader", SessionID: "holder", RunID: "mixed-run", CommandID: "mixed-run",
	})
	require.NoError(t, err)
	require.True(t, found)
	var decoder transcript.LiteralDecoder
	var reconstructed []*model.Message
	pages, parts, cursor := 0, 0, ""
	for {
		page, err := store.ListRunSeedRecords(ctx, "mixed-run", accepted.Seed.EndID, cursor, 100)
		require.NoError(t, err)
		require.NoError(t, storage.ValidateSeedPageSize(page))
		pages++
		for _, record := range page.Records {
			require.NotNil(t, record.LiteralPart)
			parts++
			delta, err := decoder.Append(ctx, *record.LiteralPart)
			require.NoError(t, err)
			reconstructed = append(reconstructed, delta...)
		}
		if page.NextCursor == "" {
			break
		}
		require.NotEqual(t, cursor, page.NextCursor)
		cursor = page.NextCursor
	}
	require.NoError(t, decoder.Finish())
	assert.Greater(t, pages, 1)
	assert.Greater(t, parts, 1)
	assert.Equal(t, messages, reconstructed)
	handle, err := client.StartPrepared(ctx, prepared)
	require.NoError(t, err)
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, calls.Load())
	assert.Equal(t, []string{"photo-2"}, owner.reads)
	require.Len(t, provider.inputs, 1)
	var native model.Request
	require.NoError(t, json.Unmarshal(provider.inputs[0], &native))
	require.Len(t, native.Messages, 1)
	assert.Equal(t, inline, native.Messages[0].Parts[1])
	assert.Equal(t, model.ImagePart{Format: model.ImageFormatPNG, Bytes: retained}, native.Messages[0].Parts[2])
	assert.Equal(t, messages[0].Meta, native.Messages[0].Meta)
	replayed, err := transcript.BuildMessagesFromRunLog(ctx, store, "mixed-run")
	require.NoError(t, err)
	assert.Equal(t, messages[0], replayed[0])
	meta, err := store.LoadRun(ctx, "mixed-run")
	require.NoError(t, err)
	records, err := store.ListRunRecords(ctx, "mixed-run", "", 100)
	require.NoError(t, err)
	require.Empty(t, records.NextCursor)

	// A new engine has no prior execution. The Store must select the closed
	// request before the planner can read history or attempt revoked image access.
	owner.allowed["holder"] = false
	restarted := newMixedImageClient(t, ctx, store, owner, provider, plan)
	retry, err := restarted.StartPrepared(ctx, prepared)
	require.NoError(t, err)
	_, err = retry.Wait(ctx)
	require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
	assert.EqualValues(t, 1, calls.Load())
	assert.Equal(t, []string{"photo-2"}, owner.reads)
	assert.Len(t, provider.inputs, 1)
	after, err := store.LoadRun(ctx, "mixed-run")
	require.NoError(t, err)
	afterRecords, err := store.ListRunRecords(ctx, "mixed-run", "", 100)
	require.NoError(t, err)
	assert.Equal(t, meta, after)
	assert.Equal(t, records, afterRecords)
	t.Logf("png_bytes=%d canonical_bytes=%d parts=%d pages=%d retained_reads=%d",
		len(inline.Bytes), len(canonical), parts, pages, len(owner.reads))
}

// newMixedImageClient creates an independent engine with the same stored
// history and generated historical decoders, without registering a tool.
func newMixedImageClient(t *testing.T, ctx context.Context, store storage.Store, owner *imageFixtureOwner, provider *imageFixtureProvider, plan planner.Planner) runtime.AgentClient {
	t.Helper()
	rt := runtime.New(store, runtime.WithImageSourceResolver(genpictures.NativeImageSources(), owner.resolve))
	modelClient, err := model.NewClient(provider)
	require.NoError(t, err)
	require.NoError(t, rt.RegisterModel("fixture", modelClient))
	definition := runtime.NewAgentDefinition(runtime.AgentRoute{
		ID: "mixed.reader", WorkflowName: "mixed.workflow", DefaultTaskQueue: "mixed.queue",
	}, nil, nil, nil, nil, nil, nil)
	require.NoError(t, rt.RegisterAgent(ctx, runtime.AgentRegistration{
		Definition: definition, Planner: plan, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "mixed.plan", ResumeActivityName: "mixed.resume", ExecuteToolActivity: "mixed.tool",
	}))
	return rt.MustClientFor(definition)
}
