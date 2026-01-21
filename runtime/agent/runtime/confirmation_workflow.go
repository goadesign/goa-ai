package runtime

// confirmation_workflow.go implements the workflow-side confirmation policy for
// tool execution.
//
// Some tools require an explicit operator approval before they may execute
// (await_confirmation). The runtime enforces this by splitting candidate tool
// calls into two sets:
// - calls that may execute immediately, and
// - calls that must pause the workflow at an await boundary before execution.
//
// This file is pure policy + rendering:
// - It decides whether a given tool call requires confirmation (design-time
//   spec vs runtime overrides).
// - It renders the operator-facing prompt and the denied-result payload using
//   templates compiled with missingkey=error so bad templates fail loudly.
//
// It intentionally does NOT execute tools or publish await events; those
// concerns live in the workflow loop/await queue handlers.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"text/template"

	"goa.design/goa-ai/runtime/agent/planner"
)

type confirmationAwait struct {
	awaitID string
	call    planner.ToolRequest
	plan    *confirmationPlan
}

// splitConfirmationCalls partitions allowed tool calls into:
// - calls that may execute immediately, and
// - calls that require an await_confirmation boundary before execution.
func (r *Runtime) splitConfirmationCalls(ctx context.Context, base *planner.PlanInput, allowed []planner.ToolRequest) ([]planner.ToolRequest, []confirmationAwait, error) {
	if len(allowed) == 0 {
		return nil, nil, nil
	}

	toExecute := make([]planner.ToolRequest, 0, len(allowed))
	toConfirm := make([]confirmationAwait, 0, 1)
	for _, call := range allowed {
		plan, needs, err := r.confirmationPlan(ctx, &call)
		if err != nil {
			return nil, nil, err
		}
		if !needs {
			toExecute = append(toExecute, call)
			continue
		}
		awaitID := generateDeterministicAwaitID(base.RunContext.RunID, base.RunContext.TurnID, call.Name, call.ToolCallID)
		toConfirm = append(toConfirm, confirmationAwait{
			awaitID: awaitID,
			call:    call,
			plan:    plan,
		})
	}
	return toExecute, toConfirm, nil
}

type confirmationPlan struct {
	Title        string
	Prompt       string
	DeniedResult any
}

// confirmationPlan returns the rendered confirmation prompt/denied-result for
// the given tool call and whether the call requires confirmation.
//
// Contract:
//   - Runtime overrides take precedence over design-time specs.
//   - When confirmation is not required, the returned plan is nil and needs is false.
//   - Template rendering uses missingkey=error; a missing field is a bug and must
//     fail loudly to surface incorrect tool schemas/templates.
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

// renderConfirmationTemplate renders a confirmation template against a decoded
// tool payload value.
//
// Templates are compiled with missingkey=error to keep the contract strict:
// if a template references a field not present in the payload, that is a bug
// in the spec/template pairing and must fail loudly.
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
