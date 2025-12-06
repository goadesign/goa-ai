# Requirements Document

## Introduction

This document specifies requirements for integrating MCP Gateway Registry functionality into goa-ai. The feature enables goa-ai agents to discover, register, and consume MCP servers and agent toolsets from centralized registries—both local and federated external sources (e.g., Anthropic MCP Registry). This brings enterprise-grade tool governance, dynamic discovery, and unified access patterns to goa-ai's design-first agentic systems.

The integration follows goa-ai's philosophy: define intent in DSL, generate infrastructure, execute reliably. Registry concepts become first-class DSL constructs that codegen transforms into typed clients, discovery helpers, and runtime wiring.

## Integration with Existing Goa-AI Functionality

This feature extends goa-ai's existing DSL and runtime patterns while unifying the toolset model.

### Unified Toolset Model

Currently goa-ai has two toolset constructs:
- `Toolset` - agent-local tools with inline schemas, optionally bound to service methods
- `MCPToolset` - references MCP servers as toolsets

This feature unifies them into a single `Toolset` construct with configurable providers:

```go
// Local toolset (current Toolset behavior)
var LocalTools = Toolset("utils", func() {
    Tool("summarize", "Summarize text", func() {
        Args(func() { Attribute("text", String) })
        Return(func() { Attribute("summary", String) })
    })
})

// MCP-backed toolset (current MCPToolset behavior)
var MCPTools = Toolset("assistant", FromMCP("assistant-service", "assistant-mcp"))

// Registry-backed toolset (new)
var RegistryTools = Toolset("enterprise-tools", FromRegistry("corp-registry", "data-tools"))

// Federated external registry (new)
var AnthropicTools = Toolset("community", FromRegistry("anthropic", "web-search"))
```

The `MCPToolset` function is removed.

### DSL Integration

#### Registry DSL

`Registry` is a new top-level DSL construct for declaring registry sources. It references Goa's existing security schemes for authentication:

```go
// Define security schemes using standard Goa DSL
var CorpAPIKey = APIKeySecurity("corp_api_key", func() {
    Description("Corporate registry API key")
})

var AnthropicOAuth = OAuth2Security("anthropic_oauth", func() {
    ClientCredentialsFlow(
        "https://auth.anthropic.com/oauth/token",
        "",
    )
    Scope("registry:read", "Read access to registry")
})

// Declare a registry source
var CorpRegistry = Registry("corp-registry", func() {
    Description("Corporate tool registry")
    URL("https://registry.corp.internal")  // Registry base URL
    APIVersion("v1")                       // API version path segment
    Security(CorpAPIKey)                   // References Goa security scheme
    
    // HTTP client configuration
    Timeout("30s")                         // Request timeout
    Retry(3, "1s")                         // Max 3 retries with 1s backoff
    
    // Sync configuration
    SyncInterval("5m")                     // How often to refresh catalog
    CacheTTL("1h")                         // Local cache duration
})

// Federated external registry
var AnthropicRegistry = Registry("anthropic", func() {
    Description("Anthropic MCP Registry")
    URL("https://registry.anthropic.com/v1")
    Security(AnthropicOAuth)
    Federation(func() {
        // Only import specific namespaces
        Include("web-search", "code-execution")
        Exclude("experimental/*")
    })
    SyncInterval("1h")
    CacheTTL("24h")
})
```

#### Toolset Provider Options

`Toolset` is extended with provider options:
- `FromMCP(service, toolset)` - backs toolset with an MCP server
- `FromRegistry(registry, toolset)` - sources toolset from a registry

```go
// MCP-backed toolset (replaces MCPToolset)
var MCPTools = Toolset("assistant", FromMCP("assistant-service", "assistant-mcp"))

// Registry-backed toolset
var RegistryTools = Toolset("enterprise-tools", FromRegistry(CorpRegistry, "data-tools"))

// Registry toolset with version pinning
var PinnedTools = Toolset("stable-tools", FromRegistry(CorpRegistry, "data-tools"), func() {
    Version("1.2.3")  // Pin to specific version
})
```

#### Export with Registry Publication

`Export` is extended with `PublishTo` for registry publication:

```go
Agent("data-agent", "Data processing agent", func() {
    Use(LocalTools)
    
    Export(LocalTools, func() {
        PublishTo(CorpRegistry)  // Publish to corporate registry
        Tags("data", "etl")      // Discovery tags
    })
})
```

