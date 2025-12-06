// A2AClient is a client for invoking external A2A agents.
// It handles task requests, streaming responses, and authentication
// based on the agent card's security schemes.
type A2AClient struct {
	// endpoint is the A2A agent's URL.
	endpoint string
	// httpClient is the HTTP client used for requests.
	httpClient *http.Client
	// auth provides authentication for requests.
	auth A2AAuthProvider
	// timeout is the request timeout.
	timeout time.Duration
}

// A2AClientOption configures an A2A client.
type A2AClientOption func(*A2AClient)

// A2AAuthProvider provides authentication credentials for A2A requests.
type A2AAuthProvider interface {
	// ApplyAuth adds authentication to the request.
	ApplyAuth(req *http.Request) error
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
{{- if .HasSecuritySchemes }}
{{- range .SecuritySchemes }}
{{- if eq .Type "http" }}
{{- if eq .Scheme "bearer" }}

// {{ goify .Name true }}A2AAuth provides bearer token authentication for A2A requests.
type {{ goify .Name true }}A2AAuth struct {
	// Token is the bearer token.
	Token string
}
{{- else if eq .Scheme "basic" }}

// {{ goify .Name true }}A2AAuth provides basic authentication for A2A requests.
type {{ goify .Name true }}A2AAuth struct {
	// Username is the basic auth username.
	Username string
	// Password is the basic auth password.
	Password string
}
{{- end }}
{{- end }}
{{- if eq .Type "apiKey" }}

// {{ goify .Name true }}A2AAuth provides API key authentication for A2A requests.
type {{ goify .Name true }}A2AAuth struct {
	// Key is the API key value.
	Key string
}
{{- end }}
{{- if eq .Type "oauth2" }}

// {{ goify .Name true }}A2AAuth provides OAuth2 authentication for A2A requests.
type {{ goify .Name true }}A2AAuth struct {
	// Token is the OAuth2 access token.
	Token string
}
{{- end }}
{{- end }}
{{- end }}

type (
	// jsonRPCRequest represents a JSON-RPC 2.0 request.
	jsonRPCRequest struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
		ID      string `json:"id"`
	}

	// jsonRPCResponse represents a JSON-RPC 2.0 response.
	jsonRPCResponse struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *A2AError       `json:"error,omitempty"`
		ID      string          `json:"id"`
	}
)

