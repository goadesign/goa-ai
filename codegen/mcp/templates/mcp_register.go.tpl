// {{ .Register.HelperName }}ToolSpecs contains the tool specifications for the {{ .Register.SuiteName }} toolset.
var {{ .Register.HelperName }}ToolSpecs = []tools.ToolSpec{
{{- range .Register.Tools }}
	{
		Name:        {{ printf "%q" .ID }},
		Description: {{ printf "%q" .Description }},
		Payload: tools.TypeSpec{
			Name:        {{ printf "%q" .PayloadType }},
			Schema:      []byte({{ printf "%q" .InputSchema }}),
			ExampleJSON: []byte({{ printf "%q" .ExampleArgs }}),
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					{{- if .HasPayload }}
					value, ok := v.({{ .PayloadType }})
					if !ok {
						return nil, errors.New({{ printf "%q" (printf "tool %s payload must be %s" .QualifiedName .PayloadType) }})
					}
					return {{ $.CodecPackage }}.{{ .Codec.PayloadEncode }}(value)
					{{- else }}
					if v != nil {
						return nil, errors.New({{ printf "%q" (printf "tool %s does not accept a payload" .QualifiedName) }})
					}
					return []byte("{}"), nil
					{{- end }}
				},
				FromJSON: func(data []byte) (any, error) {
					{{- if .HasPayload }}
					return {{ $.CodecPackage }}.{{ .Codec.PayloadDecode }}(data)
					{{- else }}
					if err := validateNoArguments(data); err != nil {
						return nil, err
					}
					return nil, nil
					{{- end }}
				},
			},
		},
		{{- if .HasResult }}
		Result: tools.TypeSpec{
			Name:   {{ printf "%q" .ResultType }},
			Schema: []byte({{ printf "%q" .ResultSchema }}),
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					value, ok := v.({{ .ResultType }})
					if !ok {
						return nil, errors.New({{ printf "%q" (printf "tool %s result must be %s" .QualifiedName .ResultType) }})
					}
					return {{ $.CodecPackage }}.{{ .Codec.ResultEncode }}(value)
				},
				FromJSON: func(data []byte) (any, error) {
					return {{ $.CodecPackage }}.{{ .Codec.ResultDecode }}(data)
				},
			},
		},
		{{- end }}
	},
{{- end }}
}

// {{ .Register.HelperName }}ToolMetadata describes each tool in the {{ .Register.SuiteName }} toolset.
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

// {{ .Register.HelperName }}ToolMetadataByName returns the description for the named MCP tool.
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
			Tool:    toolName,
			Payload: json.RawMessage(call.Payload),
		})
		if err != nil {
			return {{ .Register.HelperName }}HandleError(fullName, call.Payload, err), nil
		}

		var value any
		switch toolName {
		{{- range .Register.Tools }}
		case {{ printf "%q" .ID }}:
			{{- if .HasStructuredResult }}
			if len(resp.StructuredContent) == 0 {
				return {{ $.Register.HelperName }}HandleError(
					fullName,
					call.Payload,
					mcpruntime.NewMalformedResponseError(errors.New("MCP response is missing structured content")),
				), nil
			}
			value, err = {{ $.CodecPackage }}.{{ .Codec.ResultDecode }}(resp.StructuredContent)
			{{- else if .TextResult }}
			if len(resp.Content) != 1 {
				return {{ $.Register.HelperName }}HandleError(
					fullName,
					call.Payload,
					mcpruntime.NewMalformedResponseError(errors.New("MCP response must contain one text result")),
				), nil
			}
			encoded, err := json.Marshal(resp.Content[0])
			if err != nil {
				return {{ $.Register.HelperName }}HandleError(fullName, call.Payload, err), nil
			}
			value, err = {{ $.CodecPackage }}.{{ .Codec.ResultDecode }}(encoded)
			{{- else if .HasResult }}
			if len(resp.Content) != 1 {
				return {{ $.Register.HelperName }}HandleError(
					fullName,
					call.Payload,
					mcpruntime.NewMalformedResponseError(errors.New("MCP response must contain one text result")),
				), nil
			}
			value, err = {{ $.CodecPackage }}.{{ .Codec.ResultDecode }}([]byte(resp.Content[0]))
			{{- else }}
			break
			{{- end }}
		{{- end }}
		default:
			return planner.ToolResult{}, errors.New("generated MCP toolset does not define " + toolName)
		}
		if err != nil {
			return {{ .Register.HelperName }}HandleError(
				fullName,
				call.Payload,
				mcpruntime.NewMalformedResponseError(err),
			), nil
		}

		return planner.ToolResult{Name: fullName, Result: value}, nil
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

// {{ .Register.HelperName }}HandleError uses the runtime's MCP error rules. The
// runtime adds the retained model input and registered example when needed.
func {{ .Register.HelperName }}HandleError(toolName tools.Ident, _ rawjson.Message, err error) planner.ToolResult {
	return *agentsruntime.MCPCallFailure(toolName, err)
}

// {{ .Register.HelperName }}CorrectionFailure keeps the generated helper used by
// existing plugins. The runtime adds the retained input and registered example.
func {{ .Register.HelperName }}CorrectionFailure(toolName string, _ rawjson.Message, err error) *planner.ToolFailure {
	rpcErr := &mcpruntime.Error{Code: mcpruntime.JSONRPCInvalidParams, Message: err.Error()}
	return agentsruntime.MCPCallFailure(tools.Ident(toolName), rpcErr).Failure
}
