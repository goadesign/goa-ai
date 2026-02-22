package runtime

// workflow_await_queue.go contains workflow-side support for queued await
// prompts returned by planners.
//
// Contract:
// - Planners may return an Await barrier containing multiple ordered await
//   items (clarifications, questions, external tool handshakes).
// - The runtime publishes all await events, pauses once, then waits for each
//   item to be satisfied in order.
// - The runtime resumes planning exactly once after the entire await queue is
//   satisfied, so planners observe all user/external inputs together.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const awaitReasonQueue = "await_queue"

func (r *Runtime) waitAwaitConfirmation(
	ctx context.Context,
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	toolOpts engine.ActivityOptions,
	expectedChildren int,
	parentTracker *childTracker,
	turnID string,
	ctrl *interrupt.Controller,
	deadlines *runDeadlines,
	it confirmationAwait,
) ([]*planner.ToolResult, *RunOutput, error) {
	if deadlines == nil {
		return nil, nil, errors.New("missing run deadlines")
	}
	waitStartedAt := wfCtx.Now()
	dec, err := ctrl.WaitProvideConfirmation(ctx, 0)
	if err != nil {
		return nil, nil, err
	}
	deadlines.pause(wfCtx.Now().Sub(waitStartedAt))
	if dec == nil {
		return nil, nil, errors.New("await_confirmation: received nil confirmation decision")
	}
	if dec.ID != "" && dec.ID != it.awaitID {
		return nil, nil, fmt.Errorf("unexpected confirmation id %q (expected %q)", dec.ID, it.awaitID)
	}
	if dec.RequestedBy == "" {
		return nil, nil, fmt.Errorf("confirmation decision missing requested_by for %q (%s)", it.call.Name, it.call.ToolCallID)
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
		return nil, nil, err
	}

	// Confirmation gates tool execution. We represent both approval and denial as
	// a provider-visible tool_use + tool_result pair so planners see a deterministic
	// outcome for the tool call they requested.
	r.recordAssistantTurn(base, st.Transcript, []planner.ToolRequest{it.call}, st.Ledger)

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
			return nil, nil, err
		}
		resultJSON, err := r.marshalToolValue(ctx, it.call.Name, deniedResult, false)
		if err != nil {
			return nil, nil, fmt.Errorf("encode %s denied tool result for streaming: %w", it.call.Name, err)
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
				deniedResult,
				resultJSON,
				nil,
				formatResultPreview(it.call.Name, deniedResult),
				nil,
				0,
				nil,
				nil,
				nil,
			),
			turnID,
		); err != nil {
			return nil, nil, err
		}

		tr := &planner.ToolResult{
			Name:       it.call.Name,
			ToolCallID: it.call.ToolCallID,
			Result:     deniedResult,
			Error:      nil,
		}
		st.ToolEvents = append(st.ToolEvents, cloneToolResults([]*planner.ToolResult{tr})...)
		if err := r.appendUserToolResults(base, []planner.ToolRequest{it.call}, []*planner.ToolResult{tr}, st.Ledger); err != nil {
			return nil, nil, err
		}
		return []*planner.ToolResult{tr}, nil, nil
	}

	// Approved: execute the tool call.
	call := it.call
	if call.ToolCallID == "" {
		call.ToolCallID = generateDeterministicToolCallID(base.RunContext.RunID, call.TurnID, base.RunContext.Attempt, call.Name, 0)
	}

	grouped, timeouts := r.groupToolCallsByTimeout([]planner.ToolRequest{call}, input, toolOpts.Timeout)
	finishBy := time.Time{}
	if !deadlines.Hard.IsZero() {
		finishBy = deadlines.Hard.Add(-deadlines.finalizeReserve())
	}
	vals, timedOut, err := r.executeGroupedToolCalls(
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
		return nil, nil, err
	}
	st.ToolEvents = append(st.ToolEvents, cloneToolResults(vals)...)
	if err := r.appendUserToolResults(base, []planner.ToolRequest{call}, vals, st.Ledger); err != nil {
		return nil, nil, err
	}
	if timedOut {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonTimeBudget, deadlines.Hard)
		return nil, out, err
	}
	return vals, nil, nil
}

