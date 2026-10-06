// These tests send real HTTPS requests through the built-in client-secret
// transport. Synthetic metadata and token endpoints prove exact owner binding,
// credential placement, pre-dispatch failures and isolation between clients.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"goa.design/goa/v3/jsonrpc"
)

type (
	// oauthPeer records the HTTP requests made to a synthetic issuer and resource.
	oauthPeer struct {
		server        *httptest.Server
		resource      string
		issuer        string
		metadata      string
		issuerBody    string
		tokenBody     string
		tokenStatus   int
		mcpStatus     int
		challenges    []string
		probeCalls    atomic.Int32
		missingPaths  []string
		tokenRedirect string
		tokenMedia    string
		tokenCalls    atomic.Int32
		mcpCalls      atomic.Int32
		mutex         sync.Mutex
		addresses     []string
		forms         []url.Values
		tokenEntered  chan struct{}
		tokenContinue chan struct{}
		verifyGrant   func(url.Values) error
	}
)

func TestClientCredentialsTransportAndDiscoveryCaller(t *testing.T) {
	for _, discovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("discovered=%t", discovered), func(t *testing.T) {
			peer := newOAuthPeer(t)
			transport := peer.transport(t, "client-one", "secret +/", []string{"records:read"})
			if discovered {
				caller, err := NewHTTPCaller(HTTPOptions{Endpoint: peer.resource, Client: transport, ClientInfo: ClientInfo{Name: "host", Version: "1"}})
				require.NoError(t, err)
				response, err := caller.CallTool(t.Context(), CallRequest{Tool: "read", Payload: []byte(`{}`)})
				require.NoError(t, err)
				assert.JSONEq(t, `{"value":"ok"}`, string(response.StructuredContent))
				assert.EqualValues(t, 2, peer.mcpCalls.Load())
			} else {
				// Generated callers wrap an existing transport with their known tool
				// bindings. The grant must survive that same constructor path.
				wrapped := NewHTTPTransport(transport, ClientInfo{Name: "generated", Version: "1"}, nil, InputSupport{}, HTTPRetryPolicy{})
				require.NoError(t, callOAuthPeer(t.Context(), wrapped, peer.resource))
				require.NoError(t, callOAuthPeer(t.Context(), wrapped, peer.resource))
				assert.EqualValues(t, 2, peer.mcpCalls.Load())
			}
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			require.Len(t, peer.forms, 1)
			assert.Equal(t, url.Values{
				"client_id": {"client-one"}, "client_secret": {"secret +/"},
				"grant_type": {"client_credentials"}, "resource": {peer.resource}, "scope": {"records:read"},
			}, peer.forms[0])
			assert.Contains(t, peer.addresses, "/.well-known/oauth-protected-resource/mcp/a%2Fb?tenant=blue")
			assert.Contains(t, peer.addresses, "/.well-known/oauth-authorization-server/tenant/a%2Fb")
			assert.Contains(t, peer.addresses, "/token/a%2Fb?route=selected")
		})
	}
}

