// This file defines the JSON-RPC messages shared by the HTTP and subprocess MCP
// clients and turns tool results into the runtime's transport-neutral result.

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	toolcontent "goa.design/goa-ai/runtime/content"
)

type (
	rpcRequest struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		ID      any    `json:"id"`
		Params  any    `json:"params"`
	}

	rpcNotification struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}

	rpcResponse struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *rpcError       `json:"error,omitempty"`
		ID      uint64          `json:"id"`
	}

	rpcMessage struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}

	rpcError struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	}

	toolsCallResult struct {
		Task              *TaskInfo               `json:"-"`
		ResultType        string                  `json:"resultType"`    //nolint:tagliatelle // MCP defines this wire field name.
		InputRequests     map[string]InputRequest `json:"inputRequests"` //nolint:tagliatelle // MCP defines this wire field name.
		RequestState      *string                 `json:"requestState"`  //nolint:tagliatelle // MCP defines this wire field name.
		Meta              json.RawMessage         `json:"_meta"`         //nolint:tagliatelle // MCP defines this wire field name.
		Content           *toolcontent.Blocks     `json:"content"`
		StructuredContent json.RawMessage         `json:"structuredContent,omitempty"` //nolint:tagliatelle // MCP protocol field.
		IsError           bool                    `json:"isError"`                     //nolint:tagliatelle // MCP protocol field.
	}
)

const (
	rpcVersion               = "2.0"
	methodToolsCall          = "tools/call"
	methodPromptsGet         = "prompts/get"
	methodCompletionComplete = "completion/complete"
	resultComplete           = "complete"
	resultInputRequired      = "input_required"
	elicitationForm          = "form"
)

// UnmarshalJSON reads resultType before decoding its fields. MCP permits extra
// result fields, so an input request never becomes completed content, and a
// completed result never becomes a continuation. Null branch fields are invalid;
// a completed structuredContent may intentionally contain JSON null.
func (r *toolsCallResult) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("tool result must be an object")
	}
	decoded := toolsCallResult{Meta: fields["_meta"]}
	if raw, ok := fields["resultType"]; ok {
		if err := json.Unmarshal(raw, &decoded.ResultType); err != nil {
			return err
		}
	}
	var controls []string
	switch decoded.ResultType {
	case resultComplete:
		controls = []string{"content", "isError"}
	case resultInputRequired:
		controls = []string{"inputRequests", "requestState"}
	}
	for _, name := range controls {
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return fmt.Errorf("tool result %s cannot be null", name)
		}
	}
	switch decoded.ResultType {
	case resultComplete:
		var complete struct {
			Content           *toolcontent.Blocks `json:"content"`
			StructuredContent json.RawMessage     `json:"structuredContent"` //nolint:tagliatelle // MCP wire name.
			IsError           bool                `json:"isError"`           //nolint:tagliatelle // MCP wire name.
		}
		if err := json.Unmarshal(data, &complete); err != nil {
			return err
		}
		decoded.Content = complete.Content
		decoded.StructuredContent = complete.StructuredContent
		decoded.IsError = complete.IsError
	case resultInputRequired:
		var pending struct {
			InputRequests map[string]InputRequest `json:"inputRequests"` //nolint:tagliatelle // MCP wire name.
			RequestState  *string                 `json:"requestState"`  //nolint:tagliatelle // MCP wire name.
		}
		if err := json.Unmarshal(data, &pending); err != nil {
			return err
		}
		decoded.InputRequests = pending.InputRequests
		decoded.RequestState = pending.RequestState
	case resultTask:
		task, err := decodeTaskInfo(data)
		if err != nil {
			return err
		}
		decoded.Task = &task
	}
	*r = decoded
	return nil
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message)
}

// numericResponse validates one JSON-RPC message, then returns a response to
// one of this client's numbered requests. Valid messages with a method are
// server requests or notifications.
func (m rpcMessage) numericResponse() (rpcResponse, bool, error) {
	if err := m.validateMessage(); err != nil {
		return rpcResponse{}, false, err
	}
	if m.Method != "" {
		return rpcResponse{}, false, nil
	}
	if bytes.Equal(bytes.TrimSpace(m.ID), []byte("null")) {
		return rpcResponse{}, false, NewMalformedResponseError(errors.New("invalid JSON-RPC response ID"))
	}
	var id uint64
	if err := json.Unmarshal(m.ID, &id); err != nil {
		return rpcResponse{}, false, NewMalformedResponseError(errors.New("invalid JSON-RPC response ID"))
	}
	rpcErr, err := m.responseError()
	if err != nil {
		return rpcResponse{}, false, err
	}
	return rpcResponse{
		JSONRPC: m.JSONRPC,
		Result:  m.Result,
		Error:   rpcErr,
		ID:      id,
	}, true, nil
}

// validateMessage requires JSON-RPC 2.0. A message may contain a method or a
// response result or error, never both.
func (m rpcMessage) validateMessage() error {
	if m.Method == "" {
		return m.validateResponse()
	}
	if m.JSONRPC != rpcVersion || m.Result != nil || m.Error != nil {
		return NewMalformedResponseError(errors.New("invalid JSON-RPC message"))
	}
	return nil
}

