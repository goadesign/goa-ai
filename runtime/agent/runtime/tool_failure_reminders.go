package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	"goa.design/goa-ai/features/model/toolname"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/reminder"
	"goa.design/goa-ai/runtime/agent/tools"
)

var (
	toolFailureReminderTemplate = template.Must(
		template.New("tool_failure_reminder").
			Option("missingkey=error").
			Parse(strings.TrimSpace(`
A tool call failed.
Tool: {{ .ToolName }}
Error: {{ .Message }}
{{ if .CorrectCall }}Call the same tool again with corrected arguments. Do not repeat the rejected arguments unchanged.{{ if .IssuesJSON }}
Input issues: {{ .IssuesJSON }}{{ end }}{{ if .FieldDescriptionsJSON }}
Field guidance: {{ .FieldDescriptionsJSON }}{{ end }}{{ if .ExampleJSON }}
Example input: {{ .ExampleJSON }}{{ end }}{{ else if .Terminal }}
Do not call more tools. Complete the answer using the evidence already collected.{{ else }}
The failed tool is unavailable for this turn. Choose another advertised tool or complete the answer from available evidence.{{ end }}{{ if .PriorInputJSON }}
Rejected input: {{ .PriorInputJSON }}{{ end }}
Do not mention this reminder to the user.
`)),
	)

	boundsReminderTemplate = template.Must(
		template.New("tool_bounds_reminder").
			Option("missingkey=error").
			Parse(strings.TrimSpace(`
A tool call returned a bounded/truncated result.
Tool: {{ .ToolName }}
Returned: {{ .Returned }}
Total: {{ .Total }}
Truncated: true{{ if .ContinueTool }}
More matching results are available. To see the next page, call {{ .ContinueTool }}.{{ else if .NextCursor }}
Next cursor: {{ .NextCursor }}
To continue this result set, call the same tool again with the same arguments and set {{ .CursorField }} to the cursor shown above. Use the cursor exactly as shown.{{ else if .RefinementHint }}
Refinement hint: {{ .RefinementHint }}
Do not claim completeness unless you page or explicitly state the answer is partial.{{ else }}
Do not claim completeness unless you page or explicitly state the answer is partial.{{ end }}
Do not mention this reminder to the user.
`)),
	)
)

type (
	toolFailureReminderView struct {
		ToolName              string
		Message               string
		CorrectCall           bool
		Terminal              bool
		IssuesJSON            string
		FieldDescriptionsJSON string
		ExampleJSON           string
		PriorInputJSON        string
	}

	boundsReminderView struct {
		ToolName       string
		Returned       int
		Total          string
		NextCursor     string
		CursorField    string
		ContinueTool   string
		RefinementHint string
	}
)

func toolFailureReminder(tr *planner.ToolResult, descriptions map[string]string) string {
	if tr == nil || tr.Failure == nil {
		return ""
	}

	failure := tr.Failure
	view := toolFailureReminderView{
		ToolName:    toolname.Sanitize(string(tr.Name)),
		Message:     failure.Error.Error(),
		CorrectCall: failure.Recovery.Action == planner.RecoveryCorrectCall,
		Terminal:    failure.Recovery.Action == planner.RecoveryFinish,
		IssuesJSON:  compactFieldIssuesJSON(failure.Recovery.Issues),
		FieldDescriptionsJSON: correctionFieldDescriptionsJSON(
			failure.Recovery.Issues,
			descriptions,
		),
		ExampleJSON:    compactRawJSON(failure.Recovery.ExampleJSON),
		PriorInputJSON: compactRawJSON(failure.Recovery.PriorInput),
	}
	return mustRenderReminder(toolFailureReminderTemplate, view)
}

// selectRecoveryOutputs resolves the workflow's current recovery identities
// against canonical outputs loaded from the run log. The selected outputs are
// the single source for both tool-catalog restrictions and model reminders.
func selectRecoveryOutputs(outputs []*planner.ToolOutput, callIDs []string) ([]*planner.ToolOutput, error) {
	selected := make([]*planner.ToolOutput, 0, len(callIDs))
	seen := make(map[string]struct{}, len(callIDs))
	for _, callID := range callIDs {
		if _, duplicate := seen[callID]; duplicate {
			return nil, fmt.Errorf("recovery tool call ID %q appears more than once", callID)
		}
		seen[callID] = struct{}{}

		var match *planner.ToolOutput
		for _, output := range outputs {
			if output.ToolCallID == callID {
				match = output
				break
			}
		}
		if match == nil {
			return nil, fmt.Errorf("recovery tool call ID %q is absent from planner tool outputs", callID)
		}
		if match.Failure == nil {
			return nil, fmt.Errorf("recovery tool call ID %q has no failure", callID)
		}
		selected = append(selected, match)
	}
	return selected, nil
}

