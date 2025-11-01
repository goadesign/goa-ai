package codegen

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// GeneratorData holds the complete design metadata extracted from Goa DSL
	// expressions and transformed into a template-friendly structure. It groups
	// all declared agents by their owning Goa services and provides the root
	// import path for generated code.
	//
	// Instances are created by buildGeneratorData during the Goa code generation
	// phase by scanning AgentExpr expressions from the DSL root. The structure is
	// immutable after construction and safe for concurrent template rendering.
	// Services are sorted alphabetically by name, and agents within each service
	// are also sorted for deterministic output.
	//
	// If no agents are declared in the design, Services will be empty and the
	// Generate function will skip code generation entirely.
	GeneratorData struct {
		// Genpkg is the Go import path to the generated code root (typically `<module>/gen`).
		Genpkg string
		// Services bundles the agent metadata grouped by Goa service.
		Services []*ServiceAgentsData
	}

	// ServiceAgentsData groups all agents declared under a single Goa service.
	// It bundles the service's core metadata (from Goa's service generators) with
	// the agent-specific metadata needed for code generation.
	//
	// Each ServiceAgentsData corresponds to one Goa service that declares at least
	// one agent. The structure is created during buildGeneratorData and is immutable
	// after construction. Agents are sorted alphabetically by name for deterministic
	// generation.
	//
	// Templates use this to generate service-scoped agent packages and to access
	// both service-level types (via Service.UserTypes) and agent-level artifacts.
	ServiceAgentsData struct {
		// Service contains the Goa service metadata from the core generators.
		Service *service.Data
		// Agents lists each agent declared within the service DSL.
		Agents []*AgentData
		// HasMCP indicates whether any agent under this service references external
		// MCP toolsets. Example-phase generators use this to decide whether to emit
		// MCP caller scaffolding/imports.
		HasMCP bool
	}

	// AgentData transforms a single AgentExpr DSL declaration into a template-ready
	// structure with all derived names, paths, and metadata needed for code generation.
	// It bridges the gap between design-time expressions (AgentExpr) and generated
	// runtime artifacts (workflow handlers, activity definitions, configuration types).
	//
	// The structure maintains references to both the original DSL expression (Expr) and
	// the transformed Goa service metadata (Service), enabling templates to access both
	// design-time and codegen-time information.
	//
	// Name variants serve different purposes:
	//   - Name: original DSL identifier (e.g., "chat_assistant")
	//   - GoName: exported Go identifier (e.g., "ChatAssistant")
	//   - Slug: filesystem-safe token (e.g., "chat_assistant")
	//   - ID: service-scoped identifier (e.g., "assistant_service.chat_assistant")
	//
	// Toolsets are categorized by usage:
	//   - UsedToolsets: tools this agent calls (agent-as-consumer)
	//   - ExportedToolsets: tools this agent provides (agent-as-tool)
	//   - AllToolsets: convenience union of both for iteration
	//
	// All slices are sorted for deterministic code generation. Tools flattens all
	// tools across toolsets for easy lookup and global iteration.
	//
	// Created by newAgentData during buildGeneratorData; immutable after construction.
	AgentData struct {
		// Genpkg stores the Go import path to the generated root (module/gen).
		Genpkg string
		// Name is the DSL-provided identifier.
		Name string
		// Description is the DSL-provided description for the agent.
		Description string
		// ID is the service-scoped identifier (`service.agent`).
		ID string
		// GoName is the exported Go identifier derived from Name.
		GoName string
		// Slug is a filesystem-safe version of the agent name.
		Slug string
		// Service is the Goa service metadata this agent belongs to.
		Service *service.Data
		// PackageName is the Go package name for generated agent code.
		PackageName string
		// PathName mirrors PackageName but is tailored for directory names.
		PathName string
		// Dir is the output directory for the agent package.
		Dir string
		// ImportPath is the full Go import path to the agent package.
		ImportPath string

		// ConfigType names the generated configuration struct.
		ConfigType string
		// StructName names the generated agent wrapper type.
		StructName string
		// WorkflowFunc is the workflow entry point name.
		WorkflowFunc string
		// WorkflowDefinitionVar stores the exported workflow definition variable name.
		WorkflowDefinitionVar string
		// WorkflowName is the logical workflow identifier registered with the engine.
		WorkflowName string
		// WorkflowQueue is the default workflow task queue.
		WorkflowQueue string
		// ToolSpecsPackage is the package name housing generated tool specs.
		ToolSpecsPackage string
		// ToolSpecsImportPath is the import path for the tool specs package.
		ToolSpecsImportPath string
		// ToolSpecsDir is the filesystem directory containing the tool specs files.
		ToolSpecsDir string
		// RunPolicy captures caps/time budget settings.
		RunPolicy RunPolicyData
		// UsedToolsets lists toolsets referenced by `Uses`.
		UsedToolsets []*ToolsetData
		// ExportedToolsets lists toolsets declared under `Exports`.
		ExportedToolsets []*ToolsetData
		// AllToolsets concatenates consumed and exported toolsets for convenience.
		AllToolsets []*ToolsetData
		// MethodBackedToolsets lists toolsets that contain at least one tool bound
		// to a Goa service method via BindTo.
		MethodBackedToolsets []*ToolsetData
		// Tools flattens every tool declared across the agent's toolsets.
		Tools []*ToolData
		// MCPToolsets lists the distinct external MCP toolsets referenced via UseMCPToolset.
		// Each entry captures the helper import/path information required to register
		// the suite with the runtime at agent registration time.
		MCPToolsets []*MCPToolsetMeta
		// Runtime captures derived workflow/activity data used by templates.
		Runtime RuntimeData
	}

	// RunPolicyData represents the runtime execution constraints and resource limits
	// configured for an agent via the DSL RunPolicy expression. It defines per-run
	// boundaries for execution time, tool usage, and interrupt handling.
	//
	// The policy is transformed from RunPolicyExpr during agent data construction
	// (newRunPolicyData) and is embedded in AgentData for template access. Generated
	// code uses these values to configure the runtime's policy enforcement (see
	// agents/runtime/policy package).
	//
	// Zero-valued fields indicate no limit: TimeBudget = 0 means unlimited execution
	// time, and zero caps mean no resource restrictions. Templates use these values
	// to generate registration and validation logic.
	//
	// The policy is enforced by the agent runtime during workflow execution, not at
	// code generation time.
	RunPolicyData struct {
		// TimeBudget is the maximum wall-clock time allocated to the run.
		TimeBudget time.Duration
		// InterruptsAllowed indicates whether human interrupts are honored.
		InterruptsAllowed bool
		// Caps enumerates max tool-call limits.
		Caps CapsData
	}

	// CapsData captures per-run resource limits that restrict agent tool usage.
	// It prevents runaway execution and excessive resource consumption by capping
	// the number of tool invocations and consecutive failures allowed within a
	// single agent run.
	//
	// Zero values indicate no cap is enforced. These limits are transformed from
	// CapsExpr during policy data construction and are enforced by the runtime
	// policy engine, not at generation time.
	//
	// The runtime increments counters for each tool call and failure, terminating
	// the agent run with an error if caps are exceeded.
	CapsData struct {
		// MaxToolCalls caps the number of tool invocations per run (0 = unlimited).
		MaxToolCalls int
		// MaxConsecutiveFailedToolCalls stops execution after N consecutive failures (0 = unlimited).
		MaxConsecutiveFailedToolCalls int
	}

	// ToolsetData captures metadata about a toolset and its relationship to agents
	// and services. Toolsets can be declared inline within an agent's Uses/Exports
	// blocks, or can reference external providers (e.g., MCP servers declared in
	// other services).
	//
	// Service attribution:
	//   - ServiceName: the consuming agent's service (where Uses/Exports appears)
	//   - SourceServiceName: the service that declared the toolset/provider
	//   - SourceService: Goa service metadata for the source (enables type imports)
	//
	// For inline toolsets, ServiceName == SourceServiceName. For external toolsets
	// (MCP providers), they differ: ServiceName is the agent's service, while
	// SourceServiceName/SourceService point to the service declaring the MCP server.
	//
	// Kind determines code generation strategy:
	//   - ToolsetKindUsed: tools consumed by the agent (generates executors)
	//   - ToolsetKindExported: tools provided by the agent (generates adapters + helpers)
	//   - ToolsetKindGlobal: shared toolsets not bound to an agent
	//
	// The AgentTools* fields are only populated when Kind == ToolsetKindExported,
	// as these generate helper functions for invoking the agent-as-tool pattern.
	//
	// External toolsets have their Tools populated by populateMCPToolset rather than
	// from DSL expressions, reflecting the runtime discovery of MCP capabilities.
	//
	// Created by newToolsetData during agent data construction; immutable afterward.
	ToolsetData struct {
		// Expr is the original toolset expression.
		Expr *agentsExpr.ToolsetExpr
		// Name is the DSL-specified toolset identifier.
		Name string
		// Description captures the DSL description for docs/registries.
		Description string
		// Tags attaches suite-level tags used by policy/filtering at registration time.
		Tags []string
		// ServiceName identifies the owning Goa service (blank for global toolsets).
		ServiceName string
		// SourceServiceName maintains the original service that declared the toolset
		// (used for external providers such as MCP). Defaults to ServiceName.
		SourceServiceName string
		// SourceService points to the Goa service that originally declared the toolset.
		// For external providers (e.g., MCP) this differs from the consuming agent service.
		SourceService *service.Data
		// SourceServiceImports caches the user type imports for the source service. This
		// enables tool spec generation to reference external service types.
		SourceServiceImports map[string]*codegen.ImportSpec
		// QualifiedName is the service-scoped identifier (`service.toolset`).
		QualifiedName string
		// TaskQueue is the derived Temporal/engine queue for tool execution.
		TaskQueue string
		// Kind states whether this toolset is consumed, exported, or global.
		Kind ToolsetKind
		// Agent references the owning agent when one exists.
		Agent *AgentData
		// PathName is the filesystem-safe slug for the toolset.
		PathName string
		// PackageName is the Go package name for generated helper code.
		PackageName string
		// PackageImportPath is the Go import path to that helper package.
		PackageImportPath string
		// Dir is the filesystem target for toolset-specific files.
		Dir string
		// SpecsPackageName is the Go package name for per-toolset specs/types/codecs.
		SpecsPackageName string
		// SpecsImportPath is the import path for per-toolset specs/types/codecs.
		SpecsImportPath string
		// SpecsDir is the filesystem directory for per-toolset specs/types/codecs.
		SpecsDir string
		// AgentToolsPackage is the package name for exported tool helpers.
		AgentToolsPackage string
		// AgentToolsDir is the filesystem directory for exported tool helpers.
		AgentToolsDir string
		// Tools lists the tools defined inside the toolset.
		Tools []*ToolData
		// MCP describes the external MCP helper metadata when this toolset references
		// an MCP suite.
		MCP *MCPToolsetMeta
		// NeedsAdapter indicates whether any method-backed tool in this toolset
		// requires an adapter for payload or result mapping (i.e., the tool
		// payload/result do not alias the bound method types). When false, the
		// generated service toolset can bypass adapters entirely.
		NeedsAdapter bool
	}

	// MCPToolsetMeta captures the information required to register an external MCP
	// suite with the runtime from within an agent package. It records the helper
	// import path, alias, and function name emitted by the MCP plugin as well as
	// the canonical toolset identifier.
	MCPToolsetMeta struct {
		// ServiceName is the Goa service that declared the MCP suite.
		ServiceName string
		// SuiteName is the MCP suite identifier provided in the service DSL.
		SuiteName string
		// QualifiedName is the fully qualified toolset identifier (service.suite).
		QualifiedName string
		// HelperImportPath is the Go import path for the generated register helper.
		HelperImportPath string
		// HelperAlias is the import alias used inside generated agent code.
		HelperAlias string
		// HelperFunc is the helper function name (Register<Service><Suite>Toolset).
		HelperFunc string
		// ConstName is the generated Go identifier for the toolset ID constant.
		ConstName string
	}

	// ToolData captures metadata about an individual tool, including its DSL
	// declaration, type information, and code generation directives. Tools are
	// the atomic units of agent capability, representing functions that can be
	// invoked by the agent runtime or by other agents.
	//
	// Name variants serve different purposes:
	//   - Name: original DSL identifier (e.g., "analyze_data")
	//   - ConstName: Go constant name for referencing in code (e.g., "AnalyzeData")
	//   - QualifiedName: globally unique identifier (e.g., "service.toolset.tool")
	//   - DisplayName: human-readable label (e.g., "toolset.tool")
	//
	// Tool implementation strategies:
	//   - IsMethodBacked=true: tool dispatches to a Goa service method (client call)
	//   - IsExportedByAgent=true: tool invokes another agent (agent-as-tool pattern)
	//   - Both false: tool requires a custom executor implementation
	//
	// Args and Return are Goa attribute expressions that define the tool's input/output
	// schema. Code generation uses these to create type-safe marshalers, validators,
	// and specification builders for the runtime tool registry.
	//
	// Created by newToolData during toolset data construction; immutable afterward.
	ToolData struct {
		// Name is the DSL-provided tool identifier.
		Name string
		// ConstName is a Go-safe exported identifier for referencing this tool
		// in generated code (e.g., AnalyzeData for tool name "analyze_data").
		ConstName string
		// Description is the DSL description for docs and planners.
		Description string
		// QualifiedName is the service/toolset scoped identifier (`service.toolset.tool`).
		QualifiedName string
		// DisplayName is the human-readable label (`toolset.tool`).
		DisplayName string
		// Tags holds optional metadata tags supplied in the DSL.
		Tags []string
		// Args is the Goa attribute describing the tool payload.
		Args *goaexpr.AttributeExpr
		// Return is the Goa attribute describing the tool result.
		Return *goaexpr.AttributeExpr
		// MethodPayloadAttr is the Goa attribute for the bound service payload
		// (resolved user type). Used to generate default payload adapters.
		MethodPayloadAttr *goaexpr.AttributeExpr

		// MethodResultAttr is the Goa attribute for the bound service result
		// (resolved user type). Used to generate default result adapters and to
		// materialize specs when the tool Return is not specified.
		MethodResultAttr *goaexpr.AttributeExpr

		// Toolset links back to the parent toolset metadata.
		Toolset *ToolsetData
		// HasResult indicates whether the tool produces a result value. It is true
		// when either the tool Return is specified in the DSL or the bound service
		// method defines a non-empty result type.
		HasResult bool
		// IsExportedByAgent indicates this tool is exported by an agent (agent-as-tool).
		// Set to true when the tool's toolset is in an agent's Exports block.
		IsExportedByAgent bool
		// ExportingAgentID is the fully qualified agent identifier (e.g., "service.agent_name").
		// Only set when IsExportedByAgent is true.
		ExportingAgentID string

		// IsMethodBacked indicates this tool is bound to a Goa service method via
		// the DSL Method() function. When true, code generation emits client dispatch
		// logic that adapts tool arguments to the method's payload type and maps the
		// method result back to the tool's return type.
		//
		// This enables tools to be implemented by calling existing service endpoints
		// rather than requiring custom executor functions. The binding is established
		// during DSL evaluation (see ToolExpr.Method in expr/toolset.go).
		//
		// Set by newToolData based on expr.Method != nil.
		IsMethodBacked bool
		// MethodGoName is the Goified method name from the bound service method,
		// used to reference the generated client endpoint. For example, if Method()
		// binds to a method named "get_device", MethodGoName will be "GetDevice",
		// matching the generated client function name.
		//
		// Only populated when IsMethodBacked is true.
		MethodGoName string
		// MethodPayloadTypeName is the exact payload type name for the bound method
		// as generated by Goa (e.g., ADGetAlarmsPayload). Empty when not bound.
		MethodPayloadTypeName string
		// MethodResultTypeName is the exact result type name for the bound method
		// as generated by Goa (e.g., ADGetAlarmsResult). Empty when not bound.
		MethodResultTypeName string
		// MethodPayloadTypeRef is the fully-qualified reference for the bound method payload type.
		MethodPayloadTypeRef string
		// MethodResultTypeRef is the fully-qualified reference for the bound method result type.
		MethodResultTypeRef string
		// MethodPayloadLoc is the Goa location for the payload user type when specified via
		// Meta("struct:pkg:path", ...). Nil when the payload is local to the service package
		// or is not a user type.
		MethodPayloadLoc *codegen.Location
		// MethodResultLoc is the Goa location for the result user type when specified via
		// Meta("struct:pkg:path", ...). Nil when the result is local to the service package
		// or is not a user type.
		MethodResultLoc *codegen.Location

		// PayloadAliasesMethod is true when the tool payload user type matches
		// the bound method payload user type or any of its Extend bases. In this
		// case the generated code bypasses the payload adapter and forwards the
		// tool payload directly to the client.
		PayloadAliasesMethod bool
		// ResultAliasesMethod is true when the tool result user type matches the
		// bound method result user type or any of its Extend bases. In this case the
		// generated code bypasses the result adapter and returns the service result
		// directly as the tool result.
		ResultAliasesMethod bool
	}

	// RuntimeData contains the workflow and activity artifacts generated for an agent,
	// mapping DSL-level agent declarations to engine-level registrations. Each agent
	// produces exactly one workflow handler and multiple activity handlers (planner,
	// resume, tool execution).
	//
	// The Workflow field describes the generated workflow entry point (see workflow.go.tpl),
	// which delegates to the runtime's ExecuteWorkflow logic. Activities enumerates all
	// activity handlers to be registered with the engine before workers start.
	//
	// Specialized activity pointers (ExecuteTool, PlanActivity, ResumeActivity) provide
	// direct access to the standard activities for templates that need to reference them
	// by name or queue. These point into the Activities slice and are set during agent
	// initialization (see newAgentData).
	//
	// All activities include retry policies and timeouts derived from DSL policy expressions
	// or codegen defaults (e.g., defaultPlannerActivityTimeout).
	RuntimeData struct {
		// Workflow describes the generated workflow definition.
		Workflow WorkflowArtifact
		// Activities enumerates the activity handlers to register.
		Activities []ActivityArtifact
		// ExecuteTool references the activity used to run tools via the workflow engine.
		ExecuteTool *ActivityArtifact
		// PlanActivity references the activity wrapping PlanStart.
		PlanActivity *ActivityArtifact
		// ResumeActivity references the activity wrapping PlanResume.
		ResumeActivity *ActivityArtifact
	}

	// WorkflowArtifact describes the generated workflow entry point for an agent.
	// Each agent produces exactly one workflow function that serves as the durable
	// execution entry point.
	//
	// The workflow function (FuncName) is a thin wrapper that validates input and
	// delegates to the runtime's ExecuteWorkflow logic (see workflow.go.tpl). The
	// definition variable (DefinitionVar) is exported from the generated agent package
	// and used during engine registration.
	//
	// Name is the logical identifier registered with the workflow engine (e.g.,
	// "assistant_service.chat_assistant.workflow"), which must be unique across all
	// agents in the system. Queue is the default Temporal task queue for workflow
	// tasks; workers subscribe to this queue to process workflow executions.
	//
	// Generated by newAgentData during agent data construction.
	WorkflowArtifact struct {
		// FuncName is the Go function name implementing the workflow handler.
		FuncName string
		// DefinitionVar is the exported variable name holding the workflow definition.
		DefinitionVar string
		// Name is the logical workflow identifier registered with the engine.
		Name string
		// Queue is the default workflow task queue.
		Queue string
	}

	// ActivityArtifact captures metadata about a generated activity handler, including
	// its identity, execution constraints, and implementation kind. Activities are
	// short-lived, stateless tasks invoked from workflow handlers.
	//
	// Each agent generates three standard activities:
	//   - Plan (ActivityKindPlan): wraps Planner.Start to initialize the planning loop
	//   - Resume (ActivityKindResume): wraps Planner.Resume to continue after tool execution
	//   - ExecuteTool (ActivityKindExecuteTool): generic tool executor shared across agents
	//
	// The Kind field determines the activity's behavior. Templates use Kind to generate
	// appropriate handler logic (see activities.go.tpl). FuncName and DefinitionVar
	// follow Goa naming conventions and are used during registration and invocation.
	//
	// RetryPolicy and Timeout control resilience: Plan/Resume activities use
	// plannerActivityRetryPolicy() with a 2-minute timeout, while ExecuteTool has
	// no default timeout. Queue overrides the default activity queue when set; empty
	// means inherit from the workflow's queue.
	//
	// Generated by newActivity during RuntimeData initialization.
	ActivityArtifact struct {
		// Name is the logical activity identifier registered with the engine.
		Name string
		// FuncName is the Go function implementing the activity handler.
		FuncName string
		// DefinitionVar is the exported variable name storing the activity definition.
		DefinitionVar string
		// Queue overrides the default queue if provided.
		Queue string
		// RetryPolicy describes the activity retry behavior.
		RetryPolicy engine.RetryPolicy
		// Timeout bounds the execution time for the activity including retries.
		Timeout time.Duration
		// Kind categorizes the activity so templates can render concrete logic.
		Kind ActivityKind
	}

	// ToolsetKind categorizes how a toolset relates to an agent, which determines
	// the code generation strategy and registration approach.
	//
	// The kind is set during toolset collection (collectToolsets) based on whether
	// the toolset appears in the agent's Uses block (consumed) or Exports block
	// (provided). Global toolsets declared at the top level have no owning agent.
	//
	// Templates use Kind to decide:
	//   - Used: generate tool executors and runtime registration
	//   - Exported: generate agent-tool adapters and invocation helpers
	//   - Global: generate standalone specs without agent wiring
	ToolsetKind string

	// ActivityKind identifies the semantic purpose of an activity handler, allowing
	// templates to generate appropriate activity logic. The kind is set during
	// RuntimeData initialization (newActivity) and determines retry policies, timeouts,
	// and queue assignments.
	//
	// Every agent gets three standard activities:
	//   - Plan: invokes PlanStart to initialize the planning loop
	//   - Resume: invokes PlanResume to continue after tool execution
	//   - ExecuteTool: generic tool executor shared across all agents
	ActivityKind string
)