func TestClientCredentialsRejectsBeforeMCPDispatch(t *testing.T) {
	cases := []struct {
		name          string
		configure     func(*oauthPeer)
		wantTokenCall int32
	}{
		{"wrong resource", func(p *oauthPeer) {
			p.metadata = `{"resource":"https://other.example/mcp","authorization_servers":["` + p.issuer + `"]}`
		}, 0},
		{"other issuer", func(p *oauthPeer) {
			p.metadata = `{"resource":"` + p.resource + `","authorization_servers":["https://other.example/issuer"]}`
		}, 0},
		{"issuer mismatch", func(p *oauthPeer) {
			p.issuerBody = strings.ReplaceAll(p.issuerBody, p.issuer, "https://other.example/issuer")
		}, 0},
		{"Basic only", func(p *oauthPeer) {
			p.issuerBody = strings.ReplaceAll(p.issuerBody, `"client_secret_basic","client_secret_post"`, `"client_secret_basic"`)
		}, 0},
		{"POST only", func(p *oauthPeer) {
			p.issuerBody = strings.ReplaceAll(p.issuerBody, `"client_secret_basic","client_secret_post"`, `"client_secret_post"`)
		}, 0},
		{"unsupported grant", func(p *oauthPeer) {
			p.issuerBody = strings.ReplaceAll(p.issuerBody, "client_credentials", "authorization_code")
		}, 0},
		{"insecure token endpoint", func(p *oauthPeer) {
			p.issuerBody = strings.ReplaceAll(p.issuerBody, p.server.URL+"/token", "http://other.example/token")
		}, 0},
		{"case-folded resource", func(p *oauthPeer) { p.metadata = strings.Replace(p.metadata, `"resource"`, `"RESOURCE"`, 1) }, 0},
		{"duplicate metadata", func(p *oauthPeer) {
			p.metadata = strings.Replace(p.metadata, `"resource":`, `"resource":"https://other.example","resource":`, 1)
		}, 0},
		{"token endpoint rejects", func(p *oauthPeer) {
			p.tokenStatus = http.StatusUnauthorized
			p.tokenBody = `{"error_description":"secret diagnostic"}`
		}, 1},
		{"null scope", func(p *oauthPeer) {
			p.tokenBody = `{"access_token":"private-token","token_type":"Bearer","scope":null}`
		}, 1},
		{"null expiry", func(p *oauthPeer) {
			p.tokenBody = `{"access_token":"private-token","token_type":"Bearer","expires_in":null}`
		}, 1},
		{"missing token type", func(p *oauthPeer) { p.tokenBody = `{"access_token":"private-token"}` }, 1},
		{"ID token type", func(p *oauthPeer) { p.tokenBody = `{"access_token":"private-token","token_type":"id_token"}` }, 1},
		{"header injection", func(p *oauthPeer) {
			p.tokenBody = `{"access_token":"private-token\r\nInjected: yes","token_type":"Bearer"}`
		}, 1},
		{"expired token", func(p *oauthPeer) {
			p.tokenBody = `{"access_token":"private-token","token_type":"Bearer","expires_in":0}`
		}, 1},
		{"invalid scope spacing", func(p *oauthPeer) {
			p.tokenBody = `{"access_token":"private-token","token_type":"Bearer","scope":" records:read"}`
		}, 1},
		{"duplicate token", func(p *oauthPeer) {
			p.tokenBody = `{"access_token":"private-token","access_token":"other","token_type":"Bearer"}`
		}, 1},
		{"trailing JSON", func(p *oauthPeer) { p.tokenBody += `{}` }, 1},
		{"redirect", func(p *oauthPeer) { p.tokenRedirect = p.server.URL + "/mcp/a%2Fb?tenant=blue" }, 1},
		{"wrong media type", func(p *oauthPeer) { p.tokenMedia = "text/html" }, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newOAuthPeer(t)
			tc.configure(peer)
			transport := peer.transport(t, "client-one", "private-secret", []string{"records:read"})
			err := callOAuthPeer(t.Context(), transport, peer.resource)
			require.Error(t, err)
			assert.Equal(t, tc.wantTokenCall, peer.tokenCalls.Load())
			assert.Zero(t, peer.mcpCalls.Load())
			var unknown *OutcomeUnknownError
			var internal *InternalError
			assert.NotErrorAs(t, err, &unknown)
			assert.NotErrorAs(t, err, &internal)
			assert.NotContains(t, err.Error(), "private-secret")
			assert.NotContains(t, err.Error(), "private-token")
			assert.NotContains(t, err.Error(), "secret diagnostic")
		})
	}
}

func TestClientCredentialsKeepsExplicitHTTPRejections(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			peer := newOAuthPeer(t)
			peer.mcpStatus = code
			transport := peer.transport(t, "client-one", "secret", nil)
			err := callOAuthPeer(t.Context(), transport, peer.resource)
			var failure *HTTPResponseError
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, code, failure.StatusCode)
			assert.EqualValues(t, 1, peer.mcpCalls.Load())
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
		})
	}
}

