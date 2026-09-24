package runtime

// Callback publication and start use the same immutable commands after lost
// replies. These tests commit through the real Store before injecting each loss.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type uncertainSynchronousHistoryStore struct {
	storage.Store
	step     string
	cancel   context.CancelFunc
	failed   bool
	commands map[string][][]byte
}

func TestRunOneShotRetainsPublicationThroughLostReplies(t *testing.T) {
	for _, step := range []string{"begin", "append", "publish", "start"} {
		for _, closed := range []bool{false, true} {
			name := step + "/running"
			if closed {
				name = step + "/closed"
			}
			t.Run(name, func(t *testing.T) {
				base := newTestStore()
				var owner storage.Store = base
				if closed {
					owner = &closedStartStore{Store: base, status: session.RunStatusCompleted}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				store := &uncertainSynchronousHistoryStore{
					Store: owner, step: step, cancel: cancel, commands: make(map[string][][]byte),
				}
				bus := &recordingHooks{}
				rt := newFromOptions(store, Options{Hooks: bus})
				calls := 0
				err := rt.RunOneShot(ctx, OneShotRunInput{
					AgentID: "agent", RunID: "run", TurnID: "turn",
				}, func(ctx context.Context) error {
					calls++
					return ctx.Err()
				})
				if closed {
					require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
					assert.Zero(t, calls)
					assert.Empty(t, bus.events)
				} else {
					require.ErrorIs(t, err, context.Canceled)
					assert.Equal(t, 1, calls)
				}
				require.True(t, store.failed)
				for _, operation := range []string{"begin", "append", "publish", "start"} {
					want := 1
					if operation == step {
						want = 2
					}
					require.Len(t, store.commands[operation], want, operation)
					for _, command := range store.commands[operation][1:] {
						assert.Equal(t, store.commands[operation][0], command, operation)
					}
				}
				meta, err := base.LoadRun(t.Context(), "run")
				require.NoError(t, err)
				assert.Equal(t, storage.EmptySeedEndID, meta.SeedEndID)
				page, err := base.ListRunRecords(t.Context(), "run", "", 10)
				require.NoError(t, err)
				require.Len(t, page.Events, 2)
				assert.Equal(t, hooks.RunStarted, page.Events[0].Type)
				assert.Equal(t, hooks.RunCompleted, page.Events[1].Type)
				assert.Equal(t, meta.StartedAt, page.Events[0].Timestamp)
				accepted, found, err := base.FindRunPreparation(t.Context(), storage.PreparationOperation{
					AgentID: "agent", RunID: "run", CommandID: "run",
				})
				require.NoError(t, err)
				require.True(t, found)
				assert.Equal(t, "run", accepted.Seed.Declaration.AttemptID)
				assert.Equal(t, storage.EmptySeedEndID, accepted.Seed.EndID)
				prepared, err := base.ListRunPreparationRecords(t.Context(), "run", accepted.EndID, "", 1)
				require.NoError(t, err)
				require.Len(t, prepared.Records, 1)
				assert.Empty(t, prepared.NextCursor)
				assert.NotContains(t, string(prepared.Records[0].Prepared), `"RequestDigest"`)
				assert.Contains(t, string(prepared.Records[0].Prepared), `"SynchronousStart"`)
			})
		}
	}
}

func (s *uncertainSynchronousHistoryStore) BeginRunSeed(ctx context.Context, command storage.SeedDeclaration) (storage.RunSeed, error) {
	if err := s.capture("begin", command); err != nil {
		return storage.RunSeed{}, err
	}
	result, err := s.Store.BeginRunSeed(ctx, command)
	if err == nil {
		err = s.lostReply("begin")
	}
	return result, err
}

func (s *uncertainSynchronousHistoryStore) AppendRunSeed(ctx context.Context, command storage.SeedAppend) (string, error) {
	if err := s.capture("append", command); err != nil {
		return "", err
	}
	result, err := s.Store.AppendRunSeed(ctx, command)
	if err == nil {
		err = s.lostReply("append")
	}
	return result, err
}

func (s *uncertainSynchronousHistoryStore) PublishRunSeed(ctx context.Context, command storage.SeedPublication) error {
	if err := s.capture("publish", command); err != nil {
		return err
	}
	if err := s.Store.PublishRunSeed(ctx, command); err != nil {
		return err
	}
	return s.lostReply("publish")
}

func (s *uncertainSynchronousHistoryStore) StartSynchronousRun(ctx context.Context, command storage.SynchronousRunStart) (storage.OneShotRunStartResult, error) {
	if err := s.capture("start", command); err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	result, err := s.Store.StartSynchronousRun(ctx, command)
	if err == nil {
		err = s.lostReply("start")
	}
	return result, err
}

// capture owns the serialized command before storage can return or mutate it.
func (s *uncertainSynchronousHistoryStore) capture(step string, command any) error {
	data, err := json.Marshal(command)
	if err != nil {
		return storage.NewContractError(err)
	}
	s.commands[step] = append(s.commands[step], data)
	return nil
}

// lostReply cancels the caller after the selected write has already committed.
func (s *uncertainSynchronousHistoryStore) lostReply(step string) error {
	if step != s.step || s.failed {
		return nil
	}
	s.failed = true
	s.cancel()
	return errors.New("storage reply lost after commit")
}
