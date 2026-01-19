package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"text/template"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/planner"
)

// confirmToolsIfNeeded emits an await_confirmation protocol boundary for any tool call
// that requires explicit user approval and returns the subset of calls to execute.
//
// When hardDeadline is set, the await is bounded to the remaining time and returns
// context.DeadlineExceeded when the deadline is reached.
func (r *Runtime) confirmToolsIfNeeded(wfCtx engine.WorkflowContext, input *RunInput, base *planner.PlanInput, allowed []planner.ToolRequest, turnID string, ctrl *interrupt.Controller, hardDeadline time.Time) (toExecute []planner.ToolRequest, denied []*planner.ToolResult, err error) {
	if len(allowed) == 0 {
		return allowed, nil, nil
	}

	ctx := wfCtx.Context()

	toExecute = make([]planner.ToolRequest, 0, len(allowed))
	denied = make([]*planner.ToolResult, 0, 1)

	for _, call := range allowed {
		plan, needs, err := r.confirmationPlan(ctx, &call)
		if err != nil {
			return nil, nil, err
		}
		if !needs {
			toExecute = append(toExecute, call)
			continue
		}
		if ctrl == nil {
			return nil, nil, fmt.Errorf("confirmation required for tool %q but interrupts are not available", call.Name)
		}

		awaitID := generateDeterministicAwaitID(
			base.RunContext.RunID,
			base.RunContext.TurnID,
			call.Name,
			call.ToolCallID,
		)

		title := strings.TrimSpace(plan.Title)
		if title == "" {
			title = "Confirm command"
		}
		// Publish await + pause. Confirmation is a runtime protocol boundary.
		if err := r.publishHook(ctx, hooks.NewAwaitConfirmationEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			awaitID,
			title,
			plan.Prompt,
			call.Name,
			call.ToolCallID,
			call.Payload,
		), turnID); err != nil {
			return nil, nil, err
		}
		if err := r.publishHook(ctx, hooks.NewRunPausedEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			"await_confirmation",
			"runtime",
			nil,
			nil,
		), turnID); err != nil {
			return nil, nil, err
		}

		timeout, ok := timeoutUntil(hardDeadline, wfCtx.Now())
		if !ok {
			if err := r.publishHook(ctx, hooks.NewRunResumedEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				"confirmation_timeout",
				"runtime",
				map[string]string{
					"resumed_by":   "confirmation_timeout",
					"tool_name":    call.Name.String(),
					"tool_call_id": call.ToolCallID,
				},
				0,
			), turnID); err != nil {
				return nil, nil, err
			}
			return nil, nil, context.DeadlineExceeded
		}
		dec, err := ctrl.WaitProvideConfirmation(ctx, timeout)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				if err := r.publishHook(ctx, hooks.NewRunResumedEvent(
					base.RunContext.RunID,
					input.AgentID,
					base.RunContext.SessionID,
					"confirmation_timeout",
					"runtime",
					map[string]string{
						"resumed_by":   "confirmation_timeout",
						"tool_name":    call.Name.String(),
						"tool_call_id": call.ToolCallID,
					},
					0,
				), turnID); err != nil {
					return nil, nil, err
				}
				return nil, nil, err
			}
			if err2 := r.publishHook(ctx, hooks.NewRunResumedEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				"confirmation_error",
				"runtime",
				map[string]string{
					"resumed_by":   "confirmation_error",
					"tool_name":    call.Name.String(),
					"tool_call_id": call.ToolCallID,
				},
				0,
			), turnID); err2 != nil {
				return nil, nil, err2
			}
			return nil, nil, err
		}
		if dec.ID != "" && dec.ID != awaitID {
			return nil, nil, fmt.Errorf("unexpected confirmation id %q (expected %q)", dec.ID, awaitID)
		}

		if strings.TrimSpace(dec.RequestedBy) == "" {
			return nil, nil, fmt.Errorf("confirmation decision missing requested_by for %q (%s)", call.Name, call.ToolCallID)
		}

		approved := dec.Approved

		labels := map[string]string{
			"resumed_by":   "confirmation",
			"tool_name":    call.Name.String(),
			"tool_call_id": call.ToolCallID,
		}
		maps.Copy(labels, dec.Labels)

		if err := r.publishHook(ctx, hooks.NewToolAuthorizationEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			call.Name,
			call.ToolCallID,
			approved,
			plan.Prompt,
			dec.RequestedBy,
		), turnID); err != nil {
			return nil, nil, err
		}

		if err := r.publishHook(ctx, hooks.NewRunResumedEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			"confirmation",
			dec.RequestedBy,
			labels,
			0,
		), turnID); err != nil {
			return nil, nil, err
		}

		if approved {
			toExecute = append(toExecute, call)
			continue
		}

		deniedResult := plan.DeniedResult

		// Publish a result event for the denied tool call so subscribers/UI
		// see a resolved tool call without counting it as a failure.
		if err := r.publishHook(
			ctx,
			hooks.NewToolCallScheduledEvent(
				call.RunID,
				call.AgentID,
				call.SessionID,
				call.Name,
				call.ToolCallID,
				call.Payload,
				"",
				call.ParentToolCallID,
				0,
			),
			turnID,
		); err != nil {
			return nil, nil, err
		}
		resultJSON, err := r.marshalToolValue(ctx, call.Name, deniedResult, false)
		if err != nil {
			return nil, nil, fmt.Errorf("encode %s denied tool result for streaming: %w", call.Name, err)
		}
		if err := r.publishHook(
			ctx,
			hooks.NewToolResultReceivedEvent(
				call.RunID,
				call.AgentID,
				call.SessionID,
				call.Name,
				call.ToolCallID,
				call.ParentToolCallID,
				deniedResult,
				resultJSON,
				formatResultPreview(call.Name, deniedResult),
				nil,
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

		denied = append(denied, &planner.ToolResult{
			Name:       call.Name,
			ToolCallID: call.ToolCallID,
			Result:     deniedResult,
			Error:      nil,
		})
	}

	return toExecute, denied, nil
}

