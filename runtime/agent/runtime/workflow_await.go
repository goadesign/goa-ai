package runtime

// workflow_await.go contains workflow-side support for “await” planner results.
//
// Contract:
// - Await is a first-class plan result: the runtime publishes typed await events,
//   pauses the run, then blocks waiting for the matching Provide* signal.
// - Await.Questions and Await.ExternalTools are treated as provider-native tool
//   handshakes: the assistant tool_use is recorded before blocking, and tool_result
//   blocks are appended when the external results arrive.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const (
	awaitReasonClarification = "await_clarification"
	awaitReasonExternal      = "await_external"
	awaitReasonQuestions     = "await_questions"
)

// handleAwaitOnlyResult executes an await-only planner result (no tool calls).
//
// Return contract:
// - **out != nil**: the run finalized (e.g., time budget expired while awaiting).
// - **out == nil && err == nil**: the await input was received and a new plan result is returned.
func (r *Runtime) handleAwaitOnlyResult(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	resumeOpts engine.ActivityOptions,
	ctrl *interrupt.Controller,
	budgetDeadline time.Time,
	hardDeadline time.Time,
	turnID string,
) (*RunOutput, error) {
	ctx := wfCtx.Context()
	r.logger.Info(ctx, "PlanResult has Await, handling await")
	if ctrl == nil {
		return nil, errors.New("await not supported in inline runs")
	}
	result := st.Result

	await := result.Await
	if await == nil {
		return nil, errors.New("await: missing await payload")
	}

	var reason string
	switch {
	case await.Clarification != nil:
		reason = awaitReasonClarification
	case await.Questions != nil:
		reason = awaitReasonQuestions
	case await.ExternalTools != nil:
		reason = awaitReasonExternal
	default:
		return nil, errors.New("await: missing clarification, questions, and external tools")
	}

	switch {
	case await.Clarification != nil:
		c := result.Await.Clarification
		if err := r.publishHook(ctx, hooks.NewAwaitClarificationEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			c.ID,
			c.Question,
			c.MissingFields,
			c.RestrictToTool,
			c.ExampleInput,
		), turnID); err != nil {
			return nil, err
		}
	case await.Questions != nil:
		q := result.Await.Questions
		qs := make([]hooks.AwaitQuestion, 0, len(q.Questions))
		for _, qq := range q.Questions {
			opts := make([]hooks.AwaitQuestionOption, 0, len(qq.Options))
			for _, o := range qq.Options {
				opts = append(opts, hooks.AwaitQuestionOption{
					ID:    o.ID,
					Label: o.Label,
				})
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
			return nil, err
		}
		if q.ToolCallID != "" {
			if err := r.publishHook(
				ctx,
				hooks.NewToolCallScheduledEvent(
					base.RunContext.RunID,
					input.AgentID,
					base.RunContext.SessionID,
					q.ToolName,
					q.ToolCallID,
					q.Payload,
					"",
					"",
					0,
				),
				turnID,
			); err != nil {
				return nil, err
			}
		}
	case await.ExternalTools != nil:
		e := result.Await.ExternalTools
		items := make([]hooks.AwaitToolItem, 0, len(e.Items))
		for _, it := range e.Items {
			items = append(items, hooks.AwaitToolItem{
				ToolName:   it.Name,
				ToolCallID: it.ToolCallID,
				Payload:    it.Payload,
			})
		}
		if err := r.publishHook(ctx, hooks.NewAwaitExternalToolsEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			e.ID,
			items,
		), turnID); err != nil {
			return nil, err
		}
		for _, it := range e.Items {
			if it.ToolCallID == "" {
				continue
			}
			if err := r.publishHook(
				ctx,
				hooks.NewToolCallScheduledEvent(
					base.RunContext.RunID,
					input.AgentID,
					base.RunContext.SessionID,
					it.Name,
					it.ToolCallID,
					it.Payload,
					"",
					"",
					0,
				),
				turnID,
			); err != nil {
				return nil, err
			}
		}
	}

	if err := r.publishHook(
		ctx,
		hooks.NewRunPausedEvent(base.RunContext.RunID, input.AgentID, base.RunContext.SessionID, reason, "runtime", nil, nil),
		turnID,
	); err != nil {
		return nil, err
	}

	timeout, ok := timeoutUntil(budgetDeadline, wfCtx.Now())
	if !ok {
		out, err := r.finalizeAwaitTimeout(wfCtx, reg, input, base, st, turnID, hardDeadline, reason)
		return out, err
	}

	if result.Await != nil && result.Await.Clarification != nil {
		ans, err := ctrl.WaitProvideClarification(ctx, timeout)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				out, err := r.finalizeAwaitTimeout(wfCtx, reg, input, base, st, turnID, hardDeadline, reason)
				return out, err
			}
			return nil, err
		}
		if c := result.Await.Clarification; c != nil && c.ID != "" && ans.ID != "" && ans.ID != c.ID {
			return nil, errors.New("unexpected await ID for clarification")
		}
		if ans.Answer != "" {
			base.Messages = append(base.Messages, &model.Message{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: ans.Answer}},
			})
		}
		if err := r.publishHook(
			ctx,
			hooks.NewRunResumedEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				"clarification_provided",
				ans.RunID,
				ans.Labels,
				1,
			),
			turnID,
		); err != nil {
			return nil, err
		}
		resumeReq, err := r.buildNextResumeRequest(input.AgentID, base, nil, &st.NextAttempt)
		if err != nil {
			return nil, err
		}
		resOutput, err := r.runPlanActivity(wfCtx, reg.ResumeActivityName, resumeOpts, resumeReq, budgetDeadline)
		if err != nil {
			return nil, err
		}
		if resOutput == nil || resOutput.Result == nil {
			return nil, fmt.Errorf("plan resume activity returned nil result after clarification")
		}
		st.AggUsage = addTokenUsage(st.AggUsage, resOutput.Usage)
		st.Result = resOutput.Result
		st.Transcript = resOutput.Transcript
		st.Ledger = transcript.FromModelMessages(st.Transcript)
		return nil, nil
	}

	var (
		awaitID     string
		awaitCalls  []planner.ToolRequest
		expectedIDs map[string]struct{}
		errPrefix   string
	)
	if q := result.Await.Questions; q != nil {
		if q.ToolCallID == "" {
			return nil, errors.New("await_questions: missing tool_call_id")
		}
		awaitID = q.ID
		errPrefix = "await_questions"
		expectedIDs = map[string]struct{}{
			q.ToolCallID: {},
		}
		awaitCalls = []planner.ToolRequest{
			{
				Name:       q.ToolName,
				ToolCallID: q.ToolCallID,
				Payload:    q.Payload,
			},
		}
	} else {
		e := result.Await.ExternalTools
		if e == nil {
			return nil, errors.New("await: missing clarification, questions, and external tools")
		}
		if len(e.Items) == 0 {
			return nil, errors.New("await_external_tools: no items in await")
		}
		awaitID = e.ID
		errPrefix = "await_external_tools"
		awaitCalls = make([]planner.ToolRequest, 0, len(e.Items))
		expectedIDs = make(map[string]struct{}, len(e.Items))
		for _, it := range e.Items {
			if it.ToolCallID == "" {
				return nil, fmt.Errorf(
					"await_external_tools: missing tool_call_id for external tool %q",
					it.Name,
				)
			}
			if _, dup := expectedIDs[it.ToolCallID]; dup {
				return nil, fmt.Errorf(
					"await_external_tools: duplicate awaited tool_call_id %q",
					it.ToolCallID,
				)
			}
			expectedIDs[it.ToolCallID] = struct{}{}
			awaitCalls = append(awaitCalls, planner.ToolRequest{
				Name:       it.Name,
				ToolCallID: it.ToolCallID,
				Payload:    it.Payload,
			})
		}
	}
	r.recordAssistantTurn(base, st.Transcript, awaitCalls, st.Ledger)

	rs, err := ctrl.WaitProvideToolResults(ctx, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			out, err := r.finalizeAwaitTimeout(wfCtx, reg, input, base, st, turnID, hardDeadline, reason)
			return out, err
		}
		return nil, err
	}
	if awaitID != "" && rs.ID != "" && rs.ID != awaitID {
		return nil, fmt.Errorf("unexpected await ID for %s", errPrefix)
	}
	if len(rs.Results) == 0 {
		return nil, fmt.Errorf("%s: no tool results provided", errPrefix)
	}
	seen := make(map[string]struct{}, len(rs.Results))
	for _, tr := range rs.Results {
		if tr == nil {
			return nil, fmt.Errorf("%s: nil tool result", errPrefix)
		}
		if tr.ToolCallID == "" {
			return nil, fmt.Errorf(
				"%s: result for tool %q missing tool_call_id",
				errPrefix,
				tr.Name,
			)
		}
		if _, ok := expectedIDs[tr.ToolCallID]; !ok {
			return nil, fmt.Errorf(
				"%s: unexpected tool result for tool_call_id %q",
				errPrefix,
				tr.ToolCallID,
			)
		}
		if _, dup := seen[tr.ToolCallID]; dup {
			return nil, fmt.Errorf(
				"%s: duplicate result for tool_call_id %q",
				errPrefix,
				tr.ToolCallID,
			)
		}
		seen[tr.ToolCallID] = struct{}{}
	}
	if len(seen) != len(expectedIDs) {
		return nil, fmt.Errorf(
			"%s: tool result ids did not match awaited tool_use ids (awaited=%d, got=%d)",
			errPrefix,
			len(expectedIDs),
			len(seen),
		)
	}

	lastToolResults := rs.Results
	for _, tr := range lastToolResults {
		if tr == nil {
			continue
		}
		if spec, ok := r.toolSpec(tr.Name); ok && spec.BoundedResult && tr.Error == nil && tr.Bounds == nil {
			b := deriveBounds(tr.Result)
			if b == nil {
				return nil, fmt.Errorf(
					"bounded tool %q external result missing bounds (tool_call_id=%s, type=%T)",
					tr.Name,
					tr.ToolCallID,
					tr.Result,
				)
			}
			tr.Bounds = b
		}
	}

	st.ToolEvents = append(st.ToolEvents, cloneToolResults(rs.Results)...)
	r.appendUserToolResults(base, awaitCalls, lastToolResults, st.Ledger, nil)

	for _, tr := range lastToolResults {
		if tr == nil {
			continue
		}
		var resultJSON json.RawMessage
		if tr.Error == nil {
			var err error
			resultJSON, err = r.marshalToolValue(ctx, tr.Name, tr.Result, false)
			if err != nil {
				return nil, fmt.Errorf("encode %s tool result for streaming: %w", tr.Name, err)
			}
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
				formatResultPreview(tr.Name, tr.Result),
				tr.Bounds,
				nil,
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

	if err := r.publishHook(
		ctx,
		hooks.NewRunResumedEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			"tool_results_provided",
			input.RunID,
			nil,
			0,
		),
		turnID,
	); err != nil {
		return nil, err
	}

	resumeReq, err := r.buildNextResumeRequest(input.AgentID, base, lastToolResults, &st.NextAttempt)
	if err != nil {
		return nil, err
	}
	resOutput, err := r.runPlanActivity(wfCtx, reg.ResumeActivityName, resumeOpts, resumeReq, budgetDeadline)
	if err != nil {
		return nil, err
	}
	if resOutput == nil || resOutput.Result == nil {
		return nil, fmt.Errorf("plan resume activity returned nil result after tool results")
	}
	st.AggUsage = addTokenUsage(st.AggUsage, resOutput.Usage)
	st.Result = resOutput.Result
	st.Transcript = resOutput.Transcript
	st.Ledger = transcript.FromModelMessages(st.Transcript)
	return nil, nil
}

