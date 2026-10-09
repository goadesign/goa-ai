// These tests use synthetic HTTPS issuers to verify generated introspection
// requests, current revocation checks and safe failures. The resource server
// uses the same HTTP guard and native Goa auth callbacks as the signed profile.
package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/security"
)

const introspectionTestAudience = "https://resource.example/mcp"

func TestIntrospectionResourceChecksTypedIssuerResponse(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name, body string
		status     int
		authorized bool
		identity   bool
	}{
		{"active identity", `{"active":true,"aud":"https://resource.example/mcp","scope":"records:read","sub":"subject","client_id":"issued-client","token_type":"bEaReR"}`, 200, true, true},
		{"optional identity and times", `{"active":true,"aud":"https://resource.example/mcp","scope":"records:read"}`, 200, true, false},
		{"exact supplied issuer", `{"active":true,"aud":"https://resource.example/mcp","scope":"records:read","iss":"https://issuer.example"}`, 200, true, false},
		{"audience list", `{"active":true,"aud":["https://other.example","https://resource.example/mcp"],"scope":"records:read","extension":null}`, 200, true, false},
		{"inactive minimal", `{"active":false}`, 401, false, false},
		{"missing audience", `{"active":true,"scope":"records:read"}`, 401, false, false},
		{"wrong audience", `{"active":true,"aud":"https://other.example","scope":"records:read"}`, 401, false, false},
		{"empty audience", `{"active":true,"aud":[],"scope":"records:read"}`, 401, false, false},
		{"wrong issuer", `{"active":true,"aud":"https://resource.example/mcp","iss":"https://other.example","scope":"records:read"}`, 401, false, false},
		{"scope absent", `{"active":true,"aud":"https://resource.example/mcp"}`, 403, false, false},
		{"scope insufficient", `{"active":true,"aud":"https://resource.example/mcp","scope":"other"}`, 403, false, false},
		{"expired", fmt.Sprintf(`{"active":true,"aud":"https://resource.example/mcp","scope":"records:read","exp":%d}`, now-1), 401, false, false},
		{"expiration instant", fmt.Sprintf(`{"active":true,"aud":"https://resource.example/mcp","scope":"records:read","exp":%d}`, now), 401, false, false},
		{"future expiration", fmt.Sprintf(`{"active":true,"aud":"https://resource.example/mcp","scope":"records:read","exp":%d}`, now+3600), 200, true, false},
		{"future not before", fmt.Sprintf(`{"active":true,"aud":"https://resource.example/mcp","scope":"records:read","nbf":%d}`, now+3600), 401, false, false},
		{"not before instant", fmt.Sprintf(`{"active":true,"aud":"https://resource.example/mcp","scope":"records:read","nbf":%d}`, now), 200, true, false},
		{"missing activity", `{"aud":"https://resource.example/mcp"}`, 503, false, false},
		{"null activity", `{"active":null}`, 503, false, false},
		{"wrong activity type", `{"active":"true"}`, 503, false, false},
		{"wrong activity case", `{"Active":true}`, 503, false, false},
		{"duplicate activity", `{"active":false,"active":true}`, 503, false, false},
		{"null audience", `{"active":true,"aud":null}`, 503, false, false},
		{"number audience", `{"active":true,"aud":123}`, 503, false, false},
		{"mixed audience", `{"active":true,"aud":["https://resource.example/mcp",123]}`, 503, false, false},
		{"null audience entry", `{"active":true,"aud":["https://resource.example/mcp",null]}`, 503, false, false},
		{"null identity", `{"active":true,"aud":"https://resource.example/mcp","sub":null}`, 503, false, false},
		{"empty identity", `{"active":true,"aud":"https://resource.example/mcp","sub":""}`, 503, false, false},
		{"invalid scopes", `{"active":true,"aud":"https://resource.example/mcp","scope":"records:read  records:write"}`, 503, false, false},
		{"fractional introspection time", `{"active":true,"aud":"https://resource.example/mcp","exp":1900000000.5}`, 503, false, false},
		{"wrong token type", `{"active":true,"aud":"https://resource.example/mcp","token_type":"MAC"}`, 503, false, false},
		{"duplicate extension", `{"active":true,"extension":1,"extension":2}`, 503, false, false},
		{"trailing data", `{"active":false} {}`, 503, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			owner := newIntrospectionTestResource(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				assertIntrospectionRequest(t, request, "opaque.+/==")
				writer.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(writer, tc.body)
				assert.NoError(t, err)
			}))
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, introspectionTestAudience, nil)
			request.Header.Set("Authorization", "bEaReR   opaque.+/==")
			writer := httptest.NewRecorder()
			authorized := owner.AuthorizeHTTP(writer, request, [][]string{{"records:read"}})
			assert.Equal(t, tc.status, writer.Code)
			assert.EqualValues(t, 1, calls.Load())
			assert.Empty(t, writer.Body.String())
			if tc.authorized {
				require.NotNil(t, authorized)
				principal, ok := ResourcePrincipalFromContext(authorized.Context())
				require.True(t, ok)
				assert.Equal(t, owner.issuer, principal.Issuer)
				if tc.identity {
					assert.Equal(t, "subject", principal.Subject)
					assert.Equal(t, "issued-client", principal.ClientID)
				} else {
					assert.Empty(t, principal.Subject)
					assert.Empty(t, principal.ClientID)
				}
				return
			}
			assert.Nil(t, authorized)
			assert.Equal(t, "no-store", writer.Header().Get("Cache-Control"))
			if tc.status == http.StatusServiceUnavailable {
				assert.Empty(t, writer.Header().Get("WWW-Authenticate"))
			} else {
				assert.Contains(t, writer.Header().Get("WWW-Authenticate"), "resource_metadata=")
			}
		})
	}
}

