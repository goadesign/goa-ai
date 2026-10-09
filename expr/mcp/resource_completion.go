// Package mcp validates suggestion methods for URI-template variables. It uses
// the template's declared names rather than attempting to recover original
// values from the resource URI received by the reader.
package mcp

import (
	"errors"
	"slices"

	"github.com/yosida95/uritemplate/v3"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type (
	// ResourceCompletionExpr selects a suggestion method for one template variable.
	ResourceCompletionExpr struct {
		eval.Expression
		// URI is the exact declared URI template used in the completion reference.
		URI string
		// Argument names the variable being completed.
		Argument string
		// Method owns the ordered suggestions for the supplied partial value.
		Method *expr.MethodExpr
	}
)

// EvalName identifies the template and variable in design errors.
func (c *ResourceCompletionExpr) EvalName() string {
	return "MCP resource completion for " + c.URI + "." + c.Argument
}

// Validate checks the shared typed suggestion contract for this method.
func (c *ResourceCompletionExpr) Validate() error {
	if c.URI == "" || c.Argument == "" {
		verr := new(eval.ValidationErrors)
		verr.Add(c, "resource completion requires a URI template and variable name")
		return verr
	}
	return validateCompletionMethod(c, c.Method)
}

// validateResourceCompletions rejects unknown references and duplicate providers
// after all template declarations have been evaluated.
func (m *MCPExpr) validateResourceCompletions(verr *eval.ValidationErrors) {
	seen := make(map[[2]string]bool, len(m.ResourceCompletions))
	for _, completion := range m.ResourceCompletions {
		key := [2]string{completion.URI, completion.Argument}
		if seen[key] {
			verr.Add(completion, "resource completion binding is used more than once")
		}
		seen[key] = true
		found := false
		for _, declaration := range m.ResourceTemplates {
			if declaration.URI != completion.URI {
				continue
			}
			template, err := uritemplate.New(declaration.URI)
			if err == nil && slices.Contains(template.Varnames(), completion.Argument) {
				found = true
			}
		}
		if !found {
			verr.Add(completion, "resource completion must select a declared template variable")
		}
		if err := completion.Validate(); err != nil {
			var validation *eval.ValidationErrors
			if errors.As(err, &validation) {
				verr.Merge(validation)
			}
		}
	}
}
