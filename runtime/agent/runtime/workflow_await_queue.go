package runtime

// workflow_await_queue.go contains workflow-side support for queued await
// prompts returned by planners or the just-executed tool batch.
//
// Contract:
// - Await items may come from the planner result or tool-owned awaits emitted by
//   the current execution batch.
// - The runtime publishes the current await queue and ends the workflow with a
//   versioned suspension checkpoint.
// - A later workflow consumes the queue in order and resumes planning exactly
//   once after every item is satisfied.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func (r *Runtime) resolveConfirmationDecision(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	toolOpts engine.ActivityOptions,
	call planner.ToolRequest,
	awaitID string,
	plan *confirmationPlan,
	dec *api.ConfirmationDecision,
	expectedChildren int,
	parentTracker *childTracker,
	turnID string,
	deadlines *runDeadlines,
) ([]stepToolRecord, []planner.AwaitItem, bool, error) {
	ctx := wfCtx.Context()
	it := confirmationAwait{awaitID: awaitID, call: call, plan: plan}
	if deadlines == nil {
		return nil, nil, false, errors.New("missing run deadlines")
	}
	if dec == nil {
		return nil, nil, false, errors.New("await_confirmation: received nil confirmation decision")
	}
	if dec.ID != it.awaitID {
		return nil, nil, false, fmt.Errorf("unexpected confirmation id %q (expected %q)", dec.ID, it.awaitID)
	}
	if dec.RequestedBy == "" {
		return nil, nil, false, fmt.Errorf("confirmation decision missing requested_by for %q (%s)", it.call.Name, it.call.ToolCallID)
	}

	approved := dec.Approved
	var decisionRecords []stepToolRecord
	if !approved {
		decisionRecords = []stepToolRecord{{
			call:             it.call,
			scheduleRequired: true,
			scheduleExact:    true,
			expectedChildren: expectedChildren,
			result: &planner.ToolResult{
				Name:       it.call.Name,
				ToolCallID: it.call.ToolCallID,
				Result:     it.plan.DeniedResult,
			},
			requiresResume: true,
		}}
	}
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
		return decisionRecords, nil, false, err
	}

	if !approved {
		deniedResult := it.plan.DeniedResult
		if err := r.publishHook(
			ctx,
			newToolCallScheduledEvent(
				it.call.RunID,
				it.call.AgentID,
				it.call.SessionID,
				it.call,
				"",
				it.call.ParentToolCallID,
				expectedChildren,
			),
			turnID,
		); err != nil {
			return decisionRecords, nil, false, err
		}
		decisionRecords[0].scheduleRequired = false
		resultJSON, err := r.marshalToolValue(ctx, it.call.Name, deniedResult, nil)
		if err != nil {
			return decisionRecords, nil, false, fmt.Errorf("encode %s denied tool result for streaming: %w", it.call.Name, err)
		}
		preview, err := formatResultPreviewForCall(ctx, r, &it.call, deniedResult, nil)
		if err != nil {
			return decisionRecords, nil, false, err
		}
		event := hooks.NewToolResultReceivedEvent(
			it.call.RunID,
			it.call.AgentID,
			it.call.SessionID,
			it.call.RunID,
			it.call.Name,
			it.call.ToolCallID,
			it.call.ParentToolCallID,
			rawjson.Message(resultJSON),
			len(resultJSON),
			false,
			"",
			nil,
			preview,
			nil,
			0,
			nil,
			nil,
		)
		prepared, err := prepareHookRecordInput(ctx, event, turnID)
		if err != nil {
			return decisionRecords, nil, false, err
		}
		decisionRecords[0].resultRecord = prepared
		if err := r.publishPreparedHook(ctx, prepared, engine.ActivityOptions{}); err != nil {
			return decisionRecords, nil, false, err
		}
		decisionRecords[0].resultPublished = true
		return decisionRecords, nil, false, nil
	}

	// Approved: execute the tool call.
	if call.ToolCallID == "" {
		call.ToolCallID = generateDeterministicToolCallID(base.RunContext.RunID, call.TurnID, base.RunContext.Attempt, call.Name, 0)
	}

	grouped, timeouts := r.groupToolCallsByTimeout([]planner.ToolRequest{call}, input, toolOpts.StartToCloseTimeout)
	finishBy := deadlines.Budget
	if r.isBookkeeping(call.Name) {
		finishBy = deadlines.Hard
	}
	outcomes, timedOut, executionErr := r.executeGroupedToolCalls(
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
	records, resultErr := stepToolRecordsAfterExecution(
		[]planner.ToolRequest{call},
		outcomes,
		executionErr,
	)
	if err := errors.Join(executionErr, resultErr); err != nil {
		return records, nil, timedOut, err
	}
	clarifications := toolClarificationsFromRecords(records)
	if len(clarifications) == 0 {
		return records, nil, timedOut, nil
	}
	clarificationItems, err := toolClarificationAwaitItems(clarifications)
	if err != nil {
		return records, nil, timedOut, err
	}
	return records, clarificationItems, timedOut, nil
}

