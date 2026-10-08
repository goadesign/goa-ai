// These tests compile synthetic Goa services and exercise their generated
// metadata, authentication callbacks and MCP endpoints. Resource authorization
// must run before configured middleware, while domain credentials and errors
// retain their original meaning. Token verification itself remains a fixture.
package codegen

import "testing"

func TestMCPResourceAuthorizationUsesNativeContracts(t *testing.T) {
	runNativeCredentialPaths(t, resourceAuthorizationDesign, resourceAuthorizationTest)
}

const resourceAuthorizationDesign = `package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("resource_metadata", func() {
	Description("Serve typed metadata for a synthetic protected resource")
})

var _ = Service("metadata", func() {
	Description("Tell a client which resource and authorization servers own access")
	Method("read", func() {
		Description("Return the configured resource identifier and its authorization servers")
		Result(func() {
			Field(1, "resource", String, "Exact configured protected resource identifier", func() { Format(FormatURI) })
			Field(2, "authorization_servers", ArrayOf(String), "Authorization servers trusted by this resource", func() { MinLength(1) })
			Field(3, "scopes_supported", ArrayOf(String), "Scopes for basic access to this resource")
			Required("resource", "authorization_servers")
		})
		HTTP(func() { GET("/metadata") })
	})
})

var resourceOAuth = OAuth2Security("resource_oauth", func() {
	Description("Verify synthetic tokens through the native authentication callback")
	Scope("catalog:read", "Read synthetic catalog values")
	AuthorizationCodeFlow("https://issuer.example/authorize", "https://issuer.example/token", "")
})

var _ = Service("protected", func() {
	Description("Prove native authentication and transport context composition")
	Method("read", func() {
		Description("Return the authenticated resource identity after configured middleware runs")
		Security(resourceOAuth, func() { Scope("catalog:read") })
		Payload(func() {
			AccessToken("token", String, "Synthetic credential supplied in the bearer header")
			Field(1, "tenant", String, "Synthetic resource path value")
			Required("token", "tenant")
		})
		Result(String)
		HTTP(func() {
			GET("/mcp/{tenant}")
			Param("tenant")
		})
	})
})

var domainKey = APIKeySecurity("domain_key")

var _ = Service("records", func() {
	Description("Keep protected-resource access separate from domain credentials")
	MCP("records", "1.0")
	JSONRPC(func() {
		POST("/records/{tenant}")
		Param("tenant")
	})
	Method("read", func() {
		Description("Read the selected synthetic token using the original domain credential")
		Security(domainKey)
		Payload(func() {
			APIKey("domain_key", "key", String, "Credential owned by the domain service")
			Field(1, "tenant", String, "Tenant supplied by the endpoint URL")
			Field(2, "token", String, "Ordinary domain value selected by the caller")
			Required("key", "tenant", "token")
		})
		Result(String)
		Tool("read", "Read one selected synthetic domain value")
		JSONRPC(func() { Header("key:X-Domain-Key") })
	})
})
`

