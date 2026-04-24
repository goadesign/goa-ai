package runtime

// workflow_await_queue.go contains workflow-side support for queued await
// prompts returned by planners or the just-executed tool batch.
//
// Contract:
// - Await items may come from the planner result or tool-owned awaits emitted by
//   the current execution batch.
// - The runtime publishes the current await queue before pausing, then publishes
//   any newly discovered tool-pause awaits before waiting on them.
// - The runtime resumes planning exactly once after the entire await queue is
//   satisfied, so planners observe all user/external inputs together.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

const awaitReasonQueue = "await_queue"

func (r *Runtime) waitAwaitConfirmation(
	ctx context.Context,
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	toolOpts engine.ActivityOptions,
	expectedChildren int,
	parentTracker *childTracker,
	turnID string,
	ctrl *interrupt.Controller,
	deadlines *runDeadlines,
	it confirmationAwait,
) ([]stepToolRecord, []planner.AwaitItem, bool, error) {
	if deadlines == nil {
		return nil, nil, false, errors.New("missing run deadlines")
	}
	waitStartedAt := wfCtx.Now()
	dec, err := ctrl.WaitProvideConfirmation(ctx, 0)
	if err != nil {
		return nil, nil, false, err
	}
	deadlines.pause(wfCtx.Now().Sub(waitStartedAt))
	if dec == nil {
		return nil, nil, false, errors.New("await_confirmation: received nil confirmation decision")
	}
	if dec.ID != "" && dec.ID != it.awaitID {
		return nil, nil, false, fmt.Errorf("unexpected confirmation id %q (expected %q)", dec.ID, it.awaitID)
	}
	if dec.RequestedBy == "" {
		return nil, nil, false, fmt.Errorf("confirmation decision missing requested_by for %q (%s)", it.call.Name, it.call.ToolCallID)
	}

	approved := dec.Approved
	if err := r.publishHook(ctx, hooks.NewToolAuthorizationEvent(
		base.RunContext.RunID,
		input.AgentID,
		base.RunContext.SessionID,
		it.call.Name,
		it.call.ToolCallID,
		approved,
		it.plan.Prompt,
		dec.RequestedBy,
	), turnID); err != nil {
		return nil, nil, false, err
	}

	if !approved {
		deniedResult := it.plan.DeniedResult
		if err := r.publishHook(
			ctx,
			hooks.NewToolCallScheduledEvent(
				it.call.RunID,
				it.call.AgentID,
				it.call.SessionID,
				it.call.Name,
				it.call.ToolCallID,
				it.call.Payload,
				"",
				it.call.ParentToolCallID,
				expectedChildren,
			),
			turnID,
		); err != nil {
			return nil, nil, false, err
		}
		resultJSON, err := r.marshalToolValue(ctx, it.call.Name, deniedResult, nil)
		if err != nil {
			return nil, nil, false, fmt.Errorf("encode %s denied tool result for streaming: %w", it.call.Name, err)
		}
		if err := r.publishHook(
			ctx,
			hooks.NewToolResultReceivedEvent(
				it.call.RunID,
				it.call.AgentID,
				it.call.SessionID,
				it.call.Name,
				it.call.ToolCallID,
				it.call.ParentToolCallID,
				rawjson.Message(resultJSON),
				len(resultJSON),
				false,
				"",
				nil,
				formatResultPreviewForCall(ctx, r, &it.call, deniedResult, nil),
				nil,
				0,
				nil,
				nil,
				nil,
			),
			turnID,
		); err != nil {
			return nil, nil, false, err
		}

		tr := &planner.ToolResult{
			Name:       it.call.Name,
			ToolCallID: it.call.ToolCallID,
			Result:     deniedResult,
			Error:      nil,
		}
		records := []stepToolRecord{{call: it.call, result: tr}}
		return records, nil, false, nil
	}

	// Approved: execute the tool call.
	call := it.call
	if call.ToolCallID == "" {
		call.ToolCallID = generateDeterministicToolCallID(base.RunContext.RunID, call.TurnID, base.RunContext.Attempt, call.Name, 0)
	}

	grouped, timeouts := r.groupToolCallsByTimeout([]planner.ToolRequest{call}, input, toolOpts.StartToCloseTimeout)
	finishBy := time.Time{}
	if !deadlines.Hard.IsZero() {
		finishBy = deadlines.Hard.Add(-deadlines.finalizeReserve())
	}
	outcomes, timedOut, err := r.executeGroupedToolCalls(
		wfCtx,
		reg,
		input.AgentID,
		base,
		expectedChildren,
		parentTracker,
		finishBy,
		grouped,
		timeouts,
		toolOpts,
	)
	if err != nil {
		return nil, nil, false, err
	}
	records, err := stepToolRecordsFromExecutions([]planner.ToolRequest{call}, outcomes)
	if err != nil {
		return nil, nil, false, err
	}
	toolPauses := toolPausesFromRecords(records)
	if len(toolPauses) == 0 {
		return records, nil, timedOut, nil
	}
	pauseItems, err := toolPauseAwaitItems(toolPauses)
	if err != nil {
		return nil, nil, false, err
	}
	return records, pauseItems, timedOut, nil
}

