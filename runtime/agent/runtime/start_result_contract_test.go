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
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type savedStartContext struct {
	*routeWorkflowContext
	result    *api.StorageActivityResult
	calls     *atomic.Int32
	terminals *[]*api.UnstartedTerminalCommand
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
				f := newClosedStartFixture(t, kind, session.RunStatusCompleted, true)
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
					oneShot: storage.OneShotRunStartResult{Outcome: session.RunStartProceed, RunStatus: test.status, Record: record},
					oneShotChild: storage.OneShotChildRunStartResult{
						Outcome: session.RunStartProceed, RunStatus: test.status, ParentRecord: record, Started: record,
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
			{"stop completed", func(s *api.StartRunResult) {
				s.Outcome, s.RunStatus, s.CancellationReason = session.RunStartStop, session.RunStatusCompleted, run.CancellationReasonSessionEnded
			}, "requires running, canceled, or failed status"},
			{"stop missing reason", func(s *api.StartRunResult) {
				s.Outcome, s.RunStatus, s.CancellationReason = session.RunStartStop, session.RunStatusCanceled, ""
			}, "nonempty cancellation reason"},
			{"missing records", func(s *api.StartRunResult) { s.Records = nil }, "records, want"},
			{"extra record", func(s *api.StartRunResult) { s.Records = append(s.Records, s.Records[0]) }, "records, want"},
			{"missing id", func(s *api.StartRunResult) { s.Records[0].ID = "" }, "no committed id"},
		} {
			t.Run(fmt.Sprintf("%d/%s", kind, test.name), func(t *testing.T) {
				f := newClosedStartFixture(t, kind, session.RunStatusCompleted, true)
				result := testStorageResult(f.command)
				test.mutate(runStartStorageResult(f.input, result))
				err := validateStorageResult(kind, result)
				assert.ErrorContains(t, err, test.want)
			})
		}
	}
}

func TestStartResultPreservesOwnerCancellationReason(t *testing.T) {
	for _, kind := range []storageCommandKind{
		storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart,
	} {
		for _, reason := range []string{run.CancellationReasonSessionEnded, run.CancellationReasonUserRequested, run.CancellationReasonEngineCanceled} {
			t.Run(fmt.Sprintf("%d/%s", kind, reason), func(t *testing.T) {
				f := newClosedStartFixture(t, kind, session.RunStatusCanceled, true)
				result := testStorageResult(f.command)
				start := runStartStorageResult(f.input, result)
				start.Outcome, start.RunStatus, start.CancellationReason = session.RunStartStop, session.RunStatusCanceled, reason
				start.Records = append(start.Records, storage.AppendResult{ID: "cancellation", SessionStatus: start.Records[0].SessionStatus})
				require.NoError(t, validateStorageResult(kind, result))
				assert.Equal(t, reason, start.CancellationReason)
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
			f := newClosedStartFixture(t, kind, session.RunStatusCompleted, true)
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
			wfCtx := &savedStartContext{routeWorkflowContext: f.context, result: &old, calls: &atomic.Int32{}, terminals: &[]*api.UnstartedTerminalCommand{}}
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
			require.Len(t, *wfCtx.terminals, 1)
			terminal := (*wfCtx.terminals)[0]
			assert.Equal(t, f.input.RunID, terminal.RunID)
			require.NotNil(t, terminal.PublishedSeed)
			assert.Equal(t, f.input.SeedEndID, terminal.PublishedSeed.EndID)
			failure, ok := terminal.Outcome.AsFailed()
			require.True(t, ok)
			require.NotNil(t, failure.Message)
			assert.Equal(t, hooks.PublicErrorInternal, *failure.Message)
			require.NotNil(t, failure.DebugMessage)
			assert.Contains(t, *failure.DebugMessage, `unknown run status ""`)

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
		terminals:            s.terminals,
	}
}

func (s *savedStartContext) WithCancel() (engine.WorkflowContext, func()) {
	ctx, cancel := context.WithCancel(s.ctx)
	return &savedStartContext{routeWorkflowContext: s.withContext(ctx), result: s.result, calls: s.calls, terminals: s.terminals}, cancel
}

// Saved start history returns its malformed response only for the start command.
// A later failure write uses the real store so the test observes durable completion.
func (s *savedStartContext) ExecuteStorageActivity(call engine.StorageActivityCall) (*api.StorageActivityResult, error) {
	command := call.Command
	if command.RootStart != nil || command.ChildStart != nil || command.OneShotStart != nil || command.OneShotChildStart != nil {
		s.calls.Add(1)
		return s.result, nil
	}
	if command.UnstartedTerminal != nil {
		*s.terminals = append(*s.terminals, command.UnstartedTerminal)
	}
	return s.routeWorkflowContext.ExecuteStorageActivity(call)
}
