// These tests exercise current HTTP request metadata, exact headers, response
// correlation and non-200 protocol errors without a session handshake.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"goa.design/goa-ai/internal/mcpprotocol"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestHTTPTransportCurrentProtocol(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	ctx, expectedTrace := contextWithTrace()
	for _, id := range []string{`"call-1"`, `9007199254740993`} {
		t.Run(id, func(t *testing.T) {
			next := transportFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				assert.Nil(t, ValidateHTTPRequest(req, body, nil))
				assert.Equal(t, ProtocolVersion, req.Header.Get("MCP-Protocol-Version"))
				assert.Equal(t, "tools/call", req.Header.Get("Mcp-Method"))
				assert.Equal(t, "=?base64?IHLDqWd1bGllciA=?=", req.Header.Get("Mcp-Name"))
				assert.Equal(t, expectedTrace, req.Header.Get("Traceparent"))
				assert.Empty(t, req.Header.Get("Mcp-Session-Id"))
				assert.Contains(t, string(body), `"custom/example":42`)
				var envelope struct {
					Params struct {
						Meta map[string]json.RawMessage `json:"_meta"` //nolint:tagliatelle // MCP defines this wire field name.
					} `json:"params"`
				}
				require.NoError(t, json.Unmarshal(body, &envelope))
				assert.JSONEq(t, `{}`, string(envelope.Params.Meta[clientCapabilitiesKey]))
				assert.JSONEq(t, `"`+expectedTrace+`"`, string(envelope.Params.Meta["traceparent"]))
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + id + `,"result":{"resultType":"complete","content":[],"structuredContent":null}}`))}, nil
			})
			transport := NewHTTPTransport(next, ClientInfo{Name: "test", Version: "1"}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
			request, err := http.NewRequestWithContext(ctx, "POST", "https://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":`+id+`,"method":"tools/call","params":{"name":" régulier ","_meta":{"custom/example":42},"arguments":{}}}`))
			require.NoError(t, err)
			response, err := transport.Do(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
		})
	}
}

func TestHTTPTransportResponseBoundary(t *testing.T) {
	for _, test := range []struct {
		name              string
		status            int
		contentType, body string
		code              int
		failure           string
	}{
		{"non-200 protocol error", 400, "application/json", `{"jsonrpc":"2.0","id":"a","error":{"code":-32022,"message":"unsupported","data":{"requested":"old","supported":["2026-07-28"]}}}`, -32022, ""},
		{"exact ID", 200, "application/json", `{"jsonrpc":"2.0","id":"b","result":{"resultType":"complete"}}`, 0, "response ID does not match"},
		{"legacy result", 200, "application/json", `{"jsonrpc":"2.0","id":"a","result":{}}`, 0, "resultType"},
		{"SSE progress and comments", 200, "text/event-stream", ": keepalive\n\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"updates\",\"progress\":0}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"a\",\"result\":{\"resultType\":\"complete\"}}\n\n", 0, ""},
		{"SSE server request", 200, "text/event-stream", "data: {\"jsonrpc\":\"2.0\",\"id\":\"server\",\"method\":\"ping\"}\n\n", 0, "independent server requests"},
		{"SSE disconnect", 200, "text/event-stream", ": keepalive\n\n", 0, "ended before"},
	} {
		t.Run(test.name, func(t *testing.T) {
			count := 0
			transport := NewHTTPTransport(transportFunc(func(req *http.Request) (*http.Response, error) {
				count++
				require.NoError(t, req.Body.Close())
				return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {test.contentType}}, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			}), ClientInfo{}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
			req, err := http.NewRequestWithContext(context.Background(), "POST", "https://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"a","method":"server/discover","params":{"_meta":{"progressToken":"updates"}}}`))
			require.NoError(t, err)
			response, err := transport.Do(req)
			switch {
			case test.code != 0:
				var failure *Error
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, test.code, failure.Code)
				assert.JSONEq(t, `{"requested":"old","supported":["2026-07-28"]}`, string(failure.Data))
			case test.failure != "":
				require.ErrorContains(t, err, test.failure)
			default:
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
			}
			assert.Equal(t, 1, count)
		})
	}
}

func TestHeaderBindings(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"nested":{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"}}},"count":{"type":"integer","x-mcp-header":"Count"},"flag":{"type":"boolean","x-mcp-header":"Flag"}}}`)
	bindings, err := mcpprotocol.CompileHeaderBindings(schema)
	require.NoError(t, err)
	for _, test := range []struct {
		name, arguments string
		expected        map[string]string
		failure         bool
	}{
		{"inclusive maximum", `{"count":9007199254740991,"flag":false,"nested":{"region":" café "},"unmirrored":9007199254740992}`, map[string]string{"Mcp-Param-Count": "9007199254740991", "Mcp-Param-Flag": "false", "Mcp-Param-Region": "=?base64?IGNhZsOpIA==?="}, false},
		{"inclusive minimum", `{"count":-9007199254740991}`, map[string]string{"Mcp-Param-Count": "-9007199254740991"}, false},
		{"null and absent", `{"count":null}`, map[string]string{}, false},
		{"exponent integer", `{"count":1e3}`, map[string]string{"Mcp-Param-Count": "1000"}, false},
		{"above maximum", `{"count":9007199254740992}`, nil, true},
		{"below minimum", `{"count":-9007199254740992}`, nil, true},
		{"fraction", `{"count":1.5}`, nil, true},
		{"string integer", `{"count":"1"}`, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			values, err := mcpprotocol.ParameterValues(json.RawMessage(test.arguments), bindings)
			if test.failure {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, test.expected, values)
			}
		})
	}
	for _, schema := range []string{
		`{"properties":{"a":{"type":"number","x-mcp-header":"A"}}}`,
		`{"properties":{"a":{"type":"string","x-mcp-header":"A"},"b":{"type":"string","x-mcp-header":"a"}}}`,
		`{"allOf":[{"properties":{"a":{"type":"string","x-mcp-header":"A"}}}]}`,
		`{"properties":{"a":{"type":"string","x-mcp-header":"bad name"}}}`,
	} {
		_, err := mcpprotocol.CompileHeaderBindings(json.RawMessage(schema))
		require.Error(t, err)
	}
	for _, value := range []string{" text ", "é", "=?base64?literal?=", "\n", "plain"} {
		decoded, err := mcpprotocol.DecodeHeaderValue(mcpprotocol.EncodeHeaderValue(value))
		require.NoError(t, err)
		assert.Equal(t, value, decoded)
	}
}

