package runtime

// Store responses and decoded activity history use the same start-result
// validator. Invalid responses cannot reach hook observers or execution.

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type savedStartContext struct {
	*routeWorkflowContext
	result *api.StorageActivityResult
	calls  *atomic.Int32
}

func TestStartResultValidationBeforeHooks(t *testing.T) {
	for _, kind := range []storageCommandKind{
		storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart,
	} {
		for _, test := range []struct {
			name     string
			status   session.RunStatus
			inserted bool
			want     string
		}{
			{"missing", "", true, "unknown run status"},
			{"unknown", "unknown", true, "unknown run status"},
			{"closed inserted", session.RunStatusCompleted, true, "closed run replay inserted"},
		} {
			t.Run(fmt.Sprintf("%d/%s", kind, test.name), func(t *testing.T) {
				f := newClosedStartFixture(t, kind, session.RunStatusCompleted)
				sessionStatus := session.StatusActive
				if f.input.SessionID == "" {
					sessionStatus = ""
				}
				record := storage.AppendResult{ID: "stored", Inserted: test.inserted, SessionStatus: sessionStatus}
				f.runtime.Store = startSessionStatusStore{
					root: storage.RootRunStartResult{
						Outcome: session.RunStartProceed, RunStatus: test.status, Started: record,
					},
					child: storage.ChildRunStartResult{
						Outcome: session.RunStartProceed, RunStatus: test.status, ParentRecord: record, Started: record,
					},
					oneShot: storage.OneShotRunStartResult{RunStatus: test.status, Record: record},
					oneShotChild: storage.OneShotChildRunStartResult{
						RunStatus: test.status, ParentRecord: record, Started: record,
					},
				}

				result, err := f.runtime.executeStorageCommand(t.Context(), f.command)

				require.ErrorContains(t, err, test.want)
				assert.True(t, engine.IsActivityErrorNonRetryable(err))
				assert.Nil(t, result)
				assert.Empty(t, f.bus.events)
				assert.Empty(t, f.sink.snapshot())
			})
		}
	}
}

func TestStartResultRejectsInvalidRelations(t *testing.T) {
	for _, kind := range []storageCommandKind{
		storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart,
	} {
		for _, test := range []struct {
			name   string
			mutate func(*api.StartRunResult)
			want   string
		}{
			{"proceed reason", func(s *api.StartRunResult) { s.CancellationReason = run.CancellationReasonSessionEnded }, "cannot have a cancellation reason"},
			{"stop running", func(s *api.StartRunResult) {
				s.Outcome, s.CancellationReason = session.RunStartStop, run.CancellationReasonSessionEnded
			}, "requires canceled status"},
			{"stop other reason", func(s *api.StartRunResult) {
				s.Outcome, s.RunStatus, s.CancellationReason = session.RunStartStop, session.RunStatusCanceled, run.CancellationReasonUserRequested
			}, "session_ended reason"},
			{"missing records", func(s *api.StartRunResult) { s.Records = nil }, "records, want"},
			{"extra record", func(s *api.StartRunResult) { s.Records = append(s.Records, s.Records[0]) }, "records, want"},
			{"missing id", func(s *api.StartRunResult) { s.Records[0].ID = "" }, "no committed id"},
		} {
			t.Run(fmt.Sprintf("%d/%s", kind, test.name), func(t *testing.T) {
				f := newClosedStartFixture(t, kind, session.RunStatusCompleted)
				result := testStorageResult(f.command)
				test.mutate(runStartStorageResult(f.input, result))
				err := validateStorageResult(kind, result)
				assert.ErrorContains(t, err, test.want)
			})
		}
	}
}

func TestStartResultCodecAndOldHistory(t *testing.T) {
	dc := workflowcodec.NewDataConverter()
	for _, kind := range []storageCommandKind{
		storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart,
	} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			f := newClosedStartFixture(t, kind, session.RunStatusCompleted)
			result := testStorageResult(f.command)
			payload, err := dc.ToPayload(result)
			require.NoError(t, err)
			assert.Contains(t, string(payload.Data), `"RunStatus":"running"`)
			var decoded api.StorageActivityResult
			require.NoError(t, dc.FromPayload(payload, &decoded))
			assert.Equal(t, result, &decoded)
			require.NoError(t, validateStorageResult(kind, &decoded))

			payload.Data = []byte(strings.Replace(string(payload.Data), `"RunStatus":"running",`, "", 1))
			var old api.StorageActivityResult
			require.NoError(t, dc.FromPayload(payload, &old))
			wfCtx := &savedStartContext{routeWorkflowContext: f.context, result: &old, calls: &atomic.Int32{}}
			f.input.Messages = []*model.Message{{
				Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "preserve this input"}},
			}}
			f.input.RenderedPrompts = []prompt.RenderEvent{{PromptID: "agent.system", Version: "v1"}}
			output, err := f.runtime.ExecuteWorkflow(wfCtx, f.input)
			require.ErrorContains(t, err, `unknown run status ""`)
			assert.True(t, engine.IsActivityErrorNonRetryable(err))
			assert.Nil(t, output)
			assert.Empty(t, f.bus.events)
			assert.Empty(t, f.sink.snapshot())
			assert.Empty(t, f.context.lastPlannerCall.Name)
			assert.Empty(t, f.context.lastToolCall.Name)
			assert.Zero(t, f.store.ordinaryCalls)
			assert.Zero(t, f.store.terminalCalls)
			assert.EqualValues(t, 1, wfCtx.calls.Load())

			payload.Data = []byte(strings.Replace(string(payload.Data), `"Outcome":`, `"Unexpected":true,"Outcome":`, 1))
			assert.ErrorContains(t, dc.FromPayload(payload, &decoded), `unknown field "Unexpected"`)
		})
	}
	oversized := &commonpb.Payload{
		Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)},
		Data:     []byte(strings.Repeat("x", engine.MaxPayloadBytes+1)),
	}
	var decoded api.StorageActivityResult
	assert.Error(t, dc.FromPayload(oversized, &decoded))
}

func (s *savedStartContext) Context() context.Context {
	return engine.WithWorkflowContext(s.ctx, s)
}

func (s *savedStartContext) Detached() engine.WorkflowContext {
	return &savedStartContext{
		routeWorkflowContext: s.withContext(context.WithoutCancel(s.ctx)),
		result:               s.result,
		calls:                s.calls,
	}
}

func (s *savedStartContext) WithCancel() (engine.WorkflowContext, func()) {
	ctx, cancel := context.WithCancel(s.ctx)
	return &savedStartContext{routeWorkflowContext: s.withContext(ctx), result: s.result, calls: s.calls}, cancel
}

func (s *savedStartContext) ExecuteStorageActivity(engine.StorageActivityCall) (*api.StorageActivityResult, error) {
	s.calls.Add(1)
	return s.result, nil
}
