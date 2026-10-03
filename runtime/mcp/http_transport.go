// Package mcp owns MCP's HTTP binding for generated and handwritten clients. It
// derives request headers from typed bindings, validates response IDs, and reads
// one final JSON or request-scoped event-stream response. It never resumes or
// repeats a disconnected request.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"goa.design/goa-ai/internal/mcpprotocol"
	"goa.design/goa/v3/jsonrpc"
)

type (
	// HeaderBinding describes one scalar tool property mirrored to an HTTP header.
	// Generated clients supply static bindings; discovered catalogs are compiled
	// privately for the caller's current authorization context.
	HeaderBinding = mcpprotocol.HeaderBinding
	// HTTPTransport applies the current MCP binding to Goa or HTTP client requests.
	HTTPTransport struct {
		next interface {
			Do(*http.Request) (*http.Response, error)
		}
		clientInfo   ClientInfo
		inputSupport InputSupport
		headers      map[string][]HeaderBinding
	}
)

// NewHTTPTransport wraps an already-built HTTP dependency. Each request derives
// metadata and headers; no initialization or connection state is created.
func NewHTTPTransport(next interface {
	Do(*http.Request) (*http.Response, error)
}, info ClientInfo, headers map[string][]HeaderBinding, support InputSupport) *HTTPTransport {
	if existing, ok := next.(*HTTPTransport); ok {
		next = existing.next
	}
	return &HTTPTransport{next: next, clientInfo: info, headers: headers, inputSupport: support}
}

// Do sends one request with its own metadata and validates the response before
// a generated decoder sees it. Protocol errors retain their code and raw data
// instead of being replaced by a generic HTTP status error.
func (t *HTTPTransport) Do(original *http.Request) (response *http.Response, err error) {
	body, readErr := io.ReadAll(original.Body)
	closeErr := original.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, NewInternalError(err)
	}
	var request jsonrpc.RawRequest
	if err := request.UnmarshalJSON(body); err != nil || request.Invalid || !request.HasMethod || request.JSONRPC != rpcVersion {
		return nil, NewInternalError(errors.New("invalid outgoing JSON-RPC request"))
	}
	var params map[string]json.RawMessage
	if len(request.Params) > 0 {
		if err := json.Unmarshal(request.Params, &params); err != nil || params == nil {
			return nil, NewInternalError(errors.New("params must be an object"))
		}
	} else {
		params = make(map[string]json.RawMessage)
	}
	var existing map[string]json.RawMessage
	if raw, ok := params["_meta"]; ok {
		if err := json.Unmarshal(raw, &existing); err != nil || existing == nil {
			return nil, NewInternalError(errors.New("_meta must be an object"))
		}
	}
	meta, err := requestMeta(original.Context(), t.clientInfo, t.inputSupport, existing)
	if err != nil {
		return nil, err
	}
	params["_meta"], err = json.Marshal(meta)
	if err != nil {
		return nil, NewInternalError(err)
	}
	request.Params, err = json.Marshal(params)
	if err != nil {
		return nil, NewInternalError(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, NewInternalError(err)
	}
	envelope["params"] = request.Params
	body, err = json.Marshal(envelope)
	if err != nil {
		return nil, NewInternalError(err)
	}
	outgoing := original.Clone(original.Context())
	outgoing.Body = io.NopCloser(bytes.NewReader(body))
	outgoing.ContentLength = int64(len(body))
	outgoing.GetBody = nil
	outgoing.Header.Set("Content-Type", "application/json")
	outgoing.Header.Set("Accept", "application/json, text/event-stream")
	outgoing.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	outgoing.Header.Set("Mcp-Method", request.Method)
	name, err := requestName(request.Method, params)
	if err != nil {
		return nil, NewInternalError(err)
	}
	if name != nil {
		outgoing.Header.Set("Mcp-Name", mcpprotocol.EncodeHeaderValue(*name))
	}
	if request.Method == methodToolsCall {
		if err := setParameterHeaders(outgoing.Header, params["arguments"], t.headers[*name]); err != nil {
			return nil, err
		}
	}
	injectTraceHeaders(original.Context(), outgoing.Header)
	if request.Method == methodToolsCall {
		defer func() { err = unknownToolOutcome(err) }()
	}
	response, err = t.next.Do(outgoing)
	if err != nil {
		return nil, err
	}
	if !request.HasID {
		if response.StatusCode != http.StatusAccepted {
			return nil, closeResponseWithError(response, fmt.Errorf("mcp notification HTTP status %d", response.StatusCode))
		}
		data, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		if len(data) != 0 {
			return nil, NewMalformedResponseError(errors.New("notification response must be empty"))
		}
		response.Body = io.NopCloser(bytes.NewReader(nil))
		return response, nil
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return nil, closeResponseWithError(response, NewMalformedResponseError(err))
	}
	var data []byte
	switch mediaType {
	case "application/json":
		data, err = io.ReadAll(response.Body)
	case "text/event-stream":
		data, err = readEventStream(response.Body)
	default:
		err = NewMalformedResponseError(fmt.Errorf("unsupported content type %q", mediaType))
	}
	closeErr = response.Body.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	var incoming rpcMessage
	if err := json.Unmarshal(data, &incoming); err != nil {
		return nil, NewMalformedResponseError(err)
	}
	if err := incoming.validateResponse(); err != nil {
		return nil, err
	}
	if !bytes.Equal(bytes.TrimSpace(envelope["id"]), bytes.TrimSpace(incoming.ID)) {
		return nil, NewMalformedResponseError(errors.New("response ID does not match request"))
	}
	rpcErr, err := incoming.responseError()
	if err != nil {
		return nil, err
	}
	if rpcErr != nil {
		return nil, rpcErr.callerError()
	}
	if response.StatusCode != http.StatusOK {
		return nil, NewMalformedResponseError(fmt.Errorf("success result with HTTP status %d", response.StatusCode))
	}
	var result struct {
		ResultType string `json:"resultType"` //nolint:tagliatelle // MCP defines this wire field name.
	}
	if err := json.Unmarshal(incoming.Result, &result); err != nil || (result.ResultType != resultComplete && result.ResultType != resultInputRequired) {
		return nil, NewMalformedResponseError(errors.New("unsupported or absent resultType"))
	}
	if result.ResultType == resultInputRequired && request.Method != methodToolsCall && request.Method != "resources/read" && request.Method != "prompts/get" {
		return nil, NewMalformedResponseError(errors.New("input_required is not permitted for this method"))
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	response.Header.Set("Content-Type", "application/json")
	return response, nil
}

// readEventStream consumes comments and notifications until one final response.
// An independent server request or a stream that ends early is a protocol error.
func readEventStream(body io.Reader) ([]byte, error) {
	reader := bufio.NewReader(body)
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, NewMalformedResponseError(err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" || errors.Is(err, io.EOF) {
			if data.Len() > 0 {
				encoded := []byte(data.String())
				var message rpcMessage
				if decodeErr := json.Unmarshal(encoded, &message); decodeErr != nil {
					return nil, NewMalformedResponseError(decodeErr)
				}
				if validateErr := message.validateMessage(); validateErr != nil {
					return nil, validateErr
				}
				if message.Method == "" {
					return encoded, nil
				}
				if len(message.ID) > 0 {
					return nil, NewMalformedResponseError(errors.New("independent server requests are not supported by this protocol"))
				}
				data.Reset()
			}
		} else if value, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(value, " "))
		}
		if errors.Is(err, io.EOF) {
			return nil, NewMalformedResponseError(errors.New("event stream ended before the request response"))
		}
	}
}