#### Summary of DSL Changes
- **Registry** - new top-level construct for declaring registry sources with auth and sync config
- **MCP** - renamed from `MCPServer` for declaring MCP servers on services
- **Tool** - unified context-aware function that works in both Toolset (agent tools) and Method (MCP tools) contexts; `MCPTool` is removed
- **Toolset** - extended with provider options: `FromMCP(service, toolset)`, `FromRegistry(registry, toolset)`; `MCPToolset` is removed
- **Use** - works unchanged—agents consume any toolset regardless of provider
- **Export** - extended with `PublishTo(registry)` option for registry publication

#### DSL File Organization

The DSL package (`dsl/`) is reorganized to group functions by concept rather than by protocol:

| File | Purpose | Functions |
|------|---------|-----------|
| `agent.go` | Agent definition & composition | `Agent`, `Use`, `Export`, `DisableAgentDocs`, `Passthrough`, `AgentToolset`, `UseAgentToolset` |
| `tool.go` | Tool definition (unified) | `Tool`, `Args`, `Return`, `Artifact`, `Tags`, `BindTo`, `Inject`, `ToolTitle`, `CallHintTemplate`, `ResultHintTemplate`, `BoundedResult` |
| `toolset.go` | Toolset definition & providers | `Toolset`, `FromMCP`, `FromRegistry`, `ToolsetDescription`, `Version` |
| `mcp.go` | MCP server declaration | `MCP`, `ProtocolVersion`, `Resource`, `WatchableResource`, `StaticPrompt`, `DynamicPrompt`, `Notification`, `Subscription`, `SubscriptionMonitor` |
| `registry.go` | Registry declaration (new) | `Registry`, `Federation`, `Include`, `Exclude`, `SyncInterval`, `CacheTTL`, `PublishTo` |
| `policy.go` | Run policies & caps | `RunPolicy`, `DefaultCaps`, `MaxToolCalls`, `MaxConsecutiveFailedToolCalls`, `InterruptsAllowed`, `OnMissingFields` |
| `timing.go` | Timing configuration | `Timing`, `Budget`, `Plan`, `Tools`, `TimeBudget` |
| `history.go` | History & caching | `History`, `Cache`, `Compress`, `KeepRecentTurns`, `AfterSystem`, `AfterTools` |

This organization:
- Groups functions by the concept they define (Agent, Tool, Toolset, MCP, Registry)
- Separates configuration concerns (policy, timing, history) from definition concerns
- Makes it clear where new functions should be added
- Removes protocol-specific prefixes (`MCPTool`, `MCPToolset`) in favor of context-aware unified functions

### Runtime Integration
- Registry clients integrate with the existing `runtime.Options` pattern
- All toolsets (local, MCP, registry) flow through the same `tool_specs.Specs` registry
- Caching integrates with existing feature module patterns (like `features/memory/mongo`)
- Observability uses the existing Clue telemetry stack (Logger, Metrics, Tracer)

### Breaking Changes
- **MCPToolset** is removed—use `Toolset(name, FromMCP(service, toolset))` instead
- **MCPServer** is renamed to **MCP**—use `MCP(name, version)` instead of `MCPServer(name, version)`
- **MCPTool** is removed—use `Tool(name, description)` inside Method instead (Tool is context-aware and works in both Toolset and Method contexts)
- **Tool schema format** must align with MCP tool schema specification for registry interoperability
- **Runtime initialization** order may change to support registry discovery before agent startup
- **ToolsetExpr** gains a `Provider` field that replaces the current `External`/`MCPService`/`MCPToolset` fields

### Goa Framework Changes

This feature requires changes to the Goa framework to enable DSL reuse. Both interfaces follow the same pattern: define an interface in `expr` package, extend the DSL function to check for it.

#### SecurityHolder Interface
Goa's `Security()` DSL function currently only accepts `*expr.MethodExpr`, `*expr.ServiceExpr`, or `*expr.APIExpr` as parent contexts. To allow `Security()` inside `Registry` declarations, Goa will add a `SecurityHolder` interface:

```go
// In goa.design/goa/v3/expr
type SecurityHolder interface {
    AddSecurityRequirement(*SecurityExpr)
}
```

The `Security()` function will be extended to check for this interface:

