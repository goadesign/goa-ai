type (
	// Provider dispatches tool call messages to the bound Goa service methods and
	// returns canonical JSON tool results and optional artifacts.
	//
	// Provider is intended to run inside the toolset-owning service process,
	// paired with a Pulse subscription loop (see runtime/toolregistry/provider).
	Provider struct {
		svc {{ .ServiceTypeRef }}
	}
)

func toolErrorCode(err error) string {
	var se *goa.ServiceError
	if errors.As(err, &se) {
		if se.Timeout {
			return "timeout"
		}
		if se.Name != "" {
			return se.Name
		}
	}
	return "execution_failed"
}

// NewProvider returns a Provider for the toolset.
func NewProvider(svc {{ .ServiceTypeRef }}) *Provider {
	if svc == nil {
		panic("tool provider service is required")
	}
	return &Provider{svc: svc}
}

// HandleToolCall executes the requested tool and returns a tool result message.
func (p *Provider) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	if msg.ToolUseID == "" {
		return toolregistry.NewToolResultErrorMessage("", "invalid_call", "tool_use_id is required"), nil
	}
	if msg.Meta == nil {
		return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "invalid_call", "meta is required"), nil
	}

	switch msg.Tool {
{{- range .Tools }}
{{- if .IsMethodBacked }}
	case {{ .ConstName }}:
		args, err := {{ .ConstName }}PayloadCodec.FromJSON(msg.Payload)
		if err != nil {
			if issues := toolregistry.ValidationIssues(err); len(issues) > 0 {
				return toolregistry.NewToolResultErrorMessageWithIssues(msg.ToolUseID, "invalid_arguments", err.Error(), issues), nil
			}
			return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "invalid_arguments", err.Error()), nil
		}
		methodIn := Init{{ .ConstName }}MethodPayload(args)
{{- if .InjectedFields }}
{{- range .InjectedFields }}
		methodIn.{{ goify . true }} = msg.Meta.{{ goify . true }}
{{- end }}
{{- end }}
		methodOut, err := p.svc.{{ .MethodGoName }}(ctx, methodIn)
		if err != nil {
			if issues := toolregistry.ValidationIssues(err); len(issues) > 0 {
				return toolregistry.NewToolResultErrorMessageWithIssues(msg.ToolUseID, "invalid_arguments", err.Error(), issues), nil
			}
			return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, toolErrorCode(err), err.Error()), nil
		}
{{- if .HasResult }}
		result := Init{{ .ConstName }}ToolResult(methodOut)
		resultJSON, err := {{ .ConstName }}ResultCodec.ToJSON(result)
		if err != nil {
			return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "encode_failed", err.Error()), nil
		}
		var server []*toolregistry.ServerDataItem
{{- $tool := . }}
{{- range .ServerData }}
{{- if eq .Mode "optional" }}
		optionalData := Init{{ $tool.ConstName }}ServerDataFromMethodResult(methodOut)
		if optionalData != nil {
			optionalJSON, err := {{ $tool.ConstName }}ServerDataCodec.ToJSON(optionalData)
			if err != nil {
				return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "encode_failed", err.Error()), nil
			}
			server = append(server, &toolregistry.ServerDataItem{
				Kind: {{ printf "%q" .Kind }},
				Data: optionalJSON,
			})
		}
{{- else if eq .Mode "always" }}
		alwaysJSON, err := json.Marshal(methodOut.{{ .MethodResultField }})
		if err != nil {
			return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "encode_failed", err.Error()), nil
		}
		server = append(server, &toolregistry.ServerDataItem{
			Kind: {{ printf "%q" .Kind }},
			Data: alwaysJSON,
		})
{{- end }}
{{- end }}
		if len(server) > 0 {
			return toolregistry.NewToolResultMessageWithServer(msg.ToolUseID, resultJSON, server), nil
		}
		return toolregistry.NewToolResultMessage(msg.ToolUseID, resultJSON), nil
{{- else }}
		return toolregistry.NewToolResultMessage(msg.ToolUseID, nil), nil
{{- end }}
{{- end }}
{{- end }}
	default:
		return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "unknown_tool", fmt.Sprintf("unknown tool %q", msg.Tool)), nil
	}
}


