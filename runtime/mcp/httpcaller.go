// Package mcp sends stateless MCP tool requests over HTTP. Each invocation carries
// its own protocol metadata and returns one validated result. Explicit trust and
// behavior hints control retries after an interrupted SSE response.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	schema "github.com/santhosh-tekuri/jsonschema/v6"

	"goa.design/goa-ai/internal/jsonschema"
	"goa.design/goa-ai/internal/mcpprotocol"
	goahttp "goa.design/goa/v3/http"
)

type (
	// HTTPOptions configures an HTTP caller with dependencies built by the host.
	HTTPOptions struct {
		// Endpoint is the URL that accepts MCP JSON-RPC POST requests.
		Endpoint string
		// Client sends HTTP requests, including through an already-built MCP
		// transport. When omitted, http.DefaultClient is used.
		Client goahttp.Doer
		// ClientInfo identifies the application on each request.
		ClientInfo ClientInfo
		// InputSupport names the interactions the host can fulfill.
		InputSupport InputSupport
		// RetryPolicy controls retries after stream loss at this endpoint.
		RetryPolicy HTTPRetryPolicy
	}
	// remoteToolContract retains only the selected, credential-scoped catalog contract.
	remoteToolContract struct {
		binding ToolBinding
		input   *schema.Schema
		output  *schema.Schema
	}
	// remoteToolAnnotations preserves declared hints from a third-party catalog.
	remoteToolAnnotations struct {
		Title           *string `json:"title"`
		ReadOnlyHint    *bool   `json:"readOnlyHint"`    //nolint:tagliatelle // MCP wire name.
		DestructiveHint *bool   `json:"destructiveHint"` //nolint:tagliatelle // MCP wire name.
		IdempotentHint  *bool   `json:"idempotentHint"`  //nolint:tagliatelle // MCP wire name.
		OpenWorldHint   *bool   `json:"openWorldHint"`   //nolint:tagliatelle // MCP wire name.
	}
	// HTTPCaller invokes MCP tools over stateless HTTP requests.
	HTTPCaller struct {
		endpoint  string
		transport *HTTPTransport
	}
)

// ProtocolVersion is the only MCP revision implemented by this release.
const ProtocolVersion = mcpprotocol.Version

// NewHTTPCaller checks the endpoint and identity without sending network requests.
func NewHTTPCaller(opts HTTPOptions) (*HTTPCaller, error) {
	if err := opts.RetryPolicy.Validate(); err != nil {
		return nil, err
	}
	if err := opts.ClientInfo.Validate(); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(opts.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != httpsScheme) {
		return nil, fmt.Errorf("mcp: invalid HTTP endpoint %q", opts.Endpoint)
	}
	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPCaller{endpoint: endpoint.String(), transport: NewHTTPTransport(client, opts.ClientInfo, HTTPBindings{}, opts.InputSupport, opts.RetryPolicy)}, nil
}

// CallTool loads header annotations for this authorization context, sends the
// tool arguments with the host's retry policy, and returns a result or request for input.
func (c *HTTPCaller) CallTool(ctx context.Context, req CallRequest) (CallResponse, error) {
	params, err := toolParams(ctx, req)
	if err != nil {
		return CallResponse{}, err
	}
	contract, err := c.toolContract(ctx, req.Tool)
	if err != nil {
		return CallResponse{}, err
	}
	payload := req.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	if err := jsonschema.Validate(contract.input, payload); err != nil {
		return CallResponse{}, &Error{Code: JSONRPCInvalidParams, Message: fmt.Sprintf("MCP tool arguments: %v", err)}
	}
	transport := NewHTTPTransport(c.transport, c.transport.clientInfo, HTTPBindings{Tools: map[string]ToolBinding{req.Tool: contract.binding}}, c.transport.inputSupport, c.transport.retry)
	var result toolsCallResult
	if err := transport.call(ctx, c.endpoint, methodToolsCall, params, &result); err != nil {
		return CallResponse{}, err
	}
	response, err := normalizeCallResult(ctx, result, transport.inputSupport)
	if err != nil {
		return CallResponse{}, err
	}
	if response.InputRequired == nil && contract.output != nil {
		if err := jsonschema.Validate(contract.output, response.StructuredContent); err != nil {
			return CallResponse{}, NewMalformedResponseError(fmt.Errorf("MCP structured result: %w", err))
		}
	}
	return response, nil
}

