package temporal

// Real preparation and planner activities preserve a valid PNG larger than a
// single seed command. Temporal uses its local SDK environment and a captured
// start RPC; neither backend calls a model or an external service.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	literalImageStore struct {
		storage.Store
		mu         sync.Mutex
		writes     []storage.SeedAppend
		maxCommand int
		maxPage    int
		pages      int
		corrupt    bool
	}
	literalImagePlanner struct {
		want  []byte
		calls int
	}
)

func (s *literalImageStore) AppendRunSeed(ctx context.Context, command storage.SeedAppend) (string, error) {
	payload, err := workflowcodec.NewDataConverter().ToPayload(&api.StorageActivityCommand{SeedAppend: &command})
	if err != nil {
		return "", err
	}
	size := len(payload.Data)
	for key, value := range payload.Metadata {
		size += len(key) + len(value)
	}
	if size > storage.MaxSeedCommandBytes {
		return "", fmt.Errorf("encoded seed command exceeds limit: %d", size)
	}
	// The activity decoder must preserve the explicit false Final value.
	var decoded api.StorageActivityCommand
	if err := workflowcodec.NewDataConverter().FromPayload(payload, &decoded); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.writes = append(s.writes, *decoded.SeedAppend)
	s.maxCommand = max(s.maxCommand, size)
	s.mu.Unlock()
	return s.Store.AppendRunSeed(ctx, *decoded.SeedAppend)
}

func (s *literalImageStore) ListRunSeedRecords(ctx context.Context, runID, endID, after string, limit int) (storage.SeedPage, error) {
	page, err := s.Store.ListRunSeedRecords(ctx, runID, endID, after, limit)
	if err != nil {
		return storage.SeedPage{}, err
	}
	if s.corrupt {
		for index := range page.Records {
			if part := page.Records[index].LiteralPart; part != nil {
				part.Data[0] = 0xff
				break
			}
		}
	}
	data, err := json.Marshal(page)
	if err != nil {
		return storage.SeedPage{}, err
	}
	if len(data) > storage.MaxSeedPageBytes {
		return storage.SeedPage{}, fmt.Errorf("encoded seed page exceeds limit: %d", len(data))
	}
	s.mu.Lock()
	s.maxPage = max(s.maxPage, len(data))
	s.pages++
	s.mu.Unlock()
	return page, nil
}

func (p *literalImagePlanner) PlanStart(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	actual, err := transcript.EncodeRunLogDelta(input.Messages)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(actual, p.want) {
		return nil, fmt.Errorf("planner input differs from original image history")
	}
	p.calls++
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "preserved"}},
	}}}, nil
}

func (*literalImagePlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return nil, fmt.Errorf("unexpected resume")
}

