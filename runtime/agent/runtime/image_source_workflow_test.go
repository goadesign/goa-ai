// These tests use a generated service executor, the real workflow activities,
// and the canonical run log. The owner is synthetic: copying its descriptors
// proves framework independence from old result records, not an application's
// authorization, retention, or copy implementation.
package runtime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genimages "goa.design/goa-ai/internal/testimage/gen/images"
	genobserver "goa.design/goa-ai/internal/testimage/gen/images/agents/observer"
	genexecutor "goa.design/goa-ai/internal/testimage/gen/images/agents/observer/pictures"
	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	imageFixtureOwner struct {
		mu       sync.Mutex
		bodies   map[string][]byte
		allowed  map[string]bool
		reads    []string
		selected []string
		library  map[string]bool
	}

	imageFixturePlanner struct {
		start  func(context.Context, *planner.PlanInput) (*planner.PlanResult, error)
		resume func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error)
	}

	imageFixtureProvider struct {
		inputs [][]byte
	}

	imageFixtureStream struct {
		index int
	}

	imageFixtureEngine struct {
		engine.Engine
		mu      sync.Mutex
		tools   []*api.ToolOutput
		planner []*api.PlanActivityOutput
	}
)

func TestNativeImageGeneratedWorkflowAndCopiedHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	owner := newImageFixtureOwner(t)
	store := inmem.New()
	_, err := store.CreateSession(ctx, "holder", time.Now())
	require.NoError(t, err)
	engine := &imageFixtureEngine{Engine: engineinmem.New()}
	rt := runtime.New(store, runtime.WithEngine(engine),
		runtime.WithImageSourceResolver(genpictures.NativeImageSources(), owner.resolve))
	provider := &imageFixtureProvider{}
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	require.NoError(t, rt.RegisterModel("fixture", client))
	exec := genexecutor.NewObserverPicturesExec(genexecutor.WithView(owner.view))
	require.NoError(t, genobserver.RegisterUsedToolsets(ctx, rt, genobserver.WithPicturesExecutor(exec)))

	var canonical []*model.Message
	pl := &imageFixturePlanner{
		start: func(_ context.Context, _ *planner.PlanInput) (*planner.PlanResult, error) {
			// The twenty other resources never become requested image sources.
			calls := make([]planner.ToolRequest, 0, 2)
			for _, id := range []string{"photo-21", "photo-2"} {
				payload, err := genpictures.ViewPayloadCodec().ToJSON(&genpictures.ViewPayload{ID: id})
				require.NoError(t, err)
				calls = append(calls, planner.ToolRequest{Name: genpictures.View, Payload: payload})
			}
			return &planner.PlanResult{ToolCalls: calls}, nil
		},
		resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			owner.mu.Lock()
			assert.Empty(t, owner.reads, "execution, result recording and PlanResume decoding must not read pixels")
			clear(owner.library)
			owner.bodies["photo-22"] = slices.Clone(owner.bodies["photo-2"])
			owner.mu.Unlock()
			require.Len(t, input.ToolOutputs, 2)
			canonical = cloneImageFixtureMessages(t, input.Messages)
			var resultIDs []string
			var sources []model.ImageSourcePart
			for _, message := range canonical {
				for _, part := range message.Parts {
					switch part := part.(type) {
					case model.ToolResultPart:
						resultIDs = append(resultIDs, part.ToolUseID)
					case model.ImageSourcePart:
						sources = append(sources, part)
					case model.ImagePart:
						t.Error("expanded bytes leaked into canonical history")
					}
				}
			}
			require.Len(t, sources, 2)
			for i, output := range input.ToolOutputs {
				assert.Equal(t, output.ToolCallID, resultIDs[i])
				decoded, err := genpictures.ViewFixtureImageV1ServerDataCodec().FromJSON(sources[i].Data)
				require.NoError(t, err)
				assert.Equal(t, []string{"photo-21", "photo-2"}[i], decoded.ID)
				assert.NotContains(t, string(output.Result), "sha256")
				assert.Contains(t, string(output.ServerData), `"kind":"fixture.image.v1"`)
			}
			assert.NotEqual(t, resultIDs[0], resultIDs[1])
			modelClient, ok := input.Agent.ModelClient("fixture")
			require.True(t, ok)
			request := &model.Request{Model: "fixture", Messages: input.Messages,
				Tools: input.Agent.AdvertisedToolDefinitions()}
			response, err := modelClient.Complete(ctx, request)
			if err != nil {
				return nil, err
			}
			assert.Equal(t, provider.inputs[0], provider.inputs[1])
			stream, err := modelClient.Stream(ctx, request)
			if err != nil {
				return nil, err
			}
			summary, err := planner.ConsumeStream(ctx, stream)
			if err != nil {
				return nil, err
			}
			require.Len(t, provider.inputs, 4)
			for _, recorded := range provider.inputs[1:] {
				assert.Equal(t, provider.inputs[0], recorded)
			}
			assert.Equal(t, canonical, input.Messages)
			assert.Equal(t, response.Content[0].Text(), summary.Text)
			return &planner.PlanResult{FinalResponse: summary.FinalResponse()}, nil
		},
	}
	require.NoError(t, genobserver.RegisterObserverAgent(ctx, rt, genobserver.ObserverAgentConfig{
		Planner: pl, HistoryModel: client,
		HistoryCompression: &runtime.HistoryCompressionConfig{
			CompressAtMaxInputTokens: 200, KeepMaxTurns: 1, AllowEstimatedTokens: true,
		},
	}))
	initial := []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Compare photo 21 and the earlier photo 2."}}}}
	options := []runtime.RunOption{runtime.WithRunID("image-run"), runtime.WithTurnID("image-turn")}
	output, err := genobserver.NewClient(rt).Run(ctx, "holder", initial, options...)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, []string{"photo-21", "photo-2", "photo-21", "photo-2", "photo-21", "photo-2", "photo-21", "photo-2"}, owner.reads)
	require.Len(t, engine.tools, 2, "real ExecuteToolActivity outputs")
	require.Len(t, engine.planner, 2, "actual plan and resume activity outputs")
	replayed, err := transcript.BuildMessagesFromRunLog(ctx, store, "image-run")
	require.NoError(t, err)
	assert.Equal(t, canonical, replayed[:len(canonical)])

	// An exact start retry after a new resource exists returns the accepted run.
	_, err = genobserver.NewClient(rt).Run(ctx, "holder", initial, options...)
	require.NoError(t, err)
	assert.Len(t, owner.selected, 2)
	assert.Len(t, owner.reads, 8)

	// A fresh runtime has only retained kind decoders, no producing tool or
	// original result records. A new holder has its own access decisions.
	copyStore := inmem.New()
	_, err = copyStore.CreateSession(ctx, "copy", time.Now())
	require.NoError(t, err)
	owner.allowed["copy"] = true
	copyRuntime := runtime.New(copyStore, runtime.WithImageSourceResolver(genpictures.NativeImageSources(), owner.resolve))
	copyProvider := &imageFixtureProvider{}
	copyClient, err := model.NewClient(copyProvider)
	require.NoError(t, err)
	require.NoError(t, copyRuntime.RegisterModel("fixture", copyClient))
	copyDefinition := runtime.NewAgentDefinition(runtime.AgentRoute{
		ID: "copy.reader", WorkflowName: "copy.reader.workflow", DefaultTaskQueue: "copy",
	}, nil, nil, nil, nil, nil, nil)
	copyPlanner := &imageFixturePlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		assert.Empty(t, input.Agent.AdvertisedToolDefinitions())
		modelClient, ok := input.Agent.ModelClient("fixture")
		require.True(t, ok)
		request := &model.Request{Model: "fixture", Messages: input.Messages}
		response, err := modelClient.Complete(ctx, request)
		if err != nil {
			return nil, err
		}
		// Current authorization is checked again, even for the same descriptor.
		owner.allowed["copy"] = false
		_, err = modelClient.Complete(ctx, request)
		require.ErrorContains(t, err, "current access denied")
		require.Len(t, copyProvider.inputs, 1)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
	}}
	require.NoError(t, copyRuntime.RegisterAgent(ctx, runtime.AgentRegistration{
		Definition: copyDefinition, Planner: copyPlanner, WorkflowHandler: copyRuntime.ExecuteWorkflow,
		PlanActivityName: "copy.plan", ResumeActivityName: "copy.resume", ExecuteToolActivity: "copy.execute",
	}))
	_, err = copyRuntime.MustClientFor(copyDefinition).Run(ctx, "copy", replayed,
		runtime.WithRunID("copy-run"), runtime.WithTurnID("copy-turn"))
	require.NoError(t, err)
	assert.Len(t, owner.selected, 2, "copy reads do not reexecute the removed producer")
	t.Run("published history preparation and continuation", func(t *testing.T) {
		assertNativeImagePreparedContinuation(t, ctx, canonical, owner)
	})
	t.Run("strict provider preserves generated historical exchange", func(t *testing.T) {
		assertNativeImageStrictGeneratedExchange(t, ctx, canonical, owner)
	})
}