func closeResponseWithError(response *http.Response, err error) error {
	return errors.Join(err, response.Body.Close())
}

// requestName selects the protocol-defined header source without normalizing a
// user-authored name, URI, or task identifier.
func requestName(method string, params map[string]json.RawMessage) (*string, error) {
	var field string
	switch method {
	case methodToolsCall, "prompts/get":
		field = "name"
	case "resources/read":
		field = "uri"
	case "tasks/get", "tasks/update", "tasks/cancel":
		field = "taskId"
	default:
		return nil, nil
	}
	raw, ok := params[field]
	var value string
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return nil, fmt.Errorf("%s must be a string", field)
	}
	return &value, nil
}

// setParameterHeaders copies the schema-selected scalar arguments into headers.
func setParameterHeaders(headers http.Header, arguments json.RawMessage, bindings []HeaderBinding) error {
	values, err := mcpprotocol.ParameterValues(arguments, bindings)
	if err != nil {
		return err
	}
	for name, value := range values {
		headers.Set(name, value)
	}
	return nil
}

// CallTool sends one tools/call round to a generated or discovered endpoint.
// The transport owns IDs and full protocol decoding, including unfinished input.
func (t *HTTPTransport) CallTool(ctx context.Context, endpoint string, request CallRequest) (CallResponse, error) {
	var result toolsCallResult
	if err := t.call(ctx, endpoint, methodToolsCall, toolParams(request), &result); err != nil {
		return CallResponse{}, err
	}
	return normalizeCallResult(result, t.inputSupport)
}

// call assigns a fresh UUID so a new caller after worker replacement cannot
// reuse the prior network round's ID. Protocol errors remain unchanged.
func (t *HTTPTransport) call(ctx context.Context, endpoint string, method string, params, result any) (err error) {
	request := rpcRequest{JSONRPC: rpcVersion, Method: method, ID: uuid.NewString(), Params: params}
	body, err := json.Marshal(request)
	if err != nil {
		return NewInternalError(err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return NewInternalError(err)
	}
	response, err := t.Do(httpRequest)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	var envelope rpcMessage
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return NewMalformedResponseError(err)
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return NewMalformedResponseError(err)
	}
	return nil
}
