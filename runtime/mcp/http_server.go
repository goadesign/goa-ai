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