func (p *imageFixturePlanner) PlanStart(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	return p.start(ctx, input)
}

func (p *imageFixturePlanner) PlanResume(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return p.resume(ctx, input)
}

func (p *imageFixtureProvider) Complete(_ context.Context, request *model.Request) (*model.Response, error) {
	if err := p.record(request); err != nil {
		return nil, err
	}
	return &model.Response{Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Compared the selected images."}}}}, StopReason: "stop"}, nil
}

func (p *imageFixtureProvider) CountTokens(_ context.Context, request *model.Request) (model.TokenCount, error) {
	return model.TokenCount{InputTokens: 1, Exact: false, Model: "fixture"}, p.record(request)
}

func (p *imageFixtureProvider) Stream(_ context.Context, request *model.Request) (model.Streamer, error) {
	if err := p.record(request); err != nil {
		return nil, err
	}
	return &imageFixtureStream{}, nil
}

func (s *imageFixtureStream) Recv() (model.Chunk, error) {
	s.index++
	switch s.index {
	case 1:
		return model.TextChunk{Message: model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.TextPart{Text: "Compared the selected images."},
		}}}, nil
	case 2:
		return model.StopChunk{Reason: "stop"}, nil
	default:
		return nil, io.EOF
	}
}

func (s *imageFixtureStream) Response() *model.Response {
	if s.index < 3 {
		return nil
	}
	return &model.Response{Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{
		model.TextPart{Text: "Compared the selected images."},
	}}}, StopReason: "stop"}
}