// finalizeAwaitTimeout converts an expired await into a deterministic RunResumedEvent
// and then requests finalization from the planner.
func (r *Runtime) finalizeAwaitTimeout(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	turnID string,
	hardDeadline time.Time,
	reason string,
) (*RunOutput, error) {
	ctx := wfCtx.Context()
	if err := r.publishHook(ctx, hooks.NewRunResumedEvent(
		base.RunContext.RunID,
		input.AgentID,
		base.RunContext.SessionID,
		"await_timeout",
		"runtime",
		map[string]string{
			"resumed_by": "await_timeout",
			"await":      reason,
		},
		0,
	), turnID); err != nil {
		return nil, err
	}
	return r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonAwaitTimeout, hardDeadline)
}

// handleAwaitAfterTools executes the “mixed” mode where the planner returned ToolCalls
// plus an Await handshake (Questions or ExternalTools): it publishes the await,
// pauses, waits for ProvideToolResults, records external tool results, and resumes
// planning.
func (r *Runtime) handleAwaitAfterTools(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	await *planner.Await,
	declaredCalls []planner.ToolRequest,
	awaitExpectedIDs map[string]struct{},
	artifactsModeByCallID map[string]tools.ArtifactsMode,
	internalResults []*planner.ToolResult,
	st *runLoopState,
	resumeOpts engine.ActivityOptions,
	ctrl *interrupt.Controller,
	budgetDeadline time.Time,
	hardDeadline time.Time,
	turnID string,
) (*RunOutput, error) {
	ctx := wfCtx.Context()
	if ctrl == nil {
		return nil, errors.New("await not supported in inline runs")
	}
	if await == nil {
		return nil, errors.New("missing await")
	}
	if await.Clarification != nil {
		return nil, errors.New("planner returned both tool calls and await clarification")
	}
	if await.ExternalTools != nil && await.Questions != nil {
		return nil, errors.New("planner returned multiple await kinds with tool calls")
	}

	var (
		awaitID   string
		reason    string
		errPrefix string
	)
	switch {
	case await.Questions != nil:
		q := await.Questions
		awaitID = q.ID
		reason = awaitReasonQuestions
		errPrefix = "await_questions"
		qs := make([]hooks.AwaitQuestion, 0, len(q.Questions))
		for _, qq := range q.Questions {
			opts := make([]hooks.AwaitQuestionOption, 0, len(qq.Options))
			for _, o := range qq.Options {
				opts = append(opts, hooks.AwaitQuestionOption{
					ID:    o.ID,
					Label: o.Label,
				})
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
			return nil, err
		}
		if q.ToolCallID != "" {
			if err := r.publishHook(
				ctx,
				hooks.NewToolCallScheduledEvent(
					base.RunContext.RunID,
					input.AgentID,
					base.RunContext.SessionID,
					q.ToolName,
					q.ToolCallID,
					q.Payload,
					"",
					"",
					0,
				),
				turnID,
			); err != nil {
				return nil, err
			}
		}
	case await.ExternalTools != nil:
		e := await.ExternalTools
		awaitID = e.ID
		reason = awaitReasonExternal
		errPrefix = "await_external_tools"
		items := make([]hooks.AwaitToolItem, 0, len(e.Items))
		for _, it := range e.Items {
			items = append(items, hooks.AwaitToolItem{
				ToolName:   it.Name,
				ToolCallID: it.ToolCallID,
				Payload:    it.Payload,
			})
		}
		if err := r.publishHook(ctx, hooks.NewAwaitExternalToolsEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			e.ID,
			items,
		), turnID); err != nil {
			return nil, err
		}
		for _, it := range e.Items {
			if it.ToolCallID == "" {
				continue
			}
			if err := r.publishHook(
				ctx,
				hooks.NewToolCallScheduledEvent(
					base.RunContext.RunID,
					input.AgentID,
					base.RunContext.SessionID,
					it.Name,
					it.ToolCallID,
					it.Payload,
					"",
					"",
					0,
				),
				turnID,
			); err != nil {
				return nil, err
			}
		}
	default:
		return nil, errors.New("await with tool calls is only supported for questions or external_tools")
	}

	if err := r.publishHook(ctx, hooks.NewRunPausedEvent(
		base.RunContext.RunID,
		input.AgentID,
		base.RunContext.SessionID,
		reason,
		"runtime",
		nil,
		nil,
	), turnID); err != nil {
		return nil, err
	}

	timeout, ok := timeoutUntil(budgetDeadline, wfCtx.Now())
	if !ok {
		out, err := r.finalizeAwaitTimeout(wfCtx, reg, input, base, st, turnID, hardDeadline, "await_external")
		return out, err
	}

	rs, err := ctrl.WaitProvideToolResults(ctx, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			out, err := r.finalizeAwaitTimeout(wfCtx, reg, input, base, st, turnID, hardDeadline, reason)
			return out, err
		}
		return nil, err
	}

	if awaitID != "" && rs.ID != "" && rs.ID != awaitID {
		return nil, fmt.Errorf("unexpected await ID for %s", errPrefix)
	}

	if len(rs.Results) == 0 {
		return nil, fmt.Errorf("%s: no tool results provided", errPrefix)
	}
	seen := make(map[string]struct{}, len(rs.Results))
	for _, tr := range rs.Results {
		if tr == nil {
			return nil, fmt.Errorf("%s: nil tool result", errPrefix)
		}
		if tr.ToolCallID == "" {
			return nil, fmt.Errorf(
				"%s: result for tool %q missing tool_call_id",
				errPrefix,
				tr.Name,
			)
		}
		if awaitExpectedIDs != nil {
			if _, ok := awaitExpectedIDs[tr.ToolCallID]; !ok {
				return nil, fmt.Errorf(
					"%s: unexpected tool result for tool_call_id %q",
					errPrefix,
					tr.ToolCallID,
				)
			}
		}
		if _, dup := seen[tr.ToolCallID]; dup {
			return nil, fmt.Errorf(
				"%s: duplicate result for tool_call_id %q",
				errPrefix,
				tr.ToolCallID,
			)
		}
		seen[tr.ToolCallID] = struct{}{}
	}
	if awaitExpectedIDs != nil && len(seen) != len(awaitExpectedIDs) {
		return nil, fmt.Errorf(
			"%s: tool result ids did not match awaited tool_use ids (awaited=%d, got=%d)",
			errPrefix,
			len(awaitExpectedIDs),
			len(seen),
		)
	}

	for _, tr := range rs.Results {
		if tr == nil {
			continue
		}
		if spec, ok := r.toolSpec(tr.Name); ok && spec.BoundedResult && tr.Error == nil && tr.Bounds == nil {
			b := deriveBounds(tr.Result)
			if b == nil {
				return nil, fmt.Errorf(
					"bounded tool %q external result missing bounds (tool_call_id=%s, type=%T)",
					tr.Name,
					tr.ToolCallID,
					tr.Result,
				)
			}
			tr.Bounds = b
		}
	}

	lastToolResults := internalResults
	lastToolResults = append(lastToolResults, rs.Results...)
	st.ToolEvents = append(st.ToolEvents, cloneToolResults(rs.Results)...)
	r.appendUserToolResults(base, declaredCalls, lastToolResults, st.Ledger, artifactsModeByCallID)

	for _, tr := range rs.Results {
		if tr == nil {
			continue
		}
		var resultJSON json.RawMessage
		if tr.Error == nil {
			var err error
			resultJSON, err = r.marshalToolValue(ctx, tr.Name, tr.Result, false)
			if err != nil {
				return nil, fmt.Errorf("encode %s tool result for streaming: %w", tr.Name, err)
			}
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
				formatResultPreview(tr.Name, tr.Result),
				tr.Bounds,
				nil,
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

	if err := r.publishHook(
		ctx,
		hooks.NewRunResumedEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			"tool_results_provided",
			input.RunID,
			nil,
			0,
		),
		turnID,
	); err != nil {
		return nil, err
	}

	resumeReq, err := r.buildNextResumeRequest(input.AgentID, base, lastToolResults, &st.NextAttempt)
	if err != nil {
		return nil, err
	}
	resOutput, err := r.runPlanActivity(
		wfCtx,
		reg.ResumeActivityName,
		resumeOpts,
		resumeReq,
		budgetDeadline,
	)
	if err != nil {
		return nil, err
	}
	if resOutput == nil || resOutput.Result == nil {
		return nil, fmt.Errorf("plan activity returned nil result on resume")
	}
	st.AggUsage = addTokenUsage(st.AggUsage, resOutput.Usage)
	st.Result = resOutput.Result
	st.Transcript = resOutput.Transcript
	st.Ledger = transcript.FromModelMessages(st.Transcript)
	return nil, nil
}
