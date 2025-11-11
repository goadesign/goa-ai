package dsl

import (
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentsexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
)

// Toolset defines a group of related tools that agents can invoke. Toolsets provide
// logical packaging for capability exposure and enable tool reuse across multiple agents.
// Tools are bound to service method implementations at runtime.
//
// Toolset has three distinct usage patterns:
//
// 1. Global Toolset (top-level declaration):
//
// Declare a toolset outside any agent or service to create a reusable set of tools
// that multiple agents can consume. Global toolsets are stored as design-time
// expressions and must be explicitly referenced by agents that use them.
//
//	var CommonTools = Toolset("common", func() {
//	    Tool("notify", "Send notification", func() {
//	        // Tool implementation here
//	    })
//	})
//
// 2. Agent Export (inside agent Exports):
//
// Declare a toolset that this agent provides for other agents to consume. Export
// toolsets define the agent's public API.
//
//	Agent("docs", "Document processor", func() {
//	    Exports(func() {
//	        Toolset("document-tools", func() {
//	            Tool("summarize", "Summarize document", func() {
//	                // Tool implementation here
//	            })
//	        })
//	    })
//	})
//
// 3. Agent Reference (inside agent Uses):
//
// Reference an existing global toolset or another agent's export. Pass the toolset
// expression (not the name) to indicate which toolset this agent consumes.
//
//	Agent("assistant", "helper", func() {
//	    Uses(func() {
//	        Toolset(CommonTools) // reference global toolset by expression
//	    })
//	})
//
// Toolset accepts two forms:
//
// - Toolset("name", func()) - declares a new toolset with the given name and tools
// - Toolset(existingToolset) - references an existing toolset expression
//
// The DSL function for new toolsets supports:
//   - Tool("name", "description", func()) - declare tools with Args, Return, Tags
//
// Complete example showing global toolset declaration and reuse:
//
//	// 1. Declare global toolset at top level
//	var SharedTools = Toolset("shared", func() {
//	    Tool("log", "Log a message", func() {
//	        Args(func() {
//	            Attribute("level", String, "Log level")
//	            Attribute("message", String, "Log message")
//	            Required("level", "message")
//	        })
//	    })
//	})
//
//	// 2. Multiple agents reference the same toolset
//	Service("operations", func() {
//	    Agent("monitor", "System monitor", func() {
//	        Uses(func() {
//	            Toolset(SharedTools)  // First agent uses it
//	        })
//	    })
//	    Agent("analyzer", "Log analyzer", func() {
//	        Uses(func() {
//	            Toolset(SharedTools)  // Second agent uses it
//	        })
//	    })
//	})
//
// Note: For external MCP toolsets, use MCPToolset() instead of Toolset().
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

// MCPToolset declares an external MCP toolset that an agent consumes. It must be
// used inside an agent's Uses expression. MCPToolset supports two forms:
//
//  1. Reference a Goa-backed MCP service defined in the same design:
//     MCPToolset(service, suite) - tools are discovered from the service's MCP declaration
//
//  2. Declare an external MCP server with inline tool schemas:
//     MCPToolset(service, suite, func() { Tool(...) }) - tools must be declared explicitly
//
// For Goa-backed MCP, the service and suite must match an MCP server declared via
// the MCP DSL on a Goa service. Tool schemas are automatically extracted from the
// service methods.
//
// For external MCP servers (not defined in this design), provide a DSL function
// that declares the tools using Tool(). The runtime will require an mcpruntime.Caller
// configured to communicate with the external server.
//
// Example (Goa-backed MCP):
//
//	Agent("assistant", "Helper agent", func() {
//	    Uses(func() {
//	        MCPToolset("calc", "core")  // References calc service's "core" MCP suite
//	    })
//	})
//
// Example (external MCP with inline tools):
//
//	Agent("assistant", "Helper agent", func() {
//	    Uses(func() {
//	        MCPToolset("remote", "search", func() {
//	            Tool("web_search", "Search the web", func() {
//	                Args(func() { Attribute("query", String) })
//	                Return(func() { Attribute("results", ArrayOf(String)) })
//	            })
//	        })
//	    })
//	})
func MCPToolset(service, suite string, dsl ...func()) *agentsexpr.ToolsetExpr {
	var dslFunc func()
	if len(dsl) > 0 {
		dslFunc = dsl[0]
	}
	group, ok := eval.Current().(*agentsexpr.ToolsetGroupExpr)
	if !ok {
		eval.IncompatibleDSL()
		return nil
	}
	if service == "" {
		eval.ReportError("MCPToolset requires non-empty service name")
		return nil
	}
	if suite == "" {
		eval.ReportError("MCPToolset requires non-empty suite name")
		return nil
	}
	ts := &agentsexpr.ToolsetExpr{
		Name:       suite,
		Agent:      group.Agent,
		External:   true,
		MCPService: service,
		MCPSuite:   suite,
		DSLFunc:    dslFunc,
	}
	group.Toolsets = append(group.Toolsets, ts)
	return ts
}

