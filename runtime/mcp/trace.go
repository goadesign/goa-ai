// Package mcp attaches trace context and release-owned protocol metadata to each
// outgoing request. Trace propagation never replaces a request's existing metadata.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	protocolVersionKey    = "io.modelcontextprotocol/protocolVersion"
	clientCapabilitiesKey = "io.modelcontextprotocol/clientCapabilities"
	clientInfoKey         = "io.modelcontextprotocol/clientInfo"
)

func injectTraceHeaders(ctx context.Context, header http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}

// requestMeta starts with the caller's extension metadata, then derives the
// version, capabilities and trace context owned by this client invocation.
func requestMeta(ctx context.Context, info ClientInfo, support InputSupport, existing map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if ctx.Value(hostInputDisabledKey{}) != nil {
		support = InputSupport{}
	}
	meta := make(map[string]json.RawMessage, len(existing)+3)
	for key, value := range existing {
		meta[key] = cloneRaw(value)
	}
	meta[protocolVersionKey] = json.RawMessage(`"` + ProtocolVersion + `"`)
	capabilities := map[string]any{}
	if support.Form || support.URL {
		elicitation := map[string]any{}
		if support.Form {
			elicitation[elicitationForm] = struct{}{}
		}
		if support.URL {
			elicitation["url"] = struct{}{}
		}
		capabilities["elicitation"] = elicitation
	}
	encodedCapabilities, err := json.Marshal(capabilities)
	if err != nil {
		return nil, NewInternalError(err)
	}
	meta[clientCapabilitiesKey] = encodedCapabilities
	if info.Name != "" || info.Version != "" {
		if err := info.Validate(); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(info)
		if err != nil {
			return nil, NewInternalError(err)
		}
		meta[clientInfoKey] = encoded
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	for key, value := range carrier {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, NewInternalError(err)
		}
		meta[key] = encoded
	}
	return meta, nil
}

// toolParams adds continuation data to the original tool name and arguments.
// A restricted operation rejects continuation data before any network dispatch.
// Transport metadata is attached when the request is sent.
func toolParams(ctx context.Context, req CallRequest) (map[string]any, error) {
	if req.Continuation != nil && ctx.Value(hostInputDisabledKey{}) != nil {
		return nil, &Error{Code: JSONRPCInvalidParams, Message: "host input is disabled for this operation"}
	}
	params := map[string]any{"name": req.Tool}
	if len(req.Payload) > 0 {
		params["arguments"] = req.Payload
	}
	if req.Continuation != nil && req.Continuation.RequestState != nil {
		params["requestState"] = *req.Continuation.RequestState
	}
	if req.Continuation != nil {
		// An empty answer object marks a continuation even when the service
		// returned no state or questions. Omitting it would start a new call.
		responses := req.Continuation.InputResponses
		if responses == nil {
			responses = map[string]json.RawMessage{}
		}
		params["inputResponses"] = responses
	}
	return params, nil
}
