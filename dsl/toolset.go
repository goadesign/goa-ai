package dsl

import (
	"strings"

	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentsexpr "goa.design/goa-ai/expr/agents"
	mcpexpr "goa.design/goa-ai/expr/mcp"
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
func Toolset(value any, fn ...func()) *agentsexpr.ToolsetExpr {
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
		agentsexpr.Root.Toolsets = append(agentsexpr.Root.Toolsets, ts)
		return ts
	case *agentsexpr.ToolsetGroupExpr:
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

// UseMCPToolset is kept for backward compatibility. Prefer MCP(..., mcp.FromService(service)).
//
// Example:
//
//	Uses(func(){
//	    UseMCPToolset("calc", "core")
//	})
func UseMCPToolset(service, suite string) {
	if _, ok := eval.Current().(*agentsexpr.ToolsetGroupExpr); !ok {
		eval.IncompatibleDSL()
		return
	}
	service = strings.TrimSpace(service)
	suite = strings.TrimSpace(suite)
	if service == "" || suite == "" {
		eval.ReportError("UseMCPToolset requires non-empty service and suite")
		return
	}
	MCP(service+"."+suite, func() { FromService(service) })
}

// Tool defines a single tool inside the current toolset.

// Tool marks the current method as an MCP tool.
// Call Tool inside a Method DSL to expose it to MCP clients. The method payload
// becomes the tool input schema and the method result becomes the tool output.
//
// Tool must be declared inside a Tool expression.
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
func Tool(name, description string, fn ...func()) {
	if name == "" {
		eval.ReportError("tool name cannot be empty")
		return
	}
	switch parent := eval.Current().(type) {
	case *agentsexpr.ToolsetExpr:
		var dslf func()
		if len(fn) > 0 {
			dslf = fn[0]
		}
		parent.Tools = append(parent.Tools, &agentsexpr.ToolExpr{
			Name:        name,
			Description: description,
			Toolset:     parent,
			DSLFunc:     dslf,
		})
	case *goaexpr.MethodExpr:
		svc := parent.Service
		var mcp *mcpexpr.MCPExpr
		if r := mcpexpr.Root; r != nil {
			mcp = r.GetMCP(svc)
		}
		if mcp == nil {
			eval.IncompatibleDSL()
			return
		}
		tool := &mcpexpr.ToolExpr{
			Name:        name,
			Description: description,
			Method:      parent,
		}
		if parent.Payload != nil {
			tool.InputSchema = parent.Payload
		}
		tool.Expression = parent
		mcp.Tools = append(mcp.Tools, tool)
	default:
		eval.IncompatibleDSL()
		return
	}
}

// Args defines the arguments data structure for a Tool.
// See Goa's methodDSL function used to define method payload and result.
func Args(val any, args ...any) {
	if len(args) > 2 {
		eval.TooManyArgError()
		return
	}
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Args = toolDSL(tool, "Args", val, args...)
}

// Return defines the data type of a tool's output.
func Return(val any, args ...any) {
	if len(args) > 2 {
		eval.TooManyArgError()
		return
	}
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Return = toolDSL(tool, "Return", val, args...)
}

// Tags attaches metadata tags to the current tool.
func Tags(values ...string) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Tags = append(tool.Tags, values...)
}

// BindTo associates the current tool with a Goa service method.
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
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	// Defer resolution to Prepare/Validate by recording the binding intent.
	tool.RecordBinding(serviceName, methodName)
}

// buildToolsetExpr constructs a ToolsetExpr from a value and DSL function.
func buildToolsetExpr(value any, dsl func(), agent *agentsexpr.AgentExpr) *agentsexpr.ToolsetExpr {
	switch v := value.(type) {
	case string:
		if v == "" {
			eval.ReportError("toolset name cannot be empty")
			return nil
		}
		return &agentsexpr.ToolsetExpr{
			Name:    v,
			DSLFunc: dsl,
			Agent:   agent,
		}
	case *agentsexpr.ToolsetExpr:
		if dsl != nil {
			eval.ReportError("toolset reference cannot include DSL overrides")
			return nil
		}
		dup := &agentsexpr.ToolsetExpr{
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

// toolDSL mirrors Goa's method DSL helpers to define tool shapes.
func toolDSL(m *agentsexpr.ToolExpr, suffix string, p any, args ...any) *goaexpr.AttributeExpr {
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
				if renamer, ok := dupped.(interface{ Rename(string) }); ok {
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
