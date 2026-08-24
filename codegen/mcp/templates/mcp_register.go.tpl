package {{ .Register.Package }}

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
)

// {{ .Register.HelperName }}ToolSpecs contains the tool specifications for the {{ .Register.SuiteName }} toolset.
var {{ .Register.HelperName }}ToolSpecs = []tools.ToolSpec{
{{- range .Register.Tools }}
	{
		Name:        {{ printf "%q" .ID }},
		Service:     {{ printf "%q" $.Register.ServiceName }},
		Toolset:     {{ printf "%q" $.Register.SuiteQualifiedName }},
		Description: {{ printf "%q" .Description }},
		Payload: tools.TypeSpec{
			Name:        {{ printf "%q" .PayloadType }},
			Schema:      []byte({{ printf "%q" .InputSchema }}),
			ExampleJSON: []byte({{ printf "%q" .ExampleArgs }}),
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
			Name:   {{ printf "%q" .ResultType }},
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
{{- end }}
}

// {{ .Register.HelperName }}ToolMetadata contains canonical policy metadata for the {{ .Register.SuiteName }} toolset.
var {{ .Register.HelperName }}ToolMetadata = []policy.ToolMetadata{
{{- range .Register.Tools }}
	{
		ID:          {{ printf "%q" .ID }},
		Title:       {{ printf "%q" .Title }},
		Description: {{ printf "%q" .Description }},
		BudgetClass: policy.ToolBudgetClassBudgeted,
	},
{{- end }}
}

// {{ .Register.HelperName }}ToolMetadataByName returns canonical policy metadata for the named MCP tool.
func {{ .Register.HelperName }}ToolMetadataByName(name tools.Ident) (policy.ToolMetadata, bool) {
	switch name {
{{- range $i, $tool := .Register.Tools }}
	case {{ printf "%q" $tool.ID }}:
		return {{ $.Register.HelperName }}ToolMetadata[{{ $i }}], true
{{- end }}
	default:
		return policy.ToolMetadata{}, false
	}
}

// Register{{ .Register.HelperName }} registers the {{ .Register.SuiteName }} toolset with the runtime.
// The caller parameter provides the MCP client for making remote calls.
func Register{{ .Register.HelperName }}(ctx context.Context, rt *agentsruntime.Runtime, caller mcpruntime.Caller) error {
	if rt == nil {
		return errors.New("runtime is required")
	}
	if caller == nil {
		return errors.New("mcp caller is required")
	}

	exec := func(ctx context.Context, call agentsruntime.ToolCall) (planner.ToolResult, error) {
		fullName := call.Name
		toolName := string(fullName)
		const suitePrefix = {{ printf "%q" .Register.SuiteQualifiedName }} + "."
		if strings.HasPrefix(toolName, suitePrefix) {
			toolName = toolName[len(suitePrefix):]
		}

		resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
			Suite:   {{ printf "%q" .Register.SuiteQualifiedName }},
			Tool:    toolName,
			Payload: json.RawMessage(call.Payload),
		})
		if err != nil {
			return {{ .Register.HelperName }}HandleError(fullName, call.Payload, err), nil
		}

		var value any
		if len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, &value); err != nil {
				return {{ .Register.HelperName }}HandleError(
					fullName,
					call.Payload,
					mcpruntime.NewMalformedResponseError(err),
				), nil
			}
		}

		var toolTelemetry *telemetry.ToolTelemetry
		if len(resp.Structured) > 0 {
			var structured any
			if err := json.Unmarshal(resp.Structured, &structured); err != nil {
				return {{ .Register.HelperName }}HandleError(
					fullName,
					call.Payload,
					mcpruntime.NewMalformedResponseError(err),
				), nil
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
		Name:        {{ printf "%q" .Register.SuiteQualifiedName }},
		Description: {{ printf "%q" .Register.Description }},
		Execute: func(ctx context.Context, call *agentsruntime.ToolCall) (*agentsruntime.ToolExecutionResult, error) {
			if call == nil {
				return nil, errors.New("tool request is nil")
			}
			out, err := exec(ctx, *call)
			if err != nil {
				return nil, err
			}
			return agentsruntime.Executed(&out), nil
		},
		Specs:            {{ .Register.HelperName }}ToolSpecs,
		ToolMetadataLookup: {{ .Register.HelperName }}ToolMetadataByName,
		DecodeInExecutor: true,
	})
}

// {{ .Register.HelperName }}HandleError converts an MCP failure into the
// canonical classification and recovery transition.
func {{ .Register.HelperName }}HandleError(toolName tools.Ident, input rawjson.Message, err error) planner.ToolResult {
	failure := &planner.ToolFailure{
		Kind:  planner.FailureUnavailable,
		Error: planner.ToolErrorFromError(err),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryReplan,
		},
	}
	if errors.Is(err, context.DeadlineExceeded) {
		failure.Kind = planner.FailureTimeout
		failure.Recovery.Action = planner.RecoveryFinish
		return planner.ToolResult{Name: toolName, Failure: failure}
	}
	var malformed *mcpruntime.MalformedResponseError
	if errors.As(err, &malformed) {
		failure.Kind = planner.FailureMalformedResult
		failure.Recovery.Action = planner.RecoveryFinish
		return planner.ToolResult{Name: toolName, Failure: failure}
	}
	var internal *mcpruntime.InternalError
	if errors.As(err, &internal) {
		failure.Kind = planner.FailureInternal
		failure.Recovery.Action = planner.RecoveryFinish
		return planner.ToolResult{Name: toolName, Failure: failure}
	}
	var execution *mcpruntime.ToolExecutionError
	if errors.As(err, &execution) {
		failure.Kind = planner.FailureDomainRejection
		return planner.ToolResult{Name: toolName, Failure: failure}
	}
	var rpcErr *mcpruntime.Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case mcpruntime.JSONRPCInvalidParams:
			failure = {{ .Register.HelperName }}CorrectionFailure(string(toolName), input, err)
		case mcpruntime.JSONRPCMethodNotFound:
			failure = &planner.ToolFailure{
				Kind:  planner.FailureInvalidCall,
				Error: planner.ToolErrorFromError(err),
				Recovery: planner.RecoveryDirective{
					Action: planner.RecoveryReplan,
				},
			}
		}
	}
	return planner.ToolResult{Name: toolName, Failure: failure}
}

// {{ .Register.HelperName }}CorrectionFailure attaches the exact rejected input
// and the generated example for one MCP tool payload.
func {{ .Register.HelperName }}CorrectionFailure(toolName string, input rawjson.Message, err error) *planner.ToolFailure {
	var example rawjson.Message
	switch toolName {
{{- range .Register.Tools }}
	case {{ printf "%q" .ID }}:
		example = rawjson.Message({{ printf "%q" .ExampleArgs }})
{{- end }}
	}
	return &planner.ToolFailure{
		Kind:  planner.FailureInvalidCall,
		Error: planner.ToolErrorFromError(err),
		Recovery: planner.RecoveryDirective{
			Action:      planner.RecoveryCorrectCall,
			PriorInput:  append(rawjson.Message(nil), input...),
			ExampleJSON: example,
		},
	}
}
