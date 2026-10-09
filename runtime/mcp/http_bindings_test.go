// These tests exercise resource identity at the OAuth boundary. Only generated
// credential mappings may change a request's query; other URL differences stop
// before metadata or token requests can disclose credentials to another owner.
package mcp

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuthResourceAddressCredentialQueries(t *testing.T) {
	const resource = "https://resource.example/mcp/a%2Fb?tenant=a%2Fb&order=1&order=2"
	cases := []struct {
		name, resource, request string
		allowed                 []string
		want                    bool
	}{
		{name: "unchanged", resource: resource, request: resource, want: true},
		{name: "mapped credential", resource: resource, request: resource + "&api_key=opaque+key", allowed: []string{"api_key"}, want: true},
		{name: "encoded name", resource: resource, request: resource + "&api%5Fkey=opaque%2Bkey", allowed: []string{"api_key"}, want: true},
		{name: "credential first", resource: resource, request: "https://resource.example/mcp/a%2Fb?api_key=opaque&tenant=a%2Fb&order=1&order=2", allowed: []string{"api_key"}, want: true},
		{name: "two credentials", resource: resource, request: resource + "&api_key=first&other_key=second", allowed: []string{"api_key", "other_key"}, want: true},
		{name: "wrong protocol method", resource: resource, request: resource + "&api_key=opaque"},
		{name: "case changed", resource: resource, request: resource + "&API_KEY=opaque", allowed: []string{"api_key"}},
		{name: "ordinary query added", resource: resource, request: resource + "&api_key=opaque&other=changed", allowed: []string{"api_key"}},
		{name: "tenant changed", resource: resource, request: "https://resource.example/mcp/a%2Fb?tenant=other&order=1&order=2&api_key=opaque", allowed: []string{"api_key"}},
		{name: "query order changed", resource: resource, request: "https://resource.example/mcp/a%2Fb?order=1&tenant=a%2Fb&order=2&api_key=opaque", allowed: []string{"api_key"}},
		{name: "query escape changed", resource: resource, request: "https://resource.example/mcp/a%2Fb?tenant=a%2fb&order=1&order=2&api_key=opaque", allowed: []string{"api_key"}},
		{name: "path escape changed", resource: resource, request: "https://resource.example/mcp/a%2fb?tenant=a%2Fb&order=1&order=2&api_key=opaque", allowed: []string{"api_key"}},
		{name: "different host", resource: resource, request: "https://other.example/mcp/a%2Fb?tenant=a%2Fb&order=1&order=2&api_key=opaque", allowed: []string{"api_key"}},
		{name: "different scheme", resource: resource, request: "http://resource.example/mcp/a%2Fb?tenant=a%2Fb&order=1&order=2&api_key=opaque", allowed: []string{"api_key"}},
		{name: "invalid credential escape", resource: resource, request: resource + "&api_key=%ZZ", allowed: []string{"api_key"}},
		{name: "invalid credential name", resource: resource, request: resource + "&api%ZZkey=value", allowed: []string{"api_key"}},
		{name: "invalid credential separator", resource: resource, request: resource + "&api_key=first;second", allowed: []string{"api_key"}},
		{name: "configured credential", resource: resource + "&api_key=private", request: resource + "&api_key=private", allowed: []string{"api_key"}},
		{name: "configured credential before catalog", resource: resource + "&api_key=private", request: resource + "&api_key=private"},
		{name: "ordinary URI separator", resource: "https://resource.example/mcp?tenant=a;b", request: "https://resource.example/mcp?tenant=a;b&api_key=opaque", allowed: []string{"api_key"}, want: true},
		{name: "no query", resource: "https://resource.example/mcp", request: "https://resource.example/mcp?api_key=opaque", allowed: []string{"api_key"}, want: true},
		{name: "empty query marker", resource: "https://resource.example/mcp?", request: "https://resource.example/mcp?", want: true},
		{name: "query marker removed", resource: "https://resource.example/mcp?", request: "https://resource.example/mcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configured, err := url.Parse(tc.resource)
			require.NoError(t, err)
			request, err := url.Parse(tc.request)
			require.NoError(t, err)
			assert.Equal(t, tc.want, matchesResourceAddress(configured, request, tc.allowed, []string{"api_key", "other_key"}))
			assert.Equal(t, tc.resource, configured.String())
			assert.Equal(t, tc.request, request.String())
		})
	}
}

