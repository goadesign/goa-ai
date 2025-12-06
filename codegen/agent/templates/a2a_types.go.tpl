// A2AProtocolVersion is the A2A protocol version supported by this agent.
const A2AProtocolVersion = "1.0"

// AgentCard represents an A2A-compliant agent card that describes the agent's
// capabilities, skills, and authentication requirements. The card is used for
// agent discovery and registration with A2A-compatible registries.
type AgentCard struct {
	// ProtocolVersion is the A2A protocol version (e.g., "1.0").
	ProtocolVersion string `json:"protocolVersion"`
	// Name is the agent identifier.
	Name string `json:"name"`
	// Description explains what the agent does.
	Description string `json:"description,omitempty"`
	// URL is the agent's endpoint for receiving A2A task requests.
	URL string `json:"url"`
	// Version is the agent version.
	Version string `json:"version,omitempty"`
	// Capabilities lists agent capabilities.
	Capabilities map[string]any `json:"capabilities,omitempty"`
	// DefaultInputModes lists the default input content types accepted.
	DefaultInputModes []string `json:"defaultInputModes,omitempty"`
	// DefaultOutputModes lists the default output content types produced.
	DefaultOutputModes []string `json:"defaultOutputModes,omitempty"`
	// Skills lists the agent's skills (mapped from exported tools).
	Skills []*Skill `json:"skills,omitempty"`
	// SecuritySchemes defines the authentication schemes supported.
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
	// Security lists the security requirements for accessing the agent.
	Security []map[string][]string `json:"security,omitempty"`
}

// Skill represents an A2A skill, which maps to an exported tool in goa-ai.
// Skills are the discrete capabilities that an A2A agent can perform.
type Skill struct {
	// ID is the skill identifier (matches the tool's qualified name).
	ID string `json:"id"`
	// Name is the human-readable skill name.
	Name string `json:"name"`
	// Description explains what the skill does.
	Description string `json:"description,omitempty"`
	// Tags are metadata tags for discovery and filtering.
	Tags []string `json:"tags,omitempty"`
	// InputModes lists the input content types accepted by this skill.
	InputModes []string `json:"inputModes,omitempty"`
	// OutputModes lists the output content types produced by this skill.
	OutputModes []string `json:"outputModes,omitempty"`
}

// SecurityScheme represents an A2A security scheme definition.
type SecurityScheme struct {
	// Type is the security scheme type (e.g., "http", "apiKey", "oauth2").
	Type string `json:"type"`
	// Scheme is the HTTP authentication scheme (e.g., "bearer", "basic").
	// Only used when Type is "http".
	Scheme string `json:"scheme,omitempty"`
	// In specifies where the API key is sent (e.g., "header", "query").
	// Only used when Type is "apiKey".
	In string `json:"in,omitempty"`
	// Name is the parameter name for API key authentication.
	// Only used when Type is "apiKey".
	Name string `json:"name,omitempty"`
	// Flows contains OAuth2 flow configurations.
	// Only used when Type is "oauth2".
	Flows *OAuth2Flows `json:"flows,omitempty"`
}

// OAuth2Flows contains OAuth2 flow configurations.
type OAuth2Flows struct {
	// ClientCredentials is the client credentials flow configuration.
	ClientCredentials *OAuth2Flow `json:"clientCredentials,omitempty"`
	// AuthorizationCode is the authorization code flow configuration.
	AuthorizationCode *OAuth2Flow `json:"authorizationCode,omitempty"`
	// Implicit is the implicit flow configuration.
	Implicit *OAuth2Flow `json:"implicit,omitempty"`
}

// OAuth2Flow represents a single OAuth2 flow configuration.
type OAuth2Flow struct {
	// TokenURL is the token endpoint URL.
	TokenURL string `json:"tokenUrl,omitempty"`
	// AuthorizationURL is the authorization endpoint URL.
	AuthorizationURL string `json:"authorizationUrl,omitempty"`
	// Scopes maps scope names to descriptions.
	Scopes map[string]string `json:"scopes,omitempty"`
}

// TaskRequest represents an A2A task request.
type TaskRequest struct {
	// ID is the unique task identifier.
	ID string `json:"id"`
	// Message contains the task message.
	Message *TaskMessage `json:"message"`
	// SessionID is an optional session identifier for multi-turn conversations.
	SessionID string `json:"sessionId,omitempty"`
	// Metadata contains optional task metadata.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TaskMessage represents a message in an A2A task.
type TaskMessage struct {
	// Role is the message role (e.g., "user", "assistant").
	Role string `json:"role"`
	// Parts contains the message content parts.
	Parts []*MessagePart `json:"parts"`
}

// MessagePart represents a part of a message.
type MessagePart struct {
	// Type is the part type (e.g., "text", "data").
	Type string `json:"type"`
	// Text is the text content (when Type is "text").
	Text string `json:"text,omitempty"`
	// Data is structured data content (when Type is "data").
	Data any `json:"data,omitempty"`
	// MimeType is the MIME type for file content.
	MimeType string `json:"mimeType,omitempty"`
	// URI is the URI for file content.
	URI string `json:"uri,omitempty"`
}

// TaskResponse represents an A2A task response.
type TaskResponse struct {
	// ID is the task identifier.
	ID string `json:"id"`
	// Status is the task status.
	Status *TaskStatus `json:"status"`
	// Artifacts contains the task output artifacts.
	Artifacts []*Artifact `json:"artifacts,omitempty"`
	// History contains the conversation history.
	History []*TaskMessage `json:"history,omitempty"`
	// Metadata contains optional response metadata.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TaskStatus represents the status of an A2A task.
type TaskStatus struct {
	// State is the task state (e.g., "completed", "failed", "working").
	State string `json:"state"`
	// Message is an optional status message.
	Message *TaskMessage `json:"message,omitempty"`
	// Timestamp is when the status was updated.
	Timestamp string `json:"timestamp,omitempty"`
}

// Artifact represents an output artifact from a task.
type Artifact struct {
	// Name is the artifact name.
	Name string `json:"name,omitempty"`
	// Description describes the artifact.
	Description string `json:"description,omitempty"`
	// Parts contains the artifact content parts.
	Parts []*MessagePart `json:"parts"`
	// Index is the artifact index.
	Index int `json:"index,omitempty"`
	// Append indicates if this artifact appends to a previous one.
	Append bool `json:"append,omitempty"`
	// LastChunk indicates if this is the last chunk of a streaming artifact.
	LastChunk bool `json:"lastChunk,omitempty"`
	// Metadata contains optional artifact metadata.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TaskEvent represents a streaming event from an A2A task.
type TaskEvent struct {
	// Type is the event type (e.g., "status", "artifact", "message").
	Type string `json:"type"`
	// TaskID is the task identifier.
	TaskID string `json:"taskId"`
	// Status is present for status events.
	Status *TaskStatus `json:"status,omitempty"`
	// Artifact is present for artifact events.
	Artifact *Artifact `json:"artifact,omitempty"`
	// Message is present for message events.
	Message *TaskMessage `json:"message,omitempty"`
	// Final indicates if this is the final event.
	Final bool `json:"final,omitempty"`
}

// A2AError represents an error from an A2A agent.
type A2AError struct {
	// Code is the error code.
	Code int `json:"code"`
	// Message is the error message.
	Message string `json:"message"`
	// Data contains optional error data.
	Data any `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *A2AError) Error() string {
	return fmt.Sprintf("a2a error (code %d): %s", e.Code, e.Message)
}
