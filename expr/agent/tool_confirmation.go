package agent

import (
	"encoding/json"
	"fmt"
	"text/template"

	"goa.design/goa/v3/eval"
)

type (
	// ToolConfirmationExpr captures design-time confirmation requirements for a tool.
	// When present, the runtime must request an external confirmation before executing
	// the tool, unless runtime overrides disable or supersede the confirmation.
	ToolConfirmationExpr struct {
		// Title is an optional title presented in the confirmation UI.
		Title string

		// PromptTemplate is a Go text/template rendered with the tool payload to
		// produce the operator-facing confirmation prompt.
		PromptTemplate string

		// DeniedResultTemplate is a Go text/template rendered with the tool payload to
		// produce canonical JSON for the denied tool result. The rendered JSON must
		// decode successfully using the tool result codec.
		DeniedResultTemplate string
	}
)

// EvalName implements eval.Expression.
func (c *ToolConfirmationExpr) EvalName() string {
	return "tool confirmation"
}

// SetTitle implements expr.TitleHolder, allowing the Goa Title() DSL function
// to set the confirmation UI title when used inside Confirmation(...).
func (c *ToolConfirmationExpr) SetTitle(title string) {
	c.Title = title
}

func validateToolConfirmation(tool *ToolExpr, verr *eval.ValidationErrors) {
	c := tool.Confirmation
	if c == nil {
		return
	}
	if c.PromptTemplate == "" {
		verr.Add(tool, "Confirmation: PromptTemplate is required")
	} else if err := validateConfirmationTemplate("PromptTemplate", c.PromptTemplate); err != nil {
		verr.Add(tool, "Confirmation: invalid PromptTemplate: %v", err)
	}
	if c.DeniedResultTemplate == "" {
		verr.Add(tool, "Confirmation: DeniedResultTemplate is required")
	} else if err := validateConfirmationTemplate("DeniedResultTemplate", c.DeniedResultTemplate); err != nil {
		verr.Add(tool, "Confirmation: invalid DeniedResultTemplate: %v", err)
	}
}

func validateConfirmationTemplate(name string, src string) error {
	_, err := template.New(name).
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
	return err
}