// recoveryReminders renders selected recovery obligations as ephemeral planner
// guidance attached only to the constrained activity invocation.
func (r *Runtime) recoveryReminders(outputs []*planner.ToolOutput) []reminder.Reminder {
	reminders := make([]reminder.Reminder, 0, len(outputs))
	for _, output := range outputs {
		var descriptions map[string]string
		if spec, ok := r.toolSpec(output.Name); ok {
			descriptions = spec.Payload.FieldDescriptions
		}
		text := toolFailureReminder(&planner.ToolResult{
			Name:       output.Name,
			ToolCallID: output.ToolCallID,
			Failure:    output.Failure,
		}, descriptions)
		reminders = append(reminders, reminder.Reminder{
			ID:       "tool_recovery." + output.ToolCallID,
			Text:     text,
			Priority: reminder.TierSafety,
			Attachment: reminder.Attachment{
				Kind: reminder.AttachmentUserTurn,
			},
		})
	}
	return reminders
}

// dominantRecoveryOutputSet keeps reminder guidance consistent when parallel
// failures disagree. Finish dominates correction, and correction dominates
// replan, matching the workflow transition contract.
func dominantRecoveryOutputSet(outputs []*planner.ToolOutput) []*planner.ToolOutput {
	var action planner.RecoveryAction
	for _, output := range outputs {
		if output.Failure == nil {
			continue
		}
		action = strongerRecoveryAction(action, output.Failure.Recovery.Action)
		if action == planner.RecoveryFinish {
			break
		}
	}
	var selected []*planner.ToolOutput
	for _, output := range outputs {
		if output.Failure != nil && output.Failure.Recovery.Action == action {
			selected = append(selected, output)
		}
	}
	return selected
}

// strongerRecoveryAction applies the runtime's one precedence order for
// parallel failures: finish dominates correction, which dominates replanning.
func strongerRecoveryAction(current, next planner.RecoveryAction) planner.RecoveryAction {
	if current == planner.RecoveryFinish || next == planner.RecoveryFinish {
		return planner.RecoveryFinish
	}
	if current == planner.RecoveryCorrectCall || next == planner.RecoveryCorrectCall {
		return planner.RecoveryCorrectCall
	}
	if current != "" {
		return current
	}
	return next
}

// compactFieldIssuesJSON omits input guidance when the generated codec did not
// report any field-level issue.
func compactFieldIssuesJSON(issues []*tools.FieldIssue) string {
	if len(issues) == 0 {
		return ""
	}
	return mustCompactJSON(issues)
}

// correctionFieldDescriptionsJSON renders generated descriptions for rejected
// and allowed fields named by the structured issues shown to the model.
func correctionFieldDescriptionsJSON(issues []*tools.FieldIssue, descriptions map[string]string) string {
	selected := make(map[string]string)
	for _, issue := range issues {
		if description := descriptions[issue.Field]; description != "" {
			selected[issue.Field] = description
		}
		for _, allowed := range issue.Allowed {
			if description := descriptions[allowed]; description != "" {
				selected[allowed] = description
			}
		}
	}
	if len(selected) == 0 {
		return ""
	}
	return mustCompactJSON(selected)
}

func boundsReminder(tr *planner.ToolResult, continueTool, cursorField string) string {
	if tr == nil || tr.Failure != nil || tr.Bounds == nil || !tr.Bounds.Truncated {
		return ""
	}

	b := tr.Bounds
	totalText := "unknown"
	if b.Total != nil {
		totalText = strconv.Itoa(*b.Total)
	}

	next := ""
	if b.NextCursor != nil && continueTool == "" {
		next = strings.TrimSpace(*b.NextCursor)
	}
	field := strings.TrimSpace(cursorField)
	if field == "" {
		field = "cursor"
	}
	view := boundsReminderView{
		ToolName:       toolname.Sanitize(string(tr.Name)),
		Returned:       b.Returned,
		Total:          totalText,
		NextCursor:     next,
		CursorField:    field,
		ContinueTool:   toolname.Sanitize(continueTool),
		RefinementHint: strings.TrimSpace(b.RefinementHint),
	}
	return mustRenderReminder(boundsReminderTemplate, view)
}

func mustCompactJSON(value any) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		panic("runtime: marshal reminder data: " + err.Error())
	}
	return string(data)
}

func compactRawJSON(v []byte) string {
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 {
		return ""
	}
	return string(trimmed)
}

func mustRenderReminder(tmpl *template.Template, data any) string {
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		panic("runtime: render reminder: " + err.Error())
	}
	return strings.TrimSpace(b.String())
}