// UseMCPToolset is a compatibility alias for MCPToolset. Prefer MCPToolset.
func UseMCPToolset(service, suite string, dsl ...func()) *agentsexpr.ToolsetExpr {
	return MCPToolset(service, suite, dsl...)
}

// Tool declares a tool for agents or MCP servers. It has two distinct use cases:
//
//  1. Inside a Toolset (agent tools): Declares a tool with inline argument and
//     return schemas. Use this for custom tools in agent toolsets or external MCP
//     servers where you manually define the schemas.
//
//  2. Inside a Method (MCP tools): Marks a Goa service method as an MCP tool.
//     The method's payload becomes the tool input schema and the method result
//     becomes the tool output schema. This automatically exposes the method via
//     the service's MCP server.
//
// Tool takes two required arguments and one optional DSL function:
//   - name: the tool identifier
//   - description: a concise summary presented to the LLM
//   - dsl (optional): configuration block (only for toolset tools, ignored for method tools)
//
// Inside toolsets, the DSL function can use:
//   - Args: defines the input parameter schema
//   - Return: defines the output result schema
//   - Tags: attaches metadata labels
//   - BindTo: binds to a service method for implementation (optional)
//
// Example (toolset tool with inline schemas):
//
//	Toolset("utils", func() {
//	    Tool("summarize", "Summarize a document", func() {
//	        Args(func() {
//	            Attribute("text", String, "Document text")
//	            Required("text")
//	        })
//	        Return(func() {
//	            Attribute("summary", String, "Summary text")
//	        })
//	        Tags("nlp", "summarization")
//	    })
//	})
//
// Example (external MCP tool with inline schemas):
//
//	Agent("helper", "", func() {
//	    Uses(func() {
//	        MCPToolset("remote", "search", func() {
//	            Tool("web_search", "Search the web", func() {
//	                Args(func() { Attribute("query", String) })
//	                Return(func() { Attribute("results", ArrayOf(String)) })
//	            })
//	        })
//	    })
//	})
//
// Example (MCP tool from service method):
//
//	Service("calculator", func() {
//	    MCP("calc", "1.0", "Calculator tools", func() {})
//	    Method("add", func() {
//	        Payload(func() {
//	            Attribute("a", Int)
//	            Attribute("b", Int)
//	        })
//	        Result(func() {
//	            Attribute("sum", Int)
//	        })
//	        Tool("add", "Add two numbers")  // Exposes method as MCP tool
//	    })
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