const (
	// ToolsetKindUsed labels toolsets in an agent's Uses block. The agent
	// consumes these tools by invoking them through the runtime executor.
	// Code generation creates specs and registers executors.
	ToolsetKindUsed ToolsetKind = "used"

	// ToolsetKindExported labels toolsets in an agent's Exports block. The
	// agent provides these tools to other agents (agent-as-tool pattern).
	// Code generation creates adapters that launch agent workflows when tools
	// are invoked, plus helper functions in the agenttools package.
	ToolsetKindExported ToolsetKind = "exported"

	// ToolsetKindGlobal labels toolsets declared at the design's top level,
	// not bound to any agent. These represent shared capabilities or external
	// providers (MCP servers) available across all services.
	ToolsetKindGlobal ToolsetKind = "global"

	// ActivityKindPlan identifies the planner initialization activity. It wraps
	// the Planner.Start call with a 2-minute timeout and exponential retry policy.
	ActivityKindPlan ActivityKind = "plan"

	// ActivityKindResume identifies the planner continuation activity. It wraps
	// Planner.Resume with the same retry/timeout as Plan.
	ActivityKindResume ActivityKind = "resume"

	// ActivityKindExecuteTool identifies the generic tool execution activity.
	// Unlike Plan/Resume, it has no default timeout and runs on a global queue
	// (empty queue string) to share capacity across all agents.
	ActivityKindExecuteTool ActivityKind = "execute_tool"
)