```go
// In goa.design/goa/v3/dsl/security.go
func Security(args ...any) {
    // ... existing scheme resolution ...
    
    current := eval.Current()
    switch actual := current.(type) {
    case *expr.MethodExpr:
        actual.Requirements = append(actual.Requirements, security)
    case *expr.ServiceExpr:
        actual.Requirements = append(actual.Requirements, security)
    case *expr.APIExpr:
        actual.Requirements = append(actual.Requirements, security)
    case expr.SecurityHolder:  // NEW: interface-based extension
        actual.AddSecurityRequirement(security)
    default:
        eval.IncompatibleDSL()
    }
}
```

Goa-ai's `RegistryExpr` will implement this interface to enable `Security()` inside `Registry` declarations.

#### URLHolder Interface
Goa's `URL()` DSL function currently only accepts `*expr.ContactExpr`, `*expr.DocsExpr`, or `*expr.LicenseExpr` as parent contexts. To allow `URL()` inside `Registry` declarations, Goa will add a `URLHolder` interface:

```go
// In goa.design/goa/v3/expr
type URLHolder interface {
    SetURL(string)
}
```

The `URL()` function will be extended to check for this interface:

```go
// In goa.design/goa/v3/dsl/api.go
func URL(url string) {
    switch actual := eval.Current().(type) {
    case *expr.ContactExpr:
        actual.URL = url
    case *expr.DocsExpr:
        actual.URL = url
    case *expr.LicenseExpr:
        actual.URL = url
    case expr.URLHolder:  // NEW: interface-based extension
        actual.SetURL(url)
    default:
        eval.IncompatibleDSL()
    }
}
```

Goa-ai's `RegistryExpr` will implement this interface to enable `URL()` inside `Registry` declarations, keeping the DSL consistent with Goa patterns.

### Coherence Goals
- All toolsets are first-class citizens regardless of provider (local, MCP, registry)
- Agents use `Use(toolset)` uniformly—provider is an implementation detail
- Generated code follows the same `gen/<service>/agents/<agent>/specs/` layout for all providers
- Runtime tool execution is provider-agnostic; the executor abstraction handles routing

## Glossary

- **Registry**: A centralized catalog of MCP servers, toolsets, and agents with metadata, versioning, and access control. In DSL, declared via `Registry(name, func())`.
- **MCP Server**: A Model Context Protocol server exposing tools, resources, and prompts
- **Toolset**: A named group of related tools that agents can consume, with a configurable provider (local, MCP, or registry)
- **Provider**: The source/executor for a toolset—local (inline schemas), MCP (MCP server), or registry (remote catalog)
- **Agent Card**: Metadata describing an agent's capabilities, skills, and authentication schemes (A2A protocol)
- **Federation**: Importing and synchronizing servers/agents from external registries. Configured via `Federation(func())` inside a Registry declaration.
- **Discovery**: Runtime lookup of available tools/servers based on semantic queries or filters
- **Goa-AI Runtime**: The execution layer that runs agents with durable orchestration
- **DSL**: Domain-Specific Language used in goa-ai designs to declare agents, toolsets, and policies
- **FromRegistry**: A DSL provider option `FromRegistry(registry, toolset)` that configures a toolset to be sourced from a registry
- **FromMCP**: A DSL provider option `FromMCP(service, toolset)` that configures a toolset to be backed by an MCP server
- **MCP**: DSL function `MCP(name, version)` that declares an MCP server on a service (renamed from `MCPServer`)
- **PublishTo**: A DSL option `PublishTo(registry)` used inside Export to configure registry publication
- **Security (in Registry)**: References a Goa security scheme (`APIKeySecurity`, `OAuth2Security`, `JWTSecurity`, `BasicAuthSecurity`) for registry authentication
- **SyncInterval**: DSL option specifying how often to refresh the registry catalog (e.g., `SyncInterval("5m")`)
- **CacheTTL**: DSL option specifying local cache duration for registry data (e.g., `CacheTTL("1h")`)
- **APIVersion**: DSL option specifying the registry API version (e.g., `APIVersion("v1")`)
- **Timeout**: DSL option specifying HTTP request timeout for registry operations (e.g., `Timeout("30s")`)
- **Retry**: DSL option configuring retry policy for failed requests (e.g., `Retry(3, "1s")`)
- **URL (in Registry)**: Goa's `URL()` function extended via `URLHolder` interface to work in Registry context for specifying the registry endpoint
- **SecurityHolder**: Interface added to Goa enabling `Security()` to work in extension contexts like Registry
- **URLHolder**: Interface added to Goa enabling `URL()` to work in extension contexts like Registry
- **A2A Protocol**: Agent-to-Agent protocol specification for agent discovery, communication, and task execution between heterogeneous agent platforms
- **A2A Agent Card**: Machine-readable agent profile following A2A spec, containing protocol version, name, description, URL, capabilities, skills, and security schemes
- **A2A Skill**: A discrete capability that an A2A agent can perform, with input/output modes and tags (analogous to tools in MCP)
- **A2A Task**: A unit of work sent to an A2A agent, containing a skill ID and input data, returning a task result