func (l *workflowLoop) handleAwaitQueue(
	confirmations []confirmationAwait,
	items []planner.AwaitItem,
	batch *stepBatch,
) error {
	r := l.r
	input := l.input
	base := l.base
	turnID := l.turnID
	ctx := l.wfCtx.Context()
	if len(confirmations) == 0 && len(items) == 0 && len(batch.pending) == 0 {
		return errors.New("await: empty await queue")
	}

	for i, it := range confirmations {
		if it.plan == nil {
			return fmt.Errorf("await confirmation item %d missing plan", i)
		}
	}
	for i, it := range items {
		if err := r.publishAwaitToolUses(ctx, input, base, turnID, it, i); err != nil {
			return err
		}
	}

	batch.confirmations += len(confirmations)
	batch.awaitItems += len(items)
	batch.awaited = true
	suspension, err := l.suspendRun(*batch, confirmations, items)
	if err != nil {
		return err
	}
	batch.suspension = suspension
	return nil
}

// publishAwaitToolUses records provider-correlated tool calls when an await is
// first created. The visible prompt is published later by the suspension
// boundary under the workflow run that owns the pending response.
func (r *Runtime) publishAwaitToolUses(ctx context.Context, input *RunInput, base *planner.PlanInput, turnID string, it planner.AwaitItem, idx int) error {
	if it.Kind == "" {
		return fmt.Errorf("await item %d missing kind", idx)
	}

	switch it.Kind {
	case planner.AwaitItemKindClarification:
		if it.Clarification == nil {
			return fmt.Errorf("await clarification item %d missing payload", idx)
		}
		return nil
	case planner.AwaitItemKindToolClarification:
		c := it.ToolClarification
		if c == nil {
			return fmt.Errorf("await tool clarification item %d missing payload", idx)
		}
		if c.ToolCallID == "" {
			return errors.New("await_tool_clarification: missing tool_call_id")
		}
		return r.publishHook(ctx, newToolCallScheduledEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			planner.ToolRequest{
				Name:       c.ToolName,
				ToolCallID: c.ToolCallID,
				Payload:    c.Payload,
			},
			"",
			base.RunContext.ParentToolCallID,
			0,
		), turnID)
	case planner.AwaitItemKindQuestions:
		q := it.Questions
		if q == nil {
			return fmt.Errorf("await questions item %d missing payload", idx)
		}
		if q.ToolCallID == "" {
			return errors.New("await_questions: missing tool_call_id")
		}
		return r.publishHook(ctx, newToolCallScheduledEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			planner.ToolRequest{
				Name:       q.ToolName,
				ToolCallID: q.ToolCallID,
				Payload:    q.Payload,
			},
			"",
			base.RunContext.ParentToolCallID,
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
		awaitCalls := make([]planner.ToolRequest, 0, len(e.Items))
		for _, item := range e.Items {
			if item.ToolCallID == "" {
				return fmt.Errorf("await_external_tools: missing tool_call_id for external tool %q", item.Name)
			}
			awaitCalls = append(awaitCalls, planner.ToolRequest{
				Name:       item.Name,
				ToolCallID: item.ToolCallID,
				Payload:    item.Payload,
			})
		}
		for _, call := range awaitCalls {
			if err := r.publishHook(ctx, newToolCallScheduledEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				call,
				"",
				base.RunContext.ParentToolCallID,
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

// publishAwaitPrompt publishes one customer-visible request without repeating
// the provider-correlated tool call that may have created it in an earlier run.
func (r *Runtime) publishAwaitPrompt(ctx context.Context, input *RunInput, turnID string, item planner.AwaitItem, idx int) error {
	switch item.Kind {
	case planner.AwaitItemKindClarification:
		clarification := item.Clarification
		if clarification == nil {
			return fmt.Errorf("await clarification item %d missing payload", idx)
		}
		return r.publishHook(ctx, hooks.NewAwaitClarificationEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			clarification.ID,
			clarification.Question,
			clarification.MissingFields,
			clarification.RestrictToTool,
			clarification.ExampleJSON,
		), turnID)
	case planner.AwaitItemKindToolClarification:
		clarification := item.ToolClarification
		if clarification == nil {
			return fmt.Errorf("await tool clarification item %d missing payload", idx)
		}
		return r.publishHook(ctx, hooks.NewAwaitClarificationEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			clarification.ID,
			clarification.Question,
			nil,
			clarification.ToolName,
			nil,
		), turnID)
	case planner.AwaitItemKindQuestions:
		questions := item.Questions
		if questions == nil {
			return fmt.Errorf("await questions item %d missing payload", idx)
		}
		visible := make([]hooks.AwaitQuestion, 0, len(questions.Questions))
		for _, question := range questions.Questions {
			options := make([]hooks.AwaitQuestionOption, 0, len(question.Options))
			for _, option := range question.Options {
				options = append(options, hooks.AwaitQuestionOption{ID: option.ID, Label: option.Label})
			}
			visible = append(visible, hooks.AwaitQuestion{
				ID:            question.ID,
				Prompt:        question.Prompt,
				AllowMultiple: question.AllowMultiple,
				Options:       options,
			})
		}
		return r.publishHook(ctx, hooks.NewAwaitQuestionsEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			questions.ID,
			questions.ToolName,
			questions.ToolCallID,
			questions.Payload,
			questions.Title,
			visible,
		), turnID)
	case planner.AwaitItemKindExternalTools:
		external := item.ExternalTools
		if external == nil {
			return fmt.Errorf("await external_tools item %d missing payload", idx)
		}
		visible := make([]hooks.AwaitToolItem, 0, len(external.Items))
		for _, call := range external.Items {
			visible = append(visible, hooks.AwaitToolItem{
				ToolName:   call.Name,
				ToolCallID: call.ToolCallID,
				Payload:    call.Payload,
			})
		}
		return r.publishHook(ctx, hooks.NewAwaitExternalToolsEvent(
			input.RunID,
			input.AgentID,
			input.SessionID,
			external.ID,
			visible,
		), turnID)
	default:
		return fmt.Errorf("unknown await item kind %q", item.Kind)
	}
}

func (r *Runtime) consumeClarificationResponse(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	parentTracker *childTracker,
	turnID string,
	it planner.AwaitItem,
	callRunID string,
	ans *api.ClarificationAnswer,
) ([]stepToolRecord, error) {
	switch it.Kind {
	case planner.AwaitItemKindClarification:
		c := it.Clarification
		if c == nil {
			return nil, errors.New("await clarification missing payload")
		}
		if ans == nil {
			return nil, errors.New("await clarification: nil answer")
		}
		if ans.ID != c.ID {
			return nil, fmt.Errorf("clarification response id %q does not match pending id %q", ans.ID, c.ID)
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
	case planner.AwaitItemKindToolClarification:
		c := it.ToolClarification
		if c == nil {
			return nil, errors.New("await tool clarification missing payload")
		}
		if ans == nil {
			return nil, errors.New("await tool clarification: nil answer")
		}
		if ans.ID != c.ID {
			return nil, fmt.Errorf("clarification response id %q does not match pending id %q", ans.ID, c.ID)
		}
		resultJSON, err := json.Marshal(struct {
			Answer string `json:"answer"`
		}{Answer: ans.Answer})
		if err != nil {
			return nil, fmt.Errorf("encode tool clarification answer: %w", err)
		}
		call := planner.ToolRequest{
			Name:       c.ToolName,
			ToolCallID: c.ToolCallID,
			Payload:    c.Payload,
		}
		call = r.prepareAllowedCallsMetadata(input.AgentID, base, []planner.ToolRequest{call}, parentTracker)[0]
		call.RunID = callRunID
		return r.consumeProvidedToolResultRecords(
			ctx,
			input,
			base,
			turnID,
			&api.ToolResultsSet{Results: []*api.ProvidedToolResult{{
				Name:       c.ToolName,
				ToolCallID: c.ToolCallID,
				Success: &api.ProvidedToolSuccess{
					Result: rawjson.Message(resultJSON),
				},
			}}},
			[]planner.ToolRequest{call},
			map[string]struct{}{c.ToolCallID: {}},
		)
	case planner.AwaitItemKindQuestions, planner.AwaitItemKindExternalTools:
		return nil, fmt.Errorf("await item %q does not accept clarification", it.Kind)
	default:
		return nil, fmt.Errorf("unknown await item kind %q", it.Kind)
	}
}

func (r *Runtime) consumeToolResultsResponse(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	parentTracker *childTracker,
	turnID string,
	it planner.AwaitItem,
	callRunID string,
	rs *api.ToolResultsSet,
) ([]stepToolRecord, error) {
	if rs == nil {
		return nil, errors.New("await: nil tool results set")
	}
	switch it.Kind {
	case planner.AwaitItemKindQuestions:
		q := it.Questions
		if q == nil {
			return nil, errors.New("await questions missing payload")
		}
		if rs.ID != q.ID {
			return nil, fmt.Errorf("tool-results response id %q does not match pending id %q", rs.ID, q.ID)
		}
		expected := map[string]struct{}{q.ToolCallID: {}}
		allowed := []planner.ToolRequest{
			{
				Name:       q.ToolName,
				ToolCallID: q.ToolCallID,
				Payload:    q.Payload,
			},
		}
		allowed = r.prepareAllowedCallsMetadata(input.AgentID, base, allowed, parentTracker)
		allowed[0].RunID = callRunID
		return r.consumeProvidedToolResultRecords(ctx, input, base, turnID, rs, allowed, expected)
	case planner.AwaitItemKindExternalTools:
		e := it.ExternalTools
		if e == nil {
			return nil, errors.New("await external_tools missing payload")
		}
		if rs.ID != e.ID {
			return nil, fmt.Errorf("tool-results response id %q does not match pending id %q", rs.ID, e.ID)
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
		allowed = r.prepareAllowedCallsMetadata(input.AgentID, base, allowed, parentTracker)
		for i := range allowed {
			allowed[i].RunID = callRunID
		}
		return r.consumeProvidedToolResultRecords(ctx, input, base, turnID, rs, allowed, expected)
	case planner.AwaitItemKindClarification, planner.AwaitItemKindToolClarification:
		return nil, fmt.Errorf("await item %q does not accept tool results", it.Kind)
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

	for i := range records {
		record := &records[i]
		record.scheduleExact = true
		tr := record.result
		if tr == nil {
			continue
		}
		call := record.call
		preview, err := formatToolResultPreviewForCall(ctx, r, &call, tr)
		if err != nil {
			return records, err
		}
		event := hooks.NewToolResultReceivedEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			call.RunID,
			tr.Name,
			tr.ToolCallID,
			parentToolCallID(call, &base.RunContext),
			record.resultJSON,
			len(record.resultJSON),
			false,
			"",
			tr.ServerData,
			preview,
			tr.Bounds,
			0,
			nil,
			tr.Failure,
		)
		prepared, err := prepareHookRecordInput(ctx, event, turnID)
		if err != nil {
			return records, err
		}
		record.resultRecord = prepared
		if err := r.publishPreparedHook(ctx, prepared, engine.ActivityOptions{}); err != nil {
			return records, err
		}
		record.resultPublished = true
	}
	return records, nil
}
