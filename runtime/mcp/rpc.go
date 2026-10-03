// This file defines the JSON-RPC messages shared by the HTTP and subprocess MCP
// clients and turns tool results into the runtime's transport-neutral result.

package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
		ResultType        string                  `json:"resultType"`    //nolint:tagliatelle // MCP defines this wire field name.
		InputRequests     map[string]InputRequest `json:"inputRequests"` //nolint:tagliatelle // MCP defines this wire field name.
		RequestState      *string                 `json:"requestState"`  //nolint:tagliatelle // MCP defines this wire field name.
		Meta              json.RawMessage         `json:"_meta"`         //nolint:tagliatelle // MCP defines this wire field name.
		Content           *[]contentItem          `json:"content"`
		StructuredContent json.RawMessage         `json:"structuredContent,omitempty"` //nolint:tagliatelle // MCP protocol field.
		IsError           bool                    `json:"isError"`                     //nolint:tagliatelle // MCP protocol field.
	}

	contentItem struct {
		Type        string          `json:"type"`
		Text        *string         `json:"text"`
		Data        *string         `json:"data"`
		MIMEType    *string         `json:"mimeType"` //nolint:tagliatelle // MCP protocol field.
		Name        *string         `json:"name"`
		Title       *string         `json:"title"`
		URI         *string         `json:"uri"`
		Description *string         `json:"description"`
		Size        *float64        `json:"size"`
		Icons       []Icon          `json:"icons"`
		Resource    json.RawMessage `json:"resource"`
		Annotations *Annotations    `json:"annotations"`
		Meta        json.RawMessage `json:"_meta,omitempty"` //nolint:tagliatelle // MCP protocol field.
	}

	resourceContents struct {
		URI      *string         `json:"uri"`
		MIMEType *string         `json:"mimeType"` //nolint:tagliatelle // MCP protocol field.
		Text     *string         `json:"text"`
		Blob     *string         `json:"blob"`
		Meta     json.RawMessage `json:"_meta,omitempty"` //nolint:tagliatelle // MCP protocol field.
	}
)

const (
	rpcVersion          = "2.0"
	methodToolsCall     = "tools/call"
	methodPromptsGet    = "prompts/get"
	resultComplete      = "complete"
	resultInputRequired = "input_required"
	elicitationForm     = "form"
)

