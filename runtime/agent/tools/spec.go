package tools

import "encoding/json"

// AnyJSONCodec is a pre-built codec for the `any` type. It uses standard JSON
// marshaling/unmarshaling and is suitable for integrations where the concrete
// type is not known at compile time.
var AnyJSONCodec = JSONCodec[any]{
	ToJSON: json.Marshal,
	FromJSON: func(data []byte) (any, error) {
		if len(data) == 0 {
			return nil, nil
		}
		var out any
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return out, nil
	},
}

type (
	// ServerDataAudience declares who a server-data payload is intended for.
	//
	// Audience is a routing contract for downstream consumers (timeline projection,
	// UI renderers, persistence sinks). It is not sent to model providers.
	ServerDataAudience string

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
		// Meta carries arbitrary design-time metadata attached to the tool via DSL.
		//
		// Meta is intended for downstream consumers (policy engines, UIs, orchestration
		// layers) that need structured tool annotations without requiring runtime
		// design introspection. Code generation emits Meta only when present.
		Meta map[string][]string
		// TerminalRun indicates that once this tool executes in a run, the runtime
		// terminates the run immediately after publishing the tool result(s), without
		// requesting a follow-up planner PlanResume/finalization turn.
		//
		// This is intended for tools whose result is the user-facing terminal output
		// (for example, a final report renderer) and should not be followed by extra
		// model narration.
		TerminalRun bool
		// IsAgentTool indicates this tool is implemented by an agent (agent-as-tool).
		// When true, the runtime executes the tool by starting the provider agent as a
		// child workflow from within the parent workflow loop. Set by codegen when
		// processing Exports blocks.
		IsAgentTool bool
		// AgentID is the fully qualified agent identifier (e.g., "service.agent_name").
		// Only set when IsAgentTool is true.
		AgentID string
		// BoundedResult indicates that this tool's result is declared as a bounded
		// view over a potentially larger data set. It is set via the BoundedResult
		// DSL helper and propagated into specs so runtimes and services can enforce
		// and surface truncation metadata consistently.
		BoundedResult bool
		// Paging optionally describes cursor-based pagination fields for this tool.
		// When set, runtimes can generate paging-aware reminders and UIs can
		// render consistent paging affordances without inspecting schemas.
		Paging *PagingSpec
		// ServerData enumerates server-only payloads emitted alongside the tool
		// result. Server data is never sent to model providers.
		//
		// Each entry declares the schema and codec for the item data payload
		// (`toolregistry.ServerDataItem.Data`) so runtimes and consumers can decode
		// and validate it without runtime design introspection.
		ServerData []*ServerDataSpec
		// ResultReminder is an optional system reminder injected into the
		// conversation after the tool result is returned. It provides backstage
		// guidance to the model about how to interpret or present the result
		// (for example, "The user sees a rendered graph of this data"). The
		// runtime wraps this text in <system-reminder> tags.
		ResultReminder string
		// Confirmation configures design-time confirmation requirements for this tool.
		// When set, runtimes may request explicit out-of-band user confirmation before
		// executing the tool. Runtime configuration can override or extend this policy.
		Confirmation *ConfirmationSpec
		// Payload describes the request schema for the tool.
		Payload TypeSpec
		// Result describes the response schema for the tool.
		Result TypeSpec
	}

	// ServerDataSpec describes one server-only payload emitted alongside a tool
	// result. Server data is never sent to model providers.
	ServerDataSpec struct {
		// Kind identifies the server-data kind.
		Kind string
		// Audience declares who this server-data payload is intended for.
		//
		// Allowed values:
		//   - "timeline": persisted and eligible for UI rendering and transcript export
		//   - "internal": tool-composition attachment; not persisted or rendered
		//   - "evidence": provenance references; persisted separately from timeline cards
		Audience ServerDataAudience
		// Description describes what an observer sees when this payload is rendered.
		Description string
		// Type describes the schema and JSON codec for this server-data payload.
		Type TypeSpec
	}

	// PagingSpec describes cursor-based pagination for a tool.
	// Field names refer to the tool payload/result schemas.
	PagingSpec struct {
		// CursorField is the name of the optional String field in the tool payload
		// used to request subsequent pages.
		CursorField string
		// NextCursorField is the name of the optional String field in the tool result
		// that carries the cursor for the next page.
		NextCursorField string
	}

	// ConfirmationSpec declares the confirmation protocol for a tool.
	// It is emitted by goa-ai codegen when a tool uses Confirmation in the DSL.
	//
	// Confirmation uses a runtime-owned confirmation transport (typically an
	// ask_question-style external interaction) to obtain an approve/deny decision
	// from a human operator. Tool authors only configure templates and optional
	// display title; the runtime owns how confirmation is requested and how the
	// decision is delivered back to the run.
	ConfirmationSpec struct {
		// Title is an optional title shown in the confirmation UI (when supported).
		Title string
		// PromptTemplate is rendered with the tool payload to produce the prompt.
		PromptTemplate string
		// DeniedResultTemplate is rendered with the tool payload to produce JSON for
		// the denied tool result. The rendered JSON must decode with the tool result
		// codec so consumers observe a schema-compliant tool_result.
		DeniedResultTemplate string
	}

	// TypeSpec describes the payload or result schema for a tool.
	TypeSpec struct {
		// Name is the Go identifier associated with the type.
		Name string
		// Schema contains the JSON schema definition rendered at code generation time.
		Schema []byte
		// ExampleJSON optionally contains a canonical example JSON document for this
		// type. When present on payload types, runtimes and planners can surface it
		// in retry hints or await-clarification prompts to guide callers toward a
		// schema-compliant shape.
		ExampleJSON []byte
		// ExampleInput is an optional parsed example payload. When present, it is a
		// JSON-object example represented as a map and can be attached to retry hints
		// without runtime JSON unmarshaling.
		ExampleInput map[string]any
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

const (
	// AudienceTimeline indicates the payload is persisted and eligible for UI rendering.
	AudienceTimeline ServerDataAudience = "timeline"
	// AudienceInternal indicates the payload is an internal tool-composition attachment.
	AudienceInternal ServerDataAudience = "internal"
	// AudienceEvidence indicates the payload carries provenance references.
	AudienceEvidence ServerDataAudience = "evidence"
)