func TestHTTPServerProtocolErrors(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":9007199254740993,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	for _, test := range []struct {
		name, body   string
		removeHeader string
		code         int
	}{
		{"valid", valid, "", 0},
		{"missing metadata", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`, "", JSONRPCInvalidParams},
		{"unsupported version", strings.ReplaceAll(valid, "2026-07-28", "2025-06-18"), "", UnsupportedProtocolVersion},
		{"method header", valid, "Mcp-Method", HeaderMismatch},
		{"unsupported body with conflicting header", strings.ReplaceAll(valid, "2026-07-28", "2025-06-18"), "", HeaderMismatch},
		{"batch", `[` + valid + `]`, "", JSONRPCInvalidRequest},
		{"malformed", `{`, "", JSONRPCParseError},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), "POST", "/mcp", strings.NewReader(test.body))
			version := ProtocolVersion
			if test.name == "unsupported version" {
				version = "2025-06-18"
			}
			req.Header.Set("MCP-Protocol-Version", version)
			req.Header.Set("Mcp-Method", "server/discover")
			if test.removeHeader != "" {
				req.Header.Del(test.removeHeader)
			}
			failure := ValidateHTTPRequest(req, []byte(test.body), nil)
			if test.code == 0 {
				assert.Nil(t, failure)
				return
			}
			require.NotNil(t, failure)
			assert.Equal(t, test.code, failure.Code)
			recorder := httptest.NewRecorder()
			require.NoError(t, WriteProtocolError(recorder, []byte(test.body), failure))
			assert.Equal(t, 400, recorder.Code)
			if test.name == "unsupported version" {
				assert.Contains(t, recorder.Body.String(), `"id":9007199254740993`)
				assert.Contains(t, recorder.Body.String(), `"supported":["2026-07-28"]`)
			}
		})
	}
}

func TestNormalizeCurrentToolResults(t *testing.T) {
	for _, value := range []string{`null`, `"text"`, `[1]`, `42`, `true`, `{}`} {
		t.Run(value, func(t *testing.T) {
			var result toolsCallResult
			require.NoError(t, json.Unmarshal([]byte(`{"resultType":"complete","content":[],"structuredContent":`+value+`}`), &result))
			response, err := normalizeToolResult(result)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(response.StructuredContent, []byte(value)))
		})
	}
	var result toolsCallResult
	require.NoError(t, json.Unmarshal([]byte(`{"resultType":"input_required","requestState":"opaque","inputRequests":{"form":{"method":"elicitation/create","params":{"mode":"form","message":"Choose","requestedSchema":{"type":"object"}}}}}`), &result))
	response, err := normalizeToolResult(result)
	require.NoError(t, err)
	require.NotNil(t, response.InputRequired)
	assert.Equal(t, "opaque", *response.InputRequired.RequestState)
	assert.Empty(t, response.StructuredContent)
	for _, encoded := range []string{`{"content":[]}`, `{"resultType":"input_required"}`} {
		require.NoError(t, json.Unmarshal([]byte(encoded), &result))
		_, err := normalizeToolResult(result)
		assert.Error(t, err, fmt.Sprint(encoded))
		result = toolsCallResult{}
	}
}

func TestLostToolResponseDoesNotRepeatSideEffect(t *testing.T) {
	effects := 0
	transport := NewHTTPTransport(transportFunc(func(req *http.Request) (*http.Response, error) {
		require.NoError(t, req.Body.Close())
		effects++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(": accepted\n\n"))}, nil
	}), ClientInfo{}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
	req, err := http.NewRequestWithContext(t.Context(), "POST", "https://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"charge-1","method":"tools/call","params":{"name":"charge","arguments":{}}}`))
	require.NoError(t, err)
	response, err := transport.Do(req)
	if response != nil {
		require.NoError(t, response.Body.Close())
	}
	assert.Nil(t, response)
	var unknown *OutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, 1, effects)
}

func TestServerComparesMirroredIntegersNumerically(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"count","arguments":{"count":42},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	bindings := map[string][]HeaderBinding{"count": {{Name: "Count", Path: []string{"count"}, Type: "integer"}}}
	for _, test := range []struct {
		value string
		valid bool
	}{{"42", true}, {"42.0", true}, {"4.2e1", true}, {"42.5", false}, {`"42"`, false}} {
		t.Run(test.value, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), "POST", "https://example.test/mcp", bytes.NewReader(body))
			req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
			req.Header.Set("MCP-Method", "tools/call")
			req.Header.Set("MCP-Name", "count")
			req.Header.Set("Mcp-Param-Count", test.value)
			failure := ValidateHTTPRequest(req, body, bindings)
			if test.valid {
				assert.Nil(t, failure)
			} else {
				require.NotNil(t, failure)
				assert.Equal(t, HeaderMismatch, failure.Code)
			}
		})
	}
}
