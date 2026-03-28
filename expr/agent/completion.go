// Package agent defines service-owned completion contracts alongside agent and
// toolset expressions. Completions model typed assistant outputs: a service
// declares the result schema once in the design, and code generation emits the
// schema, codec, and typed runtime helpers used by direct completion calls.
package agent

import (
	"fmt"

	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

const (
	maxStructuredOutputNameLen = 64
	structuredOutputNameRule   = `completion name %q must be 1-64 ASCII characters containing only letters, digits, "_" or "-", and must start with a letter or digit`
)

type (
	// CompletionExpr describes one service-owned typed assistant-output contract.
	//
	// A completion owns exactly one Return schema. Generated runtime helpers use
	// that schema to request structured output from the provider and decode the
	// assistant response through the generated codec.
	CompletionExpr struct {
		eval.DSLFunc

		// Name is the unique identifier for this completion within the service.
		Name string
		// Description provides a human-readable explanation of the completion.
		Description string
		// Service is the Goa service that owns this completion.
		Service *goaexpr.ServiceExpr
		// Return defines the typed assistant output contract.
		Return *goaexpr.AttributeExpr
	}
)

// EvalName implements eval.Expression.
func (c *CompletionExpr) EvalName() string {
	if c == nil || c.Service == nil {
		return fmt.Sprintf("completion %q", c.Name)
	}
	return fmt.Sprintf("completion %q (service %q)", c.Name, c.Service.Name)
}

// Prepare ensures the completion always has a concrete return attribute.
func (c *CompletionExpr) Prepare() {
	if c.Return == nil {
		c.Return = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
}

// Validate enforces that completions declare a non-empty supported return shape.
func (c *CompletionExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	validateStructuredOutputName(c, verr)
	if c.Return == nil || c.Return.Type == nil || c.Return.Type == goaexpr.Empty {
		verr.Add(c, "Completion must declare a non-empty Return schema")
	} else {
		validateContractShape(c, "Return", c.Return, verr)
	}
	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}

// Finalize materializes any Extend-composed completion return shape.
func (c *CompletionExpr) Finalize() {
	finalizeToolShape(c.Return)
}

// validateStructuredOutputName enforces the provider-safe identifier syntax used
// for structured-output schema names across model adapters.
func validateStructuredOutputName(c *CompletionExpr, verr *eval.ValidationErrors) {
	if isStructuredOutputName(c.Name) {
		return
	}
	verr.Add(c, structuredOutputNameRule, c.Name)
}

func isStructuredOutputName(name string) bool {
	if len(name) == 0 || len(name) > maxStructuredOutputNameLen {
		return false
	}
	if !isStructuredOutputNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isStructuredOutputNamePart(name[i]) {
			return false
		}
	}
	return true
}

func isStructuredOutputNameStart(ch byte) bool {
	return isASCIILetter(ch) || isASCIIDigit(ch)
}

func isStructuredOutputNamePart(ch byte) bool {
	return isStructuredOutputNameStart(ch) || ch == '_' || ch == '-'
}

func isASCIILetter(ch byte) bool {
	return 'a' <= ch && ch <= 'z' || 'A' <= ch && ch <= 'Z'
}

func isASCIIDigit(ch byte) bool {
	return '0' <= ch && ch <= '9'
}