// NewA2AClient creates a new A2A client for the given agent card.
func NewA2AClient(card *AgentCard, opts ...A2AClientOption) *A2AClient {
	c := &A2AClient{
		endpoint:   card.URL,
		httpClient: http.DefaultClient,
		timeout:    30 * time.Second,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// WithA2AHTTPClient sets a custom HTTP client.
func WithA2AHTTPClient(client *http.Client) A2AClientOption {
	return func(c *A2AClient) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithA2AAuth sets the authentication provider.
func WithA2AAuth(auth A2AAuthProvider) A2AClientOption {
	return func(c *A2AClient) {
		c.auth = auth
	}
}

// WithA2ATimeout sets the request timeout.
func WithA2ATimeout(timeout time.Duration) A2AClientOption {
	return func(c *A2AClient) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

// WithA2AEndpoint overrides the endpoint from the agent card.
func WithA2AEndpoint(endpoint string) A2AClientOption {
	return func(c *A2AClient) {
		if endpoint != "" {
			c.endpoint = endpoint
		}
	}
}
{{- if .HasSecuritySchemes }}
{{- range .SecuritySchemes }}
{{- if eq .Type "http" }}
{{- if eq .Scheme "bearer" }}

// WithA2A{{ goify .Name true }} creates an auth provider with the given bearer token.
func WithA2A{{ goify .Name true }}(token string) A2AClientOption {
	return WithA2AAuth(&{{ goify .Name true }}A2AAuth{Token: token})
}
{{- else if eq .Scheme "basic" }}

// WithA2A{{ goify .Name true }} creates an auth provider with the given credentials.
func WithA2A{{ goify .Name true }}(username, password string) A2AClientOption {
	return WithA2AAuth(&{{ goify .Name true }}A2AAuth{
		Username: username,
		Password: password,
	})
}
{{- end }}
{{- end }}
{{- if eq .Type "apiKey" }}

// WithA2A{{ goify .Name true }} creates an auth provider with the given API key.
func WithA2A{{ goify .Name true }}(key string) A2AClientOption {
	return WithA2AAuth(&{{ goify .Name true }}A2AAuth{Key: key})
}
{{- end }}
{{- if eq .Type "oauth2" }}

// WithA2A{{ goify .Name true }} creates an auth provider with the given OAuth2 token.
func WithA2A{{ goify .Name true }}(token string) A2AClientOption {
	return WithA2AAuth(&{{ goify .Name true }}A2AAuth{Token: token})
}
{{- end }}
{{- end }}
{{- end }}

// SendTask sends a task to the A2A agent and waits for completion.
// This method is suitable for non-streaming tasks.
func (c *A2AClient) SendTask(ctx context.Context, skillID string, input any) (*TaskResponse, error) {
	taskID := generateTaskID()

	req := &TaskRequest{
		ID: taskID,
		Message: &TaskMessage{
			Role: "user",
			Parts: []*MessagePart{
				{
					Type: "data",
					Data: map[string]any{
						"skillId": skillID,
						"input":   input,
					},
				},
			},
		},
	}

	rpcReq := &jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "tasks/send",
		Params:  req,
		ID:      taskID,
	}

	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	if c.auth != nil {
		if err := c.auth.ApplyAuth(httpReq); err != nil {
			return nil, fmt.Errorf("applying auth: %w", err)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &A2AError{
			Code:    resp.StatusCode,
			Message: string(respBody),
		}
	}

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	var taskResp TaskResponse
	if err := json.Unmarshal(rpcResp.Result, &taskResp); err != nil {
		return nil, fmt.Errorf("decoding task response: %w", err)
	}

	return &taskResp, nil
}

// SendTaskStreaming sends a task to the A2A agent and returns a channel of events.
// The channel is closed when the task completes or an error occurs.
// Callers should check for errors by examining the final event or context cancellation.
func (c *A2AClient) SendTaskStreaming(ctx context.Context, skillID string, input any) (<-chan *TaskEvent, error) {
	taskID := generateTaskID()

	req := &TaskRequest{
		ID: taskID,
		Message: &TaskMessage{
			Role: "user",
			Parts: []*MessagePart{
				{
					Type: "data",
					Data: map[string]any{
						"skillId": skillID,
						"input":   input,
					},
				},
			},
		},
	}

	rpcReq := &jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "tasks/sendSubscribe",
		Params:  req,
		ID:      taskID,
	}

	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	if c.auth != nil {
		if err := c.auth.ApplyAuth(httpReq); err != nil {
			return nil, fmt.Errorf("applying auth: %w", err)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &A2AError{
			Code:    resp.StatusCode,
			Message: string(respBody),
		}
	}

	events := make(chan *TaskEvent)
	go c.streamEvents(ctx, resp, taskID, events)

	return events, nil
}

// Error implements the error interface.
func (e *A2AError) Error() string {
	return fmt.Sprintf("a2a error (code %d): %s", e.Code, e.Message)
}
{{- if .HasSecuritySchemes }}
{{- range .SecuritySchemes }}
{{- if eq .Type "http" }}
{{- if eq .Scheme "bearer" }}

// ApplyAuth implements A2AAuthProvider.
func (a *{{ goify .Name true }}A2AAuth) ApplyAuth(req *http.Request) error {
	if a.Token == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}
{{- else if eq .Scheme "basic" }}

// ApplyAuth implements A2AAuthProvider.
func (a *{{ goify .Name true }}A2AAuth) ApplyAuth(req *http.Request) error {
	if a.Username == "" && a.Password == "" {
		return nil
	}
	req.SetBasicAuth(a.Username, a.Password)
	return nil
}
{{- end }}
{{- end }}
{{- if eq .Type "apiKey" }}

// ApplyAuth implements A2AAuthProvider.
func (a *{{ goify .Name true }}A2AAuth) ApplyAuth(req *http.Request) error {
	if a.Key == "" {
		return nil
	}
	{{- if eq .In "header" }}
	req.Header.Set({{ printf "%q" .ParamName }}, a.Key)
	{{- else if eq .In "query" }}
	q := req.URL.Query()
	q.Set({{ printf "%q" .ParamName }}, a.Key)
	req.URL.RawQuery = q.Encode()
	{{- end }}
	return nil
}
{{- end }}
{{- if eq .Type "oauth2" }}

// ApplyAuth implements A2AAuthProvider.
func (a *{{ goify .Name true }}A2AAuth) ApplyAuth(req *http.Request) error {
	if a.Token == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}
{{- end }}
{{- end }}
{{- end }}

// generateTaskID generates a unique task ID.
func generateTaskID() string {
	return fmt.Sprintf("task-%d", time.Now().UnixNano())
}

// streamEvents reads SSE events from the response and sends them to the channel.
func (c *A2AClient) streamEvents(ctx context.Context, resp *http.Response, taskID string, events chan<- *TaskEvent) {
	defer close(events)
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				events <- &TaskEvent{
					Type:   "error",
					TaskID: taskID,
					Status: &TaskStatus{
						State: "failed",
						Message: &TaskMessage{
							Role:  "system",
							Parts: []*MessagePart{{"{"}}Type: "text", Text: err.Error()}},
						},
					},
					Final: true,
				}
			}
			return
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		// SSE format: "data: {...}"
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		data := bytes.TrimPrefix(line, []byte("data: "))

		var event TaskEvent
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}

		events <- &event

		if event.Final {
			return
		}
	}
}
