package expr

import (
	"fmt"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// ToolsetExpr captures a toolset declaration from the agent DSL, representing
	// a logical collection of tools that can be used by agents or exported for
	// consumption by other agents.
	//
	// Toolsets may be internal (tools defined in the DSL) or external (referencing
	// an MCP server for runtime tool discovery). The Agent field links each toolset
	// to its declaring agent, and Tools accumulates all tool expressions during
	// evaluation. Code generation uses this metadata to produce runtime registrations,
	// schemas, and client adapters.
	ToolsetExpr struct {
		eval.DSLFunc

		// Name is the unique identifier for this toolset within the agent.
		Name string
		// Description summarizes the toolset's purpose and available capabilities.
		Description string
		// Agent is the owning agent that declares or uses this toolset (always non-nil after DSL execution).
		Agent *AgentExpr
		// Tools is the list of all tools in this toolset (accumulated during DSL evaluation).
		Tools []*ToolExpr

		// External indicates the toolset references an external MCP provider for runtime tool discovery.
		External bool
		// MCPService names the Goa service that declared the MCP server (only set when External is true).
		MCPService string
		// MCPSuite is the MCP server identifier declared in the service DSL (only set when External is true).
		MCPSuite string
	}

	// ToolExpr captures an individual tool declaration within a toolset, including
	// its name, description, argument/return types, and optional binding to a Goa
	// service method.
	//
	// Tools may be implemented as agent-tools (runtime executors) or bound to service
	// methods via BindTo. The Args and Return fields define the JSON schema for
	// marshaling/unmarshaling at runtime. Validation and Finalize phases resolve
	// method bindings after the full Goa design is evaluated.
	ToolExpr struct {
		eval.DSLFunc

		// Name is the unique identifier for this tool within its toolset.
		Name string
		// Description explains the tool's purpose, inputs, and outputs for LLM consumption.
		Description string
		// Tags are optional semantic labels for categorization and filtering.
		Tags []string
		// Args defines the tool's input schema (may be nil for tools with no arguments).
		Args *goaexpr.AttributeExpr
		// Return defines the tool's output schema (may be nil for tools with no return value).
		Return *goaexpr.AttributeExpr
		// Toolset is the parent toolset containing this tool (always non-nil after DSL execution).
		Toolset *ToolsetExpr

		// Method binds this tool to a Goa service method for automatic client dispatch.
		// When set, code generation emits adapter code that converts tool arguments to
		// method payloads and maps method results back to tool return types. The binding
		// is optional; tools without a Method are expected to be implemented by user-provided
		// executors or runtime agent-tools.
		Method *goaexpr.MethodExpr

		// bindServiceName captures the service name specified via BindTo (internal, set during DSL).
		bindServiceName string
		// bindMethodName captures the method name specified via BindTo (internal, resolved in Validate/Finalize).
		bindMethodName string
	}
)

// EvalName is part of eval.Expression allowing descriptive error messages.
func (t *ToolsetExpr) EvalName() string {
	return fmt.Sprintf("toolset %q", t.Name)
}

// WalkSets exposes the nested expressions to the eval engine.
func (t *ToolsetExpr) WalkSets(walk eval.SetWalker) {
	walk(eval.ToExpressionSet(t.Tools))
}

// EvalName implements eval.Expression.
func (t *ToolExpr) EvalName() string {
	return fmt.Sprintf("tool %q in toolset %q and service %q", t.Name, t.Toolset.Name, t.Toolset.Agent.Service.Name)
}

// RecordBinding records the service and method names specified via the DSL.
// Resolution of the actual *expr.MethodExpr is deferred to Prepare/Validate.
func (t *ToolExpr) RecordBinding(serviceName, methodName string) {
	t.bindServiceName = strings.TrimSpace(serviceName)
	t.bindMethodName = strings.TrimSpace(methodName)
}

// Validate checks that any recorded binding can be resolved to an existing
// service and method. It relies on DSL guarantees that Toolset and Agent are
// non-nil after evaluation.
func (t *ToolExpr) Validate() error {
	if t.bindMethodName == "" {
		return nil
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
			return nil
		}
	}
	verr.Add(t, "service method %q not found in service %q", t.bindMethodName, svc.Name)
	return verr
}

// Finalize resolves and assigns the bound method after successful validation.
// It relies on DSL guarantees that Toolset and Agent are non-nil after evaluation.
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
	if svc == nil {
		return
	}
	desired := codegen.Goify(t.bindMethodName, true)
	for _, m := range svc.Methods {
		if codegen.Goify(m.Name, true) == desired {
			t.Method = m
			return
		}
	}
}
