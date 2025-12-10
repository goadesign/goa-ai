// Package types defines the A2A protocol data types used for task management,
// message exchange, and agent discovery. Field names use camelCase JSON tags
// to conform to the A2A protocol specification.
//
//nolint:tagliatelle // A2A protocol specification requires camelCase JSON field names
package types

import "encoding/json"

// SendTaskPayload is the request payload for tasks/send and tasks/sendSubscribe.
// It carries the task identifier, initial message, and optional metadata.
type SendTaskPayload struct {
	// ID is the unique identifier for the task.
	ID string `json:"id"`
	// Message is the initial task message.
	Message *TaskMessage `json:"message"`
	// SessionID is an optional session identifier for multi-turn conversations.
	SessionID *string `json:"sessionId,omitempty"`
	// Metadata holds optional task-level metadata supplied by the caller.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// GetTaskPayload is the request payload for tasks/get.
type GetTaskPayload struct {
	// ID is the identifier of the task to retrieve.
	ID string `json:"id"`
}

// CancelTaskPayload is the request payload for tasks/cancel.
type CancelTaskPayload struct {
	// ID is the identifier of the task to cancel.
	ID string `json:"id"`
}

// Task represents an A2A task. It is the denormalized view returned by
// tasks/get and the internal state snapshot used by the server.
type Task struct {
	// ID is the unique identifier for the task.
	ID string `json:"id"`
	// Status is the most recent task status snapshot.
	Status *TaskStatus `json:"status,omitempty"`
	// Artifacts are the task output artifacts accumulated so far.
	Artifacts []*Artifact `json:"artifacts,omitempty"`
	// History contains the ordered message history for the task.
	History []*TaskMessage `json:"history,omitempty"`
	// Metadata holds implementation-defined task metadata.
	Metadata map[string]any `json:"metadata,omitempty"`
	// Extra is an extension point for server-specific state that should not
	// affect protocol compatibility.
	Extra map[string]any `json:"extra,omitempty"`
}

// TaskResponse is the wire representation of a task returned by A2A methods.
// It aliases Task so the server can share implementation details without
// duplicating fields.
type TaskResponse = Task

// TaskStatus represents the status of an A2A task at a point in time.
type TaskStatus struct {
	// State is the canonical task state (for example, "submitted", "working",
	// "completed").
	State string `json:"state"`
	// Message is an optional human-readable status message.
	Message *TaskMessage `json:"message,omitempty"`
	// Timestamp is an optional RFC3339 timestamp for the status update.
	Timestamp string `json:"timestamp,omitempty"`
}

// TaskEvent is emitted by tasks/sendSubscribe to stream task lifecycle
// updates. Exactly one of Status, Artifact, or Message is set depending on
// the event Type.
type TaskEvent struct {
	// Type identifies the event kind: "status", "artifact", "message", or
	// "error".
	Type string `json:"type"`
	// TaskID is the ID of the task this event belongs to.
	TaskID string `json:"taskId"`
	// Status carries the task status for "status" events.
	Status *TaskStatus `json:"status,omitempty"`
	// Artifact carries the artifact for "artifact" events.
	Artifact *Artifact `json:"artifact,omitempty"`
	// Message carries the message for "message" events.
	Message *TaskMessage `json:"message,omitempty"`
	// Final reports whether this is the final event for the task.
	Final bool `json:"final,omitempty"`
}

// TaskMessage represents a single message in an A2A task conversation.
type TaskMessage struct {
	// Role is the message role ("user", "assistant", or "system").
	Role string `json:"role"`
	// Parts are the ordered content parts that make up the message.
	Parts []*MessagePart `json:"parts"`
}

// MessagePart represents a part of a message (text, structured data, or file).
type MessagePart struct {
	// Type identifies the part kind: "text", "data", or "file".
	Type string `json:"type"`
	// Text is the textual content when Type == "text".
	Text *string `json:"text,omitempty"`
	// Data is the structured payload when Type == "data".
	Data json.RawMessage `json:"data,omitempty"`
	// MIMEType is the MIME type when Type == "file".
	MIMEType *string `json:"mimeType,omitempty"`
	// URI is the file URI when Type == "file".
	URI *string `json:"uri,omitempty"`
}

// Artifact represents an output artifact attached to a task, such as a file
// or structured result.
type Artifact struct {
	// Name is the optional display name for the artifact.
	Name *string `json:"name,omitempty"`
	// Description is an optional human-readable description of the artifact.
	Description *string `json:"description,omitempty"`
	// Parts are the content parts that make up the artifact.
	Parts []*MessagePart `json:"parts"`
	// Index is an optional sequence index for incremental artifacts.
	Index *int `json:"index,omitempty"`
	// Append indicates whether this artifact appends to a previous one.
	Append *bool `json:"append,omitempty"`
	// LastChunk reports whether this is the final chunk in a streaming
	// artifact sequence.
	LastChunk *bool `json:"lastChunk,omitempty"`
	// Metadata carries implementation-defined artifact metadata.
	Metadata map[string]any `json:"metadata,omitempty"`
	// Extra is an extension point for server-specific metadata.
	Extra map[string]any `json:"extra,omitempty"`
}

// AgentCard represents the A2A agent discovery document returned by
// agent/card.
type AgentCard struct {
	// ProtocolVersion is the A2A protocol version supported by the agent.
	ProtocolVersion string `json:"protocolVersion"`
	// Name is the human-readable agent name.
	Name string `json:"name"`
	// Description is an optional human-readable description of the agent.
	Description string `json:"description,omitempty"`
	// URL is the base URL where the agent is hosted.
	URL string `json:"url"`
	// Version is the agent implementation version.
	Version string `json:"version"`
	// Capabilities captures optional agent capabilities and extensions.
	Capabilities map[string]any `json:"capabilities,omitempty"`
	// DefaultInputModes lists the default supported input content modes.
	DefaultInputModes []string `json:"defaultInputModes,omitempty"`
	// DefaultOutputModes lists the default supported output content modes.
	DefaultOutputModes []string `json:"defaultOutputModes,omitempty"`
	// Skills enumerates the skills exposed by the agent.
	Skills []*Skill `json:"skills"`
	// SecuritySchemes defines the security schemes supported by the agent.
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
	// Security describes the default security requirements for calling the
	// agent.
	Security any `json:"security,omitempty"`
}

// AgentCardResponse is the result type returned by the agent/card method. It
// aliases AgentCard for convenience.
type AgentCardResponse = AgentCard

// Skill represents an A2A skill within an AgentCard.
type Skill struct {
	// ID is the unique identifier for the skill within the agent.
	ID string `json:"id"`
	// Name is the human-readable skill name.
	Name string `json:"name"`
	// Description is an optional human-readable description of the skill.
	Description string `json:"description,omitempty"`
	// Tags are optional labels describing the skill.
	Tags []string `json:"tags,omitempty"`
	// InputModes are the supported input content modes for the skill.
	InputModes []string `json:"inputModes,omitempty"`
	// OutputModes are the supported output content modes for the skill.
	OutputModes []string `json:"outputModes,omitempty"`
}

// SecurityScheme represents a single security scheme definition in the
// AgentCard. It is intentionally minimal and closely aligned with the A2A
// security profile.
type SecurityScheme struct {
	// Type is the security scheme type ("http", "apiKey", or "oauth2").
	Type string `json:"type"`
	// Scheme is the HTTP authentication scheme when Type == "http".
	Scheme string `json:"scheme,omitempty"`
	// In is the API key location when Type == "apiKey".
	In string `json:"in,omitempty"`
	// Name is the API key parameter name when Type == "apiKey".
	Name string `json:"name,omitempty"`
	// Flows contains the OAuth2 flow configuration when Type == "oauth2".
	Flows json.RawMessage `json:"flows,omitempty"`
}
