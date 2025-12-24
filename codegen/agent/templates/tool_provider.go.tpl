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
			return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "execution_failed", err.Error()), nil
		}
{{- if .HasResult }}
		result := Init{{ .ConstName }}ToolResult(methodOut)
		resultJSON, err := {{ .ConstName }}ResultCodec.ToJSON(result)
		if err != nil {
			return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "encode_failed", err.Error()), nil
		}
{{- if .Artifact }}
		sidecar := Init{{ .ConstName }}SidecarFromMethodResult(methodOut)
		if sidecar == nil {
			return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "internal_error", "tool declared an artifact but produced none"), nil
		}
		sidecarJSON, err := {{ .ConstName }}SidecarCodec.ToJSON(sidecar)
		if err != nil {
			return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "encode_failed", err.Error()), nil
		}
		return toolregistry.NewToolResultMessage(
			msg.ToolUseID,
			resultJSON,
			[]toolregistry.Artifact{
				{
					Kind: {{ printf "%q" .ArtifactKind }},
					Data: sidecarJSON,
				},
			},
		), nil
{{- else }}
		return toolregistry.NewToolResultMessage(msg.ToolUseID, resultJSON, nil), nil
{{- end }}
{{- else }}
		return toolregistry.NewToolResultMessage(msg.ToolUseID, nil, nil), nil
{{- end }}
{{- end }}
{{- end }}
	default:
		return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "unknown_tool", fmt.Sprintf("unknown tool %q", msg.Tool)), nil
	}
}


