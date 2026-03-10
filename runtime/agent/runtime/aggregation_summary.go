package runtime

import "goa.design/goa-ai/runtime/agent/tools"

type (
	// AggregationSummary is the provider-facing summary payload for provider-owned
	// result finalizers. The runtime exports only the stable types; it no longer
	// builds parent-side aggregation behavior itself.
	AggregationSummary struct {
		Method     tools.Ident        `json:"method"`
		ToolCallID string             `json:"tool_call_id,omitempty"`
		Payload    any                `json:"payload,omitempty"`
		Children   []AggregationChild `json:"children"`
	}

	// AggregationChild captures one child tool outcome in a provider-facing
	// aggregation summary.
	AggregationChild struct {
		Tool   tools.Ident `json:"tool"`
		Status string      `json:"status"`
		Result any         `json:"result,omitempty"`
		Error  string      `json:"error,omitempty"`
	}
)
