// This file defines the JSON-RPC messages shared by the HTTP and subprocess MCP
// clients and turns tool results into the runtime's transport-neutral result.

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type (
	rpcRequest struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		ID      uint64 `json:"id"`
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
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}

	rpcReply struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *rpcError       `json:"error,omitempty"`
		ID      json.RawMessage `json:"id"`
	}

	rpcError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}

	initializeResult struct {
		ProtocolVersion string              `json:"protocolVersion"` //nolint:tagliatelle // MCP protocol field.
		ServerInfo      *serverInfo         `json:"serverInfo"`      //nolint:tagliatelle // MCP protocol field.
		Capabilities    *serverCapabilities `json:"capabilities"`
	}

	serverInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	serverCapabilities struct {
		Tools *struct{} `json:"tools"`
	}

	toolsCallResult struct {
		Content           *[]contentItem  `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent,omitempty"` //nolint:tagliatelle // MCP protocol field.
		IsError           bool            `json:"isError"`                     //nolint:tagliatelle // MCP protocol field.
	}

	contentItem struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
)

const (
	rpcVersion           = "2.0"
	rpcMethodInitialize  = "initialize"
	rpcMethodInitialized = "notifications/initialized"
)

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
	return &rpcError{Code: code, Message: message}, nil
}

func (e *rpcError) callerError() *Error {
	if e == nil {
		return nil
	}
	return &Error{Code: e.Code, Message: e.Message}
}

// validateInitializeResult checks the server identity and the tool support
// required by both handwritten callers before they accept the MCP session.
func validateInitializeResult(result initializeResult) error {
	if result.ProtocolVersion == "" {
		return errors.New("mcp: initialize response protocolVersion is required")
	}
	if result.ProtocolVersion != DefaultProtocolVersion {
		return fmt.Errorf(
			"mcp: server selected protocol version %q, client supports %q",
			result.ProtocolVersion,
			DefaultProtocolVersion,
		)
	}
	if result.ServerInfo == nil {
		return errors.New("mcp: initialize response serverInfo is required")
	}
	if result.ServerInfo.Name == "" {
		return errors.New("mcp: initialize response serverInfo.name is required")
	}
	if result.ServerInfo.Version == "" {
		return errors.New("mcp: initialize response serverInfo.version is required")
	}
	if result.Capabilities == nil {
		return errors.New("mcp: initialize response capabilities are required")
	}
	if result.Capabilities.Tools == nil {
		return errors.New("mcp: initialize response tools capability is required")
	}
	return nil
}

func normalizeToolResult(result toolsCallResult) (CallResponse, error) {
	if result.Content == nil {
		return CallResponse{}, NewMalformedResponseError(errors.New("tool response is missing content"))
	}
	if len(result.StructuredContent) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(result.StructuredContent, &object); err != nil || object == nil {
			return CallResponse{}, NewMalformedResponseError(errors.New("structuredContent must be a JSON object"))
		}
	}
	content := make([]string, len(*result.Content))
	for i, item := range *result.Content {
		if item.Type != "text" {
			return CallResponse{}, NewMalformedResponseError(
				fmt.Errorf("unsupported MCP content type %q", item.Type),
			)
		}
		if item.Text == nil {
			return CallResponse{}, NewMalformedResponseError(errors.New("text content is missing text"))
		}
		content[i] = *item.Text
	}
	response := CallResponse{
		Content:           content,
		StructuredContent: append(json.RawMessage(nil), result.StructuredContent...),
	}
	if result.IsError {
		return CallResponse{}, NewToolExecutionError(response)
	}
	return response, nil
}