// toolContract reads every catalog page without sharing a cache across credentials.
// A malformed annotation excludes only its tool; another valid tool remains usable.
func (c *HTTPCaller) toolContract(ctx context.Context, name string) (*remoteToolContract, error) {
	var cursor *string
	seen := make(map[string]bool)
	names := make(map[string]bool)
	var selected *remoteToolContract
	var selectedError error
	for {
		params := map[string]any{}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		var catalog struct {
			ResultType string `json:"resultType"` //nolint:tagliatelle // MCP defines this wire field name.
			Tools      *[]struct {
				Annotations  json.RawMessage `json:"annotations"`
				Name         string          `json:"name"`
				InputSchema  json.RawMessage `json:"inputSchema"`  //nolint:tagliatelle // MCP defines this wire field name.
				OutputSchema json.RawMessage `json:"outputSchema"` //nolint:tagliatelle // MCP defines this wire field name.
			} `json:"tools"`
			NextCursor *string  `json:"nextCursor"` //nolint:tagliatelle // MCP defines this wire field name.
			TTLMs      *float64 `json:"ttlMs"`      //nolint:tagliatelle // MCP defines this wire field name.
			CacheScope string   `json:"cacheScope"` //nolint:tagliatelle // MCP defines this wire field name.
		}
		if err := c.transport.call(ctx, c.endpoint, "tools/list", params, &catalog); err != nil {
			return nil, err
		}
		if catalog.ResultType != resultComplete || catalog.Tools == nil || catalog.TTLMs == nil || *catalog.TTLMs < 0 ||
			(catalog.CacheScope != "public" && catalog.CacheScope != "private") {
			return nil, NewMalformedResponseError(errors.New("invalid tools/list result"))
		}
		for _, tool := range *catalog.Tools {
			if tool.Name == "" || names[tool.Name] {
				return nil, NewMalformedResponseError(errors.New("catalog tool names must be nonempty and unique"))
			}
			names[tool.Name] = true
			if tool.Name != name {
				continue
			}
			bindings, err := mcpprotocol.CompileHeaderBindings(tool.InputSchema)
			if err != nil {
				selectedError = fmt.Errorf("tool %q header annotations: %w", name, err)
				continue
			}
			input, err := jsonschema.Compile(tool.InputSchema)
			if err != nil {
				selectedError = err
				continue
			}
			var output *schema.Schema
			if len(tool.OutputSchema) != 0 {
				var schemaObject map[string]json.RawMessage
				if json.Unmarshal(tool.OutputSchema, &schemaObject) != nil || schemaObject == nil {
					selectedError = errors.New("outputSchema must be a JSON Schema object")
					continue
				}
				output, err = jsonschema.Compile(tool.OutputSchema)
				if err != nil {
					selectedError = err
					continue
				}
			}
			binding := ToolBinding{Headers: bindings}
			annotations, err := decodeToolAnnotations(tool.Annotations)
			if err != nil {
				return nil, NewMalformedResponseError(err)
			}
			if annotations != nil {
				binding.ReadOnly = annotations.ReadOnlyHint != nil && *annotations.ReadOnlyHint
				binding.Idempotent = annotations.IdempotentHint != nil && *annotations.IdempotentHint
			}
			selected = &remoteToolContract{binding: binding, input: input, output: output}
		}
		if catalog.NextCursor == nil {
			if selectedError != nil {
				return nil, NewMalformedResponseError(selectedError)
			}
			if selected != nil {
				return selected, nil
			}
			return nil, &Error{Code: JSONRPCInvalidParams, Message: "tool not found"}
		}
		if seen[*catalog.NextCursor] {
			return nil, NewMalformedResponseError(errors.New("repeated catalog cursor"))
		}
		seen[*catalog.NextCursor] = true
		cursor = catalog.NextCursor
	}
}

// decodeToolAnnotations validates optional hints from an external catalog before
// the client uses them to authorize repeated execution. Null is not a boolean
// declaration; unknown extension fields remain outside this client's decisions.
func decodeToolAnnotations(raw json.RawMessage) (*remoteToolAnnotations, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("tool annotations must be an object")
	}
	for name, value := range fields {
		switch name {
		case "title", "readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, fmt.Errorf("tool annotation %q must not be null", name)
			}
		}
	}
	var annotations remoteToolAnnotations
	if err := json.Unmarshal(raw, &annotations); err != nil {
		return nil, fmt.Errorf("tool annotations: %w", err)
	}
	return &annotations, nil
}
