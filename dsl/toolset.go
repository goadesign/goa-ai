package dsl

import (
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentsexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
)

// Toolset defines a provider-owned group of related tools. Declare toolsets at
// the top level using Toolset(...) and reference them from agents via
// Use / Export.
//
// Tools declared inside a Toolset may be:
//
//   - Bound to Goa service methods via BindTo, in which case codegen emits
//     transforms and client helpers.
//   - Backed by MCP tools declared with the MCP DSL (MCPServer + MCPTool) and
//     exposed via MCPToolset(...).
//   - Implemented by custom executors or agent logic when left unbound.
//
// Toolset accepts a single form:
//
//   - Toolset("name", func()) declares a new toolset with the given name and tools.
//
// Example (provider toolset definition):
//
//	var CommonTools = Toolset("common", func() {
//	    Tool("notify", "Send notification", func() {
//	        Args(func() {
//	            Attribute("message", String, "Message to send")
//	            Required("message")
//	        })
//	    })
//	})
//
// Agents consume this toolset via Use:
//
//	Agent("assistant", "helper", func() {
//	    Use(CommonTools, func() {
//	        Tool("notify") // reference existing tool by name
//	    })
//	})
//
// For MCP-backed toolsets, define MCP tools on service methods using the MCP DSL
// (see mcp.go), declare a provider with MCPToolset(...), then attach it to agents
// via Use(MCPToolset(...), ...).
func Toolset(name string, fn ...func()) *agentsexpr.ToolsetExpr {
	if name == "" {
		eval.ReportError("toolset name must be non-empty")
		return nil
	}
	var dsl func()
	if len(fn) > 0 {
		dsl = fn[0]
	}
	if _, ok := eval.Current().(eval.TopExpr); !ok {
		eval.IncompatibleDSL()
		return nil
	}
	ts := newToolsetDefinition(name, dsl)
	agentsexpr.Root.Toolsets = append(agentsexpr.Root.Toolsets, ts)
	return ts
}

