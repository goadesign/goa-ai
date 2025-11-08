package runtime

// ProvenancedResult is a standard envelope for tool results that carries
// the schema-typed data alongside provenance metadata. It is transport-
// agnostic and can be produced by both service-backed tools and agent-as-tool
// aggregations to provide a consistent UI/telemetry surface.
type ProvenancedResult struct {
	// Data is the schema-typed tool result or an aggregate (e.g., list) when
	// representing multiple child results from an agent-as-tool aggregation.
	Data any `json:"data"`
	// Provenance carries execution metadata useful to UIs and observability.
	Provenance struct {
		Tool       string         `json:"tool"`
		DurationMs int64          `json:"duration_ms,omitempty"`
		Attempts   int            `json:"attempts,omitempty"`
		Model      string         `json:"model,omitempty"`
		Service    string         `json:"service,omitempty"`
		Children   []string       `json:"children,omitempty"`
		Extra      map[string]any `json:"extra,omitempty"`
	} `json:"provenance"`
}
