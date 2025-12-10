package mcpassistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"goa.design/goa-ai/runtime/agent/planner"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
	"goa.design/goa-ai/runtime/mcp/retry"
)

// AssistantAssistantMcpToolsetToolSpecs contains the tool specifications for the assistant-mcp toolset.
var AssistantAssistantMcpToolsetToolSpecs = []tools.ToolSpec{
	{
		Name:        "analyze_sentiment",
		Service:     "assistant",
		Toolset:     "assistant.assistant-mcp",
		Description: "Analyze sentiment of text",
		Payload: tools.TypeSpec{
			Name:   "*assistant.AnalyzeSentimentPayload",
			Schema: []byte("{\"type\":\"object\",\"required\":[\"text\"],\"properties\":{\"text\":{\"type\":\"string\",\"description\":\"Input text to analyze\"}},\"additionalProperties\":false}"),
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
		Result: tools.TypeSpec{
			Name:   "*assistant.AnalyzeSentimentResult",
			Schema: nil,
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
	},
	{
		Name:        "extract_keywords",
		Service:     "assistant",
		Toolset:     "assistant.assistant-mcp",
		Description: "Extract keywords from text",
		Payload: tools.TypeSpec{
			Name:   "*assistant.ExtractKeywordsPayload",
			Schema: []byte("{\"type\":\"object\",\"required\":[\"text\"],\"properties\":{\"text\":{\"type\":\"string\",\"description\":\"Input text\"}},\"additionalProperties\":false}"),
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
		Result: tools.TypeSpec{
			Name:   "*assistant.ExtractKeywordsResult",
			Schema: nil,
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
	},
	{
		Name:        "summarize_text",
		Service:     "assistant",
		Toolset:     "assistant.assistant-mcp",
		Description: "Summarize text",
		Payload: tools.TypeSpec{
			Name:   "*assistant.SummarizeTextPayload",
			Schema: []byte("{\"type\":\"object\",\"required\":[\"text\"],\"properties\":{\"text\":{\"type\":\"string\",\"description\":\"Input text to summarize\"}},\"additionalProperties\":false}"),
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
		Result: tools.TypeSpec{
			Name:   "*assistant.SummarizeTextResult",
			Schema: nil,
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
	},
	{
		Name:        "search",
		Service:     "assistant",
		Toolset:     "assistant.assistant-mcp",
		Description: "Search knowledge base",
		Payload: tools.TypeSpec{
			Name:   "*assistant.SearchPayload",
			Schema: []byte("{\"type\":\"object\",\"required\":[\"query\"],\"properties\":{\"limit\":{\"type\":\"integer\",\"description\":\"Maximum number of results\"},\"query\":{\"type\":\"string\",\"description\":\"Search query\"}},\"additionalProperties\":false}"),
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
		Result: tools.TypeSpec{
			Name:   "*assistant.SearchResult",
			Schema: nil,
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
	},
	{
		Name:        "execute_code",
		Service:     "assistant",
		Toolset:     "assistant.assistant-mcp",
		Description: "Execute code",
		Payload: tools.TypeSpec{
			Name:   "*assistant.ExecuteCodePayload",
			Schema: []byte("{\"type\":\"object\",\"required\":[\"language\",\"code\"],\"properties\":{\"code\":{\"type\":\"string\",\"description\":\"Code to execute\"},\"language\":{\"type\":\"string\",\"description\":\"Language to execute\",\"enum\":[\"python\",\"javascript\"]}},\"additionalProperties\":false}"),
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
		Result: tools.TypeSpec{
			Name:   "*assistant.ExecuteCodeResult",
			Schema: nil,
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
	},
	{
		Name:        "process_batch",
		Service:     "assistant",
		Toolset:     "assistant.assistant-mcp",
		Description: "Process a batch of items",
		Payload: tools.TypeSpec{
			Name:   "*assistant.ProcessBatchPayload",
			Schema: []byte("{\"type\":\"object\",\"required\":[\"items\"],\"properties\":{\"blob\":{\"type\":\"string\",\"description\":\"Base64 blob\"},\"format\":{\"type\":\"string\",\"description\":\"Output format\",\"enum\":[\"json\",\"text\",\"blob\",\"uri\"]},\"items\":{\"type\":\"array\",\"description\":\"Items to process\",\"items\":{\"type\":\"string\"}},\"mimeType\":{\"type\":\"string\",\"description\":\"MIME type\"},\"uri\":{\"type\":\"string\",\"description\":\"Resource URI\"}},\"additionalProperties\":false}"),
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
		Result: tools.TypeSpec{
			Name:   "*assistant.ProcessBatchResult",
			Schema: nil,
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
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
			},
		},
	},
}

// RegisterAssistantAssistantMcpToolset registers the assistant-mcp toolset with the runtime.
// The caller parameter provides the MCP client for making remote calls.
func RegisterAssistantAssistantMcpToolset(ctx context.Context, rt *agentsruntime.Runtime, caller mcpruntime.Caller) error {
	if rt == nil {
		return errors.New("runtime is required")
	}
	if caller == nil {
		return errors.New("mcp caller is required")
	}

	exec := func(ctx context.Context, call planner.ToolRequest) (planner.ToolResult, error) {
		fullName := call.Name
		toolName := string(fullName)
		const suitePrefix = "assistant.assistant-mcp" + "."
		if strings.HasPrefix(toolName, suitePrefix) {
			toolName = toolName[len(suitePrefix):]
		}

		payload, err := json.Marshal(call.Payload)
		if err != nil {
			return planner.ToolResult{Name: fullName}, err
		}

		resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
			Suite:   "assistant.assistant-mcp",
			Tool:    toolName,
			Payload: payload,
		})
		if err != nil {
			return AssistantAssistantMcpToolsetHandleError(fullName, err), nil
		}

		var value any
		if len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, &value); err != nil {
				return planner.ToolResult{Name: fullName}, err
			}
		}

		var toolTelemetry *telemetry.ToolTelemetry
		if len(resp.Structured) > 0 {
			var structured any
			if err := json.Unmarshal(resp.Structured, &structured); err != nil {
				return planner.ToolResult{Name: fullName}, err
			}
			toolTelemetry = &telemetry.ToolTelemetry{
				Extra: map[string]any{"structured": structured},
			}
		}

		return planner.ToolResult{
			Name:      fullName,
			Result:    value,
			Telemetry: toolTelemetry,
		}, nil
	}

	return rt.RegisterToolset(agentsruntime.ToolsetRegistration{
		Name:        "assistant.assistant-mcp",
		Description: "AI Assistant service with full MCP protocol support",
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			if call == nil {
				return nil, errors.New("tool request is nil")
			}
			out, err := exec(ctx, *call)
			if err != nil {
				return nil, err
			}
			return &out, nil
		},
		Specs:            AssistantAssistantMcpToolsetToolSpecs,
		DecodeInExecutor: true,
	})
}

// AssistantAssistantMcpToolsetHandleError converts an error into a tool result with appropriate retry hints.
func AssistantAssistantMcpToolsetHandleError(toolName tools.Ident, err error) planner.ToolResult {
	result := planner.ToolResult{
		Name:  toolName,
		Error: planner.ToolErrorFromError(err),
	}
	if hint := AssistantAssistantMcpToolsetRetryHint(toolName, err); hint != nil {
		result.RetryHint = hint
	}
	return result
}

// AssistantAssistantMcpToolsetRetryHint determines if an error should trigger a retry and returns appropriate hints.
func AssistantAssistantMcpToolsetRetryHint(toolName tools.Ident, err error) *planner.RetryHint {
	key := string(toolName)
	var retryErr *retry.RetryableError
	if errors.As(err, &retryErr) {
		return &planner.RetryHint{
			Reason:         planner.RetryReasonInvalidArguments,
			Tool:           toolName,
			Message:        retryErr.Prompt,
			RestrictToTool: true,
		}
	}
	var rpcErr *mcpruntime.Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case mcpruntime.JSONRPCInvalidParams:
			// Schema and example are known at generation time - use switch for direct lookup
			var schemaJSON, example string
			switch key {
			case "analyze_sentiment":
				schemaJSON = "{\"type\":\"object\",\"required\":[\"text\"],\"properties\":{\"text\":{\"type\":\"string\",\"description\":\"Input text to analyze\"}},\"additionalProperties\":false}"
				example = "{\"text\":\"abc123\"}"
			case "extract_keywords":
				schemaJSON = "{\"type\":\"object\",\"required\":[\"text\"],\"properties\":{\"text\":{\"type\":\"string\",\"description\":\"Input text\"}},\"additionalProperties\":false}"
				example = "{\"text\":\"abc123\"}"
			case "summarize_text":
				schemaJSON = "{\"type\":\"object\",\"required\":[\"text\"],\"properties\":{\"text\":{\"type\":\"string\",\"description\":\"Input text to summarize\"}},\"additionalProperties\":false}"
				example = "{\"text\":\"abc123\"}"
			case "search":
				schemaJSON = "{\"type\":\"object\",\"required\":[\"query\"],\"properties\":{\"limit\":{\"type\":\"integer\",\"description\":\"Maximum number of results\"},\"query\":{\"type\":\"string\",\"description\":\"Search query\"}},\"additionalProperties\":false}"
				example = "{\"limit\":1,\"query\":\"abc123\"}"
			case "execute_code":
				schemaJSON = "{\"type\":\"object\",\"required\":[\"language\",\"code\"],\"properties\":{\"code\":{\"type\":\"string\",\"description\":\"Code to execute\"},\"language\":{\"type\":\"string\",\"description\":\"Language to execute\",\"enum\":[\"python\",\"javascript\"]}},\"additionalProperties\":false}"
				example = "{\"code\":\"abc123\",\"language\":\"javascript\"}"
			case "process_batch":
				schemaJSON = "{\"type\":\"object\",\"required\":[\"items\"],\"properties\":{\"blob\":{\"type\":\"string\",\"description\":\"Base64 blob\"},\"format\":{\"type\":\"string\",\"description\":\"Output format\",\"enum\":[\"json\",\"text\",\"blob\",\"uri\"]},\"items\":{\"type\":\"array\",\"description\":\"Items to process\",\"items\":{\"type\":\"string\"}},\"mimeType\":{\"type\":\"string\",\"description\":\"MIME type\"},\"uri\":{\"type\":\"string\",\"description\":\"Resource URI\"}},\"additionalProperties\":false}"
				example = "{\"blob\":\"abc123\",\"format\":\"text\",\"items\":[\"abc123\"],\"mimeType\":\"abc123\",\"uri\":\"abc123\"}"
			}
			prompt := retry.BuildRepairPrompt("tools/call:"+key, rpcErr.Message, example, schemaJSON)
			return &planner.RetryHint{
				Reason:         planner.RetryReasonInvalidArguments,
				Tool:           toolName,
				Message:        prompt,
				RestrictToTool: true,
			}
		case mcpruntime.JSONRPCMethodNotFound:
			return &planner.RetryHint{
				Reason:  planner.RetryReasonToolUnavailable,
				Tool:    toolName,
				Message: rpcErr.Message,
			}
		}
	}
	return nil
}
