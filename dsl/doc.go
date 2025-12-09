// Package dsl provides the Goa-AI design-time DSL for declaring agents, toolsets,
// MCP servers, registries, and run policies. These functions augment Goa's standard
// service DSL and drive the goa-ai code generators; they are not used at runtime.
//
// # Overview
//
// The DSL enables design-first development of LLM-based agents. You declare your
// agent's capabilities, tools, and policies in Go code, then run `goa gen` to
// produce type-safe packages including:
//
//   - Agent packages with workflow definitions and planner activities
//   - Tool codecs, JSON schemas, and registry entries
//   - MCP server adapters and client helpers
//   - Agent-as-tool composition helpers
//
// Import the DSL alongside Goa's standard DSL:
//
//	import (
//	    . "goa.design/goa/v3/dsl"
//	    . "goa.design/goa-ai/dsl"
//	)
//
// # Mental Model
//
// Think of the DSL as declaring intent across three domains:
//
// **Agents** define LLM-powered planners that orchestrate tool usage. Each agent
// belongs to a Goa service and declares which toolsets it consumes (Use) and
// exports (Export) for other agents.
//
// **Toolsets** group related tools owned by services. Tools have typed schemas
// (Args/Return) and can be bound to service methods (BindTo) or implemented via
// custom executors. Toolsets can be sourced from local definitions, MCP servers,
// or remote registries.
//
// **Policies** constrain agent behavior at runtime: caps on tool calls, time
// budgets, history management, and prompt caching. These policies become
// configuration that the runtime enforces.
//
// # DSL Structure
//
// The DSL functions must be called in the appropriate context:
//
//	API("name", func() {})           // Top-level API definition (Goa)
//	DisableAgentDocs()               // Inside API - disable quickstart doc generation
//
//	var MyTools = Toolset("...", func() {...})  // Top-level toolset definition
//	var MCPTools = Toolset(FromMCP(...))        // MCP-backed toolset
//	var RegTools = Toolset(FromRegistry(...))   // Registry-backed toolset
//	var MyRegistry = Registry("...", func() {...})  // Registry definition
//
//	Service("name", func() {         // Goa service definition
//	    MCP("name", "version")       // Enable MCP for this service
//
//	    Agent("name", "desc", func() {   // Inside Service
//	        Use(MyTools)                 // Reference toolsets
//	        Export("tools", func() {...}) // Export toolsets
//	        RunPolicy(func() {           // Inside Agent
//	            DefaultCaps(...)         // Inside RunPolicy
//	            TimeBudget("5m")
//	            History(func() {...})
//	        })
//	    })
//
//	    Method("search", func() {    // Goa method definition
//	        Tool("search", "...")    // Mark as MCP tool (requires MCP enabled)
//	        Resource(...)            // Mark as MCP resource
//	    })
//	})
//
// # Key Functions by Category
//
// Agent Functions:
//   - [Agent] declares an LLM agent within a service
//   - [Use] declares toolset consumption
//   - [Export] declares toolset export for agent-as-tool
//   - [DisableAgentDocs] opts out of AGENTS_QUICKSTART.md generation
//   - [Passthrough] forwards exported tools to service methods
//
// Toolset Functions:
//   - [Toolset] defines a provider-owned tool collection
//   - [FromMCP] configures an MCP-backed toolset provider
//   - [FromRegistry] configures a registry-backed toolset provider
//   - [AgentToolset] references an exported toolset by coordinates
//   - [Tags] attaches metadata labels to tools or toolsets
//
// Tool Functions:
//   - [Tool] declares a callable tool
//   - [Args] defines input parameter schema
//   - [Return] defines output result schema
//   - [Artifact] defines typed artifact data (not sent to model)
//   - [BindTo] binds a tool to a service method
//   - [Inject] marks fields as server-injected (hidden from LLM)
//   - [CallHintTemplate] configures call display hint template
//   - [ResultHintTemplate] configures result display hint template
//   - [BoundedResult] marks result as a bounded view over larger data
//
// Policy Functions:
//   - [RunPolicy] configures execution constraints
//   - [DefaultCaps] sets resource limits using [MaxToolCalls] and [MaxConsecutiveFailedToolCalls]
//   - [TimeBudget] sets maximum execution duration
//   - [InterruptsAllowed] enables user interruption handling
//   - [OnMissingFields] configures validation behavior
//   - [History] configures conversation history management
//   - [Cache] configures prompt caching hints
//
// Timing Functions (inside RunPolicy):
//   - [Timing] groups timing configuration
//   - [Budget] sets total wall-clock budget
//   - [Plan] sets planner activity timeout
//   - [Tools] sets default tool activity timeout
//
// History Functions (inside History):
//   - [KeepRecentTurns] configures sliding window retention
//   - [Compress] configures model-assisted summarization
//
// Cache Functions (inside Cache):
//   - [AfterSystem] places cache checkpoint after system messages
//   - [AfterTools] places cache checkpoint after tool definitions
//
// MCP Functions:
//   - [MCP] enables MCP protocol for a service
//   - [ProtocolVersion] configures MCP protocol version
//   - [Resource] marks a method as an MCP resource
//   - [WatchableResource] marks a method as a subscribable MCP resource
//   - [StaticPrompt] defines a static MCP prompt template
//   - [DynamicPrompt] marks a method as a dynamic prompt generator
//   - [Notification] marks a method as an MCP notification sender
//   - [Subscription] defines a subscription handler
//   - [SubscriptionMonitor] defines an SSE subscription monitor
//
// Registry Functions:
//   - [Registry] declares a remote registry source
//   - [APIVersion] sets the registry API version
//   - [Retry] configures retry policy
//   - [SyncInterval] sets catalog refresh interval
//   - [CacheTTL] sets local cache duration
//   - [Federation] configures external registry imports
//   - [Include] specifies namespaces to import
//   - [Exclude] specifies namespaces to skip
//   - [PublishTo] configures registry publication for exports
//
// # Generated Artifacts
//
// For each service with agents, `goa gen` produces:
//
//   - gen/<service>/agents/<agent>/ - Agent package with workflow and activities
//   - gen/<service>/agents/<agent>/specs/ - Tool specs, codecs, and JSON schemas
//   - gen/<service>/agents/<agent>/specs/tool_schemas.json - Backend-agnostic tool catalog
//   - gen/<service>/agents/<agent>/agenttools/ - Helpers for exported tools
//   - AGENTS_QUICKSTART.md - Contextual guide (unless disabled)
//
// For MCP-enabled services:
//
//   - gen/mcp_<service>/ - MCP server adapter and protocol helpers
//   - gen/mcp_<service>/client/ - Generated MCP client wrappers
//
// # Best Practices
//
// Design first: Put all agent and tool schemas in the DSL. Add examples and
// validations to field definitions. Let codegen own schemas and codecs.
//
// Use strong types: Define reusable Goa types (Type, ResultType) for complex
// tool payloads instead of inline anonymous schemas.
//
// Keep descriptions concise: Tool descriptions are shown to LLMs. Write clear,
// actionable summaries that help the model choose the right tool.
//
// Leverage BindTo: For service-backed tools, use BindTo to get generated
// transforms and keep tool schemas decoupled from method signatures.
//
// Mark bounded results: Tools returning potentially large data should use
// BoundedResult() so the runtime can track truncation metadata.
//
// For complete documentation and examples, see docs/dsl.md in the repository.
package dsl
