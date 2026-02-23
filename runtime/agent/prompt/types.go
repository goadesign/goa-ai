// Package prompt defines runtime contracts for prompt identity, versioning, and
// scoped overrides.
//
// The package intentionally separates prompt metadata (PromptSpec/PromptRef) from
// rendering output (PromptContent) and override targeting (Scope). This allows
// runtimes and applications to reason about prompt provenance without coupling to
// any specific model provider implementation.
package prompt

import (
	"text/template"
	"time"
)

type (
	// PromptRole classifies the role a prompt plays in a model request.
	PromptRole string

	// PromptSpec is the baseline registration record for a prompt template.
	//
	// Prompt specs are immutable once registered in a Registry. ID must be unique
	// within a registry instance.
	PromptSpec struct {
		// ID is the globally unique prompt identifier.
		ID Ident
		// AgentID identifies the owning agent.
		AgentID string
		// Role identifies how the prompt is used (system/user/tool/synthesis).
		Role PromptRole
		// Description is optional human-readable metadata.
		Description string
		// Template is the baseline template source.
		Template string
		// Version identifies the baseline prompt version. If empty, registries may
		// derive a deterministic version from Template content.
		Version string
		// Funcs is the template function map used when rendering this prompt.
		Funcs template.FuncMap
	}

	// PromptRef is a lightweight prompt identity attached to rendered outputs and
	// model requests.
	PromptRef struct {
		// ID is the prompt identifier.
		ID Ident
		// Version is the resolved version used for rendering.
		Version string
	}

	// PromptContent is the output of rendering a resolved prompt.
	PromptContent struct {
		// Text is the rendered prompt text.
		Text string
		// Ref identifies which prompt/version produced Text.
		Ref PromptRef
	}

	// Scope constrains which prompt overrides apply to a render call.
	//
	// SessionID is an optional first-class session dimension. Labels carries
	// caller-defined scope dimensions (for example, account/region/environment).
	// Empty fields mean "no scope constraint" for that dimension.
	Scope struct {
		SessionID string
		Labels    map[string]string
	}

	// Override is one stored prompt override record.
	Override struct {
		// PromptID identifies which baseline prompt this override targets.
		PromptID Ident
		// Scope constrains where the override applies.
		Scope Scope
		// Template is the override template source.
		Template string
		// Version is the version assigned to the override.
		Version string
		// CreatedAt records when this override was persisted.
		CreatedAt time.Time
		// Metadata carries optional caller-defined attributes (for example,
		// experiment identifiers).
		Metadata map[string]string
	}
)

const (
	// PromptRoleSystem identifies system prompts.
	PromptRoleSystem PromptRole = "system"
	// PromptRoleUser identifies user prompts.
	PromptRoleUser PromptRole = "user"
	// PromptRoleTool identifies tool-level prompts.
	PromptRoleTool PromptRole = "tool"
	// PromptRoleSynthesis identifies synthesis/finalization prompts.
	PromptRoleSynthesis PromptRole = "synthesis"
)