## Requirements

### Requirement 1

**User Story:** As a goa-ai developer, I want to declare registry sources in my DSL design, so that my agents can discover and consume tools from centralized catalogs without manual configuration.

#### Acceptance Criteria

1. WHEN a developer declares a Registry source in the DSL THEN the Goa-AI codegen SHALL generate typed client code for that registry
2. WHEN a Registry declaration includes authentication configuration THEN the Goa-AI codegen SHALL generate credential handling code that integrates with the runtime
3. WHEN multiple Registry sources are declared THEN the Goa-AI runtime SHALL merge their tool catalogs with configurable precedence
4. WHEN a Registry source becomes unavailable THEN the Goa-AI runtime SHALL emit structured error events and continue with cached data if available

### Requirement 2

**User Story:** As a goa-ai developer, I want to use discovered toolsets from registries in my agent definitions, so that I can leverage curated enterprise tools without duplicating schemas.

#### Acceptance Criteria

1. WHEN an agent Uses a registry-backed toolset THEN the Goa-AI codegen SHALL generate discovery and binding code that resolves tools at startup
2. WHEN a registry toolset includes tool schemas THEN the Goa-AI runtime SHALL validate tool payloads against those schemas before invocation
3. WHEN a registry toolset version changes THEN the Goa-AI runtime SHALL detect the change and optionally refresh the tool catalog
4. WHEN tool discovery fails for a required toolset THEN the Goa-AI runtime SHALL prevent agent startup and report a structured error

### Requirement 3

**User Story:** As a goa-ai developer, I want to register my goa-ai agents and MCP servers with a registry, so that other agents and systems can discover and invoke them.

#### Acceptance Criteria

1. WHEN an agent declares Export with registry publication THEN the Goa-AI codegen SHALL generate registration code that publishes the agent card to the registry
2. WHEN an MCP server is annotated for registry publication THEN the Goa-AI codegen SHALL generate registration code that publishes server metadata
3. WHEN registration succeeds THEN the Goa-AI runtime SHALL maintain a heartbeat or lease to keep the registration active
4. WHEN the agent or server shuts down gracefully THEN the Goa-AI runtime SHALL deregister from the registry

### Requirement 4

**User Story:** As a goa-ai developer, I want to search for tools using natural language queries, so that my agents can dynamically discover specialized capabilities at runtime.

#### Acceptance Criteria

1. WHEN an agent invokes semantic search on a registry THEN the Goa-AI runtime SHALL return relevance-scored tool matches
2. WHEN semantic search returns results THEN each result SHALL include tool ID, description, schema reference, and relevance score
3. WHEN no results match the query THEN the Goa-AI runtime SHALL return an empty result set without error
4. WHEN the registry does not support semantic search THEN the Goa-AI runtime SHALL fall back to keyword-based filtering

### Requirement 5

**User Story:** As a goa-ai developer, I want to federate external registries like Anthropic's MCP Registry, so that I can access curated community tools alongside my enterprise tools.

#### Acceptance Criteria

1. WHEN a Federation source is declared in the DSL THEN the Goa-AI codegen SHALL generate sync code that imports servers from that source
2. WHEN federated servers are imported THEN the Goa-AI runtime SHALL tag them with their origin for audit and filtering
3. WHEN a federated source updates its catalog THEN the Goa-AI runtime SHALL detect changes during periodic sync
4. WHEN federation sync fails THEN the Goa-AI runtime SHALL log the failure and continue with previously cached data

### Requirement 6

**User Story:** As a goa-ai developer, I want registry operations to integrate with goa-ai's observability stack, so that I can monitor discovery, registration, and invocation patterns.

#### Acceptance Criteria

