// ToolsetInfo contains metadata about a toolset available in the registry.
type ToolsetInfo struct {
	// ID is the unique identifier for the toolset.
	ID string `json:"id"`
	// Name is the human-readable name.
	Name string `json:"name"`
	// Description provides details about the toolset.
	Description string `json:"description,omitempty"`
	// Version is the toolset version.
	Version string `json:"version,omitempty"`
	// Tags are metadata tags for discovery.
	Tags []string `json:"tags,omitempty"`
}

// ToolsetSchema contains the full schema for a toolset including its tools.
type ToolsetSchema struct {
	// ID is the unique identifier for the toolset.
	ID string `json:"id"`
	// Name is the human-readable name.
	Name string `json:"name"`
	// Description provides details about the toolset.
	Description string `json:"description,omitempty"`
	// Version is the toolset version.
	Version string `json:"version,omitempty"`
	// Tools contains the tool definitions.
	Tools []*ToolSchema `json:"tools,omitempty"`
}

// ToolSchema contains the schema for a single tool.
type ToolSchema struct {
	// Name is the tool identifier.
	Name string `json:"name"`
	// Description explains what the tool does.
	Description string `json:"description,omitempty"`
	// InputSchema is the JSON Schema for tool input.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// SearchResult contains a single search result from the registry.
type SearchResult struct {
	// ID is the unique identifier.
	ID string `json:"id"`
	// Name is the human-readable name.
	Name string `json:"name"`
	// Description provides details.
	Description string `json:"description,omitempty"`
	// Type indicates the result type (e.g., "tool", "toolset", "agent").
	Type string `json:"type"`
	// SchemaRef is a reference to the full schema.
	SchemaRef string `json:"schemaRef,omitempty"`
	// RelevanceScore indicates how relevant this result is to the query.
	RelevanceScore float64 `json:"relevanceScore"`
	// Tags are metadata tags.
	Tags []string `json:"tags,omitempty"`
	// Origin indicates the federation source if applicable.
	Origin string `json:"origin,omitempty"`
}

// AuthProvider provides authentication credentials for registry requests.
type AuthProvider interface {
	// ApplyAuth adds authentication to the request.
	ApplyAuth(req *http.Request) error
}

// Client is a typed client for the {{ .Name }} registry.
type Client struct {
	endpoint   string
	httpClient *http.Client
	auth       AuthProvider
	timeout    time.Duration
	retryMax   int
	retryBase  time.Duration
}

// SemanticSearchOptions configures semantic search behavior.
type SemanticSearchOptions struct {
	// Types filters results by type.
	Types []string
	// Tags filters results by tags.
	Tags []string
	// MaxResults limits the number of results.
	MaxResults int
}

// SearchCapabilities describes what search features the registry supports.
type SearchCapabilities struct {
	// SemanticSearch indicates if the registry supports semantic/vector search.
	SemanticSearch bool
	// KeywordSearch indicates if the registry supports keyword-based search.
	KeywordSearch bool
	// TagFiltering indicates if the registry supports filtering by tags.
	TagFiltering bool
	// TypeFiltering indicates if the registry supports filtering by type.
	TypeFiltering bool
}

// RegistryError represents an error response from the registry.
type RegistryError struct {
	StatusCode int
	Message    string
}

// bytesReader is a simple io.Reader for byte slices.
type bytesReader struct {
	data []byte
	pos  int
}

// Static URL path constants for the {{ .Name }} registry.
// These are generated at compile time based on the registry's API version.
const (
	// pathToolsets is the base path for toolset operations.
	pathToolsets = "/{{ .APIVersion }}/toolsets"
	// pathSearch is the path for keyword search.
	pathSearch = "/{{ .APIVersion }}/search"
	// pathSemanticSearch is the path for semantic search.
	pathSemanticSearch = "/{{ .APIVersion }}/search/semantic"
	// pathCapabilities is the path for capabilities endpoint.
	pathCapabilities = "/{{ .APIVersion }}/capabilities"
)

// NewClient creates a new registry client with the given options.
func NewClient(opts ...Option) *Client {
	c := &Client{
		endpoint:   {{ printf "%q" .URL }},
		httpClient: http.DefaultClient,
		{{- if gt .Timeout 0 }}
		timeout:    time.Duration({{ printf "%d" .Timeout }}),
		{{- else }}
		timeout:    30 * time.Second,
		{{- end }}
		{{- if .RetryPolicy }}
		retryMax:   {{ .RetryPolicy.MaxRetries }},
		retryBase:  time.Duration({{ printf "%d" .RetryPolicy.BackoffBase }}),
		{{- else }}
		retryMax:   3,
		retryBase:  time.Second,
		{{- end }}
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// ListToolsets returns all available toolsets from the registry.
func (c *Client) ListToolsets(ctx context.Context) ([]*ToolsetInfo, error) {
	u := c.endpoint + pathToolsets

	var result []*ToolsetInfo
	if err := c.doRequest(ctx, http.MethodGet, u, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetToolset retrieves the full schema for a specific toolset.
func (c *Client) GetToolset(ctx context.Context, name string) (*ToolsetSchema, error) {
	u := c.endpoint + pathToolsets + "/" + name

	var result ToolsetSchema
	if err := c.doRequest(ctx, http.MethodGet, u, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Search performs a keyword search on the registry.
// This is the basic search method that all registries support.
func (c *Client) Search(ctx context.Context, query string) ([]*SearchResult, error) {
	u := c.endpoint + pathSearch + "?q=" + url.QueryEscape(query)

	var result []*SearchResult
	if err := c.doRequest(ctx, http.MethodGet, u, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// SemanticSearch performs a semantic/vector search on the registry.
// This method uses the registry's semantic search endpoint if available.
// Falls back to keyword search if semantic search is not supported.
func (c *Client) SemanticSearch(ctx context.Context, query string, opts SemanticSearchOptions) ([]*SearchResult, error) {
	// Build query string
	q := url.Values{}
	q.Set("q", query)
	if opts.MaxResults > 0 {
		q.Set("limit", fmt.Sprintf("%d", opts.MaxResults))
	}
	for _, t := range opts.Types {
		q.Add("type", t)
	}
	for _, tag := range opts.Tags {
		q.Add("tag", tag)
	}
	u := c.endpoint + pathSemanticSearch + "?" + q.Encode()

	var result []*SearchResult
	if err := c.doRequest(ctx, http.MethodGet, u, nil, &result); err != nil {
		// Check if semantic search is not supported (404 or 501)
		var regErr *RegistryError
		if errors.As(err, &regErr) && (regErr.StatusCode == http.StatusNotFound || regErr.StatusCode == http.StatusNotImplemented) {
			// Fall back to keyword search
			return c.Search(ctx, query)
		}
		return nil, err
	}
	return result, nil
}

// Capabilities returns the search capabilities of this registry.
// This queries the registry's capabilities endpoint to determine
// what search features are supported.
func (c *Client) Capabilities() SearchCapabilities {
	// Default capabilities - all registries support keyword search
	caps := SearchCapabilities{
		KeywordSearch:  true,
		SemanticSearch: false,
		TagFiltering:   true,
		TypeFiltering:  true,
	}

	// Try to fetch capabilities from the registry
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	u := c.endpoint + pathCapabilities

	var remoteCaps SearchCapabilities
	if err := c.doRequest(ctx, http.MethodGet, u, nil, &remoteCaps); err != nil {
		// If capabilities endpoint doesn't exist, return defaults
		return caps
	}

	// Merge remote capabilities (keyword search is always true)
	remoteCaps.KeywordSearch = true
	return remoteCaps
}

// doRequest performs an HTTP request with retry logic.
func (c *Client) doRequest(ctx context.Context, method, reqURL string, body []byte, result any) error {
	var lastErr error
	for attempt := 0; attempt <= c.retryMax; attempt++ {
		if attempt > 0 {
			// Exponential backoff
			backoff := c.retryBase * time.Duration(1<<uint(attempt-1))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		err := c.doSingleRequest(ctx, method, reqURL, body, result)
		if err == nil {
			return nil
		}
		lastErr = err

		// Don't retry on context cancellation or client errors
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return lastErr
}

func (c *Client) doSingleRequest(ctx context.Context, method, reqURL string, body []byte, result any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var bodyReader io.Reader
	if body != nil {
		bodyReader = &bytesReader{data: body}
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	// Inject trace context for distributed tracing
	registry.InjectTraceContext(ctx, req.Header)

	if c.auth != nil {
		if err := c.auth.ApplyAuth(req); err != nil {
			return fmt.Errorf("applying auth: %w", err)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return &RegistryError{
			StatusCode: resp.StatusCode,
			Message:    string(respBody),
		}
	}

	if result != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}

func (e *RegistryError) Error() string {
	return fmt.Sprintf("registry error (status %d): %s", e.StatusCode, e.Message)
}

func (r *bytesReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
