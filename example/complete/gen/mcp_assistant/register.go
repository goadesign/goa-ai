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
		Description: "Analyze text sentiment",
		Payload: tools.TypeSpec{
			Name:   "*assistant.AnalyzeSentimentPayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"text\":{\"maxLength\":10000,\"minLength\":1,\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}"),
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
			Name:   "*assistant.SentimentResult",
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
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"text\":{\"maxLength\":10000,\"minLength\":1,\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}"),
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
			Name:   "*assistant.KeywordsResult",
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
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"text\":{\"maxLength\":10000,\"minLength\":1,\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}"),
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
			Name:   "*assistant.SummaryResult",
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
		Description: "Search the knowledge base",
		Payload: tools.TypeSpec{
			Name:   "*assistant.SearchKnowledgePayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"limit\":{\"maximum\":100,\"minimum\":1,\"type\":\"integer\"},\"query\":{\"maxLength\":256,\"minLength\":1,\"type\":\"string\"}},\"required\":[\"query\"],\"type\":\"object\"}"),
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
			Name:   "assistant.SearchResults",
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
		Description: "Execute code safely in sandbox",
		Payload: tools.TypeSpec{
			Name:   "*assistant.ExecuteCodePayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"code\":{\"maxLength\":20000,\"minLength\":1,\"type\":\"string\"},\"language\":{\"enum\":[\"python\",\"javascript\",\"go\"],\"type\":\"string\"}},\"required\":[\"language\",\"code\"],\"type\":\"object\"}"),
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
			Name:   "*assistant.ExecutionResult",
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
		Description: "Process items with progress updates",
		Payload: tools.TypeSpec{
			Name:   "*assistant.ProcessBatchPayload",
			Schema: []byte("{\"additionalProperties\":false,\"properties\":{\"blob\":{\"contentEncoding\":\"base64\",\"type\":\"string\"},\"format\":{\"enum\":[\"text\",\"blob\",\"uri\"],\"type\":\"string\"},\"items\":{\"items\":{\"type\":\"string\"},\"minItems\":1,\"type\":\"array\"},\"mimeType\":{\"type\":\"string\"},\"uri\":{\"type\":\"string\"}},\"required\":[\"items\"],\"type\":\"object\"}"),
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
			Name:   "*assistant.BatchResult",
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
	"assistant.assistant-mcp.analyze_sentiment": "{\"additionalProperties\":false,\"properties\":{\"text\":{\"maxLength\":10000,\"minLength\":1,\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.extract_keywords":  "{\"additionalProperties\":false,\"properties\":{\"text\":{\"maxLength\":10000,\"minLength\":1,\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.summarize_text":    "{\"additionalProperties\":false,\"properties\":{\"text\":{\"maxLength\":10000,\"minLength\":1,\"type\":\"string\"}},\"required\":[\"text\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.search":            "{\"additionalProperties\":false,\"properties\":{\"limit\":{\"maximum\":100,\"minimum\":1,\"type\":\"integer\"},\"query\":{\"maxLength\":256,\"minLength\":1,\"type\":\"string\"}},\"required\":[\"query\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.execute_code":      "{\"additionalProperties\":false,\"properties\":{\"code\":{\"maxLength\":20000,\"minLength\":1,\"type\":\"string\"},\"language\":{\"enum\":[\"python\",\"javascript\",\"go\"],\"type\":\"string\"}},\"required\":[\"language\",\"code\"],\"type\":\"object\"}",
	"assistant.assistant-mcp.process_batch":     "{\"additionalProperties\":false,\"properties\":{\"blob\":{\"contentEncoding\":\"base64\",\"type\":\"string\"},\"format\":{\"enum\":[\"text\",\"blob\",\"uri\"],\"type\":\"string\"},\"items\":{\"items\":{\"type\":\"string\"},\"minItems\":1,\"type\":\"array\"},\"mimeType\":{\"type\":\"string\"},\"uri\":{\"type\":\"string\"}},\"required\":[\"items\"],\"type\":\"object\"}",
}

var AssistantAssistantMcpToolsetToolExamples = map[string]string{
	"assistant.assistant-mcp.analyze_sentiment": "{\"text\":\"I love this new feature! It works perfectly.\"}",
	"assistant.assistant-mcp.extract_keywords":  "{\"text\":\"Machine learning algorithms process data to identify patterns.\"}",
	"assistant.assistant-mcp.summarize_text":    "{\"text\":\"This is a long text that needs to be summarized.\"}",
	"assistant.assistant-mcp.search":            "{\"limit\":5,\"query\":\"MCP protocol\"}",
	"assistant.assistant-mcp.execute_code":      "{\"code\":\"print(2 + 2)\",\"language\":\"python\"}",
	"assistant.assistant-mcp.process_batch":     "{\"blob\":\"aGVsbG8=\",\"format\":\"text\",\"items\":[\"item1\",\"item2\"],\"mimeType\":\"text/plain\",\"uri\":\"system://info\"}",
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
		toolName := fullName
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

func AssistantAssistantMcpToolsetHandleError(toolName string, err error) planner.ToolResult {
	result := planner.ToolResult{
		Name:  toolName,
		Error: planner.ToolErrorFromError(err),
	}
	if hint := AssistantAssistantMcpToolsetRetryHint(toolName, err); hint != nil {
		result.RetryHint = hint
	}
	return result
}

func AssistantAssistantMcpToolsetRetryHint(toolName string, err error) *planner.RetryHint {
	schema := AssistantAssistantMcpToolsetToolSchemas[toolName]
	example := AssistantAssistantMcpToolsetToolExamples[toolName]
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
			prompt := retry.BuildRepairPrompt("tools/call:"+toolName, rpcErr.Message, example, schema)
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
