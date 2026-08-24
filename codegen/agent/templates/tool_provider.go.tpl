type (
	// Provider dispatches tool call messages to the bound Goa service methods and
	// returns canonical JSON tool results and typed server-only data.
	//
	// Provider is intended to run inside the toolset-owning service process,
	// paired with a Pulse subscription loop whose Serve lifecycle owns one
	// immutable registry admission generation, renews its provider lease, and
	// releases that exact lease only after consumption, terminal results, and
	// acknowledgements settle. The registry grants one durable dispatch claim
	// before Serve invokes a bound method, so redelivery never repeats handler
	// execution. Serve also stamps best-effort output deltas published from the
	// call context with that admission token (see runtime/toolregistry/provider).
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

// HandleToolCall executes the requested tool and returns a terminal result that
// echoes the call's admission-generation token on every success and error path.
// The bound method receives ctx and must return promptly on cancellation.
func (p *Provider) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	if msg.ToolUseID == "" {
		return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, "", "invalid_call", "tool_use_id is required"), nil
	}
	toolUseID, ok := toolregistry.ToolUseIDFromContext(ctx)
	if !ok || toolUseID != msg.ToolUseID {
		return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "invalid_call", "canonical tool_use_id is missing from context"), nil
	}
	if msg.Meta == nil {
		return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "invalid_call", "meta is required"), nil
	}
{{- if .NeedsInject }}
	meta := runtime.ToolCallMeta{
		RunID:            msg.Meta.RunID,
		SessionID:        msg.Meta.SessionID,
		TurnID:           msg.Meta.TurnID,
		ToolCallID:       msg.Meta.ToolCallID,
		ParentToolCallID: msg.Meta.ParentToolCallID,
		Labels:           msg.Meta.Labels,
	}
{{- end }}

	switch msg.Tool {
{{- range .Tools }}
{{- if .IsMethodBacked }}
	case {{ .ConstName }}:
{{- if or .HasMethodPayload .Injected }}
		args, err := {{ .ConstName }}PayloadCodec().FromJSON(msg.Payload)
{{- else }}
		_, err := {{ .ConstName }}PayloadCodec().FromJSON(msg.Payload)
{{- end }}
		if err != nil {
			if issues := toolregistry.ValidationIssues(err); len(issues) > 0 {
				return toolregistry.NewToolResultInvalidArgumentsMessage(msg.RegistrationToken, msg.ToolUseID, err.Error(), issues), nil
			}
			return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "invalid_arguments", err.Error()), nil
		}
{{- if .Injected }}
		if err := Inject{{ .ConstName }}(args, meta, meta.Labels); err != nil {
			return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "invalid_arguments", err.Error()), nil
		}
{{- end }}
{{- if .HasMethodPayload }}
		methodIn := Init{{ .ConstName }}MethodPayload(args)
{{- end }}
{{- if .HasMethodResult }}
		methodOut, err := p.svc.{{ .MethodGoName }}(ctx{{ if .HasMethodPayload }}, methodIn{{ end }})
{{- else }}
		err = p.svc.{{ .MethodGoName }}(ctx{{ if .HasMethodPayload }}, methodIn{{ end }})
{{- end }}
		if err != nil {
			return toolregistry.NewToolResultServiceErrorMessage(msg.RegistrationToken, msg.ToolUseID, msg.Tool, toolErrorCode(err), err), nil
		}
{{- if .HasResult }}
		result := Init{{ .ConstName }}ToolResult(methodOut)
		resultJSON, err := {{ .ConstName }}ResultCodec().ToJSON(result)
		if err != nil {
			return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "encode_failed", err.Error()), nil
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
			dataJSON, err := {{ $tool.ConstName }}{{ goify .Kind true }}ServerDataCodec().ToJSON(data)
			if err != nil {
				return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "encode_failed", err.Error()), nil
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
				RegistrationToken: msg.RegistrationToken,
				ToolUseID:          msg.ToolUseID,
				Result:             resultJSON,
{{- if and .Bounds .Bounds.Projection .Bounds.Projection.Returned .Bounds.Projection.Truncated }}
				Bounds:    bounds,
{{- end }}
				ServerData: server,
			}, nil
		}
		return toolregistry.ToolResultMessage{
			RegistrationToken: msg.RegistrationToken,
			ToolUseID:          msg.ToolUseID,
			Result:             resultJSON,
{{- if and .Bounds .Bounds.Projection .Bounds.Projection.Returned .Bounds.Projection.Truncated }}
			Bounds:    bounds,
{{- end }}
		}, nil
{{- else }}
		return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, nil), nil
{{- end }}
{{- end }}
{{- end }}
	default:
		return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "unknown_tool", fmt.Sprintf("unknown tool %q", msg.Tool)), nil
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