func (l *workflowLoop) handleAwaitQueue(
	expectedChildren int,
	confirmations []confirmationAwait,
	items []planner.AwaitItem,
	batch *stepBatch,
) error {
	r := l.r
	wfCtx := l.wfCtx
	input := l.input
	base := l.base
	st := l.st
	ctrl := l.ctrl
	turnID := l.turnID
	ctx := wfCtx.Context()
	if ctrl == nil {
		return errors.New("await not supported in inline runs")
	}
	if len(confirmations) == 0 && len(items) == 0 {
		return errors.New("await: empty await queue")
	}

	// Publish the current queue before pausing so callers can render the initial
	// wizard state without waiting for intermediate round-trips.
	for i, it := range confirmations {
		if it.plan == nil {
			return fmt.Errorf("await confirmation item %d missing plan", i)
		}
		title := it.plan.Title
		if title == "" {
			title = "Confirm command"
		}
		if err := r.publishHook(ctx, hooks.NewAwaitConfirmationEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			it.awaitID,
			title,
			it.plan.Prompt,
			it.call.Name,
			it.call.ToolCallID,
			it.call.Payload,
		), turnID); err != nil {
			return err
		}
	}
	for i, it := range items {
		if err := r.admitAwaitItem(ctx, input, base, st, turnID, it, i); err != nil {
			return err
		}
	}

	if err := r.publishHook(
		ctx,
		hooks.NewRunPausedEvent(base.RunContext.RunID, input.AgentID, base.RunContext.SessionID, awaitReasonQueue, "runtime", nil, nil),
		turnID,
	); err != nil {
		return err
	}
	// While awaiting external input we do not apply a timeout. The workflow should
	// remain blocked until the operator (or an external system) responds.
	waitTimeout := time.Duration(0)

	for _, it := range confirmations {
		records, pauseItems, timedOut, err := r.waitAwaitConfirmation(ctx, wfCtx, l.reg, input, base, l.toolOpts, expectedChildren, l.parentTracker, turnID, ctrl, &l.deadlines, it)
		if err != nil {
			return err
		}
		if len(records) > 0 {
			batch.records = append(batch.records, records...)
		}
		batch.timedOut = batch.timedOut || timedOut
		if len(pauseItems) > 0 {
			for _, item := range pauseItems {
				if err := r.admitAwaitItem(ctx, input, base, st, turnID, item, len(items)); err != nil {
					return err
				}
				items = append(items, item)
			}
		}
	}

	for _, it := range items {
		waitStartedAt := wfCtx.Now()
		records, err := r.waitAwaitQueueItem(ctx, ctrl, input, base, turnID, waitTimeout, it)
		l.deadlines.pause(wfCtx.Now().Sub(waitStartedAt))
		if err != nil {
			return err
		}
		if len(records) > 0 {
			batch.records = append(batch.records, records...)
		}
	}

	batch.awaited = true
	batch.confirmations += len(confirmations)
	batch.awaitItems += len(items)
	return nil
}

