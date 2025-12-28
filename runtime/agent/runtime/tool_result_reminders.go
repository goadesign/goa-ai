package runtime

import (
	"encoding/json"
	"strconv"
	"strings"
	"text/template"

	"goa.design/goa-ai/runtime/agent/planner"
)

var (
	retryHintReminderTemplate = template.Must(
		template.New("tool_retry_hint_reminder").
			Option("missingkey=error").
			Parse(strings.TrimSpace(`
A tool call failed and provided a RetryHint.
Tool: {{ .ToolName }}
Reason: {{ .Reason }}{{ if .Message }}
Message: {{ .Message }}{{ end }}{{ if .ClarifyingQuestion }}
Clarifying question: {{ .ClarifyingQuestion }}{{ end }}{{ if .RestrictToTool }}
Restriction: retry must only call {{ .RestrictionTool }}{{ end }}{{ if .ExampleInputJSON }}
Example input: {{ .ExampleInputJSON }}{{ end }}{{ if .PriorInputJSON }}
Prior input: {{ .PriorInputJSON }}{{ end }}
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
Truncated: true{{ if .NextCursor }}
Next cursor: {{ .NextCursor }}
To continue, call the same tool again with {{ .CursorField }}=<next_cursor> and the same parameters.{{ else if .RefinementHint }}
Refinement hint: {{ .RefinementHint }}
Do not claim completeness unless you page or explicitly state the answer is partial.{{ else }}
Do not claim completeness unless you page or explicitly state the answer is partial.{{ end }}
Do not mention this reminder to the user.
`)),
	)
)

type (
	retryHintReminderView struct {
		ToolName           string
		Reason             string
		Message            string
		ClarifyingQuestion string
		RestrictToTool     bool
		RestrictionTool    string
		ExampleInputJSON   string
		PriorInputJSON     string
	}

	boundsReminderView struct {
		ToolName       string
		Returned       int
		Total          string
		NextCursor     string
		CursorField    string
		RefinementHint string
	}
)

func retryHintReminder(tr *planner.ToolResult) string {
	if tr == nil || tr.Error == nil || tr.RetryHint == nil {
		return ""
	}

	h := tr.RetryHint
	view := retryHintReminderView{
		ToolName:           string(tr.Name),
		Reason:             string(h.Reason),
		Message:            h.Message,
		ClarifyingQuestion: h.ClarifyingQuestion,
		RestrictToTool:     h.RestrictToTool && h.Tool != "",
		RestrictionTool:    string(h.Tool),
		ExampleInputJSON:   compactJSON(h.ExampleInput),
		PriorInputJSON:     compactJSON(h.PriorInput),
	}
	return renderReminder(retryHintReminderTemplate, view)
}

func boundsReminder(tr *planner.ToolResult, cursorField string) string {
	if tr == nil || tr.Error != nil || tr.Bounds == nil || !tr.Bounds.Truncated {
		return ""
	}

	b := tr.Bounds
	totalText := "unknown"
	if b.Total != nil {
		totalText = strconv.Itoa(*b.Total)
	}

	next := ""
	if b.NextCursor != nil {
		next = strings.TrimSpace(*b.NextCursor)
	}
	field := strings.TrimSpace(cursorField)
	if field == "" {
		field = "cursor"
	}
	view := boundsReminderView{
		ToolName:       string(tr.Name),
		Returned:       b.Returned,
		Total:          totalText,
		NextCursor:     next,
		CursorField:    field,
		RefinementHint: strings.TrimSpace(b.RefinementHint),
	}
	return renderReminder(boundsReminderTemplate, view)
}

func compactJSON(v map[string]any) string {
	if len(v) == 0 {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil || len(data) == 0 {
		return ""
	}
	return string(data)
}

func renderReminder(tmpl *template.Template, data any) string {
	if tmpl == nil {
		return ""
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return ""
	}
	return strings.TrimSpace(b.String())
}