// MCPToolset declares a provider-owned MCP toolset derived from a Goa MCP
// server. It is a top-level construct (like Toolset) that returns a
// ToolsetExpr; agents then consume it via Use.
//
// MCPToolset takes:
//   - service: Goa service name that owns the MCPServer(...)
//   - toolset: MCP server name; this also becomes the toolset name
//
// There are two usage patterns:
//
//  1. Goa-backed MCP server defined in the same design:
//
//     Service("assistant", func() {
//     MCPServer("assistant", "1.0.0")
//     Method("search", func() {
//     Payload(...)
//     Result(...)
//     MCPTool("search", "Search documents")
//     })
//     })
//
//     var AssistantSuite = MCPToolset("assistant", "assistant-mcp")
//
//     Agent("chat", "LLM planner", func() {
//     Use(AssistantSuite)
//     })
//
//     In this form, tool schemas are discovered from the service's MCPTool
//     declarations.
//
//  2. External MCP server with inline tool schemas:
//
//     var RemoteSearch = MCPToolset("remote", "search", func() {
//     Tool("web_search", "Search the web", func() {
//     Args(func() { Attribute("query", String) })
//     Return(func() { Attribute("results", ArrayOf(String)) })
//     })
//     })
//
//     Agent("helper", "", func() {
//     Use(RemoteSearch)
//     })
//
//     In this form, tools must be declared explicitly. At runtime, an
//     mcpruntime.Caller must be configured for the toolset ID so the agent
//     can reach the external MCP server.
func MCPToolset(service, toolset string, fn ...func()) *agentsexpr.ToolsetExpr {
	if service == "" {
		eval.ReportError("MCPToolset requires non-empty service name")
		return nil
	}
	if toolset == "" {
		eval.ReportError("MCPToolset requires non-empty toolset name")
		return nil
	}
	var dsl func()
	if len(fn) > 0 {
		dsl = fn[0]
	}
	if _, ok := eval.Current().(eval.TopExpr); !ok {
		eval.IncompatibleDSL()
		return nil
	}
	ts := &agentsexpr.ToolsetExpr{
		Name:       toolset,
		DSLFunc:    dsl,
		External:   true,
		MCPService: service,
		MCPToolset: toolset,
	}
	agentsexpr.Root.Toolsets = append(agentsexpr.Root.Toolsets, ts)
	return ts
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
//   - Inject: marks fields as infrastructure-only (hidden from LLM)
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
//	var RemoteSearch = MCPToolset("remote", "search", func() {
//	    Tool("web_search", "Search the web", func() {
//	        Args(func() { Attribute("query", String) })
//	        Return(func() { Attribute("results", ArrayOf(String)) })
//	    })
//	})
//
//	Agent("helper", "", func() {
//	    Use(RemoteSearch)
//	})
//
// Example (service-backed tool with inheritance):
//
//	Toolset("docs", func() {
//	    Tool("search_docs", func() {
//	        BindTo("doc_service", "search")
//	        Inject("session_id")
//	    })
//	})
func Tool(name string, args ...any) {
	var description string
	var dslf func()

	if name == "" {
		eval.ReportError("tool name cannot be empty")
		return
	}

	// Parse arguments: (name, description?, func?)
	for _, arg := range args {
		switch a := arg.(type) {
		case string:
			description = a
		case func():
			dslf = a
		}
	}

	switch parent := eval.Current().(type) {
	case *agentsexpr.ToolsetExpr:
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

// Sidecar defines the typed sidecar schema for a tool result. Use Sidecar
// inside a Tool DSL to declare structured data that is attached to
// planner.ToolResult.Sidecar but never sent to the model provider.
//
// Sidecar follows the same patterns as Args/Return and Goa's Payload/Result:
// it accepts either:
//   - A function to define an inline object schema with Attribute() calls
//   - A Goa user type (Type, ResultType, etc.) to reuse existing type definitions
//   - A primitive type (String, Int, etc.) for simple single-value sidecar data
//
// Typical usage is to attach full-fidelity artifacts that back a bounded
// model-facing result. For example:
//
//	Tool("get_time_series", "Get Time Series", func() {
//	    Args(AtlasGetTimeSeriesToolArgs)
//	    Return(AtlasGetTimeSeriesToolReturn)
//	    Sidecar(AtlasGetTimeSeriesSidecar)
//	})
//
// If Sidecar is not called, the tool has no declared sidecar type and
// runtimes continue to use untyped map[string]any metadata.
func Sidecar(val any, args ...any) {
	if len(args) > 2 {
		eval.TooManyArgError()
		return
	}
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Sidecar = toolDSL(tool, "Sidecar", val, args...)
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

// BoundedResult marks the current tool's result as a bounded view over a
// potentially larger data set. It is a lightweight contract that tells the
// runtime and services that this tool:
//
//   - Applies domain-aware caps (limits, window clamping, depth bounds), and
//   - Should surface truncation metadata (returned/total/truncated/hints)
//     alongside its result.
//
// BoundedResult does not change the tool schema by itself; it annotates the
// tool so codegen and services can attach and enforce bounds in a uniform way.
//
// BoundedResult must appear in a Tool expression.
//
// Example:
//
//	Tool("list_devices", "List devices", func() {
//	    Args(AtlasListDevicesToolArgs)
//	    Return(AtlasListDevicesToolReturn)
//	    BoundedResult()
//	})
func BoundedResult() {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.BoundedResult = true
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
//		Method("search_documents", func() {
//			Payload(func() { ... })
//			Result(func() { ... })
//		})
//		Agent("assistant", "Helper", func() {
//			Use("doc-tools", func() {
//				Tool("search", "Search documents", func() {
//					Args(func() { ... })  // Can differ from method payload
//					Return(func() { ... }) // Can differ from method result
//					BindTo("search_documents")
//				})
//			})
//		})
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

// Inject marks specific payload fields as "injected" (server-side infrastructure).
// Injected fields are:
//  1. Hidden from the LLM (excluded from the JSON schema).
//  2. Exposed in the generated struct with a Setter method.
//  3. Intended to be populated by runtime hooks (ToolInterceptor).
//
// Example:
//
//	Tool("get_data", func() {
//	    BindTo("data_service", "get")
//	    // "session_id" is required by the service but hidden from the LLM
//	    Inject("session_id")
//	})
func Inject(names ...string) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.InjectedFields = append(tool.InjectedFields, names...)
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
func newToolsetDefinition(name string, dsl func()) *agentsexpr.ToolsetExpr {
	return &agentsexpr.ToolsetExpr{
		Name:    name,
		DSLFunc: dsl,
	}
}

func cloneToolset(origin *agentsexpr.ToolsetExpr, agent *agentsexpr.AgentExpr, overlay func()) *agentsexpr.ToolsetExpr {
	if origin == nil {
		eval.ReportError("toolset reference cannot be nil")
		return nil
	}
	dup := &agentsexpr.ToolsetExpr{
		Name:        origin.Name,
		Description: origin.Description,
		Tags:        append([]string(nil), origin.Tags...),
		Agent:       agent,
		External:    origin.External,
		MCPService:  origin.MCPService,
		MCPToolset:  origin.MCPToolset,
		Origin:      origin,
	}
	switch {
	case origin.DSLFunc != nil && overlay != nil:
		dup.DSLFunc = func() {
			origin.DSLFunc()
			overlay()
		}
	case overlay != nil:
		dup.DSLFunc = overlay
	default:
		dup.DSLFunc = origin.DSLFunc
	}
	return dup
}

func instantiateToolset(value any, overlay func(), agent *agentsexpr.AgentExpr) *agentsexpr.ToolsetExpr {
	switch v := value.(type) {
	case string:
		if v == "" {
			eval.ReportError("toolset name must be non-empty")
			return nil
		}
		return &agentsexpr.ToolsetExpr{
			Name:    v,
			DSLFunc: overlay,
			Agent:   agent,
		}
	case *agentsexpr.ToolsetExpr:
		return cloneToolset(v, agent, overlay)
	default:
		eval.ReportError("toolset must be referenced by name or Toolset expression")
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
	if service == "" || agent == "" || toolset == "" {
		eval.ReportError("AgentToolset requires non-empty service, agent, and toolset")
		return nil
	}
	svc := goaexpr.Root.Service(service)
	if svc == nil {
		eval.ReportError("AgentToolset could not resolve service %q", service)
		return nil
	}
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
	for _, ts := range originAgent.Exported.Toolsets {
		if ts != nil && ts.Name == toolset {
			return ts
		}
	}
	eval.ReportError("AgentToolset could not resolve toolset %q exported by agent %q.%q", toolset, service, agent)
	return nil
}

// UseAgentToolset is an alias for AgentToolset. Prefer AgentToolset in new
// designs; this alias exists for readability in some codebases.
func UseAgentToolset(service, agent, toolset string) *agentsexpr.ToolsetExpr {
	ts := AgentToolset(service, agent, toolset)
	if ts == nil {
		return nil
	}
	return Use(ts)
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
