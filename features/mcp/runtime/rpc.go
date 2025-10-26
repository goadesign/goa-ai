package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	ID      uint64 `json:"id"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
	ID      uint64          `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message)
}

func (e *rpcError) callerError() *Error {
	if e == nil {
		return nil
	}
	return &Error{Code: e.Code, Message: e.Message}
}

type toolsCallResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError"`
}

type contentItem struct {
	Type     string  `json:"type"`
	Text     *string `json:"text"`
	MimeType *string `json:"mimeType"`
}

func (c contentItem) text() string {
	if c.Text == nil {
		return ""
	}
	return *c.Text
}

func decodeToolCallResult(raw json.RawMessage) (CallResponse, error) {
	var result toolsCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return CallResponse{}, err
	}
	return normalizeToolResult(result)
}

func normalizeToolResult(result toolsCallResult) (CallResponse, error) {
	if len(result.Content) == 0 {
		return CallResponse{}, errors.New("empty MCP response")
	}
	item := result.Content[0]
	var payload json.RawMessage
	var structured json.RawMessage
	if item.Text != nil {
		textBytes := []byte(*item.Text)
		if json.Valid(textBytes) {
			payload = append(json.RawMessage(nil), textBytes...)
		} else {
			marshaled, err := json.Marshal(*item.Text)
			if err != nil {
				return CallResponse{}, err
			}
			payload = marshaled
		}
		if item.MimeType != nil && *item.MimeType == "application/json" && json.Valid(textBytes) {
			structured = append(json.RawMessage(nil), textBytes...)
		}
	}
	if len(payload) == 0 {
		text := item.text()
		if text == "" {
			return CallResponse{}, errors.New("tool returned no content")
		}
		marshaled, err := json.Marshal(text)
		if err != nil {
			return CallResponse{}, err
		}
		payload = marshaled
	}
	if structured == nil && json.Valid(payload) {
		structured = append(json.RawMessage(nil), payload...)
	}
	return CallResponse{Result: payload, Structured: structured}, nil
}
