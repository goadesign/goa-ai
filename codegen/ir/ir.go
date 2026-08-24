// Package ir defines the canonical generator-facing model for goa-ai.
//
// The IR captures ownership, output layout, and deterministic ordering once so
// downstream generators can render files without re-deriving the same facts
// from the evaluated DSL graph.
package ir

import (
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// Design is the deterministic, generator-facing intermediate representation of a
	// Goa-AI agent/toolset design.
	//
	// Design is intended to be stable and ordered deterministically so generators
	// can iterate it without relying on map iteration order.
	Design struct {
		// Genpkg is the import path root for generated code (typically "<module>/gen").
		Genpkg string `json:"genpkg"`
		// GoaRoot retains the evaluated Goa root so render adapters can reuse Goa's
		// service analysis without re-scanning the eval roots.
		GoaRoot *goaexpr.RootExpr `json:"-"`
		// AgentsRoot retains the evaluated goa-ai root that produced this IR.
		AgentsRoot *agentsExpr.RootExpr `json:"-"`
		// Services is the ordered set of Goa services known to the design.
		Services []*Service `json:"services"`
		// Agents is the set of declared agents, sorted by service then agent name.
		Agents []*Agent `json:"agents"`
		// Toolsets is the set of defining toolsets, sorted by toolset name.
		Toolsets []*Toolset `json:"toolsets"`
		// Completions is the set of service-owned typed completions, sorted by
		// service then completion name.
		Completions []*Completion `json:"completions"`
	}

	// Service describes a Goa service used to anchor ownership and output layout.
	Service struct {
		// Name is the Goa service name.
		Name string `json:"name"`
		// PathName is the Goa service path name used in generated directories.
		PathName string `json:"path_name"`

		Goa *service.Data `json:"-"`
		// Agents declared by this service, sorted by agent name.
		Agents []*Agent `json:"-"`
		// Completions declared by this service, sorted by completion name.
		Completions []*Completion `json:"-"`
	}

	// Agent describes an agent declaration anchored to a Goa service.
	Agent struct {
		// Expr is the evaluated DSL node that produced this agent.
		Expr *agentsExpr.AgentExpr `json:"-"`
		// Name is the DSL name of the agent.
		Name string `json:"name"`
		// Description is the DSL description.
		Description string `json:"description"`
		// Slug is the filesystem-safe token derived from Name.
		Slug string `json:"slug"`
		// ID is the stable runtime identifier (`service.agent`).
		ID string `json:"id"`
		// Service is the owning service of the agent.
		Service *Service `json:"service"`
		// PackageName is the generated Go package name for the agent package.
		PackageName string `json:"package_name"`
		// PathName is the filesystem-safe path segment for the agent package.
		PathName string `json:"path_name"`
		// Dir is the generated filesystem output directory for the agent package.
		Dir string `json:"dir"`
		// ImportPath is the generated Go import path for the agent package.
		ImportPath string `json:"import_path"`
		// ConfigType is the generated configuration type name.
		ConfigType string `json:"config_type"`
		// StructName is the generated wrapper type name.
		StructName string `json:"struct_name"`
		// WorkflowFunc is the generated workflow function name.
		WorkflowFunc string `json:"workflow_func"`
		// WorkflowDefinitionVar is the generated workflow definition variable name.
		WorkflowDefinitionVar string `json:"workflow_definition_var"`
		// WorkflowName is the runtime workflow identifier.
		WorkflowName string `json:"workflow_name"`
		// WorkflowQueue is the canonical workflow task queue.
		WorkflowQueue string `json:"workflow_queue"`
		// ToolSpecsPackage is the aggregate specs package name for the agent.
		ToolSpecsPackage string `json:"tool_specs_package"`
		// ToolSpecsImportPath is the import path for the aggregate specs package.
		ToolSpecsImportPath string `json:"tool_specs_import_path"`
		// ToolSpecsDir is the filesystem output directory for the aggregate specs package.
		ToolSpecsDir string `json:"tool_specs_dir"`
		// UsedToolsets lists consumed toolset references, sorted by toolset name.
		UsedToolsets []*ToolsetRef `json:"-"`
		// ExportedToolsets lists exported toolset references, sorted by toolset name.
		ExportedToolsets []*ToolsetRef `json:"-"`
	}

	// Toolset describes a defining toolset (Origin == nil) together with its chosen
	// ownership anchor for generation.
	Toolset struct {
		// Expr is the evaluated DSL node that produced this defining toolset.
		Expr *agentsExpr.ToolsetExpr `json:"-"`
		// Name is the globally unique toolset identifier in the design.
		Name string `json:"name"`
		// Slug is the filesystem-safe token derived from Name.
		Slug string `json:"slug"`
		// Owner identifies where this toolset's generated specs/codecs are generated.
		Owner Owner `json:"owner"`
		// SpecsPackageName is the canonical Go package name for generated specs/types/codecs.
		SpecsPackageName string `json:"specs_package_name"`
		// SpecsImportPath is the canonical Go import path for generated specs/types/codecs.
		SpecsImportPath string `json:"specs_import_path"`
		// SpecsDir is the canonical filesystem directory for generated specs/types/codecs.
		SpecsDir string `json:"specs_dir"`
		// AgentToolsPackage is the canonical provider-side agenttools package name
		// when this toolset is owned by an agent export.
		AgentToolsPackage string `json:"agent_tools_package,omitempty"`
		// AgentToolsImportPath is the canonical provider-side agenttools import path.
		AgentToolsImportPath string `json:"agent_tools_import_path,omitempty"`
		// AgentToolsDir is the canonical provider-side agenttools directory.
		AgentToolsDir string `json:"agent_tools_dir,omitempty"`
	}

	// OwnerKind identifies the generation anchor for a toolset.
	OwnerKind string

	// Owner describes the selected anchor for a toolset.
	Owner struct {
		// Kind identifies which anchor owns this toolset's generated specs/codecs.
		// Toolsets may be declared globally (top-level) but still require a concrete
		// owner anchor to avoid duplicate emission and to keep package layout stable.
		Kind OwnerKind `json:"kind"`

		// ServiceName is the Goa service name that owns the generated package.
		ServiceName string `json:"service_name"`
		// ServicePathName is the Goa service path name used in gen/ layout.
		ServicePathName string `json:"service_path_name"`

		// AgentName is set when Kind is OwnerKindAgentExport.
		AgentName string `json:"agent_name,omitempty"`
		// AgentSlug is set when Kind is OwnerKindAgentExport.
		AgentSlug string `json:"agent_slug,omitempty"`
	}

	// Completion describes one service-owned typed assistant-output contract.
	Completion struct {
		// Expr is the evaluated DSL node that produced this completion.
		Expr *agentsExpr.CompletionExpr `json:"-"`
		// Name is the DSL identifier.
		Name string `json:"name"`
		// Description is the DSL description.
		Description string `json:"description"`
		// GoName is the exported Go identifier derived from Name.
		GoName string `json:"go_name"`
		// Service owns this completion.
		Service *Service `json:"service"`
	}

	// ToolsetRefKind identifies how an agent references a toolset.
	ToolsetRefKind string

	// ToolsetRef describes one agent-scoped reference to a defining toolset.
	ToolsetRef struct {
		// Expr is the evaluated DSL node for this concrete reference.
		Expr *agentsExpr.ToolsetExpr `json:"-"`
		// Definition points to the canonical defining toolset.
		Definition *Toolset `json:"-"`
		// Kind states whether the agent uses or exports this toolset.
		Kind ToolsetRefKind `json:"kind"`
		// Name is the toolset identifier visible from the referencing agent.
		Name string `json:"name"`
		// Slug is the filesystem-safe token derived from Name.
		Slug string `json:"slug"`
		// QualifiedName is the canonical runtime registration identifier seen by the agent.
		QualifiedName string `json:"qualified_name"`
		// Description is the DSL description visible from the referencing agent.
		Description string `json:"description"`
		// Tags are the toolset-level tags visible from the referencing agent.
		Tags []string `json:"tags,omitempty"`
		// Service is the Goa service that owns the referencing agent.
		Service *Service `json:"-"`
		// ServiceName is the Goa service name that owns the referencing agent.
		ServiceName string `json:"service_name"`
		// Agent is the referencing agent.
		Agent *Agent `json:"-"`
		// SourceService is the Goa service that owns the underlying tool definitions.
		SourceService *Service `json:"-"`
		// SourceServiceName is the Goa service name that owns the underlying tool definitions.
		SourceServiceName string `json:"source_service_name"`
		// TaskQueue is the canonical executor queue for this agent-local toolset package.
		TaskQueue string `json:"task_queue"`
		// PackageName is the generated agent-local helper package name.
		PackageName string `json:"package_name"`
		// PackageImportPath is the generated agent-local helper import path.
		PackageImportPath string `json:"package_import_path"`
		// Dir is the generated agent-local helper directory.
		Dir string `json:"dir"`
		// SpecsPackageName is the canonical package name for shared specs/codecs.
		SpecsPackageName string `json:"specs_package_name"`
		// SpecsImportPath is the canonical import path for shared specs/codecs.
		SpecsImportPath string `json:"specs_import_path"`
		// SpecsDir is the canonical directory for shared specs/codecs.
		SpecsDir string `json:"specs_dir"`
		// AgentToolsPackage is the canonical provider-side agenttools package name
		// when the defining toolset is owned by an agent export.
		AgentToolsPackage string `json:"agent_tools_package,omitempty"`
		// AgentToolsImportPath is the canonical provider-side agenttools import path.
		AgentToolsImportPath string `json:"agent_tools_import_path,omitempty"`
		// AgentToolsDir is the canonical provider-side agenttools directory.
		AgentToolsDir string `json:"agent_tools_dir,omitempty"`
		// Provider captures provider-specific runtime metadata needed by render adapters.
		Provider *ToolsetProvider `json:"provider,omitempty"`
	}

	// ToolsetProvider captures provider-specific metadata for one concrete toolset
	// reference.
	ToolsetProvider struct {
		// Kind identifies the provider type.
		Kind agentsExpr.ProviderKind `json:"kind"`
		// MCP is populated when Kind == ProviderMCP.
		MCP *MCPToolsetMeta `json:"mcp,omitempty"`
		// Registry is populated when Kind == ProviderRegistry.
		Registry *RegistryToolsetMeta `json:"registry,omitempty"`
	}

	// MCPToolsetMeta captures generated metadata for one MCP-backed toolset reference.
	MCPToolsetMeta struct {
		// ServiceName is the Goa service that names the MCP provider.
		ServiceName string `json:"service_name"`
		// SuiteName is the referenced MCP toolset/server name.
		SuiteName string `json:"suite_name"`
		// Source identifies whether schemas come from Goa MCP or inline DSL.
		Source agentsExpr.MCPSourceKind `json:"source"`
		// QualifiedName is the canonical runtime toolset identifier.
		QualifiedName string `json:"qualified_name"`
		// ConstName is the generated Go identifier for the agent-local MCP toolset ID constant.
		ConstName string `json:"const_name"`
	}

	// RegistryToolsetMeta captures generated metadata for one registry-backed
	// toolset reference.
	RegistryToolsetMeta struct {
		// RegistryName is the registry source name.
		RegistryName string `json:"registry_name"`
		// ToolsetName is the external toolset name inside the registry.
		ToolsetName string `json:"toolset_name"`
		// Version is the optional external version pin.
		Version string `json:"version,omitempty"`
		// QualifiedName is the canonical runtime toolset identifier.
		QualifiedName string `json:"qualified_name"`
		// RegistryClientImportPath is the generated import path for the registry client package.
		RegistryClientImportPath string `json:"registry_client_import_path"`
		// RegistryClientAlias is the generated import alias for that registry client package.
		RegistryClientAlias string `json:"registry_client_alias"`
	}
)

const (
	// OwnerKindService indicates a service-owned toolset whose specs/codecs live under
	// gen/<service>/toolsets/<toolset>/...
	OwnerKindService OwnerKind = "service"
	// OwnerKindAgentExport indicates a toolset exported by an agent whose specs/codecs
	// live under gen/<service>/agents/<agent>/exports/<toolset>/...
	OwnerKindAgentExport OwnerKind = "agent_export"

	// ToolsetRefKindUsed identifies a toolset consumed by an agent.
	ToolsetRefKindUsed ToolsetRefKind = "used"
	// ToolsetRefKindExported identifies a toolset exported by an agent.
	ToolsetRefKindExported ToolsetRefKind = "exported"
)