func (r *Runtime) handleAwaitQueue(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	resumeOpts engine.ActivityOptions,
	toolOpts engine.ActivityOptions,
	expectedChildren int,
	parentTracker *childTracker,
	ctrl *interrupt.Controller,
	deadlines *runDeadlines,
	turnID string,
	confirmations []confirmationAwait,
	items []planner.AwaitItem,
	priorToolResults []*planner.ToolResult,
) (*RunOutput, error) {
	ctx := wfCtx.Context()
	if ctrl == nil {
		return nil, errors.New("await not supported in inline runs")
	}
	if deadlines == nil {
		return nil, errors.New("missing run deadlines")
	}
	if len(confirmations) == 0 && len(items) == 0 {
		return nil, errors.New("await: empty await queue")
	}

	// Publish all await prompts up front so callers can render a wizard UX
	// without waiting for intermediate round-trips.
	for i, it := range confirmations {
		if it.plan == nil {
			return nil, fmt.Errorf("await confirmation item %d missing plan", i)
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
			return nil, err
		}
	}
	for i, it := range items {
		if err := r.publishAwaitQueueItem(ctx, input, base, st, turnID, it, i); err != nil {
			return nil, err
		}
	}

	if err := r.publishHook(
		ctx,
		hooks.NewRunPausedEvent(base.RunContext.RunID, input.AgentID, base.RunContext.SessionID, awaitReasonQueue, "runtime", nil, nil),
		turnID,
	); err != nil {
		return nil, err
	}
	// While awaiting external input we do not apply a timeout. The workflow should
	// remain blocked until the operator (or an external system) responds.
	waitTimeout := time.Duration(0)

	allToolResults := make([]*planner.ToolResult, 0, len(priorToolResults)+8)
	allToolResults = append(allToolResults, priorToolResults...)

	for _, it := range confirmations {
		res, out, err := r.waitAwaitConfirmation(ctx, wfCtx, reg, input, base, st, toolOpts, expectedChildren, parentTracker, turnID, ctrl, deadlines, it)
		if err != nil {
			return nil, err
		}
		if out != nil {
			return out, nil
		}
		if len(res) > 0 {
			allToolResults = append(allToolResults, res...)
		}
	}

	for _, it := range items {
		waitStartedAt := wfCtx.Now()
		res, err := r.waitAwaitQueueItem(ctx, ctrl, input, base, st, turnID, waitTimeout, it)
		deadlines.pause(wfCtx.Now().Sub(waitStartedAt))
		if err != nil {
			return nil, err
		}
		if len(res) > 0 {
			allToolResults = append(allToolResults, res...)
		}
	}

	if capFailures(allToolResults) > 0 {
		st.Caps.RemainingConsecutiveFailedToolCalls = decrementCap(
			st.Caps.RemainingConsecutiveFailedToolCalls,
			capFailures(allToolResults),
		)
		if st.Caps.MaxConsecutiveFailedToolCalls > 0 && st.Caps.RemainingConsecutiveFailedToolCalls <= 0 {
			out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonFailureCap, deadlines.Hard)
			return out, err
		}
	} else if st.Caps.MaxConsecutiveFailedToolCalls > 0 {
		st.Caps.RemainingConsecutiveFailedToolCalls = st.Caps.MaxConsecutiveFailedToolCalls
	}

	if out, err := r.handleMissingFieldsPolicy(wfCtx, reg, input, base, allToolResults, st.ToolEvents, st.AggUsage, &st.NextAttempt, turnID, ctrl, deadlines); err != nil {
		return nil, err
	} else if out != nil {
		return out, nil
	}

	protected, err := r.hardProtectionIfNeeded(ctx, input.AgentID, base, allToolResults, turnID)
	if err != nil {
		return nil, err
	}
	if protected {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonFailureCap, deadlines.Hard)
		return out, err
	}

	if err := r.publishHook(
		ctx,
		hooks.NewRunResumedEvent(base.RunContext.RunID, input.AgentID, base.RunContext.SessionID, "await_completed", "runtime", map[string]string{
			"resumed_by":    "await_queue",
			"confirmations": fmt.Sprintf("%d", len(confirmations)),
			"items":         fmt.Sprintf("%d", len(items)),
		}, 0),
		turnID,
	); err != nil {
		return nil, err
	}

	resumeReq, err := r.buildNextResumeRequest(ctx, input.AgentID, base, allToolResults, &st.NextAttempt)
	if err != nil {
		return nil, err
	}
	resOutput, err := r.runPlanActivity(wfCtx, reg.ResumeActivityName, resumeOpts, resumeReq, deadlines.Budget)
	if err != nil {
		return nil, err
	}
	if resOutput == nil || resOutput.Result == nil {
		return nil, fmt.Errorf("plan resume activity returned nil result after await")
	}
	st.AggUsage = addTokenUsage(st.AggUsage, resOutput.Usage)
	st.Result = resOutput.Result
	st.Transcript = resOutput.Transcript
	st.Ledger = transcript.FromModelMessages(st.Transcript)
	return nil, nil
}

