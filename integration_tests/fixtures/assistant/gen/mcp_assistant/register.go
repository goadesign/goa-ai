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

var AssistantAssistantMcpToolsetToolSpecs = []tools.ToolSpec{
	{
		Name:        "assistant.assistant-mcp.analyze_sentiment",
		Service:     "assistant",
		Toolset:     "assistant-mcp",
		Description: "Analyze sentiment of text",
		Payload: tools.TypeSpec{
			Name:   "*assistant.AnalyzeSentimentPayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"text\":{\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}"),
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
		Name:        "assistant.assistant-mcp.extract_keywords",
		Service:     "assistant",
		Toolset:     "assistant-mcp",
		Description: "Extract keywords from text",
		Payload: tools.TypeSpec{
			Name:   "*assistant.ExtractKeywordsPayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"text\":{\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}"),
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
		Name:        "assistant.assistant-mcp.summarize_text",
		Service:     "assistant",
		Toolset:     "assistant-mcp",
		Description: "Summarize text",
		Payload: tools.TypeSpec{
			Name:   "*assistant.SummarizeTextPayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"text\":{\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}"),
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
		Name:        "assistant.assistant-mcp.search",
		Service:     "assistant",
		Toolset:     "assistant-mcp",
		Description: "Search knowledge base",
		Payload: tools.TypeSpec{
			Name:   "*assistant.SearchPayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"limit\":{\"type\":\"integer\"},\"query\":{\"type\":\"string\"}},\"required\":[\"query\"],\"type\":\"object\"}"),
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
		Name:        "assistant.assistant-mcp.execute_code",
		Service:     "assistant",
		Toolset:     "assistant-mcp",
		Description: "Execute code",
		Payload: tools.TypeSpec{
			Name:   "*assistant.ExecuteCodePayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"code\":{\"type\":\"string\"},\"language\":{\"enum\":[\"python\",\"javascript\"],\"type\":\"string\"}},\"required\":[\"language\",\"code\"],\"type\":\"object\"}"),
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
		Name:        "assistant.assistant-mcp.process_batch",
		Service:     "assistant",
		Toolset:     "assistant-mcp",
		Description: "Process a batch of items",
		Payload: tools.TypeSpec{
			Name:   "*assistant.ProcessBatchPayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"blob\":{\"type\":\"string\"},\"format\":{\"enum\":[\"json\",\"text\",\"blob\",\"uri\"],\"type\":\"string\"},\"items\":{\"items\":{\"type\":\"string\"},\"type\":\"array\"},\"mimeType\":{\"type\":\"string\"},\"uri\":{\"type\":\"string\"}},\"required\":[\"items\"],\"type\":\"object\"}"),
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

var AssistantAssistantMcpToolsetToolSchemas = map[string]string{
	"assistant.assistant-mcp.analyze_sentiment": "{\"additionalProperties\":false,\"properties\":{\"text\":{\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.extract_keywords":  "{\"additionalProperties\":false,\"properties\":{\"text\":{\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.summarize_text":    "{\"additionalProperties\":false,\"properties\":{\"text\":{\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.search":            "{\"additionalProperties\":false,\"properties\":{\"limit\":{\"type\":\"integer\"},\"query\":{\"type\":\"string\"}},\"required\":[\"query\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.execute_code":      "{\"additionalProperties\":false,\"properties\":{\"code\":{\"type\":\"string\"},\"language\":{\"enum\":[\"python\",\"javascript\"],\"type\":\"string\"}},\"required\":[\"language\",\"code\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.process_batch":     "{\"additionalProperties\":false,\"properties\":{\"blob\":{\"type\":\"string\"},\"format\":{\"enum\":[\"json\",\"text\",\"blob\",\"uri\"],\"type\":\"string\"},\"items\":{\"items\":{\"type\":\"string\"},\"type\":\"array\"},\"mimeType\":{\"type\":\"string\"},\"uri\":{\"type\":\"string\"}},\"required\":[\"items\"],\"type\":\"object\"}",
}

var AssistantAssistantMcpToolsetToolExamples = map[string]string{
	"assistant.assistant-mcp.analyze_sentiment": "{\"text\":\"abc123\"}",
	"assistant.assistant-mcp.extract_keywords":  "{\"text\":\"abc123\"}",
	"assistant.assistant-mcp.summarize_text":    "{\"text\":\"abc123\"}",
	"assistant.assistant-mcp.search":            "{\"limit\":1,\"query\":\"abc123\"}",
	"assistant.assistant-mcp.execute_code":      "{\"code\":\"abc123\",\"language\":\"javascript\"}",
	"assistant.assistant-mcp.process_batch":     "{\"blob\":\"abc123\",\"format\":\"text\",\"items\":[\"abc123\"],\"mimeType\":\"abc123\",\"uri\":\"abc123\"}",
}

func RegisterAssistantAssistantMcpToolset(ctx context.Context, rt *agentsruntime.Runtime, caller mcpruntime.Caller) error {
	if rt == nil {
		return errors.New("runtime is required")
	}
	if caller == nil {
		return errors.New("mcp caller is required")
	}
	suite := "assistant.assistant-mcp"
	suitePrefix := suite + "."

	exec := func(ctx context.Context, call planner.ToolRequest) (planner.ToolResult, error) {
		fullName := call.Name
		toolName := string(fullName)
		if strings.HasPrefix(toolName, suitePrefix) {
			toolName = toolName[len(suitePrefix):]
		}

		payload, err := json.Marshal(call.Payload)
		if err != nil {
			return planner.ToolResult{
				Name: fullName,
			}, err
		}

		resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
			Suite:   suite,
			Tool:    toolName,
			Payload: payload,
		})
		if err != nil {
			return AssistantAssistantMcpToolsetHandleError(fullName, err), nil
		}

		var value any
		if len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, &value); err != nil {
				return planner.ToolResult{
					Name: fullName,
				}, err
			}
		}

		var toolTelemetry *telemetry.ToolTelemetry
		if len(resp.Structured) > 0 {
			var structured any
			if err := json.Unmarshal(resp.Structured, &structured); err != nil {
				return planner.ToolResult{
					Name: fullName,
				}, err
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
		Execute:     exec,
		Specs:       AssistantAssistantMcpToolsetToolSpecs,
	})
}

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

func AssistantAssistantMcpToolsetRetryHint(toolName tools.Ident, err error) *planner.RetryHint {
	key := string(toolName)
	schema := AssistantAssistantMcpToolsetToolSchemas[key]
	example := AssistantAssistantMcpToolsetToolExamples[key]
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
			prompt := retry.BuildRepairPrompt("tools/call:"+key, rpcErr.Message, example, schema)
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