func TestRuntimeLiteralImagePreparationAndReplay(t *testing.T) {
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
	imageBytes := buffer.Bytes()
	config, err := png.DecodeConfig(bytes.NewReader(imageBytes))
	require.NoError(t, err)
	require.Equal(t, 512, config.Width)
	require.Equal(t, 512, config.Height)
	digest := sha256.Sum256(imageBytes)
	require.Equal(t, "d95ee95c69a6b1d4e0fda54eed97446d36f1ee3f3c0bb202b43d2c96786b25b4", hex.EncodeToString(digest[:]))
	messages := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "earlier input"}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "earlier answer"}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.TextPart{Text: "Inspect this image."}, model.ImagePart{Format: model.ImageFormatPNG, Bytes: imageBytes},
		}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "later answer"}}},
	}
	want, err := transcript.EncodeRunLogDelta(messages)
	require.NoError(t, err)
	require.Greater(t, len(want), storage.MaxSeedCommandBytes)
	for _, backend := range []string{"inmem", "temporal", "awaited engine one-shot", "inmem corrupt history"} {
		t.Run(backend, func(t *testing.T) {
			var eng engine.Engine
			var env *testsuite.TestWorkflowEnvironment
			var service *testWorkflowService
			if backend == "temporal" {
				var suite testsuite.WorkflowTestSuite
				env = suite.NewTestWorkflowEnvironment()
				env.SetTestTimeout(30 * time.Second)
				env.SetDataConverter(NewAgentDataConverter())
				implementation := newTestEngine(t)
				implementation.client.Close()
				service = &testWorkflowService{}
				implementation.client = newWorkflowServiceClient(t, service)
				implementation.workerFactory = func(client.Client, string, worker.Options) worker.Worker {
					return &boundedCompletionWorker{env: env}
				}
				eng = implementation
			} else {
				eng = engineinmem.New()
			}
			base := storageinmem.New()
			store := &literalImageStore{Store: base, corrupt: backend == "inmem corrupt history"}
			rt := agentruntime.New(store, agentruntime.WithEngine(eng), agentruntime.WithLogger(telemetry.NoopLogger{}))
			definition := testTemporalAgentDefinition("image.agent", "image.workflow", "default.queue", nil)
			plan := &literalImagePlanner{want: want}
			require.NoError(t, rt.RegisterAgent(t.Context(), agentruntime.AgentRegistration{
				Definition: definition, Planner: plan, WorkflowHandler: rt.ExecuteWorkflow,
				PlanActivityName: "image.plan", ResumeActivityName: "image.resume", ExecuteToolActivity: "image.tool",
			}))
			agentClient := rt.MustClientFor(definition)
			var out *api.RunOutput
			if backend == "awaited engine one-shot" {
				out, err = agentClient.OneShotRun(t.Context(), messages, agentruntime.WithRunID("image-run"))
				require.NoError(t, err)
			} else {
				_, err = base.CreateSession(t.Context(), "image-session", time.Now().UTC())
				require.NoError(t, err)
				prepared, err := agentClient.Prepare(t.Context(), "image-session", messages, agentruntime.WithRunID("image-run"))
				require.NoError(t, err)
				reference, err := prepared.MarshalBinary()
				require.NoError(t, err)
				require.Less(t, len(reference), 4096)
				restored, err := agentruntime.ParsePreparedRun(reference)
				require.NoError(t, err)
				handle, err := agentClient.StartPrepared(t.Context(), restored)
				require.NoError(t, err)
				if env != nil {
					request := service.startRequest()
					require.NotNil(t, request)
					require.Len(t, request.Input.Payloads, 1)
					input := new(api.RunInput)
					require.NoError(t, workflowcodec.NewDataConverter().FromPayload(request.Input.Payloads[0], input))
					require.Less(t, len(request.Input.Payloads[0].Data), 4096)
					// The SDK workflow receives the memo from this exact engine
					// submission, just as a real Temporal worker would.
					digest := decodePayload[[]byte](t, request.Memo.Fields[workflowStartRecipeMemoKey])
					env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: request.WorkflowId, TaskQueue: request.TaskQueue.Name})
					require.NoError(t, env.SetMemoOnStart(map[string]any{workflowStartRecipeMemoKey: digest}))
					env.ExecuteWorkflow("image.workflow", input)
					require.NoError(t, env.GetWorkflowError())
					require.NoError(t, env.GetWorkflowResult(&out))
				} else {
					out, err = handle.Wait(t.Context())
					if store.corrupt {
						require.ErrorContains(t, err, "completed literal contains invalid UTF-8")
						require.Nil(t, out)
						require.Zero(t, plan.calls, "no partial messages reach the planner")
						meta, err := store.LoadRun(t.Context(), "image-run")
						require.NoError(t, err)
						require.Equal(t, session.RunStatusFailed, meta.Status)
						return
					}
					require.NoError(t, err)
				}
			}
			require.Equal(t, "preserved", out.Final.Text())
			require.Equal(t, 1, plan.calls)
			meta, err := store.LoadRun(t.Context(), "image-run")
			require.NoError(t, err)
			require.Equal(t, session.RunStatusCompleted, meta.Status)
			seed, err := store.LoadRunSeed(t.Context(), meta.RunID, meta.SeedEndID)
			require.NoError(t, err)
			require.NotEmpty(t, seed.EndID)
			store.mu.Lock()
			defer store.mu.Unlock()
			parts, whole := 0, 0
			var original []byte
			for _, write := range store.writes {
				if write.Record.LiteralPart != nil {
					parts++
					original = append(original, write.Record.LiteralPart.Data...)
					require.Equal(t, parts == 2, write.Record.LiteralPart.Final)
				} else if len(write.Record.Messages) > 0 {
					whole++
				}
			}
			require.Equal(t, 2, parts)
			require.Equal(t, 2, whole, "complete literals before and after the image keep their message grouping")
			require.GreaterOrEqual(t, store.maxCommand, storage.MaxSeedCommandBytes-3, "base64 packing uses the measured capacity")
			imageMessages, err := transcript.DecodeRunLogDelta(original)
			require.NoError(t, err)
			require.Len(t, imageMessages, 1)
			part, ok := imageMessages[0].Parts[1].(model.ImagePart)
			require.True(t, ok)
			require.Equal(t, imageBytes, part.Bytes)
			require.Greater(t, store.pages, 1, "the real store must split the large literal across bounded pages")
			t.Logf("png_bytes=%d canonical_history_bytes=%d literal_parts=%d whole_records=%d seed_writes=%d max_command_bytes=%d max_page_bytes=%d pages=%d",
				len(imageBytes), len(want), parts, whole, len(store.writes), store.maxCommand, store.maxPage, store.pages)
		})
	}
}
