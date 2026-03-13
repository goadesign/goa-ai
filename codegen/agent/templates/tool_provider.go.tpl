type (
	// Provider dispatches tool call messages to the bound Goa service methods and
	// returns canonical JSON tool results and typed server-only data.
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
{{- if and .Bounds .Bounds.Projection .Bounds.Projection.Returned .Bounds.Projection.Truncated }}
		bounds := init{{ goify .Name true }}Bounds(methodOut)
{{- end }}
		var server []*toolregistry.ServerDataItem
{{- $tool := . }}
{{- range .ServerData }}
{{- if .MethodResultField }}
		{
			data := Init{{ $tool.ConstName }}{{ goify .Kind true }}ServerData(methodOut.{{ goify .MethodResultField true }})
			dataJSON, err := {{ $tool.ConstName }}{{ goify .Kind true }}ServerDataCodec.ToJSON(data)
			if err != nil {
				return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "encode_failed", err.Error()), nil
			}
			if string(dataJSON) != "null" {
				server = append(server, &toolregistry.ServerDataItem{
					Kind:     {{ printf "%q" .Kind }},
					Audience: {{ printf "%q" .Audience }},
					Data:     dataJSON,
				})
			}
		}
{{- end }}
{{- end }}
		if len(server) > 0 {
			return toolregistry.ToolResultMessage{
				ToolUseID: msg.ToolUseID,
				Result:    resultJSON,
{{- if and .Bounds .Bounds.Projection .Bounds.Projection.Returned .Bounds.Projection.Truncated }}
				Bounds:    bounds,
{{- end }}
				ServerData: server,
			}, nil
		}
		return toolregistry.ToolResultMessage{
			ToolUseID: msg.ToolUseID,
			Result:    resultJSON,
{{- if and .Bounds .Bounds.Projection .Bounds.Projection.Returned .Bounds.Projection.Truncated }}
			Bounds:    bounds,
{{- end }}
		}, nil
{{- else }}
		return toolregistry.NewToolResultMessage(msg.ToolUseID, nil), nil
{{- end }}
{{- end }}
{{- end }}
	default:
		return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "unknown_tool", fmt.Sprintf("unknown tool %q", msg.Tool)), nil
	}
}

{{- range .Tools }}
{{- if and .IsMethodBacked .Bounds .Bounds.Projection .Bounds.Projection.Returned .Bounds.Projection.Truncated }}

// init{{ goify .Name true }}Bounds projects canonical bounds metadata from the
// bound method result.
func init{{ goify .Name true }}Bounds(mr {{ .MethodResultTypeRef }}) *agent.Bounds {
	bounds := &agent.Bounds{}
	{{- with .Bounds.Projection.Returned }}
	bounds.Returned = mr.{{ .Name }}
	{{- end }}
	{{- with .Bounds.Projection.Total }}
		{{- if .Required }}
	total := mr.{{ .Name }}
	bounds.Total = &total
		{{- else }}
	bounds.Total = mr.{{ .Name }}
		{{- end }}
	{{- end }}
	{{- with .Bounds.Projection.Truncated }}
	bounds.Truncated = mr.{{ .Name }}
	{{- end }}
	{{- with .Bounds.Projection.NextCursor }}
	bounds.NextCursor = mr.{{ .Name }}
	{{- end }}
	{{- with .Bounds.Projection.RefinementHint }}
		{{- if .Required }}
	bounds.RefinementHint = mr.{{ .Name }}
		{{- else }}
	if mr.{{ .Name }} != nil {
		bounds.RefinementHint = *mr.{{ .Name }}
	}
		{{- end }}
	{{- end }}
	return bounds
}
{{- end }}
{{- end }}