// admitAwaitItem publishes the operator-facing await prompt and records any
// provider-native tool_use side effects required by tool-result awaits.
func (r *Runtime) admitAwaitItem(ctx context.Context, input *RunInput, base *planner.PlanInput, st *runLoopState, turnID string, it planner.AwaitItem, idx int) error {
	if it.Kind == "" {
		return fmt.Errorf("await item %d missing kind", idx)
	}

	switch it.Kind {
	case planner.AwaitItemKindClarification:
		c := it.Clarification
		if c == nil {
			return fmt.Errorf("await clarification item %d missing payload", idx)
		}
		return r.publishHook(ctx, hooks.NewAwaitClarificationEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			c.ID,
			c.Question,
			c.MissingFields,
			c.RestrictToTool,
			c.ExampleInput,
		), turnID)
	case planner.AwaitItemKindQuestions:
		q := it.Questions
		if q == nil {
			return fmt.Errorf("await questions item %d missing payload", idx)
		}
		if q.ToolCallID == "" {
			return errors.New("await_questions: missing tool_call_id")
		}
		qs := make([]hooks.AwaitQuestion, 0, len(q.Questions))
		for _, qq := range q.Questions {
			opts := make([]hooks.AwaitQuestionOption, 0, len(qq.Options))
			for _, o := range qq.Options {
				opts = append(opts, hooks.AwaitQuestionOption{ID: o.ID, Label: o.Label})
			}
			qs = append(qs, hooks.AwaitQuestion{
				ID:            qq.ID,
				Prompt:        qq.Prompt,
				AllowMultiple: qq.AllowMultiple,
				Options:       opts,
			})
		}
		if err := r.publishHook(ctx, hooks.NewAwaitQuestionsEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			q.ID,
			q.ToolName,
			q.ToolCallID,
			q.Payload,
			q.Title,
			qs,
		), turnID); err != nil {
			return err
		}
		// Questions are modeled as a provider-native tool use. Record the
		// assistant tool_use turn before waiting for out-of-band results.
		if err := r.recordAssistantTurn(ctx, input.AgentID, base, st.Transcript, []planner.ToolRequest{{
			Name:       q.ToolName,
			ToolCallID: q.ToolCallID,
			Payload:    q.Payload,
		}}, turnID); err != nil {
			return err
		}
		return r.publishHook(ctx, hooks.NewToolCallScheduledEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			q.ToolName,
			q.ToolCallID,
			q.Payload,
			"",
			"",
			0,
		), turnID)
	case planner.AwaitItemKindExternalTools:
		e := it.ExternalTools
		if e == nil {
			return fmt.Errorf("await external_tools item %d missing payload", idx)
		}
		if len(e.Items) == 0 {
			return errors.New("await_external_tools: no items in await")
		}
		items := make([]hooks.AwaitToolItem, 0, len(e.Items))
		awaitCalls := make([]planner.ToolRequest, 0, len(e.Items))
		for _, item := range e.Items {
			if item.ToolCallID == "" {
				return fmt.Errorf("await_external_tools: missing tool_call_id for external tool %q", item.Name)
			}
			items = append(items, hooks.AwaitToolItem{
				ToolName:   item.Name,
				ToolCallID: item.ToolCallID,
				Payload:    item.Payload,
			})
			awaitCalls = append(awaitCalls, planner.ToolRequest{
				Name:       item.Name,
				ToolCallID: item.ToolCallID,
				Payload:    item.Payload,
			})
		}
		if err := r.publishHook(ctx, hooks.NewAwaitExternalToolsEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			e.ID,
			items,
		), turnID); err != nil {
			return err
		}
		// External tools are modeled as a provider-native tool use. Record the
		// assistant tool_use turn before waiting for out-of-band results.
		if err := r.recordAssistantTurn(ctx, input.AgentID, base, st.Transcript, awaitCalls, turnID); err != nil {
			return err
		}
		for _, call := range awaitCalls {
			if err := r.publishHook(ctx, hooks.NewToolCallScheduledEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				call.Name,
				call.ToolCallID,
				call.Payload,
				"",
				"",
				0,
			), turnID); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown await item kind %q", it.Kind)
	}
}