func TestClientCredentialsCancellationAndClientIsolation(t *testing.T) {
	peer := newOAuthPeer(t)
	peer.tokenEntered = make(chan struct{}, 1)
	peer.tokenContinue = make(chan struct{})
	transport := peer.transport(t, "first", "first-secret", nil)
	ctx, cancel := context.WithCancel(t.Context())
	completed := make(chan error, 1)
	go func() { completed <- callOAuthPeer(ctx, transport, peer.resource) }()
	select {
	case <-peer.tokenEntered:
	case err := <-completed:
		t.Fatalf("request stopped before token exchange: %v", err)
	case <-t.Context().Done():
		t.Fatal("token exchange did not start")
	}
	waiting, stopWaiting := context.WithCancel(t.Context())
	stopWaiting()
	require.ErrorIs(t, callOAuthPeer(waiting, transport, peer.resource), context.Canceled)
	cancel()
	require.ErrorIs(t, <-completed, context.Canceled)
	close(peer.tokenContinue)
	other := peer.transport(t, "second", "second-secret", nil)
	require.NoError(t, callOAuthPeer(t.Context(), other, peer.resource))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.EqualValues(t, 3, peer.tokenCalls.Load())
	assert.EqualValues(t, 2, peer.mcpCalls.Load())
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	assert.Equal(t, []string{"first", "second", "first"}, []string{peer.forms[0].Get("client_id"), peer.forms[1].Get("client_id"), peer.forms[2].Get("client_id")})
}