// UnmarshalJSON rejects null control fields before Go can confuse them with
// absence or false. StructuredContent keeps null as an intentional JSON result.
func (r *toolsCallResult) UnmarshalJSON(data []byte) error {
	type wireResult toolsCallResult
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("tool result must be an object")
	}
	for _, name := range []string{"content", "inputRequests", "requestState", "isError"} {
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return fmt.Errorf("tool result %s cannot be null", name)
		}
	}
	var decoded wireResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = toolsCallResult(decoded)
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
	case resultInputRequired:
		if result.InputRequests == nil && result.RequestState == nil {
			return CallResponse{}, NewMalformedResponseError(errors.New("input_required needs inputRequests or requestState"))
		}
		if result.Content != nil || len(result.StructuredContent) > 0 || result.IsError {
			return CallResponse{}, NewMalformedResponseError(errors.New("unfinished tool result contains final content"))
		}
		for id, request := range result.InputRequests {
			if id == "" || request.Method == "" || len(request.Params) == 0 {
				return CallResponse{}, NewMalformedResponseError(errors.New("invalid input request"))
			}
		}
		return CallResponse{InputRequired: &InputRequired{Requests: result.InputRequests, RequestState: result.RequestState}}, nil
	case resultComplete:
		if result.InputRequests != nil || result.RequestState != nil {
			return CallResponse{}, NewMalformedResponseError(errors.New("complete result contains unfinished state"))
		}
	default:
		return CallResponse{}, NewMalformedResponseError(fmt.Errorf("unsupported resultType %q", result.ResultType))
	}
	if result.Content == nil {
		return CallResponse{}, NewMalformedResponseError(errors.New("tool response is missing content"))
	}
	content := make([]ContentBlock, len(*result.Content))
	for i, raw := range *result.Content {
		item, err := normalizeContentBlock(raw)
		if err != nil {
			return CallResponse{}, NewMalformedResponseError(fmt.Errorf("content[%d]: %w", i, err))
		}
		content[i] = item
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

// normalizeContentBlock decodes one MCP content union after the JSON-RPC
// response has been accepted.
func normalizeContentBlock(item contentItem) (ContentBlock, error) {
	if err := validateContentMetadata(item.Annotations, item.Meta); err != nil {
		return nil, err
	}
	switch item.Type {
	case "text":
		if item.Text == nil {
			return nil, errors.New("text content is missing text")
		}
		return &TextContent{Text: *item.Text, Annotations: item.Annotations, Meta: cloneRaw(item.Meta)}, nil
	case "image":
		if item.Data == nil || item.MIMEType == nil {
			return nil, errors.New("image content requires data and mimeType")
		}
		if err := validateBase64(*item.Data); err != nil {
			return nil, err
		}
		return &ImageContent{Data: *item.Data, MIMEType: *item.MIMEType, Annotations: item.Annotations, Meta: cloneRaw(item.Meta)}, nil
	case "audio":
		if item.Data == nil || item.MIMEType == nil {
			return nil, errors.New("audio content requires data and mimeType")
		}
		if err := validateBase64(*item.Data); err != nil {
			return nil, err
		}
		return &AudioContent{Data: *item.Data, MIMEType: *item.MIMEType, Annotations: item.Annotations, Meta: cloneRaw(item.Meta)}, nil
	case "resource_link":
		if item.Name == nil || item.URI == nil {
			return nil, errors.New("resource link requires name and uri")
		}
		if err := validateContentURI(*item.URI); err != nil {
			return nil, err
		}
		for _, icon := range item.Icons {
			if err := validateContentURI(icon.Src); err != nil {
				return nil, err
			}
			if icon.Theme != nil && *icon.Theme != "light" && *icon.Theme != "dark" {
				return nil, errors.New("icon theme must be light or dark")
			}
		}
		if item.Size != nil && *item.Size < 0 {
			return nil, errors.New("resource link size must not be negative")
		}
		return &ResourceLink{
			Name: *item.Name, URI: *item.URI, Title: item.Title,
			Description: item.Description, MIMEType: item.MIMEType, Size: item.Size,
			Icons: item.Icons, Annotations: item.Annotations, Meta: cloneRaw(item.Meta),
		}, nil
	case "resource":
		resource, err := normalizeResourceContents(item.Resource)
		if err != nil {
			return nil, err
		}
		return &EmbeddedResource{Resource: resource, Annotations: item.Annotations, Meta: cloneRaw(item.Meta)}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP content type %q", item.Type)
	}
}

// normalizeResourceContents decodes the text-or-blob union carried by an
// embedded resource.
func normalizeResourceContents(raw json.RawMessage) (ResourceContents, error) {
	if len(raw) == 0 {
		return nil, errors.New("embedded resource is missing resource")
	}
	var resource resourceContents
	if err := json.Unmarshal(raw, &resource); err != nil || resource.URI == nil {
		return nil, errors.New("embedded resource requires a resource object with uri")
	}
	if err := validateContentURI(*resource.URI); err != nil {
		return nil, err
	}
	if err := validateMeta(resource.Meta); err != nil {
		return nil, err
	}
	if (resource.Text == nil) == (resource.Blob == nil) {
		return nil, errors.New("embedded resource must contain exactly one of text or blob")
	}
	if resource.Text != nil {
		return &TextResourceContents{URI: *resource.URI, MIMEType: resource.MIMEType, Text: *resource.Text, Meta: cloneRaw(resource.Meta)}, nil
	}
	if err := validateBase64(*resource.Blob); err != nil {
		return nil, err
	}
	return &BlobResourceContents{URI: *resource.URI, MIMEType: resource.MIMEType, Blob: *resource.Blob, Meta: cloneRaw(resource.Meta)}, nil
}

// validateContentMetadata checks the closed MCP annotation values and the
// object shape required for extension metadata.
func validateContentMetadata(annotations *Annotations, meta json.RawMessage) error {
	if annotations != nil {
		for _, role := range annotations.Audience {
			if role != RoleUser && role != RoleAssistant {
				return fmt.Errorf("unsupported annotation audience %q", role)
			}
		}
		if annotations.Priority != nil && (*annotations.Priority < 0 || *annotations.Priority > 1) {
			return errors.New("annotation priority must be between zero and one")
		}
	}
	return validateMeta(meta)
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

// normalizeCallResult validates host support after decoding the wire result.
func normalizeCallResult(result toolsCallResult, support InputSupport) (CallResponse, error) {
	response, err := normalizeToolResult(result)
	if err != nil {
		return CallResponse{}, err
	}
	if response.InputRequired != nil {
		if err := response.InputRequired.Validate(support); err != nil {
			return CallResponse{}, NewMalformedResponseError(err)
		}
	}
	return response, nil
}

// validateBase64 checks the encoded bytes in one media or resource item. Empty
// data remains valid; decoding does not impose an operation-wide size limit.
func validateBase64(data string) error {
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return fmt.Errorf("content must contain base64 data: %w", err)
	}
	return nil
}

// validateContentURI checks one resource or icon address without opening it.
func validateContentURI(uri string) error {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("content URI %q must be an absolute URI", uri)
	}
	return nil
}