// validateResponse checks the fields required by every JSON-RPC response.
func (m rpcMessage) validateResponse() error {
	if m.JSONRPC != rpcVersion || m.Method != "" || len(m.ID) == 0 ||
		(m.Result != nil) == (m.Error != nil) {
		return NewMalformedResponseError(errors.New("invalid JSON-RPC response"))
	}
	if _, err := m.responseError(); err != nil {
		return err
	}
	return nil
}

// responseError requires the code and message members defined by JSON-RPC,
// then returns the error reported by the server.
func (m rpcMessage) responseError() (*rpcError, error) {
	if m.Error == nil {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(m.Error), []byte("null")) {
		return nil, NewMalformedResponseError(errors.New("invalid JSON-RPC response error"))
	}
	var fields struct {
		Code    json.RawMessage `json:"code"`
		Message json.RawMessage `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(m.Error, &fields); err != nil {
		return nil, NewMalformedResponseError(errors.New("invalid JSON-RPC response error"))
	}
	if len(fields.Code) == 0 {
		return nil, NewMalformedResponseError(errors.New("JSON-RPC response error code is required"))
	}
	var code int
	if bytes.Equal(bytes.TrimSpace(fields.Code), []byte("null")) || json.Unmarshal(fields.Code, &code) != nil {
		return nil, NewMalformedResponseError(errors.New("JSON-RPC response error code must be an integer"))
	}
	if len(fields.Message) == 0 {
		return nil, NewMalformedResponseError(errors.New("JSON-RPC response error message is required"))
	}
	var message string
	if bytes.Equal(bytes.TrimSpace(fields.Message), []byte("null")) || json.Unmarshal(fields.Message, &message) != nil {
		return nil, NewMalformedResponseError(errors.New("JSON-RPC response error message must be a string"))
	}
	return &rpcError{Code: code, Message: message, Data: cloneRaw(fields.Data)}, nil
}

func (e *rpcError) callerError() *Error {
	if e == nil {
		return nil
	}
	return &Error{Code: e.Code, Message: e.Message, Data: cloneRaw(e.Data)}
}

func normalizeToolResult(result toolsCallResult) (CallResponse, error) {
	if err := validateMeta(result.Meta); err != nil {
		return CallResponse{}, NewMalformedResponseError(err)
	}
	switch result.ResultType {
	case resultTask:
		return CallResponse{Task: result.Task}, nil
	case resultInputRequired:
		if result.InputRequests == nil && result.RequestState == nil {
			return CallResponse{}, NewMalformedResponseError(errors.New("input_required needs inputRequests or requestState"))
		}
		for id, request := range result.InputRequests {
			if id == "" || request.Method == "" || len(request.Params) == 0 {
				return CallResponse{}, NewMalformedResponseError(errors.New("invalid input request"))
			}
		}
		return CallResponse{InputRequired: &InputRequired{Requests: result.InputRequests, RequestState: result.RequestState}}, nil
	case resultComplete:
	default:
		return CallResponse{}, NewMalformedResponseError(fmt.Errorf("unsupported resultType %q", result.ResultType))
	}
	if result.Content == nil {
		return CallResponse{}, NewMalformedResponseError(errors.New("tool response is missing content"))
	}

	response := CallResponse{
		Content:           *result.Content,
		StructuredContent: append(json.RawMessage(nil), result.StructuredContent...),
	}
	if result.IsError {
		return CallResponse{}, NewToolExecutionError(response)
	}
	return response, nil
}

// validateMeta requires MCP extension metadata to be a JSON object.
func validateMeta(meta json.RawMessage) error {
	if len(meta) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(meta, &object); err != nil || object == nil {
		return errors.New("_meta must be a JSON object")
	}
	return nil
}

// cloneRaw gives each returned content value ownership of its encoded metadata.
func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

// normalizeCallResult decodes one wire result and checks the operation's host
// input restriction and configured capabilities before returning unfinished input.
func normalizeCallResult(ctx context.Context, result toolsCallResult, support InputSupport) (CallResponse, error) {
	response, err := normalizeToolResult(result)
	if err != nil {
		return CallResponse{}, err
	}
	if response.Task != nil && ctx.Value(taskSupportKey{}) == nil {
		return CallResponse{}, NewMalformedResponseError(errors.New("task returned without advertised Tasks support"))
	}
	if response.InputRequired != nil {
		if ctx.Value(hostInputDisabledKey{}) != nil {
			return CallResponse{}, NewMalformedResponseError(errors.New("host input is disabled for this operation"))
		}
		if err := response.InputRequired.Validate(support); err != nil {
			return CallResponse{}, NewMalformedResponseError(err)
		}
	}
	return response, nil
}

// validateContentURI checks one resource or icon address without opening it.
func validateContentURI(uri string) error {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("content URI %q must be an absolute URI", uri)
	}
	return nil
}