type confirmationPlan struct {
	Title        string
	Prompt       string
	DeniedResult any
}

func (r *Runtime) confirmationPlan(ctx context.Context, call *planner.ToolRequest) (*confirmationPlan, bool, error) {
	// Runtime override takes precedence and can require confirmation for tools that
	// do not declare design-time Confirmation.
	if r.toolConfirmation != nil && len(r.toolConfirmation.Confirm) > 0 {
		if h, ok := r.toolConfirmation.Confirm[call.Name]; ok {
			prompt, err := h.Prompt(ctx, call)
			if err != nil {
				return nil, false, err
			}
			deniedResult, err := h.DeniedResult(ctx, call)
			if err != nil {
				return nil, false, err
			}
			return &confirmationPlan{
				Title:        "",
				Prompt:       prompt,
				DeniedResult: deniedResult,
			}, true, nil
		}
	}

	spec, ok := r.toolSpec(call.Name)
	if !ok || spec.Confirmation == nil {
		return nil, false, nil
	}
	c := spec.Confirmation
	payloadVal, err := r.unmarshalToolValue(ctx, call.Name, call.Payload, true)
	if err != nil {
		return nil, false, fmt.Errorf("decode payload for confirmation %q: %w", call.Name, err)
	}

	prompt, err := renderConfirmationTemplate("prompt", c.PromptTemplate, payloadVal)
	if err != nil {
		return nil, false, fmt.Errorf("render confirmation prompt for %q: %w", call.Name, err)
	}
	deniedJSON, err := renderConfirmationTemplate("denied_result", c.DeniedResultTemplate, payloadVal)
	if err != nil {
		return nil, false, fmt.Errorf("render denied result for %q: %w", call.Name, err)
	}
	deniedRaw := json.RawMessage(deniedJSON)
	if !json.Valid(deniedRaw) {
		return nil, false, fmt.Errorf("denied result template for %q did not render valid JSON", call.Name)
	}
	deniedResult, err := r.unmarshalToolValue(ctx, call.Name, deniedRaw, false)
	if err != nil {
		return nil, false, fmt.Errorf("decode denied result for %q: %w", call.Name, err)
	}

	return &confirmationPlan{
		Title:        c.Title,
		Prompt:       prompt,
		DeniedResult: deniedResult,
	}, true, nil
}

func renderConfirmationTemplate(name string, src string, data any) (string, error) {
	t, err := template.New(name).
		Option("missingkey=error").
		Funcs(template.FuncMap{
			"json": func(v any) (string, error) {
				b, err := json.Marshal(v)
				if err != nil {
					return "", err
				}
				return string(b), nil
			},
			"quote": func(s string) string {
				return fmt.Sprintf("%q", s)
			},
		}).
		Parse(src)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func mergeToolResultsByCallID(allowed []planner.ToolRequest, executed, denied []*planner.ToolResult) ([]*planner.ToolResult, error) {
	out := make([]*planner.ToolResult, 0, len(executed)+len(denied))
	byID := make(map[string]*planner.ToolResult, len(executed)+len(denied))
	for _, tr := range executed {
		if tr == nil || tr.ToolCallID == "" {
			continue
		}
		byID[tr.ToolCallID] = tr
	}
	for _, tr := range denied {
		if tr == nil || tr.ToolCallID == "" {
			continue
		}
		byID[tr.ToolCallID] = tr
	}
	for _, call := range allowed {
		tr := byID[call.ToolCallID]
		if tr == nil {
			return nil, fmt.Errorf("missing tool result for %q (%s)", call.Name, call.ToolCallID)
		}
		out = append(out, tr)
	}
	return out, nil
}