// Args defines the input parameter schema for a tool. Use Args inside a Tool DSL
// to specify what arguments the tool accepts when invoked by an agent or LLM.
//
// Args follows the same patterns as Goa's Payload function for methods. It accepts:
//   - A function to define an inline object schema with Attribute() calls
//   - A Goa user type (Type, ResultType, etc.) to reuse existing type definitions
//   - A primitive type (String, Int, etc.) for simple single-value inputs
//
// When using a function to define the schema inline, you can use:
//   - Attribute(name, type, description) to define each parameter
//   - Required(...) to mark parameters as required
//   - All Goa attribute DSL functions (MinLength, Maximum, Pattern, etc.)
//
// Example (inline schema):
//
//	Tool("search", "Search documents", func() {
//	    Args(func() {
//	        Attribute("query", String, "Search query text")
//	        Attribute("limit", Int, "Maximum number of results")
//	        Attribute("filters", MapOf(String, String), "Search filters")
//	        Required("query")
//	    })
//	    Return(func() { ... })
//	})
//
// Example (reuse existing type):
//
//	var SearchParams = Type("SearchParams", func() {
//	    Attribute("query", String)
//	    Attribute("limit", Int)
//	    Required("query")
//	})
//
//	Tool("search", "Search documents", func() {
//	    Args(SearchParams)
//	    Return(func() { ... })
//	})
//
// Example (primitive type for simple tools):
//
//	Tool("echo", "Echo a message", func() {
//	    Args(String)  // Single string parameter
//	    Return(String)
//	})
//
// If Args is not called, the tool accepts no parameters (empty/null payload).
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

// Return defines the output result schema for a tool. Use Return inside a Tool DSL
// to specify what data structure the tool produces when successfully executed.
//
// Return follows the same patterns as Goa's Result function for methods. It accepts:
//   - A function to define an inline object schema with Attribute() calls
//   - A Goa user type (Type, ResultType, etc.) to reuse existing type definitions
//   - A primitive type (String, Int, etc.) for simple single-value outputs
//
// When using a function to define the schema inline, you can use:
//   - Attribute(name, type, description) to define each result field
//   - Required(...) to mark fields as always present
//   - All Goa attribute DSL functions (Example, MinLength, etc.)
//
// Example (inline schema):
//
//	Tool("analyze", "Analyze document", func() {
//	    Args(func() { ... })
//	    Return(func() {
//	        Attribute("summary", String, "Document summary")
//	        Attribute("keywords", ArrayOf(String), "Extracted keywords")
//	        Attribute("score", Float64, "Confidence score")
//	        Required("summary", "keywords", "score")
//	    })
//	})
//
// Example (reuse existing type):
//
//	var AnalysisResult = ResultType("application/vnd.analysis", func() {
//	    Attribute("summary", String)
//	    Attribute("keywords", ArrayOf(String))
//	    Required("summary")
//	})
//
//	Tool("analyze", "Analyze document", func() {
//	    Args(func() { ... })
//	    Return(AnalysisResult)
//	})
//
// Example (primitive type for simple tools):
//
//	Tool("count_words", "Count words in text", func() {
//	    Args(String)
//	    Return(Int)  // Returns single integer count
//	})
//
// If Return is not called, the tool produces no output (empty/null result).
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

// CallHintTemplate configures a display template for tool invocations. The
// template is rendered with the tool's payload to produce a concise hint shown
// during execution. Templates are compiled with missingkey=error.
//
// CallHintTemplate must appear in a Tool expression.
//
// CallHintTemplate takes a single string argument which is the Go template text.
// Keep templates concise (≤ 140 characters recommended).
//
// Example:
//
//	Tool("search", "Search documents", func() {
//	    Args(func() {
//	        Attribute("query", String)
//	        Attribute("limit", Int)
//	    })
//	    CallHintTemplate("Searching for: {{ .Query }} (limit: {{ .Limit }})")
//	})
func CallHintTemplate(s string) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.CallHintTemplate = s
}

// ResultHintTemplate configures a display template for tool results. The
// template is rendered with the tool's result to produce a preview shown after
// execution. Templates are compiled with missingkey=error.
//
// ResultHintTemplate must appear in a Tool expression.
//
// ResultHintTemplate takes a single string argument which is the Go template text.
//
// Example:
//
//	Tool("search", "Search documents", func() {
//	    Return(func() {
//	        Attribute("count", Int)
//	        Attribute("results", ArrayOf(String))
//	    })
//	    ResultHintTemplate("Found {{ .Count }} results")
//	})
func ResultHintTemplate(s string) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.ResultHintTemplate = s
}