1. WHEN registry operations occur THEN the Goa-AI runtime SHALL emit structured log events with operation type, duration, and outcome
2. WHEN registry operations occur THEN the Goa-AI runtime SHALL record metrics for latency, success rate, and cache hit ratio
3. WHEN registry operations occur THEN the Goa-AI runtime SHALL propagate trace context for distributed tracing
4. WHEN registry errors occur THEN the Goa-AI runtime SHALL emit error events with structured details including registry ID and operation

### Requirement 7

**User Story:** As a goa-ai developer, I want to configure access policies for registry-discovered tools, so that I can enforce governance on which tools agents can invoke.

#### Acceptance Criteria

1. WHEN a policy restricts a registry tool THEN the Goa-AI runtime SHALL prevent invocation and return a policy violation error
2. WHEN a tool requires specific permissions THEN the Goa-AI runtime SHALL validate the agent's credentials before invocation
3. WHEN policy evaluation occurs THEN the Goa-AI runtime SHALL log the decision with tool ID, agent ID, and outcome
4. WHEN policies are updated THEN the Goa-AI runtime SHALL apply changes without requiring agent restart

### Requirement 8

**User Story:** As a goa-ai developer, I want registry data to be cached locally, so that my agents can operate with reduced latency and survive temporary registry outages.

#### Acceptance Criteria

1. WHEN tool schemas are fetched from a registry THEN the Goa-AI runtime SHALL cache them locally with configurable TTL
2. WHEN cached data exists and the registry is unreachable THEN the Goa-AI runtime SHALL use cached data and emit a warning event
3. WHEN cache TTL expires THEN the Goa-AI runtime SHALL attempt to refresh from the registry in the background
4. WHEN cache storage fails THEN the Goa-AI runtime SHALL continue without caching and emit a warning event

### Requirement 9

**User Story:** As a goa-ai developer, I want to serialize and deserialize registry tool schemas, so that I can store, transmit, and validate tool definitions consistently.

#### Acceptance Criteria

1. WHEN a tool schema is serialized THEN the Goa-AI runtime SHALL produce valid JSON conforming to the MCP tool schema format
2. WHEN a tool schema is deserialized THEN the Goa-AI runtime SHALL validate it against the MCP schema specification
3. WHEN serializing then deserializing a tool schema THEN the Goa-AI runtime SHALL produce an equivalent schema (round-trip consistency)
4. WHEN an invalid schema is deserialized THEN the Goa-AI runtime SHALL return a structured validation error
5. WHEN a tool schema is printed for debugging THEN the Goa-AI runtime SHALL produce human-readable JSON output (pretty-printer for round-trip testing)

### Requirement 10

**User Story:** As a goa-ai developer, I want a unified toolset model that abstracts provider differences, so that I can define and consume toolsets without caring whether they are local, MCP-backed, or registry-sourced.

#### Acceptance Criteria

1. WHEN a developer declares a Toolset with no provider option THEN the Goa-AI DSL SHALL treat it as a local toolset with inline schemas
2. WHEN a developer declares a Toolset with FromMCP provider option THEN the Goa-AI DSL SHALL derive tool schemas from the referenced MCP server
3. WHEN a developer declares a Toolset with FromRegistry provider option THEN the Goa-AI DSL SHALL defer schema resolution to runtime discovery
4. WHEN an agent Uses any toolset THEN the Goa-AI codegen SHALL generate identical specs structure regardless of provider
5. WHEN MCPToolset is used THEN the Goa-AI DSL SHALL report a compile-time error directing users to migrate to Toolset with FromMCP provider

### Requirement 11

**User Story:** As a goa-ai developer, I want registry-backed toolsets to integrate seamlessly with existing DSL patterns, so that I can use registry tools with the same Use/Export/BindTo patterns I already know.

#### Acceptance Criteria

1. WHEN an agent Uses a registry-backed toolset THEN the Goa-AI codegen SHALL generate the same specs structure as locally-defined toolsets
2. WHEN an agent Exports a toolset with PublishTo THEN the Goa-AI codegen SHALL generate registration code alongside the standard export code
3. WHEN a registry tool is bound via BindTo THEN the Goa-AI codegen SHALL generate transforms between registry schema and service method types
4. WHEN a registry tool uses Inject for infrastructure fields THEN the Goa-AI runtime SHALL hide those fields from the LLM while populating them at invocation

### Requirement 12

