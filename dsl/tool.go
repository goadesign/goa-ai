package dsl

import (
	"strings"

	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentsexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
)

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
//	var RemoteSearch = Toolset("remote", FromMCP("search-service", "search"), func() {
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

// Artifact defines the typed artifact schema for a tool result. Use Artifact
// inside a Tool DSL to declare structured data that is attached to
// planner.ToolResult.Artifacts but never sent to the model provider.
//
// # Model awareness
//
// The artifact schema Description (set via the optional description argument or
// within the artifact DSL block) must describe what the user sees when the
// artifact is rendered in the UI (chart, card, timeline, etc.). The runtime uses
// this description to automatically inject post-tool system reminders for the
// model so it can naturally reference the rendered UI. When artifacts are
// disabled for a tool call (via the standard `artifacts` payload toggle), the
// runtime can also inject a reminder that the tool may be re-run with artifacts
// enabled to show the described UI.
//
// Artifact follows the same patterns as Args/Return and Goa's Payload/Result:
// it accepts either:
//   - A function to define an inline object schema with Attribute() calls
//   - A Goa user type (Type, ResultType, etc.) to reuse existing type definitions
//   - A primitive type (String, Int, etc.) for simple single-value artifact data
//
// Typical usage is to attach full-fidelity artifacts that back a bounded
// model-facing result. For example:
//
//	Tool("get_time_series", "Get Time Series", func() {
//	    Args(GetTimeSeriesToolArgs)
//	    Return(GetTimeSeriesToolReturn)
//	    Artifact("time_series", GetTimeSeriesSidecar)
//	})
func Artifact(kind string, val any, args ...any) {
	if kind == "" {
		eval.ReportError("artifact kind must be non-empty")
		return
	}
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
	tool.SidecarKind = kind
}

// ArtifactsDefault configures the default behavior for emitting sidecar artifacts
// when the caller does not explicitly request a mode via the reserved `artifacts`
// payload field (or sets it to "auto").
//
// Valid values are:
//   - "on": emit artifacts by default (when the tool produces them)
//   - "off": do not emit artifacts unless the caller explicitly sets `artifacts:"on"`
//
// ArtifactsDefault must appear in a Tool expression. It is only meaningful for
// tools that declare an Artifact.
func ArtifactsDefault(mode string) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if tool.Sidecar == nil || tool.Sidecar.Type == nil || tool.Sidecar.Type == goaexpr.Empty {
		eval.ReportError("ArtifactsDefault requires the tool to declare an Artifact")
		return
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "on":
		tool.ArtifactsDefault = "on"
	case "off":
		tool.ArtifactsDefault = "off"
	default:
		eval.ReportError("ArtifactsDefault mode must be \"on\" or \"off\"")
	}
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
// potentially larger data set. Bounded tools must surface truncation metadata
// (returned/total/truncated/hints) alongside their result so runtimes can guide
// planners and users when results are partial.
//
// BoundedResult may optionally include a DSL function to configure boundedness
// details. For example, cursor-based pagination fields can be declared via
// Cursor and NextCursor inside the optional DSL block:
//
//	Tool("search", "Search docs", func() {
//	    Args(SearchArgs)
//	    Return(SearchResult)
//	    BoundedResult(func() {
//	        Cursor("cursor")
//	        NextCursor("next_cursor")
//	    })
//	})
//
// Cursor-based pagination contract:
//
//   - Cursor values are opaque.
//   - When paging, callers must keep all other parameters unchanged and only set
//     the payload cursor field to the value returned by the result next-cursor field.
//
// BoundedResult must appear in a Tool expression.
func BoundedResult(fns ...func()) {
	if len(fns) > 1 {
		eval.TooManyArgError()
		return
	}
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.BoundedResult = true
	if len(fns) == 1 {
		eval.Execute(fns[0], tool)
	}
	ensureBoundedResultShape(tool)
}

// ResultReminder configures a static system reminder that is injected into the
// conversation after the tool result is returned. Use this to provide
// backstage guidance to the model about how to interpret or present the
// result to the user.
//
// The reminder text is automatically wrapped in <system-reminder> tags by
// the runtime. Do not include the tags in the text.
//
// This DSL function is for static, design-time reminders that apply every time
// the tool is called. For dynamic reminders that depend on runtime state or
// tool result content, use PlannerContext.AddReminder() in your planner
// implementation instead. Dynamic reminders support rate limiting, per-run
// caps, and can be added or removed based on runtime conditions.
//
// ResultReminder must appear in a Tool expression.
//
// Example:
//
//	Tool("get_time_series", "Get Time Series", func() {
//	    Args(GetTimeSeriesToolArgs)
//	    Return(GetTimeSeriesToolReturn)
//	    ResultReminder("The user sees a rendered graph of this data.")
//	})
func ResultReminder(s string) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.ResultReminder = s
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