// Tags attaches metadata labels to a tool for categorization and filtering. Tags
// can be used by agents, planners, or monitoring systems to organize and discover
// tools based on their capabilities or domains.
//
// Tags accepts a variadic list of strings. Each tag should be a simple lowercase
// identifier or category name. Common patterns include:
//   - Domain categories: "nlp", "database", "api", "filesystem"
//   - Capability types: "read", "write", "search", "transform"
//   - Risk levels: "safe", "destructive", "external"
//   - Performance hints: "slow", "fast", "cached"
//
// Example (domain and capability tags):
//
//	Tool("search_docs", "Search documentation", func() {
//	    Args(func() { ... })
//	    Return(func() { ... })
//	    Tags("search", "documentation", "nlp")
//	})
//
// Example (risk and performance tags):
//
//	Tool("delete_file", "Delete a file", func() {
//	    Args(func() { ... })
//	    Tags("filesystem", "write", "destructive")
//	})
//
// Tags are optional. Tools without tags are still fully functional but may be
// harder to organize in systems with many available tools.
func Tags(values ...string) {
	switch cur := eval.Current().(type) {
	case *agentsexpr.ToolExpr:
		cur.Tags = append(cur.Tags, values...)
	case *agentsexpr.ToolsetExpr:
		cur.Tags = append(cur.Tags, values...)
	default:
		eval.IncompatibleDSL()
		return
	}
}

// ToolsetDescription sets a human-readable description for the current toolset.
// This description can help document the toolset's purpose and capabilities.
//
// ToolsetDescription must appear in a Toolset expression.
//
// ToolsetDescription takes a single string argument.
//
// Example:
//
//	Toolset("data-tools", func() {
//	    ToolsetDescription("Tools for data processing and analysis")
//	    Tool("analyze", "Analyze dataset", func() { ... })
//	})
func ToolsetDescription(s string) {
	ts, ok := eval.Current().(*agentsexpr.ToolsetExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	ts.Description = s
}

// BindTo associates a tool with a Goa service method implementation. Use BindTo
// inside a Tool DSL to specify which service method executes the tool when invoked.
// This enables tools to reuse existing service logic and separates tool schema
// definitions from implementation.
//
// BindTo accepts one or two arguments:
//   - BindTo(methodName) - binds to a method in the agent's service
//   - BindTo(serviceName, methodName) - binds to a method in a different service
//
// The method name should match the Goa method name (case-insensitive). When using
// two arguments, the service name should match the Goa service name.
//
// When a tool is bound to a method:
//   - The tool's Args schema can differ from the method's Payload
//   - The tool's Return schema can differ from the method's Result
//   - Generated adapters will transform between tool and method types
//   - Method payload/result validation still applies
//
// Example (bind to method in same service):
//
//	Service("docs", func() {
//	    Method("search_documents", func() {
//	        Payload(func() { ... })
//	        Result(func() { ... })
//	    })
//	    Agent("assistant", "Helper", func() {
//	        Uses(func() {
//	            Toolset("doc-tools", func() {
//	                Tool("search", "Search documents", func() {
//	                    Args(func() { ... })  // Can differ from method payload
//	                    Return(func() { ... }) // Can differ from method result
//	                    BindTo("search_documents")
//	                })
//	            })
//	        })
//	    })
//	})
//
// Example (bind to method in different service):
//
//	Tool("notify", "Send notification", func() {
//	    Args(func() {
//	        Attribute("message", String)
//	    })
//	    BindTo("notifications", "send")  // notifications.send method
//	})
//
// BindTo is optional. Tools without BindTo are external tools that must be
// implemented through custom executors or MCP callers.
func BindTo(args ...string) {
	var (
		serviceName string
		methodName  string
	)
	switch len(args) {
	case 1:
		methodName = args[0]
	case 2:
		serviceName = args[0]
		methodName = args[1]
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

// ToolTitle sets a human-friendly display title for the current tool. If not
// specified, the generated code derives a title from the tool name by converting
// snake_case or kebab-case to Title Case.
//
// ToolTitle must appear in a Tool expression.
//
// ToolTitle takes a single string argument.
//
// Example:
//
//	Tool("web_search", "Search the web", func() {
//	    ToolTitle("Web Search")
//	    Args(func() { ... })
//	})
func ToolTitle(s string) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Title = s
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
			Origin:      v,
		}
		return dup
	default:
		eval.ReportError("toolset must be declared with a name or an existing Toolset expression")
		return nil
	}
}

