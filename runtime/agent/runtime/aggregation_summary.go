package runtime

import (
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// AggregationSummary is the provider-facing summary payload for provider-owned
	// result finalizers. The runtime exports only the stable types; it no longer
	// builds parent-side aggregation behavior itself.
	AggregationSummary struct {
		Method     tools.Ident        `json:"method"`
		ToolCallID string             `json:"tool_call_id,omitempty"`
		Payload    rawjson.Message    `json:"payload,omitempty"`
		Children   []AggregationChild `json:"children"`
	}

	// AggregationChild captures one child tool outcome in a provider-facing
	// aggregation summary.
	AggregationChild struct {
		Tool    tools.Ident          `json:"tool"`
		Status  string               `json:"status"`
		Result  rawjson.Message      `json:"result,omitempty"`
		Failure *planner.ToolFailure `json:"failure,omitempty"`
	}
)
