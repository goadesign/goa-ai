package runtime

import "goa.design/goa-ai/runtime/agent/tools"

type (
	// AggregationSummary summarizes a parent tool call and its child results for tool-based finalizers.
	AggregationSummary struct {
		Method     tools.Ident        `json:"method"`
		ToolCallID string             `json:"tool_call_id,omitempty"`
		Payload    any                `json:"payload,omitempty"`
		Children   []AggregationChild `json:"children"`
	}

	// AggregationChild captures one child tool outcome for aggregation.
	AggregationChild struct {
		Tool   tools.Ident `json:"tool"`
		Status string      `json:"status"`
		Result any         `json:"result,omitempty"`
		Error  string      `json:"error,omitempty"`
	}
)

// BuildAggregationSummary constructs an AggregationSummary payload from the provided parent/child snapshots.
func BuildAggregationSummary(input *FinalizerInput) *AggregationSummary {
	summary := &AggregationSummary{
		Method:     input.Parent.ToolName,
		ToolCallID: input.Parent.ToolCallID,
		Payload:    input.Parent.Payload,
		Children:   make([]AggregationChild, 0, len(input.Children)),
	}
	for _, child := range input.Children {
		item := AggregationChild{
			Tool:   child.ToolName,
			Status: child.Status,
			Result: child.Result,
		}
		if child.Error != nil {
			item.Error = child.Error.Error()
			if item.Status == "" {
				item.Status = "error"
			}
		}
		if item.Status == "" {
			item.Status = "ok"
		}
		summary.Children = append(summary.Children, item)
	}
	return summary
}