// AgentToolset references a toolset exported by another agent identified by
// service and agent names. Use inside an Agent's Uses block to explicitly
// depend on an exported toolset when inference is not possible or ambiguous.
//
// When to use AgentToolset vs Toolset:
//   - Prefer Toolset(X) when you already have an expression handle (e.g., a
//     top-level Toolset variable or an agent's exported Toolset). Goa-AI will
//     infer a RemoteAgent provider automatically when exactly one agent in a
//     different service Exports a toolset with the same name.
//   - Use AgentToolset(service, agent, toolset) when you:
//   - Do not have an expression handle to the exported toolset, or
//   - Have ambiguity (multiple agents export a toolset with the same name), or
//   - Want to be explicit in the design for clarity.
//
// AgentToolset(service, agent, toolset)
//   - service: Goa service name that owns the exporting agent
//   - agent:   Agent name in that service
//   - toolset: Exported toolset name in that agent
//
// The referenced toolset is resolved from the design, and a local reference is
// recorded with its Origin set to the defining toolset. Provider information is
// inferred during validation and will classify this as a RemoteAgent provider
// when the owner service differs from the consumer service.
func AgentToolset(service, agent, toolset string) *agentsexpr.ToolsetExpr {
	group, ok := eval.Current().(*agentsexpr.ToolsetGroupExpr)
	if !ok {
		eval.IncompatibleDSL()
		return nil
	}
	if service == "" || agent == "" || toolset == "" {
		eval.ReportError("AgentToolset requires non-empty service, agent, and toolset")
		return nil
	}
	svc := goaexpr.Root.Service(service)
	if svc == nil {
		eval.ReportError("AgentToolset could not resolve service %q", service)
		return nil
	}
	// Locate the exporting agent
	var originAgent *agentsexpr.AgentExpr
	for _, a := range agentsexpr.Root.Agents {
		if a != nil && a.Service == svc && a.Name == agent {
			originAgent = a
			break
		}
	}
	if originAgent == nil || originAgent.Exported == nil {
		eval.ReportError("AgentToolset could not find exported toolsets for %q.%q", service, agent)
		return nil
	}
	// Find the exported toolset
	var originTS *agentsexpr.ToolsetExpr
	for _, ts := range originAgent.Exported.Toolsets {
		if ts != nil && ts.Name == toolset {
			originTS = ts
			break
		}
	}
	if originTS == nil {
		eval.ReportError("AgentToolset could not resolve toolset %q exported by agent %q.%q", toolset, service, agent)
		return nil
	}
	// Clone a local reference and record origin; assign to current agent group
	dup := &agentsexpr.ToolsetExpr{
		Name:        originTS.Name,
		Description: originTS.Description,
		DSLFunc:     originTS.DSLFunc,
		Agent:       group.Agent,
		Origin:      originTS,
	}
	group.Toolsets = append(group.Toolsets, dup)
	return dup
}

// UseAgentToolset is an alias for AgentToolset. Prefer AgentToolset in new
// designs; this alias exists for readability in some codebases.
func UseAgentToolset(service, agent, toolset string) *agentsexpr.ToolsetExpr {
	return AgentToolset(service, agent, toolset)
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