const (
	// defaultPlannerActivityTimeout bounds planner activity execution time
	// (both Plan and Resume activities). This prevents runaway LLM calls and
	// ensures the workflow engine can reclaim resources. Activities that exceed
	// this timeout fail after retries and cause the workflow to error.
	//
	// 2 minutes accommodates typical LLM response times with retries while
	// preventing indefinite hangs.
	defaultPlannerActivityTimeout = 2 * time.Minute
)

// buildGeneratorData inspects the Goa and agents roots to build template-friendly data.
func buildGeneratorData(genpkg string, roots []eval.Root) (*GeneratorData, error) {
	var (
		goaRoot    *goaexpr.RootExpr
		agentsRoot = agentsExpr.Root
	)
	for _, root := range roots {
		if goaRoot, _ = root.(*goaexpr.RootExpr); goaRoot != nil {
			break
		}
	}

	servicesData := service.NewServicesData(goaRoot)
	serviceMap := make(map[string]*ServiceAgentsData)

	for _, agentExpr := range agentsRoot.Agents {
		svcData := servicesData.Get(agentExpr.Service.Name)
		if svcData == nil {
			return nil, fmt.Errorf("service %q not found for agent %q", agentExpr.Service.Name, agentExpr.Name)
		}
		svcAgents := serviceMap[svcData.Name]
		if svcAgents == nil {
			svcAgents = &ServiceAgentsData{Service: svcData}
			serviceMap[svcData.Name] = svcAgents
		}
		agentData := newAgentData(genpkg, svcData, agentExpr, servicesData)
		svcAgents.Agents = append(svcAgents.Agents, agentData)
		if len(agentData.MCPToolsets) > 0 {
			svcAgents.HasMCP = true
		}
	}

	services := make([]*ServiceAgentsData, 0, len(serviceMap))
	for _, svc := range serviceMap {
		services = append(services, svc)
	}
	slices.SortFunc(services, func(a, b *ServiceAgentsData) int {
		return strings.Compare(a.Service.Name, b.Service.Name)
	})
	for _, svc := range services {
		slices.SortFunc(svc.Agents, func(a, b *AgentData) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	return &GeneratorData{Genpkg: genpkg, Services: services}, nil
}

func newAgentData(
	genpkg string,
	svc *service.Data,
	agentExpr *agentsExpr.AgentExpr,
	servicesData *service.ServicesData,
) *AgentData {
	goName := codegen.Goify(agentExpr.Name, true)
	configType := goName + "AgentConfig"
	structName := goName + "Agent"

	agentSlug := sanitizeToken(agentExpr.Name, "agent")
	agentDir := filepath.Join(codegen.Gendir, svc.PathName, "agents", agentSlug)
	agentImport := path.Join(genpkg, svc.PathName, "agents", agentSlug)

	workflowFunc := goName + "Workflow"
	workflowVar := goName + "WorkflowDefinition"
	workflowName := joinIdentifier(svc.Name, agentExpr.Name, "workflow")
	workflowQueue := queueName(svc.PathName, agentSlug, "workflow")

	agent := &AgentData{
		Genpkg:                genpkg,
		Name:                  agentExpr.Name,
		Description:           agentExpr.Description,
		ID:                    joinIdentifier(svc.Name, agentExpr.Name),
		GoName:                goName,
		Slug:                  agentSlug,
		Service:               svc,
		PackageName:           agentSlug,
		PathName:              agentSlug,
		Dir:                   agentDir,
		ImportPath:            agentImport,
		ConfigType:            configType,
		StructName:            structName,
		WorkflowFunc:          workflowFunc,
		WorkflowDefinitionVar: workflowVar,
		WorkflowName:          workflowName,
		WorkflowQueue:         workflowQueue,
		// Aggregated specs package for agent-level imports
		ToolSpecsPackage:    "specs",
		ToolSpecsImportPath: path.Join(agentImport, "specs"),
		ToolSpecsDir:        filepath.Join(agentDir, "specs"),
	}

	agent.Runtime = RuntimeData{
		Workflow: WorkflowArtifact{
			FuncName:      workflowFunc,
			DefinitionVar: workflowVar,
			Name:          workflowName,
			Queue:         workflowQueue,
		},
	}
	// Register default activities (plan/resume) scoped to the workflow queue.
	agent.Runtime.Activities = append(agent.Runtime.Activities,
		newActivity(agent, ActivityKindPlan, "Plan", workflowQueue),
		newActivity(agent, ActivityKindResume, "Resume", workflowQueue),
		newActivity(agent, ActivityKindExecuteTool, "ExecuteTool", ""),
	)
	for i := range agent.Runtime.Activities {
		act := agent.Runtime.Activities[i]
		switch act.Kind {
		case ActivityKindExecuteTool:
			if agent.Runtime.ExecuteTool == nil {
				agent.Runtime.ExecuteTool = &agent.Runtime.Activities[i]
			}
		case ActivityKindPlan:
			if agent.Runtime.PlanActivity == nil {
				agent.Runtime.PlanActivity = &agent.Runtime.Activities[i]
			}
		case ActivityKindResume:
			if agent.Runtime.ResumeActivity == nil {
				agent.Runtime.ResumeActivity = &agent.Runtime.Activities[i]
			}
		}
	}

	agent.RunPolicy = newRunPolicyData(agentExpr.RunPolicy)
	agent.UsedToolsets = collectToolsets(agent, agentExpr.Used, ToolsetKindUsed, servicesData)
	agent.ExportedToolsets = collectToolsets(agent, agentExpr.Exported, ToolsetKindExported, servicesData)
	agent.AllToolsets = append([]*ToolsetData{}, agent.UsedToolsets...)
	agent.AllToolsets = append(agent.AllToolsets, agent.ExportedToolsets...)

	// Compute method-backed toolsets for convenience in templates.
	for _, ts := range agent.AllToolsets {
		has := false
		for _, t := range ts.Tools {
			if t.IsMethodBacked {
				has = true
				break
			}
		}
		if has {
			agent.MethodBackedToolsets = append(agent.MethodBackedToolsets, ts)
		}
	}

	for _, ts := range agent.AllToolsets {
		agent.Tools = append(agent.Tools, ts.Tools...)
	}
	slices.SortFunc(agent.Tools, func(a, b *ToolData) int {
		return strings.Compare(a.QualifiedName, b.QualifiedName)
	})

	if len(agent.AllToolsets) > 0 {
		mcpMap := make(map[string]*MCPToolsetMeta)
		for _, ts := range agent.AllToolsets {
			if ts.MCP == nil {
				continue
			}
			if _, ok := mcpMap[ts.MCP.QualifiedName]; !ok {
				mcpMap[ts.MCP.QualifiedName] = ts.MCP
			}
		}
		for _, meta := range mcpMap {
			agent.MCPToolsets = append(agent.MCPToolsets, meta)
		}
		slices.SortFunc(agent.MCPToolsets, func(a, b *MCPToolsetMeta) int {
			return strings.Compare(a.QualifiedName, b.QualifiedName)
		})
	}

	return agent
}

func newRunPolicyData(expr *agentsExpr.RunPolicyExpr) RunPolicyData {
	if expr == nil {
		return RunPolicyData{}
	}
	rp := RunPolicyData{
		TimeBudget:        expr.TimeBudget,
		InterruptsAllowed: expr.InterruptsAllowed,
	}
	if expr.DefaultCaps != nil {
		rp.Caps = CapsData{
			MaxToolCalls:                  expr.DefaultCaps.MaxToolCalls,
			MaxConsecutiveFailedToolCalls: expr.DefaultCaps.MaxConsecutiveFailedToolCall,
		}
	}
	return rp
}

func collectToolsets(
	agent *AgentData,
	group *agentsExpr.ToolsetGroupExpr,
	kind ToolsetKind,
	servicesData *service.ServicesData,
) []*ToolsetData {
	if group == nil || len(group.Toolsets) == 0 {
		return nil
	}
	toolsets := make([]*ToolsetData, 0, len(group.Toolsets))
	for _, tsExpr := range group.Toolsets {
		toolsets = append(toolsets, newToolsetData(agent, tsExpr, kind, servicesData))
	}
	slices.SortFunc(toolsets, func(a, b *ToolsetData) int {
		return strings.Compare(a.Name, b.Name)
	})
	return toolsets
}

func newToolsetData(
	agent *AgentData,
	expr *agentsExpr.ToolsetExpr,
	kind ToolsetKind,
	servicesData *service.ServicesData,
) *ToolsetData {
	toolsetSlug := sanitizeToken(expr.Name, "toolset")
	serviceName := agent.Service.Name
	queue := queueName(agent.Service.PathName, agent.Slug, toolsetSlug, "tasks")
	qualifiedName := fmt.Sprintf("%s.%s", serviceName, expr.Name)
	sourceService := agent.Service

	// If this is an MCP toolset, use the service name from the MCP service.
	if expr.External && servicesData != nil && expr.MCPService != "" {
		if svc := servicesData.Get(expr.MCPService); svc != nil {
			sourceService = svc
		}
	}
	// If this is a method-backed toolset, prefer the service referenced by the
	// first bound method (BindTo) when present.
	if !expr.External && servicesData != nil && len(expr.Tools) > 0 {
		if svcName := expr.Tools[0].BoundServiceName(); svcName != "" {
			if svc := servicesData.Get(svcName); svc != nil {
				sourceService = svc
			}
		}
	}
	sourceServiceName := serviceName
	if sourceService != nil && sourceService.Name != "" {
		sourceServiceName = sourceService.Name
	} else if expr.MCPService != "" {
		sourceServiceName = expr.MCPService
	}
	var imports map[string]*codegen.ImportSpec
	if sourceService != nil {
		imports = buildServiceImportMap(sourceService)
	}

	ts := &ToolsetData{
		Expr:                 expr,
		Name:                 expr.Name,
		Description:          expr.Description,
		Tags:                 slices.Clone(expr.Tags),
		ServiceName:          serviceName,
		SourceServiceName:    sourceServiceName,
		SourceService:        sourceService,
		QualifiedName:        qualifiedName,
		TaskQueue:            queue,
		Kind:                 kind,
		Agent:                agent,
		PathName:             toolsetSlug,
		PackageName:          toolsetSlug,
		SourceServiceImports: imports,
		PackageImportPath:    path.Join(agent.ImportPath, toolsetSlug),
		Dir:                  filepath.Join(agent.Dir, toolsetSlug),
		SpecsPackageName:     toolsetSlug,
		SpecsImportPath:      path.Join(agent.ImportPath, "specs", toolsetSlug),
		SpecsDir:             filepath.Join(agent.Dir, "specs", toolsetSlug),
	}

	if kind == ToolsetKindExported {
		ts.AgentToolsPackage = toolsetSlug
		ts.AgentToolsDir = filepath.Join(agent.Dir, "agenttools", toolsetSlug)
	}

	if expr.External {
		if expr.MCPSuite != "" {
			ts.Name = expr.MCPSuite
		}
		if expr.MCPService != "" {
			ts.SourceServiceName = expr.MCPService
			ts.QualifiedName = fmt.Sprintf("%s.%s", expr.MCPService, expr.MCPSuite)
		}
		helperPkg := "mcp_" + codegen.SnakeCase(expr.MCPService)
		helperImport := path.Join(agent.Genpkg, helperPkg)
		helperAlias := "mcp" + codegen.Goify(expr.MCPService, false)
		helperFunc := fmt.Sprintf("Register%s%sToolset",
			codegen.Goify(expr.MCPService, true), codegen.Goify(expr.MCPSuite, true))
		constName := fmt.Sprintf("%s%s%sToolsetID",
			agent.GoName, codegen.Goify(expr.MCPService, true), codegen.Goify(expr.MCPSuite, true))
		ts.MCP = &MCPToolsetMeta{
			ServiceName:      expr.MCPService,
			SuiteName:        expr.MCPSuite,
			QualifiedName:    ts.QualifiedName,
			HelperImportPath: helperImport,
			HelperAlias:      helperAlias,
			HelperFunc:       helperFunc,
			ConstName:        constName,
		}
		// Populate from Goa-backed MCP if available; otherwise keep/derive tools
		// from inline declarations (custom external MCP).
		found := populateMCPToolset(ts)
		if !found && len(expr.Tools) > 0 {
			for _, toolExpr := range expr.Tools {
				tool := newToolData(ts, toolExpr, servicesData)
				ts.Tools = append(ts.Tools, tool)
			}
			slices.SortFunc(ts.Tools, func(a, b *ToolData) int {
				return strings.Compare(a.Name, b.Name)
			})
		}
	} else {
		for _, toolExpr := range expr.Tools {
			tool := newToolData(ts, toolExpr, servicesData)
			ts.Tools = append(ts.Tools, tool)
		}
		slices.SortFunc(ts.Tools, func(a, b *ToolData) int {
			return strings.Compare(a.Name, b.Name)
		})
		// Any method-backed tool requires an adapter; no bypass logic.
		for _, t := range ts.Tools {
			if t.IsMethodBacked {
				ts.NeedsAdapter = true
				break
			}
		}
	}

	return ts
}

func buildServiceImportMap(svc *service.Data) map[string]*codegen.ImportSpec {
	if len(svc.UserTypeImports) == 0 {
		return nil
	}
	imports := make(map[string]*codegen.ImportSpec)
	for _, im := range svc.UserTypeImports {
		if im == nil || im.Path == "" {
			continue
		}
		alias := im.Name
		if alias == "" {
			alias = path.Base(im.Path)
		}
		imports[alias] = im
	}
	return imports
}

func newToolData(ts *ToolsetData, expr *agentsExpr.ToolExpr, servicesData *service.ServicesData) *ToolData {
	qualified := expr.Name
	display := expr.Name
	if ts != nil {
		if ts.QualifiedName != "" {
			qualified = fmt.Sprintf("%s.%s", ts.QualifiedName, expr.Name)
		} else if ts.Name != "" {
			qualified = fmt.Sprintf("%s.%s", ts.Name, expr.Name)
		}
		if ts.Name != "" {
			display = fmt.Sprintf("%s.%s", ts.Name, expr.Name)
		}
	}

	// Check if this tool is exported by an agent (agent-as-tool pattern)
	isExported := ts != nil && ts.Kind == ToolsetKindExported && ts.Agent != nil
	var exportingAgentID string
	if isExported {
		exportingAgentID = ts.Agent.ID
	}

	tool := &ToolData{
		Name:              expr.Name,
		ConstName:         codegen.Goify(expr.Name, true),
		Description:       expr.Description,
		QualifiedName:     qualified,
		DisplayName:       display,
		Tags:              slices.Clone(expr.Tags),
		Args:              expr.Args,
		Return:            expr.Return,
		Toolset:           ts,
		IsExportedByAgent: isExported,
		ExportingAgentID:  exportingAgentID,
	}
	if expr.Method == nil {
		return tool
	}
	tool.IsMethodBacked = true
	tool.MethodGoName = codegen.Goify(expr.Method.Name, true)
	// Populate exact payload/result type names using Goa service metadata.
	if servicesData == nil || ts == nil || ts.SourceService == nil {
		return tool
	}
	sd := servicesData.Get(ts.SourceService.Name)
	if sd == nil {
		return tool
	}
	for _, md := range sd.Methods {
		if codegen.Goify(md.Name, true) != tool.MethodGoName {
			continue
		}
		tool.MethodPayloadTypeName = md.Payload
		tool.MethodResultTypeName = md.Result

		// Resolve fully-qualified type references using Goa name scope.
		// If the payload/result user types do not specify a custom package
		// (no struct:pkg:path), Goa leaves PayloadRef/ResultRef unqualified
		// because the service code is generated in the same package.
		// Our generated code may live in a different package; qualify
		// local types with the source service package using the service
		// name scope to avoid manual string surgery.
		me := mustFindMethodExpr(servicesData.Root, sd.Name, md.Name)
		if me != nil && me.Payload.Type != goaexpr.Empty {
			// Expose attribute for template default adapter generation.
			tool.MethodPayloadAttr = me.Payload
			if md.PayloadLoc != nil && md.PayloadLoc.PackageName() != "" {
				tool.MethodPayloadTypeRef = md.PayloadRef
			} else {
				if sd.Scope != nil {
					tool.MethodPayloadTypeRef = sd.Scope.GoFullTypeRef(me.Payload, sd.PkgName)
				}
				if tool.MethodPayloadTypeRef == "" && md.Payload != "" && sd.PkgName != "" {
					// Fallback to pkg-qualified type name if scope resolution is unavailable.
					tool.MethodPayloadTypeRef = "*" + sd.PkgName + "." + md.Payload
				}
			}
		}
		if me != nil && me.Result.Type != goaexpr.Empty {
			tool.MethodResultAttr = me.Result
			if md.ResultLoc != nil && md.ResultLoc.PackageName() != "" {
				tool.MethodResultTypeRef = md.ResultRef
			} else {
				if sd.Scope != nil {
					tool.MethodResultTypeRef = sd.Scope.GoFullTypeRef(me.Result, sd.PkgName)
				}
				if tool.MethodResultTypeRef == "" && md.Result != "" && sd.PkgName != "" {
					tool.MethodResultTypeRef = "*" + sd.PkgName + "." + md.Result
				}
			}
		}
		// Capture user type locations when specified via struct:pkg:path.
		tool.MethodPayloadLoc = md.PayloadLoc
		tool.MethodResultLoc = md.ResultLoc
		break
	}
	// Derive HasResult from tool.Return or bound method result.
	tool.HasResult = (tool.Return != nil && tool.Return.Type != goaexpr.Empty) || (tool.MethodResultAttr != nil && tool.MethodResultAttr.Type != goaexpr.Empty)
	// Compute aliasing flags for payload and result against method types when bound.
	if tool.IsMethodBacked {
		tool.PayloadAliasesMethod = ToolAttrAliasesMethod(tool.Args, tool.MethodPayloadAttr)
		if tool.HasResult {
			tool.ResultAliasesMethod = ToolAttrAliasesMethod(tool.Return, tool.MethodResultAttr)
		}
	}
	return tool
}

// mustFindMethodExpr locates the Goa method expression for the given service and method names.
// It panics if the method cannot be found. This relies on Goa evaluation guarantees.
func mustFindMethodExpr(root *goaexpr.RootExpr, serviceName, methodName string) *goaexpr.MethodExpr {
	svc := root.Service(serviceName)
	if svc == nil {
		return nil
	}
	return svc.Method(methodName)
}

func newActivity(agent *AgentData, kind ActivityKind, logicalSuffix string, queue string) ActivityArtifact {
	funcName := fmt.Sprintf("%s%sActivity", agent.GoName, logicalSuffix)
	definitionVar := fmt.Sprintf("%s%sActivityDefinition", agent.GoName, logicalSuffix)
	name := joinIdentifier(agent.Service.Name, agent.Name, strings.ToLower(logicalSuffix))
	artifact := ActivityArtifact{
		Name:          name,
		FuncName:      funcName,
		DefinitionVar: definitionVar,
		Queue:         queue,
		Kind:          kind,
	}
	switch kind {
	case ActivityKindPlan, ActivityKindResume:
		artifact.RetryPolicy = plannerActivityRetryPolicy()
		artifact.Timeout = defaultPlannerActivityTimeout
	case ActivityKindExecuteTool:
		// ExecuteTool activity has no default timeout or retry policy
	}
	return artifact
}

func plannerActivityRetryPolicy() engine.RetryPolicy {
	return engine.RetryPolicy{
		MaxAttempts:        3,
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
	}
}

func sanitizeToken(name, fallback string) string {
	s := strings.ToLower(codegen.SnakeCase(name))
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, s)
	s = strings.Trim(s, "_")
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	if s == "" {
		return fallback
	}
	return s
}

func queueName(parts ...string) string {
	sanitized := make([]string, 0, len(parts))
	for _, part := range parts {
		token := sanitizeToken(part, "queue")
		if token != "" {
			sanitized = append(sanitized, token)
		}
	}
	if len(sanitized) == 0 {
		return "queue"
	}
	return strings.Join(sanitized, "_")
}

func joinIdentifier(parts ...string) string {
	sanitized := make([]string, 0, len(parts))
	for _, part := range parts {
		token := sanitizeToken(part, "segment")
		if token != "" {
			sanitized = append(sanitized, token)
		}
	}
	if len(sanitized) == 0 {
		return "agent"
	}
	return strings.Join(sanitized, ".")
}