func (*imageFixtureStream) Close() error {
	return nil
}

func (p *imageFixtureProvider) record(request *model.Request) error {
	if _, err := model.NewRequestContract(request); err != nil {
		return err
	}
	images := 0
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part, ok := part.(model.ImagePart); ok {
				images++
				if _, err := png.Decode(bytes.NewReader(part.Bytes)); err != nil {
					return err
				}
			}
		}
	}
	if images != 2 {
		return fmt.Errorf("expected two selected images, got %d", images)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	p.inputs = append(p.inputs, data)
	return nil
}

func newImageFixtureOwner(t *testing.T) *imageFixtureOwner {
	t.Helper()
	owner := &imageFixtureOwner{bodies: make(map[string][]byte), library: make(map[string]bool), allowed: map[string]bool{"holder": true}}
	for i := 1; i <= 21; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		img.Set(0, 0, color.RGBA{R: uint8(i), A: 255})
		var buffer bytes.Buffer
		require.NoError(t, png.Encode(&buffer, img))
		id := fmt.Sprintf("photo-%d", i)
		owner.bodies[id] = buffer.Bytes()
		owner.library[id] = true
	}
	return owner
}

func (o *imageFixtureOwner) view(_ context.Context, input any) (any, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	selection := input.(*genpictures.ViewPayload)
	body, ok := o.bodies[selection.ID]
	if !ok {
		return nil, errors.New("selected resource missing")
	}
	o.selected = append(o.selected, selection.ID)
	return &genimages.ViewResult{ID: selection.ID, Source: &genimages.ImageSource{
		ID: selection.ID, Format: "png", Size: int64(len(body)), Sha256: fmt.Sprintf("%x", sha256.Sum256(body)),
	}}, nil
}

func (o *imageFixtureOwner) resolve(ctx context.Context, current run.Context, source model.ImageSourcePart, allowance int64) (model.ImagePart, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		return model.ImagePart{}, errors.New("external read requires deadline")
	}
	if err := ctx.Err(); err != nil {
		return model.ImagePart{}, err
	}
	if !o.allowed[current.SessionID] {
		return model.ImagePart{}, errors.New("current access denied")
	}
	descriptor, err := genpictures.ViewFixtureImageV1ServerDataCodec().FromJSON(source.Data)
	if err != nil {
		return model.ImagePart{}, err
	}
	if int64(len(descriptor.Format)) > allowance || descriptor.Size > allowance-int64(len(descriptor.Format)) {
		return model.ImagePart{}, model.ErrImageSourceCapacity
	}
	body, ok := o.bodies[descriptor.ID]
	if !ok {
		return model.ImagePart{}, errors.New("retained resource missing")
	}
	if int64(len(body)) != descriptor.Size || fmt.Sprintf("%x", sha256.Sum256(body)) != descriptor.Sha256 {
		return model.ImagePart{}, errors.New("immutable image identity mismatch")
	}
	o.reads = append(o.reads, descriptor.ID)
	return model.ImagePart{Format: model.ImageFormat(descriptor.Format), Bytes: body}, nil
}

func (e *imageFixtureEngine) RegisterExecuteToolActivity(ctx context.Context, name string, options engine.ActivityOptions, fn func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error {
	return e.Engine.RegisterExecuteToolActivity(ctx, name, options, func(ctx context.Context, input *api.ToolInput) (*api.ToolOutput, error) {
		output, err := fn(ctx, input)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		e.tools = append(e.tools, output)
		e.mu.Unlock()
		return output, nil
	})
}

func (e *imageFixtureEngine) RegisterPlannerActivity(ctx context.Context, name string, options engine.ActivityOptions, fn func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error {
	return e.Engine.RegisterPlannerActivity(ctx, name, options, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		output, err := fn(ctx, input)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		e.planner = append(e.planner, output)
		e.mu.Unlock()
		return output, nil
	})
}

func cloneImageFixtureMessages(t *testing.T, messages []*model.Message) []*model.Message {
	t.Helper()
	data, err := json.Marshal(messages)
	require.NoError(t, err)
	var result []*model.Message
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}
