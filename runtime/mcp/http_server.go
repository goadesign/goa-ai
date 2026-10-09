// Package mcp validates MCP's HTTP envelope before generated service dispatch.
// It rejects mismatched headers and unsupported revisions with protocol errors
// that retain the original request ID and the required HTTP status.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"

	"goa.design/goa-ai/internal/mcpprotocol"
	"goa.design/goa/v3/jsonrpc"
)

// ValidateHTTPRequest checks one request's release-owned metadata and mirrored
// headers. Generated servers supply the static header bindings for their tools.
func ValidateHTTPRequest(request *http.Request, body []byte, bindings map[string][]HeaderBinding) *Error {
	var envelope jsonrpc.RawRequest
	if err := envelope.UnmarshalJSON(body); err != nil {
		return &Error{Code: JSONRPCParseError, Message: "Parse error"}
	}
	if envelope.Invalid || envelope.JSONRPC != rpcVersion || !envelope.HasMethod || envelope.Method == "" || (envelope.HasID && envelope.ID == nil) {
		return &Error{Code: JSONRPCInvalidRequest, Message: "Invalid request"}
	}
	// Notifications carry optional metadata and receive no JSON-RPC response.
	if !envelope.HasID {
		return nil
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(envelope.Params, &params) != nil || params == nil {
		return &Error{Code: JSONRPCInvalidParams, Message: "params must be an object"}
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(params["_meta"], &meta) != nil || meta == nil {
		return &Error{Code: JSONRPCInvalidParams, Message: "params._meta must be an object"}
	}
	var version string
	if raw := meta[protocolVersionKey]; bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &version) != nil {
		return &Error{Code: JSONRPCInvalidParams, Message: "protocolVersion metadata is required"}
	}
	if token, present := meta["progressToken"]; present {
		if _, err := protocolIDKey(token); err != nil {
			return &Error{Code: JSONRPCInvalidParams, Message: err.Error()}
		}
	}
	var capabilities map[string]json.RawMessage
	if json.Unmarshal(meta[clientCapabilitiesKey], &capabilities) != nil || capabilities == nil {
		return &Error{Code: JSONRPCInvalidParams, Message: "clientCapabilities metadata must be an object"}
	}
	if len(request.Header.Values("MCP-Protocol-Version")) != 1 || len(request.Header.Values("Mcp-Method")) != 1 || request.Header.Get("MCP-Protocol-Version") != version || request.Header.Get("Mcp-Method") != envelope.Method {
		return &Error{Code: HeaderMismatch, Message: "MCP version or method header does not match request"}
	}
	if version != ProtocolVersion {
		data, err := json.Marshal(struct {
			Supported []string `json:"supported"`
			Requested string   `json:"requested"`
		}{[]string{ProtocolVersion}, version})
		if err != nil {
			panic(err)
		}
		return &Error{Code: UnsupportedProtocolVersion, Message: "Unsupported protocol version", Data: data}
	}

	// Prompt arguments remain strings, including empty strings. Check their raw
	// JSON types before a Go map can turn null into a zero string value.
	if envelope.Method == methodPromptsGet {
		if raw, present := params["arguments"]; present {
			if failure := validateStringArguments(raw, "prompt"); failure != nil {
				return failure
			}
		}
	}

	if envelope.Method == methodCompletionComplete {
		if failure := validateCompletionRequest(params); failure != nil {
			return failure
		}
	}

	// A listen filter is checked before the source endpoint or its middleware
	// runs. The same decoder supplies the transport's accepted-filter state.
	if envelope.Method == methodSubscriptionsListen {
		filter, err := decodeSubscriptionFilter(params["notifications"])
		if err != nil {
			return &Error{Code: JSONRPCInvalidParams, Message: err.Error()}
		}
		if len(filter.TaskIDs) > 0 {
			if failure := requireTasksExtension(capabilities); failure != nil {
				return failure
			}
		}
	}

	name, err := requestName(envelope.Method, params)
	if err != nil {
		return &Error{Code: JSONRPCInvalidParams, Message: err.Error()}
	}
	if name != nil {
		values := request.Header.Values("Mcp-Name")
		if len(values) != 1 {
			return &Error{Code: HeaderMismatch, Message: "Mcp-Name is required exactly once"}
		}
		value, err := mcpprotocol.DecodeHeaderValue(values[0])
		if err != nil || value != *name {
			return &Error{Code: HeaderMismatch, Message: "Mcp-Name does not match request"}
		}
	}
	if envelope.Method == methodToolsCall {
		expected, err := mcpprotocol.ParameterValues(params["arguments"], bindings[*name])
		if err != nil {
			return &Error{Code: HeaderMismatch, Message: err.Error()}
		}
		for _, binding := range bindings[*name] {
			key := "Mcp-Param-" + binding.Name
			values := request.Header.Values(key)
			value, present := expected[key]
			decodedExpected, expectedErr := mcpprotocol.DecodeHeaderValue(value)
			var decodedActual string
			var actualErr error
			if len(values) == 1 {
				decodedActual, actualErr = mcpprotocol.DecodeHeaderValue(values[0])
			}
			equal := decodedActual == decodedExpected
			if present && binding.Type == "integer" && len(values) == 1 && actualErr == nil {
				var number json.Number
				if json.Unmarshal([]byte(decodedActual), &number) == nil && !bytes.HasPrefix(bytes.TrimSpace([]byte(decodedActual)), []byte(`"`)) {
					actual, ok := new(big.Rat).SetString(number.String())
					expected, expectedOK := new(big.Rat).SetString(decodedExpected)
					equal = ok && expectedOK && actual.Cmp(expected) == 0
				} else {
					equal = false
				}
			}
			if !present && len(values) > 0 || present && (len(values) != 1 || expectedErr != nil || actualErr != nil || !equal) {
				return &Error{Code: HeaderMismatch, Message: fmt.Sprintf("%s does not match arguments", key)}
			}
		}
	}
	return nil
}

// WriteProtocolError writes one exact JSON-RPC error with the status required by
// the MCP HTTP binding. The generated handler reports any write failure.
func WriteProtocolError(writer http.ResponseWriter, body []byte, failure *Error) error {
	var request jsonrpc.RawRequest
	decodeErr := request.UnmarshalJSON(body)
	var id any
	if decodeErr == nil && !request.Invalid && request.HasID {
		id = request.ID
	}
	status := http.StatusBadRequest
	if failure.Code == JSONRPCMethodNotFound {
		status = http.StatusNotFound
	}
	if failure.Code == JSONRPCInternalError {
		status = http.StatusInternalServerError
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	response := jsonrpc.MakeErrorResponse(id, jsonrpc.Code(failure.Code), failure.Message, failure.Data)
	if err := json.NewEncoder(writer).Encode(response); err != nil {
		return fmt.Errorf("write MCP protocol error: %w", err)
	}
	return nil
}

// ValidateTaskCapabilities checks the capabilities on one operation request.
// A task-only creator uses it before starting work; missing support returns the
// protocol error naming the Tasks extension, while malformed objects are invalid
// parameters. HTTP subscription admission uses the same extension check.
func ValidateTaskCapabilities(meta json.RawMessage) *Error {
	var metadata map[string]json.RawMessage
	if json.Unmarshal(meta, &metadata) != nil || metadata == nil {
		return &Error{Code: JSONRPCInvalidParams, Message: "params._meta must be an object"}
	}
	var capabilities map[string]json.RawMessage
	if json.Unmarshal(metadata[clientCapabilitiesKey], &capabilities) != nil || capabilities == nil {
		return &Error{Code: JSONRPCInvalidParams, Message: "clientCapabilities metadata must be an object"}
	}
	return requireTasksExtension(capabilities)
}

// requireTasksExtension checks one request's declared extension before a Task
// subscription can reach the endpoint. Missing support names the required
// capability; malformed extension objects are invalid parameters.
func requireTasksExtension(capabilities map[string]json.RawMessage) *Error {
	var extensions map[string]json.RawMessage
	if raw, present := capabilities["extensions"]; present {
		if err := json.Unmarshal(raw, &extensions); err != nil || extensions == nil {
			return &Error{Code: JSONRPCInvalidParams, Message: "client capabilities extensions must be an object"}
		}
	}
	if raw, present := extensions[tasksExtension]; present {
		var settings map[string]json.RawMessage
		if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
			return &Error{Code: JSONRPCInvalidParams, Message: "Tasks extension capability must be an object"}
		}
		return nil
	}
	return &Error{
		Code:    MissingRequiredClientCapability,
		Message: "This operation requires the Tasks extension",
		Data:    json.RawMessage(`{"requiredCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}`),
	}
}
