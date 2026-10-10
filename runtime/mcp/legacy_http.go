// Package mcp accepts the basic 2025-11-25 HTTP messages used by older hosts.
// It adds server-owned metadata for the existing generated Goa decoders; tool
// arguments, credentials, and resource selections retain their original values.
// The server does not create protocol sessions or offer client-input exchanges.
package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"

	"goa.design/goa/v3/jsonrpc"
)

// LegacyProtocolVersion is the handshake-based revision served by generated
// HTTP servers alongside the current per-request-metadata protocol.
const LegacyProtocolVersion = "2025-11-25"

const (
	methodInitialize    = "initialize"
	methodResourcesRead = "resources/read"
)

// IsLegacyHTTPRequest selects the explicitly named older revision or an
// initialize handshake. Malformed messages still go through envelope validation;
// selection does not authenticate a request or accept mixed protocol metadata.
func IsLegacyHTTPRequest(request *http.Request, body []byte) bool {
	if request.Header.Get("MCP-Protocol-Version") == LegacyProtocolVersion {
		return true
	}
	var envelope jsonrpc.RawRequest
	return envelope.UnmarshalJSON(body) == nil && envelope.Method == methodInitialize
}

// DecodeLegacyHTTPRequest validates one basic older-protocol request and returns
// the same envelope with metadata required by the generated Goa payload decoder.
// Initialization negotiates this server's supported older version. Later calls
// must explicitly name that revision in their HTTP header. Client capabilities
// are not carried forward: this path offers no tasks or client-input requests.
func DecodeLegacyHTTPRequest(request *http.Request, body []byte) (*jsonrpc.RawRequest, *Error) {
	var envelope jsonrpc.RawRequest
	if err := envelope.UnmarshalJSON(body); err != nil {
		return nil, &Error{Code: JSONRPCParseError, Message: "Parse error"}
	}
	if envelope.Invalid || envelope.JSONRPC != rpcVersion || !envelope.HasMethod || envelope.Method == "" || (envelope.HasID && envelope.ID == nil) {
		return nil, &Error{Code: JSONRPCInvalidRequest, Message: "Invalid request"}
	}
	if envelope.Method == "notifications/initialized" && !envelope.HasID {
		if len(request.Header.Values("MCP-Protocol-Version")) != 1 || request.Header.Get("MCP-Protocol-Version") != LegacyProtocolVersion {
			return nil, &Error{Code: JSONRPCInvalidRequest, Message: "Legacy notifications require MCP-Protocol-Version: " + LegacyProtocolVersion}
		}
		return &envelope, nil
	}
	if !envelope.HasID {
		return nil, &Error{Code: JSONRPCInvalidRequest, Message: "This legacy method requires a request ID"}
	}
	headers := request.Header.Values("MCP-Protocol-Version")
	if len(headers) > 1 || envelope.Method != methodInitialize && (len(headers) != 1 || headers[0] != LegacyProtocolVersion) {
		return nil, &Error{Code: JSONRPCInvalidRequest, Message: "Legacy calls require MCP-Protocol-Version: " + LegacyProtocolVersion}
	}
	params := make(map[string]json.RawMessage)
	if len(envelope.Params) != 0 && (json.Unmarshal(envelope.Params, &params) != nil || params == nil) {
		return nil, &Error{Code: JSONRPCInvalidParams, Message: "params must be an object"}
	}
	meta := make(map[string]json.RawMessage)
	if raw, present := params["_meta"]; present && (json.Unmarshal(raw, &meta) != nil || meta == nil) {
		return nil, &Error{Code: JSONRPCInvalidParams, Message: "params._meta must be an object"}
	}
	if _, present := meta[protocolVersionKey]; present {
		return nil, &Error{Code: JSONRPCInvalidParams, Message: "Legacy requests cannot carry per-request protocol version metadata"}
	}
	if _, present := meta[clientCapabilitiesKey]; present {
		return nil, &Error{Code: JSONRPCInvalidParams, Message: "Legacy requests cannot carry per-request client capabilities"}
	}
	if token, present := meta["progressToken"]; present {
		if _, err := protocolIDKey(token); err != nil {
			return nil, &Error{Code: JSONRPCInvalidParams, Message: err.Error()}
		}
	}
	for _, field := range []string{"requestState", "inputResponses", "task"} {
		if _, present := params[field]; present {
			return nil, &Error{Code: JSONRPCInvalidParams, Message: "Basic legacy calls do not accept " + field}
		}
	}
	switch envelope.Method {
	case methodInitialize:
		if failure := validateLegacyInitialize(envelope.Params); failure != nil {
			return nil, failure
		}
		// The configured discovery endpoint supplies server identity and capabilities.
		// Handshake fields are consumed here instead of entering its Goa payload.
		params = make(map[string]json.RawMessage)
	case "ping", "tools/list", "tools/call", "resources/list", "resources/templates/list", methodResourcesRead, "prompts/list", "prompts/get", "completion/complete":
	default:
		return nil, &Error{Code: JSONRPCMethodNotFound, Message: "Method is not available in basic MCP " + LegacyProtocolVersion}
	}
	if envelope.Method == methodPromptsGet {
		if raw, present := params["arguments"]; present {
			if failure := validateStringArguments(raw, "prompt"); failure != nil {
				return nil, failure
			}
		}
	}
	if envelope.Method == methodCompletionComplete {
		if failure := validateCompletionRequest(params); failure != nil {
			return nil, failure
		}
	}
	if _, err := requestName(envelope.Method, params); err != nil {
		return nil, &Error{Code: JSONRPCInvalidParams, Message: err.Error()}
	}
	meta[protocolVersionKey] = json.RawMessage(`"2025-11-25"`)
	meta[clientCapabilitiesKey] = json.RawMessage(`{}`)
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil, &Error{Code: JSONRPCInvalidParams, Message: err.Error()}
	}
	params["_meta"] = encoded
	envelope.Params, err = json.Marshal(params)
	if err != nil {
		return nil, &Error{Code: JSONRPCInvalidParams, Message: err.Error()}
	}
	return &envelope, nil
}

// validateLegacyInitialize checks the handshake's required version, capabilities,
// and client identity. An unknown requested version remains valid negotiation
// input; the generated response names the older revision actually implemented.
func validateLegacyInitialize(raw json.RawMessage) *Error {
	var params struct {
		ProtocolVersion *string                    `json:"protocolVersion"` //nolint:tagliatelle // MCP defines this field name.
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ClientInfo      *struct {
			Name    *string `json:"name"`
			Version *string `json:"version"`
		} `json:"clientInfo"` //nolint:tagliatelle // MCP defines this field name.
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &params) != nil || params.ProtocolVersion == nil || *params.ProtocolVersion == "" || params.Capabilities == nil || params.ClientInfo == nil || params.ClientInfo.Name == nil || params.ClientInfo.Version == nil {
		return &Error{Code: JSONRPCInvalidParams, Message: "initialize requires protocolVersion, capabilities, and clientInfo with name and version"}
	}
	return nil
}
