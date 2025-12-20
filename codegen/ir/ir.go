package ir

import "goa.design/goa/v3/codegen/service"

type (
	// Design is the deterministic, generator-facing intermediate representation of a
	// Goa-AI agent/toolset design.
	//
	// Design is intended to be stable and ordered deterministically so generators
	// can iterate it without relying on map iteration order.
	Design struct {
		// Genpkg is the import path root for generated code (typically "<module>/gen").
		Genpkg string `json:"genpkg"`
		// Services is the set of Goa services referenced by the design.
		Services []*Service `json:"services"`
		// Agents is the set of declared agents, sorted by service then agent name.
		Agents []*Agent `json:"agents"`
		// Toolsets is the set of defining toolsets, sorted by toolset name.
		Toolsets []*Toolset `json:"toolsets"`
	}

	// Service describes a Goa service used to anchor ownership and output layout.
	Service struct {
		// Name is the Goa service name.
		Name string `json:"name"`
		// PathName is the Goa service path name used in generated directories.
		PathName string `json:"path_name"`

		Goa *service.Data `json:"-"`
	}

	// Agent describes an agent declaration anchored to a Goa service.
	Agent struct {
		// Name is the DSL name of the agent.
		Name string `json:"name"`
		// Slug is the filesystem-safe token derived from Name.
		Slug string `json:"slug"`
		// Service is the owning service of the agent.
		Service *Service `json:"service"`
	}

	// Toolset describes a defining toolset (Origin == nil) together with its chosen
	// ownership anchor for generation.
	Toolset struct {
		// Name is the globally unique toolset identifier in the design.
		Name string `json:"name"`
		// Slug is the filesystem-safe token derived from Name.
		Slug string `json:"slug"`
		// Owner identifies where this toolset's specs/codecs are generated.
		Owner Owner `json:"owner"`
	}

	// OwnerKind identifies the generation anchor for a toolset.
	OwnerKind string

	// Owner describes the selected anchor for a defining toolset.
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
)

const (
	// OwnerKindService indicates a service-owned toolset whose specs/codecs live under
	// gen/<service>/toolsets/<toolset>/...
	OwnerKindService OwnerKind = "service"
	// OwnerKindAgentExport indicates a toolset exported by an agent whose specs/codecs
	// live under gen/<service>/agents/<agent>/exports/<toolset>/...
	OwnerKindAgentExport OwnerKind = "agent_export"
)
