package runtime

// This file sends live model text and thinking from the planner
// activity to the configured trusted-host stream. These private updates never
// enter the durable run log or hook bus, and they are not safe to forward
// unchanged to an end-user client. Emitted assistant text is append-only. The
// workflow later persists either the complete accepted transcript or, when the
// response is rejected or fails, the exact text that reached the trusted host.

import (
	"context"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/stream"
)

type (
	// historyStatusEmitterKey attaches a transient status emitter to a model
	// request context so history policies can notify the host stream before
	// long-running preparation work such as context summarization.
	historyStatusEmitterKey struct{}

	// historyStatusEmitter publishes one transient operational status update for
	// the active run.
	historyStatusEmitter func(ctx context.Context, payload stream.StatusPayload) error
)

// publishModelOutput sends one live model update through the configured stream
// profile. One-shot runs have no session ID.
func (r *Runtime) publishModelOutput(
	ctx context.Context,
	sessionID string,
	event stream.Event,
) (bool, error) {
	if sessionID == "" || r.streamSubscriber == nil {
		return false, nil
	}
	return r.streamSubscriber.HandleModelOutputEvent(ctx, event)
}

// historyStatusClient attaches a request-scoped status emitter to the model
// call context so HistoryPolicy implementations can publish live status updates
// (such as context compaction progress) before invoking the provider.
func (r *Runtime) historyStatusClient(
	client model.Client,
	runID, sessionID string,
) model.Client {
	if sessionID == "" || r.streamSubscriber == nil {
		return client
	}
	emit := historyStatusEmitter(func(ctx context.Context, payload stream.StatusPayload) error {
		_, err := r.publishModelOutput(ctx, sessionID, stream.Status{
			Base: stream.NewBase(stream.EventStatus, runID, sessionID, payload),
			Data: payload,
		})
		return err
	})
	client, err := model.WithRequestPreparation(client, func(ctx context.Context, request *model.Request) (context.Context, *model.Request, error) {
		return context.WithValue(ctx, historyStatusEmitterKey{}, emit), request, nil
	})
	if err != nil {
		panic(err)
	}
	return client
}
