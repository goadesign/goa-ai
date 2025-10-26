package dsl

import (
	"fmt"
	"strings"

	"goa.design/goa-ai/agents/expr"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// Toolset defines a group of related tools—either as reusable global toolsets
// or as exports from an agent. Use Toolset inside the root design (for global
// toolsets) or within Uses or Exports expressions on an Agent. Each Toolset
// declares a collection of actions that agents can invoke or expose to other
// agents. Toolsets provide logical packaging for capability exposure, are
// referenced by name, and are enforced at runtime.
//
// Toolset must be declared at the top level or inside Agent Uses or Exports
// expressions. When called with a name and DSL function, Toolset declares a new
// toolset and returns the expression so it can be reused elsewhere in the
// design. When called with an existing Toolset expression, the toolset is
// referenced in the current context (for example, inside another agent's Uses
// block) without redefining it.
//
// Toolset supports two forms:
//   - Toolset("name", func() { ... }) to declare a new toolset
//   - Toolset(existingToolset) to reference a previously declared toolset
//
// The DSL function can use the following functions to define the toolset:
//   - Tool: defines a tool that the toolset can use
//
// Example (global toolset):
//
//	Toolset("docs", func() {
//	    Tool("summarize", ...)
//	    Tool("tag", ...)
//	})
//
// Example (agent export):
//
//	Agent("docs-agent", "Agent for docs", func() {
//	    Exports(func() {
//	        Toolset("exported-tools", func() {
//	            Tool("transform", ...)
//	        })
//	    })
//	})
//
// Example (reuse global toolset):
//
//	var Shared = Toolset("shared", func() {
//	    Tool("ping", "Ping helper", func() {})
//	})
//	Service("ops", func() {
//	    Agent("watch", "Watcher", func() {
//	        Uses(func() {
//	            Toolset(Shared)
//	        })
//	    })
//	})
func Toolset(value any, fn ...func()) *expr.ToolsetExpr {
	var dsl func()
	if len(fn) > 0 {
		dsl = fn[0]
	}
	switch cur := eval.Current().(type) {
	case eval.TopExpr:
		ts := buildToolsetExpr(value, dsl, nil)
		if ts == nil {
			return nil
		}
		if ts.Agent != nil {
			// Top-level toolsets should not capture an agent.
			ts.Agent = nil
		}
		expr.Root.Toolsets = append(expr.Root.Toolsets, ts)
		return ts
	case *expr.ToolsetGroupExpr:
		ts := buildToolsetExpr(value, dsl, cur.Agent)
		if ts == nil {
			return nil
		}
		cur.Toolsets = append(cur.Toolsets, ts)
		return ts
	default:
		eval.IncompatibleDSL()
		return nil
	}
}

// UseMCPToolset declares a reference to a toolset exported by an MCP server
// defined in a Goa service. Must be called within an Agent Uses block.
//
// UseMCPToolset accepts two arguments:
//   - service: the name of the Goa service that declared the MCP server (via MCPServer)
//   - suite: the MCP server identifier as provided to MCPServer
//
// The referenced MCP toolset is included into the current agent's toolset group,
// enabling the agent to invoke tools exposed by the external MCP suite.
//
// Example:
//
//	Uses(func() {
//	    UseMCPToolset("calc", "core-suite")
//	})
func UseMCPToolset(service, suite string) {
	group, ok := eval.Current().(*expr.ToolsetGroupExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	service = strings.TrimSpace(service)
	suite = strings.TrimSpace(suite)
	if service == "" || suite == "" {
		eval.ReportError("UseMCPToolset requires non-empty service and suite")
		return
	}
	name := fmt.Sprintf("%s.%s", service, suite)
	ts := &expr.ToolsetExpr{
		Name:       name,
		Agent:      group.Agent,
		External:   true,
		MCPService: service,
		MCPSuite:   suite,
	}
	group.Toolsets = append(group.Toolsets, ts)
}

// Tool defines a single tool inside the current toolset.
//
// Tool must be declared inside a Toolset expression.
//
// Tool takes three arguments:
//   - name: the name of the tool
//   - description: a concise summary used to present the tool to the LLM
//   - dsl: a DSL function that defines the tool's configuration, including its
//     arguments, return type, and metadata.
//
// The dsl function can use the following functions to define the tool:
// - Args: defines the tool's arguments
// - Return: defines the tool's return value
// - Tags: attaches metadata tags to the tool
//
// Example:
//
//	Tool("summarize", "Summarize a document", func() {
//	    Args(func() { /* Input fields */ })
//	    Return(func() { /* Output fields */ })
//	    Tags("nlp", "summarization")
//	})
func Tool(name, description string, dsl func()) {
	if name == "" {
		eval.ReportError("tool name cannot be empty")
		return
	}
	toolset, ok := eval.Current().(*expr.ToolsetExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	toolset.Tools = append(toolset.Tools, &expr.ToolExpr{
		Name:        name,
		Description: description,
		Toolset:     toolset,
		DSLFunc:     dsl,
	})
}

// Args defines the arguments data structure for a Tool.
//
// Args must be declared inside a Tool expression, where it specifies the input
// shape and validations for the tool. Use Args to define fields, types, and
// validation rules required by the tool invocation. Args supports multiple
// declaration forms, mirroring the Goa Payload convention for Method, but
// adapted for tools.
//
// Args accepts:
//   - a type (UserType, ResultType, or primitive) optionally followed by a description string and/or a DSL function
//   - or a DSL function alone to define inline attributes
//
// The following forms are valid:
//
//	Args(Type)
//	Args(Type, "description")
//	Args(Type, func())
//	Args(Type, "description", func())
//
// Example:
//
//	Tool("upper", func() {
//	    // Use primitive type.
//	    Args(String)
//	})
//
//	Tool("upper", func() {
//	    // Use primitive type and description.
//	    Args(String, "string to convert to uppercase")
//	})
//
//	Tool("upper", func() {
//	    // Use primitive type, description, and validations.
//	    Args(String, "string to convert to uppercase", func() {
//	        Pattern("^[a-z]")
//	    })
//	})
//
//	Tool("add", func() {
//	    // Define arguments data structure inline.
//	    Args(func() {
//	        Description("Left and right operands to add")
//	        Attribute("left", Int32, "Left operand")
//	        Attribute("right", Int32, "Right operand")
//	        Required("left", "right")
//	    })
//	})
//
//	Tool("add", func() {
//	    // Reference arguments by user type, see Goa's UserType DSL.
//	    Args(Operands)
//	})
//
//	Tool("divide", func() {
//	    // Specify additional required attributes on user type.
//	    Args(Operands, func() {
//	        Required("left", "right")
//	    })
//	})
func Args(val any, args ...any) {
	if len(args) > 2 {
		eval.TooManyArgError()
		return
	}
	tool, ok := eval.Current().(*expr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Args = toolDSL(tool, "Args", val, args...)
}

// Return defines the data type of a tool's output.
//
// It must be declared inside a Tool expression.
//
// Return takes one to three arguments:
//   - The first argument is either a type (primitive, user type, or an
//     array/map thereof) or a DSL function.
//   - If the first argument is a type, an optional description string may be
//     passed as the second argument.
//   - A DSL function may be passed as the last argument to further specialize
//     the type (e.g., with validations)
//
// The valid syntax for Return is thus:
//
//	Return(Type)
//
//	Return(func())
//
//	Return(Type, "description")
//
//	Return(Type, func())
//
//	Return(Type, "description", func())
//
// Example usages:
//
//	Return(String)
//
//	Return(func() {
//	    Description("Sum result")
//	    Attribute("sum", Int32, "Computed sum")
//	    Required("sum")
//	})
//
//	Return(MyResult, "Result with custom description")
//
//	Return(MyResult, func() {
//	    Required("fieldA")
//	})
func Return(val any, args ...any) {
	if len(args) > 2 {
		eval.TooManyArgError()
		return
	}
	tool, ok := eval.Current().(*expr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Return = toolDSL(tool, "Return", val, args...)
}

// Tags attaches metadata tags to the current tool.
//
// Tags must be declared inside a Tool expression.
//
// Tags takes one or more string arguments representing metadata tags.
//
// Example:
//
//	Tags("nlp", "summarization")
func Tags(values ...string) {
	tool, ok := eval.Current().(*expr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Tags = append(tool.Tags, values...)
}

// Method binds the current tool to a Goa service method. Use inside a Tool
// DSL block to indicate that the generated Execute function should call the
// specified service method, optionally through a user-provided adapter.
//
// Example:
//
//	Tool("lookup", "Lookup by ID", func() {
//	    Args(func() { Attribute("id", String, "ID"); Required("id") })
//	    Return(MyResult)
//	    Method("GetByID")
//	})
//
// BindTo binds the current tool to a Goa service method.
// Usage:
//
//	BindTo("MethodName")                  // bind to a method on the owning service
//	BindTo("ServiceName", "MethodName")   // bind to a method on another service
func BindTo(args ...string) {
	var (
		serviceName string
		methodName  string
	)
	switch len(args) {
	case 1:
		methodName = strings.TrimSpace(args[0])
	case 2:
		serviceName = strings.TrimSpace(args[0])
		methodName = strings.TrimSpace(args[1])
	default:
		eval.ReportError("BindTo requires 1 or 2 arguments")
		return
	}
	if methodName == "" {
		eval.ReportError("BindTo requires a non-empty method name")
		return
	}
	tool, ok := eval.Current().(*expr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	// Defer resolution to Prepare/Validate by recording the binding intent.
	tool.RecordBinding(serviceName, methodName)
}

// Method was removed in favor of BindTo.

// buildToolsetExpr constructs a ToolsetExpr from a value and DSL function.
func buildToolsetExpr(value any, dsl func(), agent *expr.AgentExpr) *expr.ToolsetExpr {
	switch v := value.(type) {
	case string:
		if v == "" {
			eval.ReportError("toolset name cannot be empty")
			return nil
		}
		return &expr.ToolsetExpr{
			Name:    v,
			DSLFunc: dsl,
			Agent:   agent,
		}
	case *expr.ToolsetExpr:
		if dsl != nil {
			eval.ReportError("toolset reference cannot include DSL overrides")
			return nil
		}
		dup := &expr.ToolsetExpr{
			Name:        v.Name,
			Description: v.Description,
			DSLFunc:     v.DSLFunc,
			Agent:       agent,
		}
		return dup
	default:
		eval.ReportError("toolset must be declared with a name or an existing Toolset expression")
		return nil
	}
}

// See Goa's methodDSL function used to define method payload and result.
func toolDSL(m *expr.ToolExpr, suffix string, p any, args ...any) *goaexpr.AttributeExpr {
	var (
		att *goaexpr.AttributeExpr
		fn  func()
	)
	switch actual := p.(type) {
	case func():
		fn = actual
		att = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	case goaexpr.UserType:
		if len(args) == 0 {
			// Do not duplicate type if it is not customized
			return &goaexpr.AttributeExpr{Type: actual}
		}
		dupped := goaexpr.Dup(actual)
		att = &goaexpr.AttributeExpr{Type: dupped}
		if f, ok := args[len(args)-1].(func()); ok {
			numreqs := 0
			if att.Validation != nil {
				numreqs = len(att.Validation.Required)
			}
			eval.Execute(f, att)
			if att.Validation != nil && len(att.Validation.Required) != numreqs {
				// If the DSL modifies the type attributes "requiredness"
				// then rename the type to avoid collisions.
				if renamer, ok := dupped.(interface {
					Rename(string)
				}); ok {
					renamer.Rename(actual.Name() + "_" + m.Name + "_" + suffix)
				}
			}
		}
	case goaexpr.DataType:
		att = &goaexpr.AttributeExpr{Type: actual}
	default:
		eval.InvalidArgError("type or function", p)
		return nil
	}
	if len(args) >= 1 {
		if f, ok := args[len(args)-1].(func()); ok {
			if fn != nil {
				eval.InvalidArgError("(type), (func), (type, func), (type, desc) or (type, desc, func)", f)
			}
			fn = f
		}
		if d, ok := args[0].(string); ok {
			att.Description = d
		}
	}
	if fn != nil {
		eval.Execute(fn, att)
		if obj, ok := att.Type.(*goaexpr.Object); ok {
			if len(*obj) == 0 {
				att.Type = goaexpr.Empty
			}
		}
	}
	return att
}
