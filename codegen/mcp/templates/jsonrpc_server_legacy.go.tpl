// mcpLegacyMode keeps the older response choice on this one validated request.
// Authorization still runs before dispatch; another request can use the current protocol.
type (
    mcpLegacyModeKey struct{}
    mcpLegacyMode struct {
        wrapOutput bool
    }
    mcpLegacyEncoder struct {
        next goahttp.Encoder
        mode mcpLegacyMode
    }
)

// mcpLegacyResponseEncoder selects an older reply only after the request guard
// has accepted that wire format. The application's configured encoder still
// writes the final JSON-RPC response; no serialized response is read back.
func mcpLegacyResponseEncoder(next func(context.Context, http.ResponseWriter) goahttp.Encoder) func(context.Context, http.ResponseWriter) goahttp.Encoder {
    return func(ctx context.Context, w http.ResponseWriter) goahttp.Encoder {
        encoder := next(ctx, w)
        if mode, ok := ctx.Value(mcpLegacyModeKey{}).(mcpLegacyMode); ok {
            return &mcpLegacyEncoder{next: encoder, mode: mode}
        }
        return encoder
    }
}

// Encode receives the native Goa JSON-RPC reply, selects its older wire fields,
// and sends it to the configured encoder with the original ID and errors.
func (e *mcpLegacyEncoder) Encode(value any) error {
    response, ok := value.(*jsonrpc.Response)
    if !ok {
        return fmt.Errorf("older MCP encoder expected a JSON-RPC response, got %T", value)
    }
    if response.Error != nil {
        return e.next.Encode(response)
    }
    if _, ok := response.Result.(struct{}); ok {
        return e.next.Encode(response)
    }
    result, err := mcpLegacyResult(response.Result, e.mode)
    if err != nil {
        var failure *mcpruntime.Error
        if errors.As(err, &failure) {
            return e.next.Encode(jsonrpc.MakeErrorResponse(response.ID, jsonrpc.Code(failure.Code), failure.Message, failure.Data))
        }
        return err
    }
    copy := *response
    copy.Result = result
    return e.next.Encode(&copy)
}