func TestIntrospectionResourceUsesExplicitProfileAndCurrentState(t *testing.T) {
	var calls atomic.Int32
	owner := newIntrospectionTestResource(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		assertIntrospectionRequest(t, request, "looks.like.jwt")
		writer.Header().Set("Content-Type", "application/json")
		body := `{"active":true,"aud":"https://resource.example/mcp","scope":"records:read"}`
		if call > 2 {
			body = `{"active":false}`
		}
		_, err := io.WriteString(writer, body)
		assert.NoError(t, err)
	}))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, introspectionTestAudience, nil)
	request.Header.Set("Authorization", "Bearer looks.like.jwt")
	authorized := owner.AuthorizeHTTP(httptest.NewRecorder(), request, [][]string{{"records:read"}})
	require.NotNil(t, authorized)
	scheme := &security.OAuth2Scheme{RequiredScopes: []string{"records:read"}}
	ctx, err := owner.OAuth2Auth(authorized.Context(), "looks.like.jwt", scheme)
	require.NoError(t, err)
	_, err = owner.OAuth2Auth(ctx, "looks.like.jwt", scheme)
	require.ErrorIs(t, err, errInvalidAccessToken)
	assert.EqualValues(t, 3, calls.Load())
}

func TestIntrospectionResourceRejectsMalformedTokensBeforeExchange(t *testing.T) {
	var calls atomic.Int32
	owner := newIntrospectionTestResource(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	for _, token := range []string{"", "contains space", "contains\ttab", "non-ascii-é", "=bad", "bad=padding"} {
		t.Run(token, func(t *testing.T) {
			_, err := owner.BearerAuth(t.Context(), token, &security.BearerScheme{})
			assert.ErrorIs(t, err, errInvalidAccessToken)
		})
	}
	assert.Zero(t, calls.Load())
}

func TestIntrospectionResourceIssuerFailureIsUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			owner := newIntrospectionTestResource(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("WWW-Authenticate", "Basic issuer-secret-diagnostic")
				writer.WriteHeader(status)
				_, err := io.WriteString(writer, "issuer-secret-diagnostic")
				assert.NoError(t, err)
			}))
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, introspectionTestAudience, nil)
			request.Header.Set("Authorization", "Bearer opaque-token")
			writer := httptest.NewRecorder()
			assert.Nil(t, owner.AuthorizeHTTP(writer, request, nil))
			assert.Equal(t, http.StatusServiceUnavailable, writer.Code)
			assert.Empty(t, writer.Body.String())
			assert.Empty(t, writer.Header().Get("WWW-Authenticate"))
			_, err := owner.BearerAuth(t.Context(), "opaque-token", &security.BearerScheme{})
			require.ErrorIs(t, err, errResourceAuthorizationUnavailable)
			assert.NotContains(t, err.Error(), "issuer-secret-diagnostic")
		})
	}
}

