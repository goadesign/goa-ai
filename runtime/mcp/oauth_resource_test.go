// These tests check HTTP challenges, scope alternatives and the generated
// public metadata handler with real synthetic signed tokens. Rejected requests
// cannot be mistaken for completed or retryable domain operations.
package mcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genmetadataclient "goa.design/goa-ai/internal/mcpauth/gen/http/resource_metadata/client"
	genmetadata "goa.design/goa-ai/internal/mcpauth/gen/resource_metadata"
	goahttp "goa.design/goa/v3/http"
	"goa.design/goa/v3/security"
)

func TestResourceServerHTTPAuthorization(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	server, err := NewJWTResourceServer(JWTResource{
		Issuer: "https://issuer.example", Resource: "https://resource.example/mcp",
		Keys:       jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public}}},
		Algorithms: []jose.SignatureAlgorithm{jose.EdDSA},
	})
	require.NoError(t, err)
	token := signResourceJWT(t, private, jose.EdDSA, "", "at+jwt", resourceClaimsDocument)
	for _, tc := range []struct {
		name, query  string
		values       []string
		alternatives [][]string
		status       int
		code, scope  string
	}{
		{"valid", "", []string{"Bearer " + token}, [][]string{{"records:read"}}, http.StatusOK, "", ""},
		{"bearer case", "", []string{"bearer " + token}, nil, http.StatusOK, "", ""},
		{"several spaces", "", []string{"Bearer   " + token}, nil, http.StatusOK, "", ""},
		{"second alternative", "", []string{"Bearer " + token}, [][]string{{"administration"}, {"records:read", "records:write"}}, http.StatusOK, "", ""},
		{"every scope required", "", []string{"Bearer " + token}, [][]string{{"records:read", "administration"}}, http.StatusForbidden, "insufficient_scope", "records:read administration"},
		{"case distinct scope", "", []string{"Bearer " + token}, [][]string{{"Records:read"}}, http.StatusForbidden, "insufficient_scope", "Records:read"},
		{"no wildcard inference", "", []string{"Bearer " + token}, [][]string{{"records:*"}}, http.StatusForbidden, "insufficient_scope", "records:*"},
		{"missing", "", nil, [][]string{{"records:read"}}, http.StatusUnauthorized, "", "records:read"},
		{"wrong scheme", "", []string{"Basic synthetic"}, nil, http.StatusUnauthorized, "", ""},
		{"invalid token", "", []string{"Bearer private-token-content"}, nil, http.StatusUnauthorized, "invalid_token", ""},
		{"duplicate header", "", []string{"Bearer " + token, "Bearer " + token}, nil, http.StatusBadRequest, "invalid_request", ""},
		{"query token", "access_token=private-token-content", []string{"Bearer " + token}, nil, http.StatusBadRequest, "invalid_request", ""},
		{"encoded query name", "access%5ftoken=private-token-content", []string{"Bearer " + token}, nil, http.StatusBadRequest, "invalid_request", ""},
		{"empty query token", "access_token=", nil, nil, http.StatusBadRequest, "invalid_request", ""},
		{"ordinary query", "tenant=blue&api_key=domain-credential", []string{"Bearer " + token}, nil, http.StatusOK, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://resource.example/mcp?"+tc.query, nil)
			for _, value := range tc.values {
				request.Header.Add("Authorization", value)
			}
			writer := httptest.NewRecorder()
			allowed := server.AuthorizeHTTP(writer, request, tc.alternatives) != nil
			assert.Equal(t, tc.status == http.StatusOK, allowed)
			assert.Equal(t, tc.status, writer.Code)
			challenge := writer.Header().Get("WWW-Authenticate")
			assert.NotContains(t, challenge, "private-token-content")
			assert.Empty(t, writer.Body.String())
			if allowed {
				assert.Empty(t, challenge)
				return
			}
			assert.Contains(t, challenge, `resource_metadata="https://resource.example/.well-known/oauth-protected-resource/mcp"`)
			if tc.code == "" {
				assert.NotContains(t, challenge, "error=")
			} else {
				assert.Contains(t, challenge, `error="`+tc.code+`"`)
			}
			if tc.scope != "" {
				assert.Contains(t, challenge, `scope="`+tc.scope+`"`)
			}
			assert.Equal(t, "no-store", writer.Header().Get("Cache-Control"))
		})
	}
}