// mcpLegacyResult reuses each planned HTTP body. It removes modern result
// markers and converts tool results to the older advertised object.
func mcpLegacyResult(value any, mode mcpLegacyMode) (any, error) {
    switch body := value.(type) {
    {{- range .Bodies }}
    case {{ .Ref }}:
        {{- if eq .Method "server/discover" }}
        var meta struct { ServerInfo json.RawMessage `json:"io.modelcontextprotocol/serverInfo"` }
        if err := json.Unmarshal(body.Meta, &meta); err != nil {
            return nil, fmt.Errorf("decode MCP server identity: %w", err)
        }
        copy := *body
        capabilities := *body.Capabilities
        capabilities.Extensions = nil
        if capabilities.Tools != nil {
            tools := *capabilities.Tools
            tools.ListChanged = nil
            capabilities.Tools = &tools
        }
        if capabilities.Resources != nil {
            resources := *capabilities.Resources
            resources.ListChanged, resources.Subscribe = nil, nil
            capabilities.Resources = &resources
        }
        if capabilities.Prompts != nil {
            prompts := *capabilities.Prompts
            prompts.ListChanged = nil
            capabilities.Prompts = &prompts
        }
        copy.Capabilities = &capabilities
        return struct {
            *{{ .Type.Name }}
            SupportedVersions []string `json:"supportedVersions,omitempty"`
            ResultType *string `json:"resultType,omitempty"`
            TTLMs *int64 `json:"ttlMs,omitempty"`
            CacheScope *string `json:"cacheScope,omitempty"`
            ProtocolVersion string `json:"protocolVersion"`
            ServerInfo json.RawMessage `json:"serverInfo"`
        }{ {{ .Type.Name }}: &copy, ProtocolVersion: mcpruntime.LegacyProtocolVersion, ServerInfo: meta.ServerInfo }, nil
        {{- else if .CompleteType }}
        completed, ok := body.AsComplete()
        if !ok {
            return nil, &mcpruntime.Error{Code: mcpruntime.MissingRequiredClientCapability, Message: "Basic MCP 2025-11-25 does not support tasks or client-input exchanges"}
        }
        copy := *completed
        {{- if eq .Method "tools/call" }}
        if len(copy.StructuredContent) != 0 {
            {{- if not $.DynamicTools }}
            if mode.wrapOutput {
            {{- end }}
                encoded, err := json.Marshal(struct { Value json.RawMessage `json:"value"` }{copy.StructuredContent})
                if err != nil {
                    return nil, fmt.Errorf("encode older structured tool result: %w", err)
                }
                copy.StructuredContent = encoded
            {{- if not $.DynamicTools }}
            }
            {{- end }}
            // A host that reads only text still receives the complete JSON result.
            text := string(copy.StructuredContent)
            copy.Content = append(append(copy.Content[:0:0], copy.Content...), &{{ $.ContentType.Name }}{Type: "text", Text: &text})
        }
        return &copy, nil
        {{- else }}
        return struct {
            *{{ .CompleteType.Name }}
            TTLMs *int64 `json:"ttlMs,omitempty"`
            CacheScope *string `json:"cacheScope,omitempty"`
        }{ {{ .CompleteType.Name }}: &copy }, nil
        {{- end }}
        {{- else }}
        {{- if eq .Method "tools/list" }}
        copy := *body
        copy.Tools = append(copy.Tools[:0:0], copy.Tools...)
        for index, tool := range copy.Tools {
            item := *tool
            {{- if $.DynamicTools }}
            if len(item.OutputSchema) != 0 {
                // A nested schema keeps its own reference root, so recursive
                // results still validate after the older response adds value.
                var schema map[string]json.RawMessage
                if err := json.Unmarshal(item.OutputSchema, &schema); err != nil {
                    return nil, fmt.Errorf("decode output schema for tool %q: %w", item.Name, err)
                }
                if schema == nil {
                    return nil, fmt.Errorf("output schema for tool %q must be an object", item.Name)
                }
                if raw, present := schema["$schema"]; present {
                    var dialect string
                    if err := json.Unmarshal(raw, &dialect); err != nil {
                        return nil, fmt.Errorf("decode output schema dialect for tool %q: %w", item.Name, err)
                    }
                    switch dialect {
                    case "https://json-schema.org/draft/2020-12/schema", "https://json-schema.org/draft/2020-12/schema#", "http://json-schema.org/draft/2020-12/schema", "http://json-schema.org/draft/2020-12/schema#":
                    default:
                        return nil, &mcpruntime.Error{Code: mcpruntime.JSONRPCInternalError, Message: fmt.Sprintf("Basic MCP output schema for tool %q uses unsupported dialect %q; JSON Schema 2020-12 is required", item.Name, dialect)}
                    }
                }
                digest := sha256.Sum256(item.OutputSchema)
                identity := fmt.Sprintf("schema://goa-ai/mcp/result/%x", digest)
                var identifier *string
                if raw, present := schema["$id"]; present {
                    if err := json.Unmarshal(raw, &identifier); err != nil {
                        return nil, fmt.Errorf("decode output schema identifier for tool %q: %w", item.Name, err)
                    }
                    if identifier == nil {
                        return nil, fmt.Errorf("output schema for tool %q requires a string $id", item.Name)
                    }
                }
                if identifier == nil || *identifier == "" || *identifier == "#" {
                    schema["$id"] = json.RawMessage(fmt.Sprintf(`%q`, identity+"/schema"))
                }
                nested, err := json.Marshal(schema)
                if err != nil {
                    return nil, fmt.Errorf("encode output schema for tool %q: %w", item.Name, err)
                }
                item.OutputSchema = append(json.RawMessage(fmt.Sprintf(`{"$id":%q,"type":"object","properties":{"value":`, fmt.Sprintf("%s/legacy-%x", identity, digest))), nested...)
                item.OutputSchema = append(item.OutputSchema, []byte(`},"required":["value"],"additionalProperties":false}`)...)
            }
            {{- else }}
            switch item.Name {
            {{- range $name, $schema := $.OutputSchemas }}
            case {{ printf "%q" $name }}:
                item.OutputSchema = json.RawMessage({{ printf "%q" $schema }})
            {{- end }}
            }
            {{- end }}
            copy.Tools[index] = &item
        }
        body = &copy
        {{- end }}
        return struct {
            *{{ .Type.Name }}
            ResultType *string `json:"resultType,omitempty"`
            TTLMs *int64 `json:"ttlMs,omitempty"`
            CacheScope *string `json:"cacheScope,omitempty"`
        }{ {{ .Type.Name }}: body }, nil
        {{- end }}
    {{- end }}
    default:
        return nil, fmt.Errorf("older MCP encoder received unsupported result %T", value)
    }
}