func TestClientCredentialsExpiryAndMetadataChange(t *testing.T) {
	peer := newOAuthPeer(t)
	transport := peer.transport(t, "client", "secret", nil)
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	// Advancing this grant's acquisition time models an expired token without
	// making the test sleep. The next operation must exchange before dispatch.
	transport.authorization.obtained = time.Now().Add(-time.Hour)
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
	peer.metadata = strings.ReplaceAll(peer.metadata, peer.issuer, "https://other.example/issuer")
	require.Error(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.EqualValues(t, 2, peer.mcpCalls.Load())
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
}

func TestOAuthDiscoveryAddresses(t *testing.T) {
	cases := []struct {
		address string
		issuer  bool
		want    []string
	}{
		{"https://owner.example", false, []string{"https://owner.example/.well-known/oauth-protected-resource"}},
		{"https://owner.example/", false, []string{"https://owner.example/.well-known/oauth-protected-resource"}},
		{"https://owner.example/mcp/a%2Fb?tenant=a%2Fb", false, []string{"https://owner.example/.well-known/oauth-protected-resource/mcp/a%2Fb?tenant=a%2Fb", "https://owner.example/.well-known/oauth-protected-resource"}},
		{"https://owner.example/?", false, []string{"https://owner.example/.well-known/oauth-protected-resource?", "https://owner.example/.well-known/oauth-protected-resource"}},
		{"https://owner.example", true, []string{"https://owner.example/.well-known/oauth-authorization-server", "https://owner.example/.well-known/openid-configuration"}},
		{"https://owner.example/tenant/a%2Fb", true, []string{"https://owner.example/.well-known/oauth-authorization-server/tenant/a%2Fb", "https://owner.example/.well-known/openid-configuration/tenant/a%2Fb", "https://owner.example/tenant/a%2Fb/.well-known/openid-configuration"}},
	}
	for _, tc := range cases {
		t.Run(tc.address+fmt.Sprint(tc.issuer), func(t *testing.T) {
			address, err := url.Parse(tc.address)
			require.NoError(t, err)
			addresses := resourceMetadataAddresses(address)
			if tc.issuer {
				addresses = issuerMetadataAddresses(address)
			}
			actual := make([]string, len(addresses))
			for i, address := range addresses {
				actual[i] = address.String()
			}
			assert.Equal(t, tc.want, actual)
		})
	}
}

// newOAuthPeer serves exact metadata and token URLs and records credentials only
// in this test. Each response is synthetic and contains no external identity.
func newOAuthPeer(t *testing.T) *oauthPeer {
	t.Helper()
	peer := &oauthPeer{tokenBody: `{"access_token":"private-token","token_type":"bEaReR","expires_in":3600,"extension":{"valid":true}}`}
	peer.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		peer.mutex.Lock()
		peer.addresses = append(peer.addresses, request.URL.RequestURI())
		peer.mutex.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		for _, missing := range peer.missingPaths {
			if request.URL.EscapedPath() == missing {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
		}
		var body string
		switch request.URL.EscapedPath() {
		case "/.well-known/oauth-protected-resource/mcp/a%2Fb", "/.well-known/oauth-protected-resource", "/challenge-metadata/a%2Fb":
			assert.Empty(t, request.Header.Get("Authorization"))
			body = peer.metadata
		case "/.well-known/oauth-authorization-server/tenant/a%2Fb", "/.well-known/openid-configuration/tenant/a%2Fb", "/tenant/a%2Fb/.well-known/openid-configuration":
			assert.Empty(t, request.Header.Get("Authorization"))
			body = peer.issuerBody
		case "/token/a%2Fb":
			peer.tokenCalls.Add(1)
			if peer.tokenRedirect != "" {
				writer.Header().Set("Location", peer.tokenRedirect)
				writer.WriteHeader(http.StatusFound)
				return
			}
			if peer.tokenMedia != "" {
				writer.Header().Set("Content-Type", peer.tokenMedia)
			}
			assert.Empty(t, request.Header.Get("Authorization"))
			assert.Equal(t, "route=selected", request.URL.RawQuery)
			assert.Equal(t, "application/x-www-form-urlencoded", request.Header.Get("Content-Type"))
			assert.NoError(t, request.ParseForm())
			peer.mutex.Lock()
			peer.forms = append(peer.forms, request.PostForm)
			verifyGrant := peer.verifyGrant
			peer.mutex.Unlock()
			if verifyGrant != nil {
				if err := verifyGrant(request.PostForm); err != nil {
					http.Error(writer, "Invalid client authentication", http.StatusUnauthorized)
					return
				}
			}
			if peer.tokenEntered != nil {
				select {
				case peer.tokenEntered <- struct{}{}:
				default:
				}
				select {
				case <-peer.tokenContinue:
				case <-request.Context().Done():
					return
				}
			}
			if peer.tokenStatus != 0 {
				writer.WriteHeader(peer.tokenStatus)
			}
			body = peer.tokenBody
		case "/mcp/a%2Fb":
			peer.mcpCalls.Add(1)
			if request.Header.Get("Authorization") == "" {
				peer.probeCalls.Add(1)
				encoded, err := io.ReadAll(request.Body)
				assert.NoError(t, err)
				var probe jsonrpc.RawRequest
				assert.NoError(t, probe.UnmarshalJSON(encoded))
				assert.Equal(t, "server/discover", probe.Method)
				assert.Nil(t, ValidateHTTPRequest(request, encoded, nil))
				for _, challenge := range peer.challenges {
					writer.Header().Add("WWW-Authenticate", challenge)
				}
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			assert.Equal(t, "Bearer private-token", request.Header.Get("Authorization"))
			assert.Equal(t, "tenant=blue", request.URL.RawQuery)
			if peer.mcpStatus != 0 {
				writer.Header().Set("WWW-Authenticate", `Bearer realm="synthetic"`)
				writer.WriteHeader(peer.mcpStatus)
				return
			}
			encoded, err := io.ReadAll(request.Body)
			assert.NoError(t, err)
			var rpc jsonrpc.RawRequest
			assert.NoError(t, rpc.UnmarshalJSON(encoded))
			result := `{"resultType":"complete","content":[],"structuredContent":{"value":"ok"}}`
			if rpc.Method == "tools/list" {
				result = `{"resultType":"complete","tools":[{"name":"read","inputSchema":{"type":"object","additionalProperties":false},"annotations":{}}],"ttlMs":0,"cacheScope":"private"}`
			}
			response := &jsonrpc.Response{JSONRPC: rpcVersion, ID: rpc.ID, Result: json.RawMessage(result)}
			encoded, err = response.MarshalJSON()
			assert.NoError(t, err)
			body = string(encoded)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
		_, err := io.WriteString(writer, body)
		assert.NoError(t, err)
	}))
	t.Cleanup(peer.server.Close)
	peer.resource = peer.server.URL + "/mcp/a%2Fb?tenant=blue"
	peer.issuer = peer.server.URL + "/tenant/a%2Fb"
	peer.metadata = fmt.Sprintf(`{"resource":%q,"authorization_servers":[%q],"extension":null}`, peer.resource, peer.issuer)
	peer.issuerBody = fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q,"grant_types_supported":["client_credentials"],"token_endpoint_auth_methods_supported":["client_secret_basic","client_secret_post"]}`, peer.issuer, peer.server.URL+"/token/a%2Fb?route=selected")
	return peer
}

// transport constructs one isolated grant using the peer's trusted TLS client.
func (p *oauthPeer) transport(t *testing.T, clientID, secret string, scopes []string) *HTTPTransport {
	t.Helper()
	transport, err := NewClientCredentialsHTTPTransport(HTTPOptions{Endpoint: p.resource, Client: p.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, ClientCredentials{Issuer: p.issuer, ClientID: clientID, ClientSecret: secret, Scopes: scopes})
	require.NoError(t, err)
	return transport
}

// callOAuthPeer sends one complete tool request and closes a successful response.
func callOAuthPeer(ctx context.Context, transport *HTTPTransport, resource string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, resource, strings.NewReader(`{"jsonrpc":"2.0","id":"one","method":"tools/call","params":{"name":"read","arguments":{}}}`))
	if err != nil {
		return err
	}
	response, err := transport.Do(request)
	if response != nil {
		return errors.Join(err, response.Body.Close())
	}
	return err
}

func TestClientCredentialsOrderedDiscovery(t *testing.T) {
	cases := []struct {
		name    string
		missing []string
		prefix  []string
	}{
		{"resource root", []string{"/.well-known/oauth-protected-resource/mcp/a%2Fb"}, []string{"/.well-known/oauth-protected-resource/mcp/a%2Fb?tenant=blue", "/.well-known/oauth-protected-resource"}},
		{"OpenID insertion", []string{"/.well-known/oauth-authorization-server/tenant/a%2Fb"}, []string{"/.well-known/oauth-protected-resource/mcp/a%2Fb?tenant=blue", "/.well-known/oauth-authorization-server/tenant/a%2Fb", "/.well-known/openid-configuration/tenant/a%2Fb"}},
		{"OpenID append", []string{"/.well-known/oauth-authorization-server/tenant/a%2Fb", "/.well-known/openid-configuration/tenant/a%2Fb"}, []string{"/.well-known/oauth-protected-resource/mcp/a%2Fb?tenant=blue", "/.well-known/oauth-authorization-server/tenant/a%2Fb", "/.well-known/openid-configuration/tenant/a%2Fb", "/tenant/a%2Fb/.well-known/openid-configuration"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newOAuthPeer(t)
			peer.missingPaths = tc.missing
			transport := peer.transport(t, "client", "secret", nil)
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			require.GreaterOrEqual(t, len(peer.addresses), len(tc.prefix))
			assert.Equal(t, tc.prefix, peer.addresses[:len(tc.prefix)])
		})
	}
}

func TestClientCredentialsUnreportedLifetimeAndResourceBinding(t *testing.T) {
	peer := newOAuthPeer(t)
	peer.tokenBody = `{"access_token":"private-token","token_type":"Bearer"}`
	transport := peer.transport(t, "client", "secret", nil)
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
	require.Error(t, callOAuthPeer(t.Context(), transport, peer.server.URL+"/mcp/a%2Fb?tenant=other"))
	assert.EqualValues(t, 2, peer.mcpCalls.Load())
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
}

func TestClientCredentialsConcurrentCalls(t *testing.T) {
	peer := newOAuthPeer(t)
	transport := peer.transport(t, "client", "secret", nil)
	completed := make(chan error, 4)
	for range 4 {
		go func() { completed <- callOAuthPeer(t.Context(), transport, peer.resource) }()
	}
	for range 4 {
		require.NoError(t, <-completed)
	}
	assert.EqualValues(t, 4, peer.mcpCalls.Load())
	assert.EqualValues(t, 1, peer.tokenCalls.Load())
}

func TestClientCredentialsConstructionRejectsUnsafeConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		change func(*HTTPOptions, *ClientCredentials)
	}{
		{"insecure resource", func(o *HTTPOptions, _ *ClientCredentials) { o.Endpoint = "http://resource.example/mcp" }},
		{"empty resource host", func(o *HTTPOptions, _ *ClientCredentials) { o.Endpoint = "https://:443/mcp" }},
		{"unescaped resource path", func(o *HTTPOptions, _ *ClientCredentials) { o.Endpoint = "https://resource.example/my records" }},
		{"resource fragment", func(o *HTTPOptions, _ *ClientCredentials) { o.Endpoint += "#" }},
		{"resource user information", func(o *HTTPOptions, _ *ClientCredentials) { o.Endpoint = "https://user:secret@resource.example/mcp" }},
		{"insecure issuer", func(_ *HTTPOptions, c *ClientCredentials) { c.Issuer = "http://issuer.example" }},
		{"issuer query", func(_ *HTTPOptions, c *ClientCredentials) { c.Issuer += "?" }},
		{"issuer fragment", func(_ *HTTPOptions, c *ClientCredentials) { c.Issuer += "#" }},
		{"nil HTTP client", func(o *HTTPOptions, _ *ClientCredentials) { o.Client = (*http.Client)(nil) }},
		{"missing client identifier", func(_ *HTTPOptions, c *ClientCredentials) { c.ClientID = "" }},
		{"missing secret", func(_ *HTTPOptions, c *ClientCredentials) { c.ClientSecret = "" }},
		{"empty scope", func(_ *HTTPOptions, c *ClientCredentials) { c.Scopes = []string{""} }},
		{"scope contains space", func(_ *HTTPOptions, c *ClientCredentials) { c.Scopes = []string{"records:read other"} }},
		{"non-ASCII scope", func(_ *HTTPOptions, c *ClientCredentials) { c.Scopes = []string{"récirds"} }},
		{"duplicate scopes", func(_ *HTTPOptions, c *ClientCredentials) { c.Scopes = []string{"records:read", "records:read"} }},
		{"opaque HTTP dependency", func(o *HTTPOptions, _ *ClientCredentials) {
			o.Client = transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("constructor sent a request"); return nil, nil })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := HTTPOptions{Endpoint: "https://resource.example/mcp", ClientInfo: ClientInfo{Name: "host", Version: "1"}}
			credentials := ClientCredentials{Issuer: "https://issuer.example", ClientID: "registered", ClientSecret: "secret"}
			tc.change(&opts, &credentials)
			_, err := NewClientCredentialsHTTPTransport(opts, credentials)
			require.Error(t, err)
		})
	}
}

func TestClientCredentialsAcceptsReturnedPermissions(t *testing.T) {
	for _, granted := range []string{"records:read", "records:read other", "!#[]^~ records:read", "records:all", "other:read"} {
		t.Run(granted, func(t *testing.T) {
			peer := newOAuthPeer(t)
			peer.tokenBody = fmt.Sprintf(`{"access_token":"private-token","token_type":"Bearer","scope":%q}`, granted)
			transport := peer.transport(t, "client", "secret", []string{"records:read"})
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			assert.EqualValues(t, 1, peer.mcpCalls.Load())
		})
	}
}

func TestClientCredentialsTracesExcludeCredentialContent(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	peer := newOAuthPeer(t)
	peer.missingPaths = []string{"/.well-known/oauth-authorization-server/tenant/a%2Fb"}
	peer.tokenStatus = http.StatusUnauthorized
	peer.tokenBody = `{"error_description":"private issuer diagnostic"}`
	transport := peer.transport(t, "client", "private-secret", nil)
	require.Error(t, callOAuthPeer(t.Context(), transport, peer.resource))
	ended := recorder.Ended()
	var prepare sdktrace.ReadOnlySpan
	for _, span := range ended {
		if span.Name() == "mcp.oauth.prepare" {
			prepare = span
		}
	}
	require.NotNil(t, prepare)
	assert.Equal(t, codes.Error, prepare.Status().Code)
	httpSpans := 0
	for _, span := range ended {
		for _, event := range span.Events() {
			assert.NotContains(t, fmt.Sprint(event.Attributes), "private-secret")
			assert.NotContains(t, fmt.Sprint(event.Attributes), "private issuer diagnostic")
		}
		assert.NotContains(t, span.Status().Description, "private issuer diagnostic")
		if span.Name() != "mcp.oauth.http.request" {
			continue
		}
		httpSpans++
		assert.Equal(t, prepare.SpanContext().SpanID(), span.Parent().SpanID())
		for _, attr := range span.Attributes() {
			if string(attr.Key) == "http.response.status_code" && attr.Value.AsInt64() == http.StatusNotFound {
				assert.Equal(t, codes.Unset, span.Status().Code)
			}
		}
	}
	assert.Equal(t, 4, httpSpans)
}
