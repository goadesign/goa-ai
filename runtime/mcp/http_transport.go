// Package mcp owns MCP's HTTP binding for generated and handwritten clients. It
// derives request headers from typed bindings, validates response IDs, and reads
// one final JSON or request-scoped event-stream response. Explicit host trust
// and tool behavior declarations control bounded retries after stream loss.
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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

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
		tools        map[string]ToolBinding
		retry        HTTPRetryPolicy
	}
)

// NewHTTPTransport wraps an already-built HTTP dependency. Each request derives
// metadata and headers; no initialization or connection state is created.
// A negative retry attempt count panics; caller constructors return validation errors.
func NewHTTPTransport(next interface {
	Do(*http.Request) (*http.Response, error)
}, info ClientInfo, tools map[string]ToolBinding, support InputSupport, retry HTTPRetryPolicy) *HTTPTransport {
	if err := retry.Validate(); err != nil {
		panic(err)
	}
	if existing, ok := next.(*HTTPTransport); ok {
		next = existing.next
	}
	return &HTTPTransport{next: next, clientInfo: info, tools: tools, inputSupport: support, retry: retry}
}

// Do sends one request with its own metadata and validates the response before
// a generated decoder sees it. Protocol errors retain their code and raw data
// instead of being replaced by a generic HTTP status error.
func (t *HTTPTransport) Do(original *http.Request) (response *http.Response, err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(original.Context(), "mcp.http.request")
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	original = original.Clone(ctx)
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
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, NewInternalError(err)
	}
	originalID := cloneRaw(envelope["id"])
	outgoing := original.Clone(original.Context())
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
		if err := setParameterHeaders(outgoing.Header, params["arguments"], t.tools[*name].Headers); err != nil {
			return nil, err
		}
	}
	injectTraceHeaders(original.Context(), outgoing.Header)
	if request.Method == methodToolsCall {
		defer func() { err = unknownToolOutcome(err) }()
	}
	// A lost response permits repeated execution only when the host trusts the
	// tool's declaration. The same arguments, input answers and server state stay
	// fixed; each retry receives a new network request ID.
	attempts := 1
	if request.Method == methodToolsCall && t.retry.TrustToolAnnotations {
		binding := t.tools[*name]
		if binding.ReadOnly || binding.Idempotent {
			attempts = max(1, t.retry.MaxAttempts)
		}
	}
	span.SetAttributes(attribute.String("rpc.method", request.Method))
	for attempt := 1; ; attempt++ {
		receiver, receiverErr := newProgressReceiver(ctx, envelope["id"], meta)
		if receiverErr != nil {
			return nil, NewInternalError(receiverErr)
		}
		params["_meta"], err = json.Marshal(meta)
		if err != nil {
			return nil, NewInternalError(err)
		}
		envelope["params"], err = json.Marshal(params)
		if err != nil {
			return nil, NewInternalError(err)
		}
		body, err = json.Marshal(envelope)
		if err != nil {
			return nil, NewInternalError(err)
		}
		attemptRequest := outgoing.Clone(outgoing.Context())
		attemptRequest.Body = io.NopCloser(bytes.NewReader(body))
		attemptRequest.ContentLength = int64(len(body))
		response, err = t.send(attemptRequest, request.HasID, envelope, receiver)
		var interrupted *interruptedResponseError
		if err == nil || !errors.As(err, &interrupted) || interrupted.httpStatus != http.StatusOK || original.Context().Err() != nil || attempt == attempts {
			if err == nil && attempt > 1 {
				// The network reply was checked against this attempt's ID. Restore
				// the original ID only for the generated caller's private decoder.
				var completed map[string]json.RawMessage
				data, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				if err := errors.Join(readErr, closeErr); err != nil {
					return nil, NewInternalError(err)
				}
				if err := json.Unmarshal(data, &completed); err != nil {
					return nil, NewInternalError(err)
				}
				completed["id"] = originalID
				data, err = json.Marshal(completed)
				if err != nil {
					return nil, NewInternalError(err)
				}
				response.Body = io.NopCloser(bytes.NewReader(data))
				response.ContentLength = int64(len(data))
			}
			return response, err
		}
		span.AddEvent("mcp.response_interrupted", trace.WithAttributes(attribute.Int("attempt", attempt)))
		envelope["id"], err = json.Marshal(uuid.NewString())
		if err != nil {
			return nil, NewInternalError(err)
		}
	}
}

// send consumes and validates one HTTP response. A lost SSE response is marked
// separately so the caller can apply trust and attempt limits before repeating.
func (t *HTTPTransport) send(outgoing *http.Request, hasID bool, envelope map[string]json.RawMessage, receiver *progressReceiver) (*http.Response, error) {
	response, err := t.next.Do(outgoing)
	if err != nil {
		return nil, err
	}
	if !hasID {
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
		data, err = readEventStream(response.Body, func(message rpcMessage) error { return receiver.notification(outgoing.Context(), message) })
		var interrupted *interruptedResponseError
		if errors.As(err, &interrupted) {
			interrupted.httpStatus = response.StatusCode
		}
	default:
		err = NewMalformedResponseError(fmt.Errorf("unsupported content type %q", mediaType))
	}
	closeErr := response.Body.Close()
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
	if result.ResultType == resultInputRequired && outgoing.Header.Get("Mcp-Method") != methodToolsCall && outgoing.Header.Get("Mcp-Method") != "resources/read" && outgoing.Header.Get("Mcp-Method") != methodPromptsGet {
		return nil, NewMalformedResponseError(errors.New("input_required is not permitted for this method"))
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	response.Header.Set("Content-Type", "application/json")
	return response, nil
}

// readEventStream consumes comments and notifications until a complete response
// event. A lost stream discards unfinished event data; a fully framed malformed
// message fails validation instead of permitting a retry.
func readEventStream(body io.Reader, notification func(rpcMessage) error) ([]byte, error) {
	reader := bufio.NewReader(body)
	var line, data strings.Builder
	first := true
	skipLF := false
	for {
		value, _, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			return nil, &interruptedResponseError{cause: errors.New("event stream ended before the request response")}
		}
		if err != nil {
			return nil, &interruptedResponseError{cause: err}
		}
		if first {
			first = false
			if value == '\uFEFF' {
				continue
			}
		}
		if skipLF && value == '\n' {
			skipLF = false
			continue
		}
		skipLF = value == '\r'
		if value != '\r' && value != '\n' {
			line.WriteRune(value)
			continue
		}

		// CR, LF and CRLF each end one line. Only a blank line delivers the
		// buffered event; EOF never turns unfinished bytes into a response.
		text := line.String()
		line.Reset()
		if text == "" {
			if data.Len() > 0 {
				encoded := []byte(strings.TrimSuffix(data.String(), "\n"))
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
				if notification != nil {
					if err := notification(message); err != nil {
						return nil, err
					}
				}
				data.Reset()
			}
			continue
		}
		field, content, _ := strings.Cut(text, ":")
		if field == "data" {
			data.WriteString(strings.TrimPrefix(content, " "))
			data.WriteByte('\n')
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
	case methodToolsCall, methodPromptsGet:
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