func (r *Runtime) publishAwaitQueueItem(ctx context.Context, input *RunInput, base *planner.PlanInput, st *runLoopState, turnID string, it planner.AwaitItem, idx int) error {
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
		r.recordAssistantTurn(base, st.Transcript, []planner.ToolRequest{{
			Name:       q.ToolName,
			ToolCallID: q.ToolCallID,
			Payload:    q.Payload,
		}}, st.Ledger)
		if q.ToolCallID == "" {
			return errors.New("await_questions: missing tool_call_id")
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
		r.recordAssistantTurn(base, st.Transcript, awaitCalls, st.Ledger)
		for _, call := range awaitCalls {
			if call.ToolCallID == "" {
				continue
			}
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

func (r *Runtime) waitAwaitQueueItem(ctx context.Context, ctrl *interrupt.Controller, input *RunInput, base *planner.PlanInput, st *runLoopState, turnID string, timeout time.Duration, it planner.AwaitItem) ([]*planner.ToolResult, error) {
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
			base.Messages = append(base.Messages, &model.Message{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: ans.Answer}},
			})
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
		return r.consumeProvidedToolResults(ctx, input, base, st, turnID, rs, allowed, expected)
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
		return r.consumeProvidedToolResults(ctx, input, base, st, turnID, rs, allowed, expected)
	default:
		return nil, fmt.Errorf("unknown await item kind %q", it.Kind)
	}
}

func (r *Runtime) consumeProvidedToolResults(ctx context.Context, input *RunInput, base *planner.PlanInput, st *runLoopState, turnID string, rs *api.ToolResultsSet, allowed []planner.ToolRequest, expected map[string]struct{}) ([]*planner.ToolResult, error) {
	if rs == nil {
		return nil, errors.New("await: nil tool results set")
	}
	if len(rs.Results) == 0 {
		return nil, errors.New("await: no tool results provided")
	}

	seen := make(map[string]struct{}, len(rs.Results))
	for _, tr := range rs.Results {
		if tr == nil {
			return nil, errors.New("await: nil tool result")
		}
		if tr.ToolCallID == "" {
			return nil, fmt.Errorf("await: result for tool %q missing tool_call_id", tr.Name)
		}
		if expected != nil {
			if _, ok := expected[tr.ToolCallID]; !ok {
				return nil, fmt.Errorf("await: unexpected tool result for tool_call_id %q", tr.ToolCallID)
			}
		}
		if _, dup := seen[tr.ToolCallID]; dup {
			return nil, fmt.Errorf("await: duplicate result for tool_call_id %q", tr.ToolCallID)
		}
		seen[tr.ToolCallID] = struct{}{}
	}
	if expected != nil && len(seen) != len(expected) {
		return nil, fmt.Errorf("await: tool result ids did not match awaited tool_use ids (awaited=%d, got=%d)", len(expected), len(seen))
	}

	decoded, err := r.decodeToolEvents(ctx, rs.Results)
	if err != nil {
		return nil, err
	}

	// Record tool results in the run ledger and publish tool_result events for streaming.
	st.ToolEvents = append(st.ToolEvents, cloneToolResults(decoded)...)

	if err := r.appendUserToolResults(base, allowed, decoded, st.Ledger); err != nil {
		return nil, err
	}

	for i, tr := range decoded {
		if tr == nil {
			continue
		}
		var resultJSON json.RawMessage
		if i < len(rs.Results) {
			resultJSON = rs.Results[i].Result
		}
		if err := r.publishHook(
			ctx,
			hooks.NewToolResultReceivedEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				tr.Name,
				tr.ToolCallID,
				"",
				tr.Result,
				resultJSON,
				tr.ServerData,
				formatResultPreview(tr.Name, tr.Result),
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
	return decoded, nil
}