func (r *Runtime) waitAwaitQueueItem(ctx context.Context, ctrl *interrupt.Controller, input *RunInput, base *planner.PlanInput, turnID string, timeout time.Duration, it planner.AwaitItem) ([]stepToolRecord, error) {
	switch it.Kind {
	case planner.AwaitItemKindClarification:
		c := it.Clarification
		if c == nil {
			return nil, errors.New("await clarification missing payload")
		}
		ans, err := ctrl.WaitProvideClarification(ctx, timeout)
		if err != nil {
			return nil, err
		}
		if ans == nil {
			return nil, errors.New("await clarification: nil answer")
		}
		if c.ID != "" && ans.ID != "" && ans.ID != c.ID {
			return nil, errors.New("unexpected await ID for clarification")
		}
		if ans.Answer != "" {
			if err := r.appendTranscriptMessages(ctx, input.AgentID, base, turnID, []*model.Message{{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: ans.Answer}},
			}}); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case planner.AwaitItemKindQuestions:
		q := it.Questions
		if q == nil {
			return nil, errors.New("await questions missing payload")
		}
		rs, err := ctrl.WaitProvideToolResults(ctx, timeout)
		if err != nil {
			return nil, err
		}
		if rs == nil {
			return nil, errors.New("await questions: nil tool results set")
		}
		if q.ID != "" && rs.ID != "" && rs.ID != q.ID {
			return nil, errors.New("unexpected await ID for questions")
		}
		expected := map[string]struct{}{q.ToolCallID: {}}
		allowed := []planner.ToolRequest{
			{
				Name:       q.ToolName,
				ToolCallID: q.ToolCallID,
				Payload:    q.Payload,
			},
		}
		return r.consumeProvidedToolResultRecords(ctx, input, base, turnID, rs, allowed, expected)
	case planner.AwaitItemKindExternalTools:
		e := it.ExternalTools
		if e == nil {
			return nil, errors.New("await external_tools missing payload")
		}
		rs, err := ctrl.WaitProvideToolResults(ctx, timeout)
		if err != nil {
			return nil, err
		}
		if rs == nil {
			return nil, errors.New("await external_tools: nil tool results set")
		}
		if e.ID != "" && rs.ID != "" && rs.ID != e.ID {
			return nil, errors.New("unexpected await ID for external_tools")
		}
		expected := make(map[string]struct{}, len(e.Items))
		allowed := make([]planner.ToolRequest, 0, len(e.Items))
		for _, it := range e.Items {
			if it.ToolCallID == "" {
				return nil, fmt.Errorf("await_external_tools: missing tool_call_id for external tool %q", it.Name)
			}
			expected[it.ToolCallID] = struct{}{}
			allowed = append(allowed, planner.ToolRequest{
				Name:       it.Name,
				ToolCallID: it.ToolCallID,
				Payload:    it.Payload,
			})
		}
		return r.consumeProvidedToolResultRecords(ctx, input, base, turnID, rs, allowed, expected)
	default:
		return nil, fmt.Errorf("unknown await item kind %q", it.Kind)
	}
}

func (r *Runtime) consumeProvidedToolResultRecords(ctx context.Context, input *RunInput, base *planner.PlanInput, turnID string, rs *api.ToolResultsSet, allowed []planner.ToolRequest, expected map[string]struct{}) ([]stepToolRecord, error) {
	if rs == nil {
		return nil, errors.New("await: nil tool results set")
	}
	if len(rs.Results) == 0 {
		return nil, errors.New("await: no tool results provided")
	}

	seen := make(map[string]struct{}, len(rs.Results))
	providedByID := make(map[string]*api.ProvidedToolResult, len(rs.Results))
	for _, item := range rs.Results {
		if item == nil {
			return nil, errors.New("await: nil tool result")
		}
		if item.ToolCallID == "" {
			return nil, fmt.Errorf("await: result for tool %q missing tool_call_id", item.Name)
		}
		if expected != nil {
			if _, ok := expected[item.ToolCallID]; !ok {
				return nil, fmt.Errorf("await: unexpected tool result for tool_call_id %q", item.ToolCallID)
			}
		}
		if _, dup := seen[item.ToolCallID]; dup {
			return nil, fmt.Errorf("await: duplicate result for tool_call_id %q", item.ToolCallID)
		}
		seen[item.ToolCallID] = struct{}{}
		providedByID[item.ToolCallID] = item
	}
	if expected != nil && len(seen) != len(expected) {
		return nil, fmt.Errorf("await: tool result ids did not match awaited tool_use ids (awaited=%d, got=%d)", len(expected), len(seen))
	}

	records, err := r.decodeProvidedToolRecords(ctx, allowed, providedByID)
	if err != nil {
		return nil, err
	}

	for _, record := range records {
		tr := record.result
		if tr == nil {
			continue
		}
		call := record.call
		if err := r.publishHook(
			ctx,
			hooks.NewToolResultReceivedEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				tr.Name,
				tr.ToolCallID,
				"",
				record.resultJSON,
				len(record.resultJSON),
				false,
				"",
				tr.ServerData,
				formatResultPreviewForCall(ctx, r, &call, tr.Result, tr.Bounds),
				tr.Bounds,
				0,
				nil,
				tr.RetryHint,
				tr.Error,
			),
			turnID,
		); err != nil {
			return nil, err
		}
	}
	return records, nil
}