**User Story:** As a goa-ai developer, I want registry functionality to follow goa-ai's existing runtime patterns, so that the framework feels coherent and I can use familiar configuration approaches.

#### Acceptance Criteria

1. WHEN configuring registry clients THEN the Goa-AI runtime SHALL accept them via runtime.Options following the existing feature module pattern
2. WHEN registry tools are discovered THEN the Goa-AI runtime SHALL populate tool_specs.Specs with the same structure as locally-generated tools
3. WHEN registry operations emit telemetry THEN the Goa-AI runtime SHALL use the existing Clue Logger, Metrics, and Tracer interfaces
4. WHEN registry caching is configured THEN the Goa-AI runtime SHALL follow the same store interface pattern as features/memory and features/session

### Requirement 13

**User Story:** As a goa-ai developer, I want my goa-ai agents to be A2A-compatible, so that external A2A clients can discover and invoke them through the registry.

#### Acceptance Criteria

1. WHEN an agent declares Export with PublishTo THEN the Goa-AI codegen SHALL generate an A2A-compliant agent card with protocol version, name, description, URL, version, and capabilities
2. WHEN an agent exports toolsets THEN the Goa-AI codegen SHALL map exported tools to A2A skills with appropriate input/output modes and tags
3. WHEN an agent has security requirements THEN the Goa-AI codegen SHALL include corresponding A2A security schemes in the agent card
4. WHEN an A2A client sends a task request to a goa-ai agent THEN the Goa-AI runtime SHALL handle the A2A JSON-RPC protocol and route to the appropriate skill/tool
5. WHEN generating the agent card THEN the Goa-AI codegen SHALL derive the agent URL from the service's HTTP endpoint configuration

### Requirement 14

**User Story:** As a goa-ai developer, I want my agents to discover and invoke external A2A agents through the registry, so that I can compose workflows with agents built on different platforms.

#### Acceptance Criteria

1. WHEN an agent searches for A2A agents in a registry THEN the Goa-AI runtime SHALL return agent cards with skills, capabilities, and endpoint URLs
2. WHEN an agent invokes an external A2A agent skill THEN the Goa-AI runtime SHALL send a properly formatted A2A task request to the agent's endpoint
3. WHEN an A2A agent returns a task result THEN the Goa-AI runtime SHALL parse the response and make it available to the calling agent
4. WHEN an A2A agent supports streaming THEN the Goa-AI runtime SHALL handle streaming task responses appropriately
5. WHEN invoking an A2A agent requires authentication THEN the Goa-AI runtime SHALL use the security scheme specified in the agent card

### Requirement 15

**User Story:** As a goa-ai developer, I want to declare A2A agent dependencies in my DSL design, so that my agents can consume external A2A agents with the same patterns as toolsets.

#### Acceptance Criteria

1. WHEN a developer declares an A2A agent dependency via FromRegistry THEN the Goa-AI DSL SHALL resolve the agent card at startup and make skills available
2. WHEN an agent Uses an A2A agent's skills THEN the Goa-AI codegen SHALL generate typed client code for invoking those skills
3. WHEN an A2A agent skill has input/output schemas THEN the Goa-AI codegen SHALL generate corresponding Go types for type-safe invocation
4. WHEN an A2A agent is unavailable at runtime THEN the Goa-AI runtime SHALL emit a structured error and optionally use cached agent card data

### Requirement 16

**User Story:** As a goa-ai developer, I want generated code to be specialized and efficient, so that my agents have minimal runtime overhead and maximum type safety.

#### Acceptance Criteria

1. WHEN the DSL design contains static information (URLs, names, schemas, security schemes) THEN the Goa-AI codegen SHALL generate static literals rather than runtime-computed values
2. WHEN an agent card is generated THEN the Goa-AI codegen SHALL emit static struct literals for skills, security schemes, and capabilities rather than builder functions
3. WHEN schema validation is required THEN the Goa-AI codegen SHALL generate type-specific validation functions rather than generic schema interpreters
4. WHEN URL paths are known at design time THEN the Goa-AI codegen SHALL generate static URL strings rather than runtime path joining
5. WHEN only specific security schemes are used THEN the Goa-AI codegen SHALL generate only the required auth provider types rather than all possible variants
6. WHEN runtime behavior requires dynamic data (discovered schemas, user-provided URLs) THEN the Goa-AI runtime SHALL provide minimal generic constructs to support that specific dynamic behavior