func TestIntrospectionResourceCancellation(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	owner := newIntrospectionTestResource(t, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		assertIntrospectionRequest(t, request, "opaque-token")
		close(started)
		<-request.Context().Done()
		close(finished)
	}))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		_, err := owner.BearerAuth(ctx, "opaque-token", &security.BearerScheme{})
		result <- err
	}()
	<-started
	cancel()
	assert.ErrorIs(t, <-result, context.Canceled)
	<-finished
}

func TestIntrospectionResourceRejectsRedirects(t *testing.T) {
	var leaked atomic.Int32
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	t.Cleanup(redirect.Close)
	owner := newIntrospectionTestResource(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, redirect.URL, http.StatusTemporaryRedirect)
	}))
	_, err := owner.BearerAuth(t.Context(), "opaque-token", &security.BearerScheme{})
	require.ErrorIs(t, err, errResourceAuthorizationUnavailable)
	assert.Zero(t, leaked.Load())
}

func TestIntrospectionResourceValidatesConfiguration(t *testing.T) {
	valid := IntrospectionResource{
		Issuer: "https://issuer.example", Resource: introspectionTestAudience,
		Endpoint: "https://issuer.example/introspect", ClientID: "registered", ClientSecret: "secret",
	}
	for _, tc := range []struct {
		name string
		edit func(*IntrospectionResource)
	}{
		{"issuer must be HTTPS", func(config *IntrospectionResource) { config.Issuer = "http://issuer.example" }},
		{"issuer forbids queries", func(config *IntrospectionResource) { config.Issuer += "?tenant=one" }},
		{"resource must be HTTPS", func(config *IntrospectionResource) { config.Resource = "http://resource.example/mcp" }},
		{"endpoint must be HTTPS", func(config *IntrospectionResource) { config.Endpoint = "http://issuer.example/introspect" }},
		{"endpoint forbids user information", func(config *IntrospectionResource) { config.Endpoint = "https://secret@issuer.example/introspect" }},
		{"registration required", func(config *IntrospectionResource) { config.ClientID = "" }},
		{"secret required", func(config *IntrospectionResource) { config.ClientSecret = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := valid
			tc.edit(&config)
			owner, err := NewIntrospectionResourceServer(config)
			require.Error(t, err)
			assert.Nil(t, owner)
		})
	}
	owner, err := NewIntrospectionResourceServer(valid)
	require.NoError(t, err)
	assert.NotNil(t, owner)
}

// newIntrospectionTestResource constructs one resource with a trusted synthetic
// TLS issuer. Its escaped endpoint, query and separate credentials must survive
// the generated client's request construction without entering the token form.
func newIntrospectionTestResource(t *testing.T, handler http.Handler) *ResourceServer {
	t.Helper()
	peer := httptest.NewTLSServer(handler)
	t.Cleanup(peer.Close)
	owner, err := NewIntrospectionResourceServer(IntrospectionResource{
		Issuer: "https://issuer.example", Resource: introspectionTestAudience,
		Endpoint: peer.URL + "/tenant%2Fone/introspect?registration=a%2Bb",
		ClientID: "resource: client+/", ClientSecret: "private:+/ =", Client: peer.Client(),
	})
	require.NoError(t, err)
	return owner
}

// assertIntrospectionRequest checks the exact token form and independently
// encoded Basic credentials. No client secret or bearer header reaches the body.
func assertIntrospectionRequest(t *testing.T, request *http.Request, token string) {
	t.Helper()
	assert.Equal(t, http.MethodPost, request.Method)
	assert.Equal(t, "/tenant%2Fone/introspect", request.URL.EscapedPath())
	assert.Equal(t, "registration=a%2Bb", request.URL.RawQuery)
	assert.Equal(t, "application/x-www-form-urlencoded", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	username, password, ok := request.BasicAuth()
	assert.True(t, ok)
	assert.Equal(t, url.QueryEscape("resource: client+/"), username)
	assert.Equal(t, url.QueryEscape("private:+/ ="), password)
	if err := request.ParseForm(); err != nil {
		t.Error(err)
		return
	}
	assert.Equal(t, url.Values{"token": {token}, "token_type_hint": {"access_token"}}, request.PostForm)
	assert.NotContains(t, request.PostForm.Encode(), "private")
}
