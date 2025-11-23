package agent

import (
	"fmt"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// ToolExpr captures an individual tool declaration within a toolset.
	ToolExpr struct {
		eval.DSLFunc

		// Name is the unique identifier for this tool within its toolset.
		Name string

		// Title is an optional human-friendly display title. When empty, codegen
		// derives a title from Name (e.g., "analyze_sensor_patterns" -> "Analyze Sensor Patterns").
		Title string

		// Description provides a human-readable explanation of what the
		// tool does.
		Description string

		// Tags are labels for categorizing and filtering this tool.
		Tags []string

		// Args defines the input parameter schema for this tool.
		Args *goaexpr.AttributeExpr

		// Return defines the output result schema for this tool.
		Return *goaexpr.AttributeExpr

		// Toolset is the toolset expression that owns this tool.
		Toolset *ToolsetExpr

		// Method is the resolved Goa service method this tool is bound
		// to, if any.
		Method *goaexpr.MethodExpr

		// ExportPassthrough defines deterministic forwarding for this tool
		// when it is part of an exported toolset.
		ExportPassthrough *ToolPassthroughExpr

		// Optional display hint templates declared in the DSL.
		CallHintTemplate   string
		ResultHintTemplate string

		// InjectedFields are fields marked as infrastructure-only.
		InjectedFields []string

		bindServiceName string
		bindMethodName  string
	}

	// ToolPassthroughExpr defines deterministic forwarding for an exported tool.
	ToolPassthroughExpr struct {
		TargetService string
		TargetMethod  string
	}
)

// EvalName implements eval.Expression.
func (t *ToolExpr) EvalName() string {
	// Be resilient in error reporting: EvalName is used in diagnostics and
	// may be called before the owning structures are fully wired.
	ts := ""
	svc := ""
	if t != nil && t.Toolset != nil {
		ts = t.Toolset.Name
		if t.Toolset.Agent != nil && t.Toolset.Agent.Service != nil {
			svc = t.Toolset.Agent.Service.Name
		}
	}
	if svc != "" {
		return fmt.Sprintf("tool %q in toolset %q and service %q", t.Name, ts, svc)
	}
	return fmt.Sprintf("tool %q in toolset %q", t.Name, ts)
}

// RecordBinding records the service and method names specified via the DSL.
func (t *ToolExpr) RecordBinding(serviceName, methodName string) {
	t.bindServiceName = serviceName
	t.bindMethodName = methodName
}

// Prepare ensures Args and Return are always non-nil attributes.
func (t *ToolExpr) Prepare() {
	if t.Args == nil {
		t.Args = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
	if t.Return == nil {
		t.Return = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
}

// Validate checks that any recorded binding can be resolved to an existing
// service and method.
func (t *ToolExpr) Validate() error {
	if t.bindMethodName == "" {
		return t.validateShapes()
	}
	verr := new(eval.ValidationErrors)
	var svc *goaexpr.ServiceExpr
	if t.bindServiceName != "" {
		svc = goaexpr.Root.Service(t.bindServiceName)
	} else {
		svc = t.Toolset.Agent.Service
	}
	if svc == nil {
		verr.Add(t, "BindTo could not resolve target service")
		return verr
	}
	desired := codegen.Goify(t.bindMethodName, true)
	for _, m := range svc.Methods {
		if codegen.Goify(m.Name, true) == desired {
			if err := t.validateShapes(); err != nil {
				verr.AddError(t, err)
				return verr
			}
			return nil
		}
	}
	verr.Add(t, "service method %q not found in service %q", t.bindMethodName, svc.Name)
	return verr
}

func (t *ToolExpr) validateShapes() error {
	verr := new(eval.ValidationErrors)
	check := func(where string, att *goaexpr.AttributeExpr) {
		if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
			return
		}
		if _, ok := att.Type.(goaexpr.UserType); ok {
			return
		}
		if goaexpr.IsPrimitive(att.Type) {
			return
		}
		// Allow composite inline shapes (arrays, maps, objects, and composites).
		switch att.Type.(type) {
		case *goaexpr.Array, *goaexpr.Map, *goaexpr.Object, goaexpr.CompositeExpr:
			return
		}
		verr.Add(t, "%s must be a user type, primitive, or composite shape", where)
	}
	check("Args", t.Args)
	check("Return", t.Return)
	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}

// Finalize resolves and assigns the bound method after successful validation.
func (t *ToolExpr) Finalize() {
	if t.bindMethodName == "" {
		return
	}
	var svc *goaexpr.ServiceExpr
	if t.bindServiceName != "" {
		svc = goaexpr.Root.Service(t.bindServiceName)
	} else {
		svc = t.Toolset.Agent.Service
	}
	desired := codegen.Goify(t.bindMethodName, true)
	for _, m := range svc.Methods {
		if codegen.Goify(m.Name, true) == desired {
			t.Method = m
			return
		}
	}
	panic(fmt.Sprintf("tool %q: method %q not found in service %q after successful validation", t.Name, t.bindMethodName, svc.Name))
}

// BoundServiceName returns the service name specified via BindTo, if any.
func (t *ToolExpr) BoundServiceName() string {
	return t.bindServiceName
}
