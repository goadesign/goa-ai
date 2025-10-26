// Package tools exposes shared tool metadata and codec types used by generated code.
package tools

// JSONCodec serializes and deserializes strongly typed values to and from JSON.
type JSONCodec[T any] struct {
	// ToJSON encodes the value into canonical JSON.
	ToJSON func(T) ([]byte, error)
	// FromJSON decodes the JSON payload into the typed value.
	FromJSON func([]byte) (T, error)
}

// TypeSpec describes the payload or result schema for a tool.
type TypeSpec struct {
	// Name is the Go identifier associated with the type.
	Name string
	// Schema contains the JSON schema definition rendered at code generation time.
	Schema []byte
	// Codec serializes and deserializes values matching the type.
	Codec JSONCodec[any]
}

// ToolSpec enumerates the metadata and JSON codecs for a tool.
type ToolSpec struct {
	// Name is the fully qualified tool identifier (service.toolset.tool).
	Name string
	// Service identifies the Goa service that declared the tool.
	Service string
	// Toolset is the logical toolset identifier within the service.
	Toolset string
	// Description provides human-readable context for planners and tooling.
	Description string
	// Tags carries optional metadata labels used by policy or UI layers.
	Tags []string
	// IsAgentTool indicates this tool is an agent-as-tool (inline execution).
	// When true, the tool is executed by calling ExecuteAgentInline instead of
	// scheduling a workflow activity. Set by codegen when processing Exports blocks.
	IsAgentTool bool
	// AgentID is the fully qualified agent identifier (e.g., "service.agent_name").
	// Only set when IsAgentTool is true. Used to look up the agent registration
	// for inline execution.
	AgentID string
	// Payload describes the request schema for the tool.
	Payload TypeSpec
	// Result describes the response schema for the tool.
	Result TypeSpec
}

// ID is the strong type for fully qualified tool identifiers
// (e.g., "service.toolset.tool"). Use this type when referencing
// tools in maps or APIs to avoid accidental mixing with free-form strings.
type ID string