func TestOAuthUndeclaredQueriesStopBeforeDiscovery(t *testing.T) {
	for _, query := range []string{"&api_key=private", "&other=changed", "&api_key=%ZZ"} {
		t.Run(query, func(t *testing.T) {
			peer := newOAuthPeer(t)
			transport := NewHTTPTransport(peer.transport(t, "client", "secret", nil), ClientInfo{}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
			require.Error(t, callOAuthPeer(t.Context(), transport, peer.resource+query))
			assert.Zero(t, peer.tokenCalls.Load())
			assert.Zero(t, peer.mcpCalls.Load())
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			assert.Empty(t, peer.addresses)
		})
	}
}

func TestOAuthConfiguredCredentialStopsBeforeDiscovery(t *testing.T) {
	peer := newOAuthPeer(t)
	peer.resource += "&api_key=private"
	original := peer.transport(t, "client", "secret", nil)
	transport := NewHTTPTransport(original, ClientInfo{}, HTTPBindings{CredentialQueries: map[string][]string{"resources/read": {"api_key"}}}, InputSupport{}, HTTPRetryPolicy{})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, peer.resource, strings.NewReader(`{"jsonrpc":"2.0","id":"one","method":"tools/list","params":{}}`))
	require.NoError(t, err)
	response, err := transport.Do(request)
	if response != nil {
		require.NoError(t, response.Body.Close())
	}
	require.Error(t, err)
	assert.Nil(t, response)
	assert.Zero(t, peer.tokenCalls.Load())
	assert.Zero(t, peer.mcpCalls.Load())
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	assert.Empty(t, peer.addresses)
}

func TestHTTPBindingsOwnRequestFacts(t *testing.T) {
	peer := newOAuthPeer(t)
	peer.resourceQuery = "tenant=blue&api_key=private"
	bindings := HTTPBindings{CredentialQueries: map[string][]string{"tools/call": {"api_key"}}}
	transport := NewHTTPTransport(peer.transport(t, "client", "secret", nil), ClientInfo{}, bindings, InputSupport{}, HTTPRetryPolicy{})
	bindings.CredentialQueries["tools/call"][0] = "changed"
	delete(bindings.CredentialQueries, "tools/call")
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource+"&api_key=private"))
	assert.EqualValues(t, 1, peer.tokenCalls.Load())
	assert.EqualValues(t, 1, peer.mcpCalls.Load())
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	require.Len(t, peer.forms, 1)
	assert.Equal(t, peer.resource, peer.forms[0].Get("resource"))
	for _, address := range peer.addresses {
		if !strings.HasPrefix(address, "/mcp/") {
			assert.NotContains(t, address, "api_key")
		}
	}
}

func TestHTTPBindingsOwnHeaderPaths(t *testing.T) {
	bindings := HTTPBindings{Tools: map[string]ToolBinding{"read": {Headers: []HeaderBinding{{Name: "Region", Type: "string", Path: []string{"nested", "region"}}}}}}
	next := transportFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "west", request.Header.Get("Mcp-Param-Region"))
		require.NoError(t, request.Body.Close())
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":"one","result":{"resultType":"complete"}}`))}, nil
	})
	transport := NewHTTPTransport(next, ClientInfo{}, bindings, InputSupport{}, HTTPRetryPolicy{})
	bindings.Tools["read"].Headers[0].Path[1] = "other"
	bindings.Tools["read"].Headers[0].Name = "Changed"
	delete(bindings.Tools, "read")
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://resource.example/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"one","method":"tools/call","params":{"name":"read","arguments":{"nested":{"region":"west"}}}}`))
	require.NoError(t, err)
	response, err := transport.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}
