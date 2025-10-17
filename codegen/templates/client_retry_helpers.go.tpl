{{- /* Retry helpers baked into jsonrpc/mcp_<svc>/client/client.go */ -}}

{{- /* Per-tool example constants only (schema fetched from server tools/list) */ -}}
{{- range .Tools }}
{{- if .ExampleArguments }}
const example_{{ .Name }} = `{{ .ExampleArguments }}`
{{- end }}
{{- end }}

// toolExample returns a built-in example (if any) for a tool name.
func toolExample(name string) string {
    switch name {
{{- range .Tools }}
{{- if .ExampleArguments }}
    case "{{ .Name }}": return example_{{ .Name }}
{{- end }}
{{- end }}
    default:
        return "{}"
    }
}

// Cached lookup of tool input schema via tools/list.
var toolSchemaCache struct {
    sync.Mutex
    m map[string]string
}

func getToolSchema(ctx context.Context, c *Client, name string) string {
    toolSchemaCache.Lock()
    if toolSchemaCache.m == nil { toolSchemaCache.m = make(map[string]string) }
    if s, ok := toolSchemaCache.m[name]; ok { toolSchemaCache.Unlock(); return s }
    toolSchemaCache.Unlock()

    // Fetch tools list once and cache
    ep := c.ToolsList()
    ires, err := ep(ctx, &{{ .MCPAlias }}.ToolsListPayload{})
    if err != nil { return "" }
    res, ok := ires.(*{{ .MCPAlias }}.ToolsListResult)
    if !ok || res == nil { return "" }
    var schema string
    for _, t := range res.Tools {
        if t != nil && t.Name == name {
            // InputSchema is of type any; marshal back to JSON string if needed
            switch v := t.InputSchema.(type) {
            case string:
                schema = v
            case []byte:
                schema = string(v)
            default:
                // attempt JSON marshal via Goa encoder path: best-effort string
                b, _ := json.Marshal(v)
                schema = string(b)
            }
            break
        }
    }
    toolSchemaCache.Lock()
    toolSchemaCache.m[name] = schema
    toolSchemaCache.Unlock()
    return schema
}

// JSONRPCError is a typed error for convenient code checks.
type JSONRPCError struct {
	Code    int
	Message string
}

func (e *JSONRPCError) Error() string { return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message) }

func isInvalidParams(err error) bool {
    if err == nil { return false }
    var jre *JSONRPCError
    if errors.As(err, &jre) {
        return jre.Code == -32602
    }
    return false
}

// toolsCallStreamWithRetry wraps ToolsCallClientStream to convert invalid params
// into retry.RetryableError enriched with schema/example context.
type toolsCallStreamWithRetry struct {
	inner *ToolsCallClientStream
	tool  string
    client *Client
}

func (s *toolsCallStreamWithRetry) Recv(ctx context.Context) (*{{ .MCPAlias }}.ToolsCallResult, error) {
    res, err := s.inner.Recv(ctx)
    if err != nil {
        if isInvalidParams(err) {
            schema := getToolSchema(ctx, s.client, s.tool)
            example := toolExample(s.tool)
            prompt := retry.BuildRepairPrompt("tools/call:"+s.tool, err.Error(), example, schema)
            return nil, &retry.RetryableError{Prompt: prompt, Cause: err}
        }
        return nil, err
    }
    return res, nil
}

func (s *toolsCallStreamWithRetry) Close() error { return s.inner.Close() }

func (c *Client) wrapToolsCallStream(tool string, anyStream any) any {
    if s, ok := anyStream.(*ToolsCallClientStream); ok { return &toolsCallStreamWithRetry{inner: s, tool: tool, client: c} }
	return anyStream
}

func withRetryError(op string, err error, example, schema string) error {
	if err == nil { return nil }
	if isInvalidParams(err) {
		prompt := retry.BuildRepairPrompt(op, err.Error(), example, schema)
		return &retry.RetryableError{Prompt: prompt, Cause: err}
	}
	return err
}


// DecodePromptsGetResponseWithRetry is like DecodePromptsGetResponse but returns
// retry.RetryableError on JSON-RPC invalid params (-32602).
func DecodePromptsGetResponseWithRetry(decoder func(*http.Response) goahttp.Decoder, restoreBody bool) func(*http.Response) (any, error) {
	return func(resp *http.Response) (any, error) {
		if restoreBody {
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, err
			}
			resp.Body = io.NopCloser(bytes.NewBuffer(b))
			defer func() { resp.Body = io.NopCloser(bytes.NewBuffer(b)) }()
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, goahttp.ErrInvalidResponse("{{ .MCPServiceName }}", "prompts/get", resp.StatusCode, string(body))
		}

		var jresp jsonrpc.RawResponse
		if err := decoder(resp).Decode(&jresp); err != nil {
			return nil, goahttp.ErrDecodingError("{{ .MCPServiceName }}", "prompts/get", err)
		}

		if jresp.Error != nil {
			if jresp.Error.Code == -32602 {
				prompt := retry.BuildRepairPrompt("prompts/get", jresp.Error.Message, "{}", "")
				return nil, &retry.RetryableError{Prompt: prompt, Cause: fmt.Errorf("JSON-RPC error %d: %s", jresp.Error.Code, jresp.Error.Message)}
			}
			body, _ := io.ReadAll(resp.Body)
			return nil, goahttp.ErrInvalidResponse("{{ .MCPServiceName }}", "prompts/get", resp.StatusCode, string(body))
		}
		resp.Body = io.NopCloser(bytes.NewBuffer(jresp.Result))
		var body PromptsGetResponseBody
		if err := decoder(resp).Decode(&body); err != nil {
			return nil, goahttp.ErrDecodingError("{{ .MCPServiceName }}", "prompts/get", err)
		}
		if err := ValidatePromptsGetResponseBody(&body); err != nil {
			return nil, goahttp.ErrValidationError("{{ .MCPServiceName }}", "prompts/get", err)
		}
		res := NewPromptsGetResultOK(&body)
		return res, nil
	}
}