const resourceAuthorizationTest = `// This synthetic service verifies generated metadata, native authorization
// callbacks and MCP credential separation. Its token decisions are fixtures;
// they do not validate signatures or implement production OAuth.
package metadata_test

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	genclient "credential-paths.local/gen/http/metadata/client"
	genserver "credential-paths.local/gen/http/metadata/server"
	genprotectedclient "credential-paths.local/gen/http/protected/client"
	genprotectedserver "credential-paths.local/gen/http/protected/server"
	genmcpclient "credential-paths.local/gen/jsonrpc/mcp_records/client"
	genmcpserver "credential-paths.local/gen/jsonrpc/mcp_records/server"
	genmcp "credential-paths.local/gen/mcp_records"
	genmetadata "credential-paths.local/gen/metadata"
	genprotected "credential-paths.local/gen/protected"
	genrecords "credential-paths.local/gen/records"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
	goahttp "goa.design/goa/v3/http"
	"goa.design/goa/v3/http/middleware"
	goa "goa.design/goa/v3/pkg"
	"goa.design/goa/v3/security"
)

type (
	metadataService     struct{}
	metadataResourceKey struct{}
)

// Read returns the resource identifier set by this request's transport handler.
// A missing value means that the required handler did not call this endpoint.
func (s *metadataService) Read(ctx context.Context) (*genmetadata.ReadResult, error) {
	resource, ok := ctx.Value(metadataResourceKey{}).(string)
	if !ok {
		return nil, errors.New("metadata resource context missing")
	}
	return &genmetadata.ReadResult{
		Resource:             resource,
		AuthorizationServers: []string{"https://issuer.example/tenant"},
		ScopesSupported:      []string{"catalog:read"},
	}, nil
}

// ownedMetadataRequest derives one resource from the exact metadata address.
// The origin is trusted configuration; request headers cannot select a host.
func ownedMetadataRequest(next http.Handler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		resource := "https://resource.example" + strings.TrimPrefix(request.URL.EscapedPath(), "/.well-known/oauth-protected-resource")
		if request.URL.RawQuery != "" {
			resource += "?" + request.URL.RawQuery
		}
		ctx := context.WithValue(request.Context(), metadataResourceKey{}, resource)
		next.ServeHTTP(writer, request.WithContext(ctx))
	}
}

// exactMetadataDecoder reads one metadata response with exact JSON field names.
// Goa's generated response validator checks required fields after decoding.
func exactMetadataDecoder(response *http.Response) goahttp.Decoder {
	return goahttp.EncodingFunc(func(value any) error {
		return json.UnmarshalRead(response.Body, value)
	})
}

func TestMetadataUsesExactGeneratedResponseBoundary(t *testing.T) {
	cases := []struct {
		name, document, wantError string
	}{
		{"declared", ` + "`" + `{"resource":"https://resource.example/mcp","authorization_servers":["https://issuer.example"]}` + "`" + `, ""},
		{"extensions", ` + "`" + `{"resource":"https://resource.example/mcp","authorization_servers":["https://issuer.example"],"resource_name#en":"Synthetic resource","extension":{"enabled":true}}` + "`" + `, ""},
		{"case-distinct extension", ` + "`" + `{"resource":"https://resource.example/mcp","RESOURCE":"https://other.example","authorization_servers":["https://issuer.example"]}` + "`" + `, ""},
		{"uppercase resource", ` + "`" + `{"RESOURCE":"https://resource.example/mcp","authorization_servers":["https://issuer.example"]}` + "`" + `, "resource"},
		{"missing resource", ` + "`" + `{"authorization_servers":["https://issuer.example"]}` + "`" + `, "resource"},
		{"null resource", ` + "`" + `{"resource":null,"authorization_servers":["https://issuer.example"]}` + "`" + `, "resource"},
		{"wrong resource type", ` + "`" + `{"resource":12,"authorization_servers":["https://issuer.example"]}` + "`" + `, "resource"},
		{"missing issuers", ` + "`" + `{"resource":"https://resource.example/mcp"}` + "`" + `, "authorization_servers"},
		{"empty issuers", ` + "`" + `{"resource":"https://resource.example/mcp","authorization_servers":[]}` + "`" + `, "authorization_servers"},
		{"duplicate resource", ` + "`" + `{"resource":"https://other.example","resource":"https://resource.example/mcp","authorization_servers":["https://issuer.example"]}` + "`" + `, "duplicate"},
		{"duplicate extension", ` + "`" + `{"resource":"https://resource.example/mcp","authorization_servers":["https://issuer.example"],"extension":1,"extension":2}` + "`" + `, "duplicate"},
		{"trailing document", ` + "`" + `{"resource":"https://resource.example/mcp","authorization_servers":["https://issuer.example"]} {}` + "`" + `, "after top-level value"},
		{"invalid text", "{\"resource\":\"https://resource.example/" + string([]byte{0xff}) + "\",\"authorization_servers\":[\"https://issuer.example\"]}", "UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, err := writer.Write([]byte(tc.document))
				if err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(peer.Close)
			address, err := url.Parse(peer.URL)
			require.NoError(t, err)
			client := genclient.NewClient(address.Scheme, address.Host, peer.Client(), goahttp.RequestEncoder, exactMetadataDecoder, false)
			result, err := client.Read()(t.Context(), nil)
			if tc.wantError != "" {
				assert.ErrorContains(t, err, tc.wantError)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			metadata, ok := result.(*genmetadata.ReadResult)
			require.True(t, ok)
			assert.Equal(t, "https://resource.example/mcp", metadata.Resource)
			assert.Equal(t, []string{"https://issuer.example"}, metadata.AuthorizationServers)
		})
	}
}

func TestMetadataUsesGeneratedHandlerAtOwnedPath(t *testing.T) {
	cases := []struct {
		name, resource, path string
	}{
		{"root", "https://resource.example", "/.well-known/oauth-protected-resource"},
		{"path", "https://resource.example/public/mcp", "/.well-known/oauth-protected-resource/public/mcp"},
		{"sibling", "https://resource.example/other/mcp", "/.well-known/oauth-protected-resource/other/mcp"},
		{"query", "https://resource.example/mcp?tenant=blue", "/.well-known/oauth-protected-resource/mcp?tenant=blue"},
		{"query sibling", "https://resource.example/mcp?tenant=green", "/.well-known/oauth-protected-resource/mcp?tenant=green"},
		{"escaped", "https://resource.example/mcp/a%2Fb", "/.well-known/oauth-protected-resource/mcp/a%2Fb"},
	}
	mux := goahttp.NewMuxer()
	server := genserver.New(genmetadata.NewEndpoints(&metadataService{}), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, nil)
	mounted := make(map[string]bool)
	for _, tc := range cases {
		location, err := url.Parse(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		path := location.EscapedPath()
		if mounted[path] {
			continue
		}
		mux.Handle(http.MethodGet, path, ownedMetadataRequest(server.Read))
		mounted[path] = true
	}
	decode := genclient.DecodeReadResponse(goahttp.ResponseDecoder, false)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			mux.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, tc.path, nil))
			result, err := decode(writer.Result())
			if err != nil {
				t.Fatal(err)
			}
			value := result.(*genmetadata.ReadResult)
			if value.Resource != tc.resource {
				t.Fatalf("resource changed: got %q, want %q", value.Resource, tc.resource)
			}
			if !slices.Equal(value.AuthorizationServers, []string{"https://issuer.example/tenant"}) {
				t.Fatalf("issuers changed: %v", value.AuthorizationServers)
			}
			if !slices.Equal(value.ScopesSupported, []string{"catalog:read"}) {
				t.Fatalf("scopes changed: %v", value.ScopesSupported)
			}
		})
	}
	writer := httptest.NewRecorder()
	mux.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/metadata", nil))
	if writer.Code != http.StatusNotFound {
		t.Fatalf("unmounted route was served: %d", writer.Code)
	}
}

type (
	principalKey            struct{}
	configuredMiddlewareKey struct{}
	protectedService        struct {
		auth    security.AuthOAuth2Func
		work    int
		failure bool
	}
)

var (
	invalidToken      = errors.New("synthetic invalid token")
	insufficientScope = errors.New("synthetic insufficient scope")
)

// OAuth2Auth delegates method authorization to the same native callback shape
// used before dispatch. The generated endpoint retains the returned context.
func (s *protectedService) OAuth2Auth(ctx context.Context, token string, scheme *security.OAuth2Scheme) (context.Context, error) {
	return s.auth(ctx, token, scheme)
}

// Read checks the context received through the generated handler, then records
// work. A later error remains a service failure even if it names invalid_token.
func (s *protectedService) Read(ctx context.Context, _ *genprotected.ReadPayload) (string, error) {
	if ctx.Value(principalKey{}) != "synthetic-principal" || ctx.Value(configuredMiddlewareKey{}) != true {
		return "", errors.New("authenticated or configured middleware context missing")
	}
	s.work++
	if s.failure {
		return "", errors.New("invalid_token")
	}
	return requestResource(ctx)
}

// requestResource reads the exact URI supplied by Goa's request middleware.
// The configured origin owns the host; request headers and absolute-form hosts
// cannot change the token audience. Escaped paths and raw queries remain exact.
func requestResource(ctx context.Context) (string, error) {
	raw, ok := ctx.Value(middleware.RequestURIKey).(string)
	if !ok {
		return "", errors.New("native request URI context missing")
	}
	location, err := url.ParseRequestURI(raw)
	if err != nil {
		return "", fmt.Errorf("parse request URI: %w", err)
	}
	resource := "https://resource.example" + location.EscapedPath()
	if location.RawQuery != "" || location.ForceQuery {
		resource += "?" + location.RawQuery
	}
	return resource, nil
}

// syntheticAuthorization records the token's intended resource and checks the
// native method scope. Accepted requests receive principal context; rejected
// requests receive explicit synthetic decisions for the pre-dispatch guard.
func syntheticAuthorization(resource string) security.AuthOAuth2Func {
	return func(ctx context.Context, token string, scheme *security.OAuth2Scheme) (context.Context, error) {
		if err := ctx.Err(); err != nil {
			return ctx, err
		}
		actual, err := requestResource(ctx)
		if err != nil {
			return ctx, err
		}
		if actual != resource || token == "invalid" {
			return ctx, invalidToken
		}
		if token == "limited" {
			return ctx, insufficientScope
		}
		if token != "accepted" {
			return ctx, errors.New("synthetic verifier unavailable")
		}
		if err := scheme.Validate([]string{"catalog:read"}); err != nil {
			return ctx, err
		}
		return context.WithValue(ctx, principalKey{}, "synthetic-principal"), nil
	}
}

// resourceGuard calls a native OAuth function before any configured handler.
// Only this function's explicit rejections produce an authentication challenge;
// the configured handler owns every response after successful authorization.
func resourceGuard(auth security.AuthOAuth2Func, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) == 0 {
			w.Header().Set("WWW-Authenticate", ` + "`" + `Bearer resource_metadata="https://resource.example/.well-known/oauth-protected-resource"` + "`" + `)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") || strings.ContainsAny(strings.TrimPrefix(headers[0], "Bearer "), " \t\r\n") || headers[0] == "Bearer " {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ctx, err := auth(r.Context(), strings.TrimPrefix(headers[0], "Bearer "), &security.OAuth2Scheme{Name: "resource_oauth", Scopes: []string{"catalog:read"}, RequiredScopes: []string{"catalog:read"}})
		if err != nil {
			switch {
			case errors.Is(err, invalidToken):
				w.Header().Set("WWW-Authenticate", ` + "`" + `Bearer error="invalid_token"` + "`" + `)
				w.WriteHeader(http.StatusUnauthorized)
			case errors.Is(err, insufficientScope):
				w.Header().Set("WWW-Authenticate", ` + "`" + `Bearer error="insufficient_scope", scope="catalog:read"` + "`" + `)
				w.WriteHeader(http.StatusForbidden)
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestNativeAuthorizationKeepsExactResourceIdentity(t *testing.T) {
	cases := []struct{ name, target, resource string }{
		{"path", "/mcp/blue", "https://resource.example/mcp/blue"},
		{"query", "/mcp/blue?tenant=green&tenant=blue", "https://resource.example/mcp/blue?tenant=green&tenant=blue"},
		{"escaped", "/mcp/a%2Fb?tenant=a%2Fb", "https://resource.example/mcp/a%2Fb?tenant=a%2Fb"},
		{"empty query", "/mcp/blue?", "https://resource.example/mcp/blue?"},
		{"absolute form", "https://untrusted.example/mcp/blue?tenant=green", "https://resource.example/mcp/blue?tenant=green"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &protectedService{auth: syntheticAuthorization(tc.resource)}
			mux := goahttp.NewMuxer()
			server := genprotectedserver.New(genprotected.NewEndpoints(s), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, nil)
			server.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), configuredMiddlewareKey{}, true)))
				})
			})
			server.Mount(mux)
			handler := middleware.PopulateRequestContext()(resourceGuard(s.auth, mux))
			request := httptest.NewRequest(http.MethodGet, tc.target, nil)
			request.Host = "untrusted.example"
			request.Header.Set("X-Forwarded-Host", "another-untrusted.example")
			request.Header.Set("Authorization", "Bearer accepted")
			writer := httptest.NewRecorder()
			handler.ServeHTTP(writer, request)
			assert.Equal(t, http.StatusOK, writer.Code, writer.Body.String())
			assert.Equal(t, 1, s.work)
			value, err := genprotectedclient.DecodeReadResponse(goahttp.ResponseDecoder, false)(writer.Result())
			require.NoError(t, err)
			assert.Equal(t, tc.resource, value)
		})
	}
}

func TestNativeResourceRejectionRunsBeforeConfiguredMiddleware(t *testing.T) {
	cases := []struct {
		name              string
		headers           []string
		canceled, failure bool
		resource          string
		status, work      int
		challenge         string
	}{
		{name: "missing", status: 401, challenge: "resource_metadata"},
		{name: "duplicate", headers: []string{"Bearer accepted", "Bearer accepted"}, status: 400},
		{name: "malformed", headers: []string{"Bearer accepted extra"}, status: 400},
		{name: "invalid", headers: []string{"Bearer invalid"}, status: 401, challenge: "invalid_token"},
		{name: "wrong audience", headers: []string{"Bearer accepted"}, resource: "https://resource.example/mcp/green", status: 401, challenge: "invalid_token"},
		{name: "insufficient", headers: []string{"Bearer limited"}, status: 403, challenge: "insufficient_scope"},
		{name: "verifier failure", headers: []string{"Bearer unavailable"}, status: 500},
		{name: "canceled", headers: []string{"Bearer accepted"}, canceled: true, status: 500},
		{name: "accepted", headers: []string{"Bearer accepted"}, status: 200, work: 1},
		{name: "failure after work", headers: []string{"Bearer accepted"}, failure: true, status: 500, work: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource := tc.resource
			if resource == "" {
				resource = "https://resource.example/mcp/blue"
			}
			s := &protectedService{auth: syntheticAuthorization(resource), failure: tc.failure}
			mux := goahttp.NewMuxer()
			server := genprotectedserver.New(genprotected.NewEndpoints(s), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, nil)
			middlewareCalls := 0
			server.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					middlewareCalls++
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), configuredMiddlewareKey{}, true)))
				})
			})
			server.Mount(mux)
			handler := middleware.PopulateRequestContext()(resourceGuard(s.auth, mux))
			request := httptest.NewRequest(http.MethodGet, "/mcp/blue", nil)
			for _, header := range tc.headers {
				request.Header.Add("Authorization", header)
			}
			ctx, cancel := context.WithCancel(request.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			writer := httptest.NewRecorder()
			handler.ServeHTTP(writer, request.WithContext(ctx))
			assert.Equal(t, tc.status, writer.Code, writer.Body.String())
			assert.Equal(t, tc.work, s.work)
			assert.Equal(t, tc.work, middlewareCalls)
			if tc.challenge == "" {
				assert.Empty(t, writer.Header().Values("WWW-Authenticate"))
			} else {
				require.Len(t, writer.Header().Values("WWW-Authenticate"), 1)
				assert.Contains(t, writer.Header().Get("WWW-Authenticate"), tc.challenge)
			}
		})
	}
}

type (
	domainPrincipalKey struct{}
	recordsService     struct {
		order   *[]string
		work    int
		failure bool
	}
)

// APIKeyAuth checks the original domain credential after resource authorization.
// The resource token must not appear here. Successful domain authorization adds
// its own principal to the context the generated endpoint passes to Read.
func (s *recordsService) APIKeyAuth(ctx context.Context, key string, _ *security.APIKeyScheme) (context.Context, error) {
	*s.order = append(*s.order, "domain-auth")
	if ctx.Value(principalKey{}) != "synthetic-principal" {
		return ctx, errors.New("resource principal missing before domain authorization")
	}
	if key != "domain-accepted" {
		return ctx, goa.PermanentError("domain_denied", "synthetic domain key rejected")
	}
	return context.WithValue(ctx, domainPrincipalKey{}, "domain-principal"), nil
}

// Read receives the URL value, model-authored token and two authenticated
// principals through the original endpoint. A later domain failure remains a
// tool error even when it has the same name as an OAuth rejection.
func (s *recordsService) Read(ctx context.Context, payload *genrecords.ReadPayload) (string, error) {
	*s.order = append(*s.order, "work")
	if payload.Tenant != "blue" || payload.Token != "domain-selected" || payload.Key != "domain-accepted" || ctx.Value(domainPrincipalKey{}) != "domain-principal" || ctx.Value(configuredMiddlewareKey{}) != true {
		return "", errors.New("generated input or configured context changed")
	}
	s.work++
	if s.failure {
		return "", goa.PermanentError("invalid_token", "synthetic domain failure after work")
	}
	return payload.Token, nil
}

func TestMCPResourceAccessKeepsDomainCredentialOwnership(t *testing.T) {
	cases := []struct {
		name, resourceToken, domainKey string
		failure, toolError             bool
		status, work                   int
		order                          []string
		challenge                      string
	}{
		{name: "accepted", resourceToken: "accepted", domainKey: "domain-accepted", status: 200, work: 1, order: []string{"resource-auth", "http", "endpoint", "domain-auth", "work"}},
		{name: "missing resource token", domainKey: "domain-accepted", status: 401, challenge: "resource_metadata"},
		{name: "invalid resource token", resourceToken: "invalid", domainKey: "domain-accepted", status: 401, order: []string{"resource-auth"}, challenge: "invalid_token"},
		{name: "insufficient resource scope", resourceToken: "limited", domainKey: "domain-accepted", status: 403, order: []string{"resource-auth"}, challenge: "insufficient_scope"},
		{name: "wrong domain credential", resourceToken: "accepted", domainKey: "domain-rejected", status: 200, toolError: true, order: []string{"resource-auth", "http", "endpoint", "domain-auth"}},
		{name: "resource token is not a domain credential", resourceToken: "accepted", domainKey: "accepted", status: 200, toolError: true, order: []string{"resource-auth", "http", "endpoint", "domain-auth"}},
		{name: "domain failure after work", resourceToken: "accepted", domainKey: "domain-accepted", failure: true, status: 200, work: 1, toolError: true, order: []string{"resource-auth", "http", "endpoint", "domain-auth", "work"}},
	}
	for _, mode := range []string{"registered ServeHTTP", "mounted"} {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					var order []string
					s := &recordsService{order: &order, failure: tc.failure}
					endpoints := genrecords.NewEndpoints(s)
					endpoints.Use(func(next goa.Endpoint) goa.Endpoint {
						return func(ctx context.Context, input any) (any, error) {
							order = append(order, "endpoint")
							return next(ctx, input)
						}
					})
					adapter := genmcp.NewMCPAdapter(endpoints, nil)
					mux := goahttp.NewMuxer()
					server := genmcpserver.New(genmcp.NewEndpoints(adapter), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil)
					server.Use(func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							order = append(order, "http")
							next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), configuredMiddlewareKey{}, true)))
						})
					})
					if mode == "mounted" {
						server.Mount(mux)
					} else {
						mux.Handle(http.MethodPost, "/records/{tenant}", server.ServeHTTP)
					}
					auth := syntheticAuthorization("https://resource.example/records/blue")
					authorize := func(ctx context.Context, token string, scheme *security.OAuth2Scheme) (context.Context, error) {
						order = append(order, "resource-auth")
						return auth(ctx, token, scheme)
					}
					handler := middleware.PopulateRequestContext()(resourceGuard(authorize, mux))
					body := ` + "`" + `{"jsonrpc":"2.0","id":"owner","method":"tools/call","params":{"name":"read","arguments":{"token":"domain-selected"},"_meta":{"io.modelcontextprotocol/protocolVersion":"` + "`" + ` + mcpruntime.ProtocolVersion + ` + "`" + `","io.modelcontextprotocol/clientCapabilities":{}}}}` + "`" + `
					request := httptest.NewRequest(http.MethodPost, "/records/blue", strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("MCP-Protocol-Version", mcpruntime.ProtocolVersion)
					request.Header.Set("Mcp-Method", "tools/call")
					request.Header.Set("Mcp-Name", "read")
					request.Header.Set("X-Domain-Key", tc.domainKey)
					if tc.resourceToken != "" {
						request.Header.Set("Authorization", "Bearer "+tc.resourceToken)
					}
					writer := httptest.NewRecorder()
					handler.ServeHTTP(writer, request)
					assert.Equal(t, tc.status, writer.Code, writer.Body.String())
					assert.Equal(t, tc.work, s.work)
					assert.Equal(t, tc.order, order)
					if tc.challenge != "" {
						assert.Contains(t, writer.Header().Get("WWW-Authenticate"), tc.challenge)
						return
					}
					assert.Empty(t, writer.Header().Values("WWW-Authenticate"))
					value, err := genmcpclient.DecodeToolsCallResponse(goahttp.ResponseDecoder, false)(writer.Result(), "owner")
					require.NoError(t, err)
					result, ok := value.(*genmcp.ToolsCallResult).Outcome.AsComplete()
					require.True(t, ok)
					if tc.toolError {
						require.NotNil(t, result.IsError)
						assert.True(t, *result.IsError)
					} else {
						assert.JSONEq(t, ` + "`" + `"domain-selected"` + "`" + `, string(result.StructuredContent))
					}
				})
			}
		})
	}
}
`
