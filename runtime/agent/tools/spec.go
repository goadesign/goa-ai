package tools

type (
	// ToolSpec enumerates the metadata and JSON codecs for a tool.
	ToolSpec struct {
		// Name is the globally unique tool identifier (`toolset.tool`).
		Name Ident
		// Service identifies the Goa service that declared the tool.
		Service string
		// Toolset is the toolset registration identifier used for routing.
		// It is typically the DSL toolset name.
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

	// TypeSpec describes the payload or result schema for a tool.
	TypeSpec struct {
		// Name is the Go identifier associated with the type.
		Name string
		// Schema contains the JSON schema definition rendered at code generation time.
		Schema []byte
		// Codec serializes and deserializes values matching the type.
		Codec JSONCodec[any]
	}

	// JSONCodec serializes and deserializes strongly typed values to and from JSON.
	JSONCodec[T any] struct {
		// ToJSON encodes the value into canonical JSON.
		ToJSON func(T) ([]byte, error)
		// FromJSON decodes the JSON payload into the typed value.
		FromJSON func([]byte) (T, error)
	}
)