func TestResourceServerGeneratedMetadata(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	for _, resource := range []string{
		"https://resource.example", "https://resource.example/mcp",
		"https://resource.example/mcp/a%2Fb?tenant=blue", "https://resource.example/mcp?",
	} {
		t.Run(resource, func(t *testing.T) {
			server, err := NewJWTResourceServer(JWTResource{
				Issuer: "https://issuer.example", Resource: resource,
				Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public}}}, Algorithms: []jose.SignatureAlgorithm{jose.EdDSA},
			})
			require.NoError(t, err)
			mux := goahttp.NewMuxer()
			scopes := []string{"catalog:read"}
			server.MountMetadata(mux, scopes)
			scopes[0] = "modified"
			writer := httptest.NewRecorder()
			mux.ServeHTTP(writer, httptest.NewRequestWithContext(t.Context(), http.MethodGet, server.metadata.String(), nil))
			require.Equal(t, http.StatusOK, writer.Code)
			response := writer.Result()
			// This factory returns a function, not a response. The generated
			// decoder consumes and closes response.Body, including on failure.
			decode := genmetadataclient.DecodeReadResponse(generatedJSONDecoder, false) //nolint:bodyclose // The analyzer mistakes the returned function type for an HTTP response.
			value, err := decode(response)
			require.NoError(t, err)
			metadata, ok := value.(*genmetadata.ReadResult)
			require.True(t, ok)
			assert.Equal(t, resource, metadata.Resource)
			assert.Equal(t, []string{"https://issuer.example"}, metadata.AuthorizationServers)
			assert.Equal(t, []string{"catalog:read"}, metadata.ScopesSupported)
			assert.NotContains(t, writer.Body.String(), "modified")
			wrongQuery := *server.metadata
			wrongQuery.RawQuery = "tenant=other"
			wrongQuery.ForceQuery = false
			denied := httptest.NewRecorder()
			mux.ServeHTTP(denied, httptest.NewRequestWithContext(t.Context(), http.MethodGet, wrongQuery.String(), nil))
			assert.Equal(t, http.StatusNotFound, denied.Code)
			assert.NotContains(t, denied.Body.String(), resource)
		})
	}
}

func TestResourceServerRejectsIDToken(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	server, err := NewJWTResourceServer(JWTResource{
		Issuer: "https://issuer.example", Resource: "https://resource.example/mcp",
		Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public}}}, Algorithms: []jose.SignatureAlgorithm{jose.EdDSA},
	})
	require.NoError(t, err)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://resource.example/mcp", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+signResourceJWT(t, private, jose.EdDSA, "", "JWT", resourceClaimsDocument))
	writer := httptest.NewRecorder()
	assert.Nil(t, server.AuthorizeHTTP(writer, request, nil))
	assert.Equal(t, http.StatusUnauthorized, writer.Code)
	assert.Contains(t, writer.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
}

func TestResourceServerNativeAuthKeepsVerifiedIdentity(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	config := JWTResource{
		Issuer: "https://issuer.example", Resource: "https://resource.example/mcp",
		Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public}}}, Algorithms: []jose.SignatureAlgorithm{jose.EdDSA},
	}
	server, err := NewJWTResourceServer(config)
	require.NoError(t, err)
	token := signResourceJWT(t, private, jose.EdDSA, "", "at+jwt", resourceClaimsDocument)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, config.Resource, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	authorized := server.AuthorizeHTTP(httptest.NewRecorder(), request, [][]string{{"records:read"}})
	require.NotNil(t, authorized)
	for _, entry := range []struct {
		name         string
		authenticate func(context.Context, string) (context.Context, error)
	}{
		{"OAuth", func(ctx context.Context, token string) (context.Context, error) {
			return server.OAuth2Auth(ctx, token, &security.OAuth2Scheme{RequiredScopes: []string{"records:read"}})
		}},
		{"JWT", func(ctx context.Context, token string) (context.Context, error) {
			return server.JWTAuth(ctx, token, &security.JWTScheme{RequiredScopes: []string{"records:read"}})
		}},
		{"Bearer", func(ctx context.Context, token string) (context.Context, error) {
			return server.BearerAuth(ctx, token, &security.BearerScheme{RequiredScopes: []string{"records:read"}})
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			// An ordinary Goa call and an already-verified HTTP request return
			// the same identity without trusting caller-supplied identity fields.
			for _, ctx := range []context.Context{t.Context(), authorized.Context()} {
				authenticated, err := entry.authenticate(ctx, token)
				require.NoError(t, err)
				principal, ok := ResourcePrincipalFromContext(authenticated)
				assert.True(t, ok)
				assert.Equal(t, ResourcePrincipal{Issuer: config.Issuer, Subject: "subject", ClientID: "registered-client"}, principal)
				_, err = entry.authenticate(authenticated, "another-token")
				assert.ErrorIs(t, err, errInvalidAccessToken)
			}
		})
	}
	_, err = server.OAuth2Auth(t.Context(), token, &security.OAuth2Scheme{RequiredScopes: []string{"administration"}})
	require.ErrorContains(t, err, "missing scopes")
	_, present := ResourcePrincipalFromContext(t.Context())
	assert.False(t, present)
	config.Resource = "https://other.example/mcp"
	other, err := NewJWTResourceServer(config)
	require.NoError(t, err)
	_, err = other.OAuth2Auth(authorized.Context(), token, &security.OAuth2Scheme{})
	assert.ErrorIs(t, err, errInvalidAccessToken)
}
