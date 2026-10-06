// These HTTPS peers validate the complete browser and refresh exchanges through
// native generated clients. The host owns sign-in; runtime state, issuer and
// redirect checks prevent a callback from exchanging another operation's code.
package mcp

import (
	"context"
	"encoding/json"
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
	"golang.org/x/oauth2"

	"goa.design/goa/v3/jsonrpc"
)

type (
	// browserOAuthPeer records browser requests and token forms for one issuer.
	browserOAuthPeer struct {
		server          *httptest.Server
		resource        string
		issuer          string
		metadata        string
		clientID        string
		clientMetadata  string
		clientMedia     string
		missingMetadata bool
		issuerBody      string
		tokenBody       string
		tokenStatus     int
		tokenReply      func(int32) string
		mcpHandle       func(http.ResponseWriter, *http.Request) bool
		hostCalls       atomic.Int32
		tokenCalls      atomic.Int32
		mcpCalls        atomic.Int32
		mutex           sync.Mutex
		forms           []url.Values
		requests        []url.Values
		proofs          map[string]string
	}
)

func TestAuthorizationCodeTransportAndDiscoveryCaller(t *testing.T) {
	for _, discovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("discovered=%t", discovered), func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			transport := peer.transport(t, peer.authorize(t))
			if discovered {
				caller, err := NewHTTPCaller(HTTPOptions{Endpoint: peer.resource, Client: transport, ClientInfo: ClientInfo{Name: "host", Version: "1"}})
				require.NoError(t, err)
				response, err := caller.CallTool(t.Context(), CallRequest{Tool: "read", Payload: []byte(`{}`)})
				require.NoError(t, err)
				assert.JSONEq(t, `{"value":"ok"}`, string(response.StructuredContent))
			} else {
				wrapped := NewHTTPTransport(transport, ClientInfo{Name: "generated", Version: "1"}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
				require.NoError(t, callOAuthPeer(t.Context(), wrapped, peer.resource))
				require.NoError(t, callOAuthPeer(t.Context(), wrapped, peer.resource))
			}
			assert.EqualValues(t, 1, peer.hostCalls.Load())
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
			assert.EqualValues(t, 2, peer.mcpCalls.Load())
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			require.Len(t, peer.requests, 1)
			assert.Equal(t, "records:read", peer.requests[0].Get("scope"))
			assert.Equal(t, "selected", peer.requests[0].Get("routing"))
			assert.Equal(t, "authorization_code", peer.forms[0].Get("grant_type"))
			assert.Equal(t, peer.resource, peer.forms[0].Get("resource"))
			assert.Equal(t, "https://host.example/callback/a%2Fb?route=selected", peer.forms[0].Get("redirect_uri"))
		})
	}
}

func TestAuthorizationCodeRefreshRotation(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh-one","scope":"records:read"}`
	transport := peer.transport(t, peer.authorize(t))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	transport.authorization.obtained = time.Now().Add(-2 * time.Hour)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh-two"}`
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	transport.authorization.obtained = time.Now().Add(-2 * time.Hour)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600}`
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.EqualValues(t, 1, peer.hostCalls.Load())
	assert.EqualValues(t, 3, peer.tokenCalls.Load())
	assert.Equal(t, "private-refresh-two", *transport.authorization.token.RefreshToken)
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	require.Len(t, peer.forms, 3)
	assert.Equal(t, "refresh_token", peer.forms[1].Get("grant_type"))
	assert.Equal(t, "private-refresh-one", peer.forms[1].Get("refresh_token"))
	assert.Equal(t, "private-refresh-two", peer.forms[2].Get("refresh_token"))
	for _, form := range peer.forms[1:] {
		assert.Equal(t, peer.resource, form.Get("resource"))
		assert.Empty(t, form.Get("code"))
		assert.Empty(t, form.Get("code_verifier"))
		assert.Empty(t, form.Get("scope"))
	}
}

func TestAuthorizationCodeRejectsBeforeConsent(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"missing PKCE", `"code_challenge_methods_supported":["S256"]`, `"code_challenge_methods_supported":["plain"]`},
		{"wrong grant", `"authorization_code"`, `"client_credentials"`},
		{"wrong method", `"none"`, `"client_secret_basic"`},
		{"insecure authorization URL", `https://`, `http://`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			peer.issuerBody = strings.ReplaceAll(peer.issuerBody, tc.old, tc.replacement)
			transport := peer.transport(t, peer.authorize(t))
			require.Error(t, callOAuthPeer(t.Context(), transport, peer.resource))
			assert.Zero(t, peer.hostCalls.Load())
			assert.Zero(t, peer.tokenCalls.Load())
			assert.Zero(t, peer.mcpCalls.Load())
		})
	}
}

func TestAuthorizationCodeResponseIssuerAndShape(t *testing.T) {
	cases := []struct {
		name   string
		change func(url.Values)
		valid  bool
	}{
		{"valid", func(url.Values) {}, true},
		{"missing required issuer", func(q url.Values) { q.Del("iss") }, false},
		{"wrong issuer", func(q url.Values) { q.Set("iss", "https://private-canary.example/issuer") }, false},
		{"issuer normalized", func(q url.Values) { q.Set("iss", q.Get("iss")+"/") }, false},
		{"wrong state", func(q url.Values) { q.Set("state", "private-canary") }, false},
		{"missing state", func(q url.Values) { q.Del("state") }, false},
		{"duplicate state", func(q url.Values) { q.Add("state", q.Get("state")) }, false},
		{"duplicate issuer", func(q url.Values) { q.Add("iss", q.Get("iss")) }, false},
		{"duplicate code", func(q url.Values) { q.Add("code", q.Get("code")) }, false},
		{"empty code", func(q url.Values) { q.Set("code", "") }, false},
		{"missing code", func(q url.Values) { q.Del("code") }, false},
		{"both code and error", func(q url.Values) { q.Set("error", "access_denied") }, false},
		{"declined", func(q url.Values) { q.Del("code"); q.Set("error", "access_denied") }, false},
		{"wrong issuer with error", func(q url.Values) {
			q.Del("code")
			q.Set("iss", "https://private-canary.example")
			q.Set("error", "private-canary")
		}, false},
		{"static redirect query changed", func(q url.Values) { q.Set("route", "other") }, false},
		{"unknown extension", func(q url.Values) { q.Set("extension", "opaque") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			host := peer.authorize(t)
			transport := peer.transport(t, func(ctx context.Context, address string) (string, error) {
				callback, err := host(ctx, address)
				if err != nil {
					return "", err
				}
				parsed, err := url.Parse(callback)
				if err != nil {
					return "", err
				}
				query := parsed.Query()
				tc.change(query)
				parsed.RawQuery = query.Encode()
				return parsed.String(), nil
			})
			err := callOAuthPeer(t.Context(), transport, peer.resource)
			if tc.valid {
				require.NoError(t, err)
				assert.EqualValues(t, 1, peer.tokenCalls.Load())
				assert.EqualValues(t, 1, peer.mcpCalls.Load())
			} else {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "private-canary")
				assert.Zero(t, peer.tokenCalls.Load())
				assert.Zero(t, peer.mcpCalls.Load())
				if tc.name == "wrong issuer with error" {
					assert.Contains(t, err.Error(), "issuer")
					assert.NotContains(t, err.Error(), "declined")
				}
			}
		})
	}
}

func TestAuthorizationCodeUnadvertisedIssuer(t *testing.T) {
	for _, advertisement := range []string{`"authorization_response_iss_parameter_supported":false`, ""} {
		property := advertisement
		if property != "" {
			property = "," + property
		}
		for _, responseIssuer := range []string{"absent", "matching", "wrong"} {
			t.Run(advertisement+"/"+responseIssuer, func(t *testing.T) {
				peer := newBrowserOAuthPeer(t)
				peer.issuerBody = strings.Replace(peer.issuerBody, `,"authorization_response_iss_parameter_supported":true`, property, 1)
				host := peer.authorize(t)
				transport := peer.transport(t, func(ctx context.Context, address string) (string, error) {
					callback, err := host(ctx, address)
					if err != nil {
						return "", err
					}
					parsed, err := url.Parse(callback)
					if err != nil {
						return "", err
					}
					query := parsed.Query()
					switch responseIssuer {
					case "absent":
						query.Del("iss")
					case "wrong":
						query.Set("iss", "https://other.example/issuer")
					}
					parsed.RawQuery = query.Encode()
					return parsed.String(), nil
				})
				err := callOAuthPeer(t.Context(), transport, peer.resource)
				if responseIssuer == "wrong" {
					require.Error(t, err)
					assert.Zero(t, peer.tokenCalls.Load())
					assert.Zero(t, peer.mcpCalls.Load())
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestAuthorizationCodeCancellationAndReplay(t *testing.T) {
	t.Run("host cancellation", func(t *testing.T) {
		peer := newBrowserOAuthPeer(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transport := peer.transport(t, func(ctx context.Context, _ string) (string, error) {
			cancel()
			return "", ctx.Err()
		})
		require.ErrorIs(t, callOAuthPeer(ctx, transport, peer.resource), context.Canceled)
		assert.Zero(t, peer.tokenCalls.Load())
		assert.Zero(t, peer.mcpCalls.Load())
	})
	t.Run("replayed callback", func(t *testing.T) {
		peer := newBrowserOAuthPeer(t)
		host := peer.authorize(t)
		var first string
		transport := peer.transport(t, func(ctx context.Context, address string) (string, error) {
			if first != "" {
				return first, nil
			}
			var err error
			first, err = host(ctx, address)
			return first, err
		})
		require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
		transport.authorization.obtained = time.Now().Add(-2 * time.Hour)
		require.Error(t, callOAuthPeer(t.Context(), transport, peer.resource))
		assert.EqualValues(t, 1, peer.tokenCalls.Load())
		assert.EqualValues(t, 1, peer.mcpCalls.Load())
	})
}

// newBrowserOAuthPeer serves independently checked metadata, form and MCP
// responses. It verifies that PKCE protects the code and that no private value
// enters the MCP request body or the issuer's request URL.
func TestAuthorizationCodeChallengeRecovery(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprintf("scope upgrade=%t", upgrade), func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			peer.tokenReply = func(count int32) string {
				if count == 1 {
					return `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh","scope":"records:read"}`
				}
				scope := "records:read"
				if upgrade {
					scope += " records:write"
				}
				return fmt.Sprintf(`{"access_token":"changed-token","token_type":"Bearer","expires_in":3600,"scope":%q}`, scope)
			}
			var accepted atomic.Int32
			peer.mcpHandle = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Header.Get("Authorization") == "Bearer changed-token" {
					accepted.Add(1)
					return false
				}
				assert.Equal(t, "Bearer opaque-token", r.Header.Get("Authorization"))
				if upgrade {
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm="other", Bearer error="insufficient_scope", scope="records:write", resource_metadata=%q`, peer.server.URL+"/challenged/resource"))
					w.WriteHeader(http.StatusForbidden)
				} else {
					w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
					w.WriteHeader(http.StatusUnauthorized)
				}
				return true
			}
			transport := peer.transport(t, peer.authorize(t))
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			assert.EqualValues(t, 1, accepted.Load())
			assert.EqualValues(t, 2, peer.mcpCalls.Load())
			assert.EqualValues(t, 2, peer.tokenCalls.Load())
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			if upgrade {
				assert.EqualValues(t, 2, peer.hostCalls.Load())
				assert.Equal(t, "records:read records:write", peer.requests[1].Get("scope"))
				assert.Equal(t, "authorization_code", peer.forms[1].Get("grant_type"))
			} else {
				assert.EqualValues(t, 1, peer.hostCalls.Load())
				assert.Equal(t, "refresh_token", peer.forms[1].Get("grant_type"))
			}
		})
	}
}

func TestAuthorizationCodeRecoveryStops(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		challenge string
		changed   bool
		tokens    int32
		requests  int32
	}{
		{"ordinary forbidden", http.StatusForbidden, `Bearer`, false, 1, 1},
		{"already granted", http.StatusForbidden, `Bearer error="insufficient_scope", scope="records:read"`, false, 1, 1},
		{"missing scopes", http.StatusForbidden, `Bearer error="insufficient_scope"`, false, 1, 1},
		{"other scheme", http.StatusUnauthorized, `Basic realm="other"`, false, 1, 1},
		{"unknown error", http.StatusUnauthorized, `Bearer error="private-canary"`, false, 1, 1},
		{"invalid challenge", http.StatusUnauthorized, `Bearer scope="private-canary`, false, 1, 1},
		{"renewed opaque token rejected", http.StatusUnauthorized, `Bearer error="invalid_token"`, false, 2, 2},
		{"second rejection", http.StatusUnauthorized, `Bearer error="invalid_token"`, true, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			peer.tokenReply = func(count int32) string {
				value := "opaque-token"
				if tc.changed && count > 1 {
					value = "changed-token"
				}
				return fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, value)
			}
			peer.mcpHandle = func(w http.ResponseWriter, _ *http.Request) bool {
				w.Header().Set("WWW-Authenticate", tc.challenge)
				w.WriteHeader(tc.status)
				return true
			}
			transport := peer.transport(t, peer.authorize(t))
			err := callOAuthPeer(t.Context(), transport, peer.resource)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-canary")
			var uncertain *OutcomeUnknownError
			assert.NotErrorAs(t, err, &uncertain)
			assert.Equal(t, tc.tokens, peer.tokenCalls.Load())
			assert.Equal(t, tc.requests, peer.mcpCalls.Load())
		})
	}
}

func TestAuthorizationCodeConcurrentChallengeUsesOneRefresh(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh"}`
	transport := peer.transport(t, peer.authorize(t))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	peer.tokenReply = func(_ int32) string {
		return `{"access_token":"changed-token","token_type":"Bearer","expires_in":3600}`
	}
	peer.mcpHandle = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") == "Bearer changed-token" {
			return false
		}
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	var calls sync.WaitGroup
	for range 5 {
		calls.Go(func() { assert.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource)) })
	}
	calls.Wait()
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
	assert.EqualValues(t, 1, peer.hostCalls.Load())
}

func TestAuthorizationCodeClientMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, document string
		valid          bool
	}{
		{"public document", `{"client_id":"CLIENT","client_name":"Example","redirect_uris":["REDIRECT"],"token_endpoint_auth_method":"none"}`, true},
		{"unknown extension", `{"client_id":"CLIENT","client_name":"Example","redirect_uris":["REDIRECT"],"token_endpoint_auth_method":"none","vendor_note":null}`, true},
		{"wrong identifier", `{"client_id":"https://wrong.example/client","client_name":"Example","redirect_uris":["REDIRECT"],"token_endpoint_auth_method":"none"}`, false},
		{"wrong redirect", `{"client_id":"CLIENT","client_name":"Example","redirect_uris":["https://wrong.example/callback"],"token_endpoint_auth_method":"none"}`, false},
		{"missing name", `{"client_id":"CLIENT","redirect_uris":["REDIRECT"],"token_endpoint_auth_method":"none"}`, false},
		{"private registration", `{"client_id":"CLIENT","client_name":"Example","redirect_uris":["REDIRECT"],"token_endpoint_auth_method":"private_key_jwt"}`, false},
		{"secret forbidden", `{"client_id":"CLIENT","client_name":"Example","redirect_uris":["REDIRECT"],"token_endpoint_auth_method":"none","client_secret":"private-canary"}`, false},
		{"secret expiration forbidden", `{"client_id":"CLIENT","client_name":"Example","redirect_uris":["REDIRECT"],"token_endpoint_auth_method":"none","client_secret_expires_at":0}`, false},
		{"wrong grant", `{"client_id":"CLIENT","client_name":"Example","redirect_uris":["REDIRECT"],"token_endpoint_auth_method":"none","grant_types":["client_credentials"]}`, false},
		{"wrong response", `{"client_id":"CLIENT","client_name":"Example","redirect_uris":["REDIRECT"],"token_endpoint_auth_method":"none","response_types":["token"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			peer.clientID = peer.server.URL + "/client/document.json"
			peer.issuerBody = strings.TrimSuffix(peer.issuerBody, "}") + `,"client_id_metadata_document_supported":true}`
			redirect := "https://host.example/callback/a%2Fb?route=selected"
			peer.clientMetadata = strings.NewReplacer("CLIENT", peer.clientID, "REDIRECT", redirect).Replace(tc.document)
			transport, err := NewClientMetadataHTTPTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, AuthorizationCode{
				Issuer: peer.issuer, ClientID: peer.clientID, RedirectURI: redirect, Authorize: peer.authorize(t),
			})
			require.NoError(t, err)
			err = callOAuthPeer(t.Context(), transport, peer.resource)
			if tc.valid {
				require.NoError(t, err)
				assert.EqualValues(t, 1, peer.hostCalls.Load())
				assert.EqualValues(t, 1, peer.tokenCalls.Load())
				assert.EqualValues(t, 1, peer.mcpCalls.Load())
			} else {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "private-canary")
				assert.Zero(t, peer.hostCalls.Load())
				assert.Zero(t, peer.tokenCalls.Load())
				assert.Zero(t, peer.mcpCalls.Load())
			}
		})
	}
}

func TestAuthorizationCodeClientMetadataIdentifier(t *testing.T) {
	for _, identifier := range []string{
		"http://client.example/document.json", "https://client.example",
		"https://client.example/./client", "https://client.example/a/../client",
		"https://client.example/%2e/client", "https://client.example/a/%2e%2E/client",
		"https://client.example/client#fragment", "https://user:secret@client.example/client",
	} {
		t.Run(identifier, func(t *testing.T) {
			_, err := NewClientMetadataHTTPTransport(HTTPOptions{Endpoint: "https://resource.example/mcp"}, AuthorizationCode{
				Issuer: "https://issuer.example", ClientID: identifier,
				RedirectURI: "https://host.example/callback", Authorize: func(context.Context, string) (string, error) { return "", nil },
			})
			assert.Error(t, err)
		})
	}
}

func TestAuthorizationCodeResourceOwnsScopeHierarchy(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"scope":"records:all"}`
	transport := peer.transport(t, peer.authorize(t))
	for range 2 {
		require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	}
	assert.EqualValues(t, 1, peer.hostCalls.Load())
	assert.EqualValues(t, 1, peer.tokenCalls.Load())
	assert.EqualValues(t, 2, peer.mcpCalls.Load())
}

func TestAuthorizationCodeRefreshCanReuseOpaqueValue(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh"}`
	peer.mcpHandle = func(w http.ResponseWriter, r *http.Request) bool {
		assert.Equal(t, "Bearer opaque-token", r.Header.Get("Authorization"))
		if peer.tokenCalls.Load() > 1 {
			return false
		}
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	transport := peer.transport(t, peer.authorize(t))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.EqualValues(t, 1, peer.hostCalls.Load())
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
	assert.EqualValues(t, 2, peer.mcpCalls.Load())
}

func TestAuthorizationCodeMetadataDefaultsAndHostIsolation(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.issuerBody = strings.Replace(peer.issuerBody, `"grant_types_supported":["authorization_code","refresh_token"],`, "", 1)
	first := peer.transport(t, peer.authorize(t))
	second := peer.transport(t, peer.authorize(t))
	require.NoError(t, callOAuthPeer(t.Context(), first, peer.resource))
	require.NoError(t, callOAuthPeer(t.Context(), second, peer.resource))
	assert.EqualValues(t, 2, peer.hostCalls.Load())
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
	assert.NotSame(t, first.authorization.token, second.authorization.token)
}

func TestAuthorizationCodeMetadataRegistrationRequiresIssuerSupport(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	transport, err := NewClientMetadataHTTPTransport(HTTPOptions{
		Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"},
	}, AuthorizationCode{Issuer: peer.issuer, ClientID: peer.server.URL + "/client/document.json",
		RedirectURI: "https://host.example/callback", Authorize: peer.authorize(t)})
	require.NoError(t, err)
	err = callOAuthPeer(t.Context(), transport, peer.resource)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support client metadata")
	assert.Zero(t, peer.hostCalls.Load())
	assert.Zero(t, peer.tokenCalls.Load())
	assert.Zero(t, peer.mcpCalls.Load())
}

func TestAuthorizationCodeClientMetadataValidIdentifiers(t *testing.T) {
	for _, identifier := range []string{"https://client.example/", "https://client.example/client.json", "https://client.example/client.json?edition=one"} {
		t.Run(identifier, func(t *testing.T) {
			_, err := NewClientMetadataHTTPTransport(HTTPOptions{
				Endpoint: "https://resource.example/mcp", ClientInfo: ClientInfo{Name: "host", Version: "1"},
			}, AuthorizationCode{Issuer: "https://issuer.example", ClientID: identifier,
				RedirectURI: "https://host.example/callback", Authorize: func(context.Context, string) (string, error) { return "", nil }})
			assert.NoError(t, err)
		})
	}
}

// The first unauthenticated discovery request selects permissions before browser
// consent. Its challenge can require scopes absent from the basic metadata list.
func TestAuthorizationCodeInitialChallengeSelectsScopes(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.missingMetadata = true
	peer.mcpHandle = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, scope="operations:start"`, peer.server.URL+"/challenged/resource"))
			w.WriteHeader(http.StatusUnauthorized)
			return true
		}
		return false
	}
	transport := peer.transport(t, peer.authorize(t))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.EqualValues(t, 1, peer.hostCalls.Load())
	assert.EqualValues(t, 1, peer.tokenCalls.Load())
	assert.EqualValues(t, 2, peer.mcpCalls.Load())
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	require.Len(t, peer.requests, 1)
	require.Len(t, peer.forms, 1)
	assert.Equal(t, "operations:start", peer.requests[0].Get("scope"))
	assert.Empty(t, peer.forms[0].Get("scope"))
}

func TestAuthorizationCodeClientMetadataRefresh(t *testing.T) {
	for _, registeredRefresh := range []bool{false, true} {
		t.Run(fmt.Sprint(registeredRefresh), func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			peer.clientID = peer.server.URL + "/client/document.json"
			peer.clientMedia = "application/client+json"
			grants := `["authorization_code"]`
			if registeredRefresh {
				grants = `["authorization_code","refresh_token"]`
			}
			peer.clientMetadata = fmt.Sprintf(`{"client_id":%q,"client_name":"Example","redirect_uris":["https://host.example/callback/a%%2Fb?route=selected"],"token_endpoint_auth_method":"none","grant_types":%s}`, peer.clientID, grants)
			peer.issuerBody = strings.TrimSuffix(peer.issuerBody, "}") + `,"client_id_metadata_document_supported":true}`
			peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh"}`
			transport, err := NewClientMetadataHTTPTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, AuthorizationCode{Issuer: peer.issuer, ClientID: peer.clientID, RedirectURI: "https://host.example/callback/a%2Fb?route=selected", Authorize: peer.authorize(t)})
			require.NoError(t, err)
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			transport.authorization.obtained = time.Now().Add(-2 * time.Hour)
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			require.Len(t, peer.forms, 2)
			if registeredRefresh {
				assert.EqualValues(t, 1, peer.hostCalls.Load())
				assert.Equal(t, "refresh_token", peer.forms[1].Get("grant_type"))
			} else {
				assert.EqualValues(t, 2, peer.hostCalls.Load())
				assert.Equal(t, "authorization_code", peer.forms[1].Get("grant_type"))
			}
		})
	}
}

// One rejected attempt, its renewed grant, and a lost stream all belong to one
// operation. A later rejection cannot trigger another grant or erase the loss.
func TestAuthorizationCodeRecoveryRemainsBoundedAcrossStreams(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh"}`
	peer.mcpHandle = func(w http.ResponseWriter, _ *http.Request) bool {
		if peer.mcpCalls.Load() == 2 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, err := io.WriteString(w, ": accepted\n\n")
			assert.NoError(t, err)
		} else {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
		}
		return true
	}
	transport := NewHTTPTransport(peer.transport(t, peer.authorize(t)), ClientInfo{Name: "host", Version: "1"}, HTTPBindings{Tools: map[string]ToolBinding{"read": {ReadOnly: true}}}, InputSupport{}, HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true})
	err := callOAuthPeer(t.Context(), transport, peer.resource)
	var unknown *OutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	var response *HTTPResponseError
	require.ErrorAs(t, err, &response)
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assert.EqualValues(t, 3, peer.mcpCalls.Load())
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
	assert.EqualValues(t, 1, peer.hostCalls.Load())
}

// Two successive request rounds can each replace a rejected credential once.
// The bound belongs to one HTTP round, rather than the host's entire session.
func TestAuthorizationCodeRecoveryIsPerRequestRound(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh"}`
	transport := peer.transport(t, peer.authorize(t))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	var requests atomic.Int32
	peer.mcpHandle = func(w http.ResponseWriter, _ *http.Request) bool {
		if requests.Add(1)%2 == 0 {
			return false
		}
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	for round := range 2 {
		require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
		assert.EqualValues(t, 2*(round+1), requests.Load())
		assert.EqualValues(t, round+2, peer.tokenCalls.Load())
	}
	assert.EqualValues(t, 1, peer.hostCalls.Load())
}

func TestAuthorizationCodeMetadataScopesUseExistingGrant(t *testing.T) {
	for _, tc := range []struct {
		scope     string
		exchanges int32
	}{
		{"records:write", 1},
		{"records:admin", 2},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"scope":"records:read records:write"}`
			transport := peer.transport(t, peer.authorize(t))
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			peer.metadata = strings.Replace(peer.metadata, `["records:read"]`, fmt.Sprintf(`["records:read",%q]`, tc.scope), 1)
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			assert.Equal(t, tc.exchanges, peer.hostCalls.Load())
			assert.Equal(t, tc.exchanges, peer.tokenCalls.Load())
			assert.EqualValues(t, 2, peer.mcpCalls.Load())
			if tc.exchanges == 2 {
				peer.mutex.Lock()
				defer peer.mutex.Unlock()
				require.Len(t, peer.requests, 2)
				assert.Equal(t, "records:read records:write records:admin", peer.requests[1].Get("scope"))
			}
		})
	}
}

func newBrowserOAuthPeer(t *testing.T) *browserOAuthPeer {
	t.Helper()
	peer := &browserOAuthPeer{
		tokenBody: `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600}`,
		proofs:    make(map[string]string),
	}
	peer.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body string
		switch r.URL.EscapedPath() {
		case "/.well-known/oauth-protected-resource/mcp/a%2Fb", "/challenged/resource":
			if peer.missingMetadata && r.URL.Path != "/challenged/resource" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			assert.Empty(t, r.Header.Get("Authorization"))
			body = peer.metadata
		case "/.well-known/oauth-authorization-server/issuer":
			assert.Empty(t, r.Header.Get("Authorization"))
			body = peer.issuerBody
		case "/client/document.json":
			if peer.clientMedia != "" {
				w.Header().Set("Content-Type", peer.clientMedia)
			}
			assert.Empty(t, r.Header.Get("Authorization"))
			body = peer.clientMetadata
		case "/token/a%2Fb":
			count := peer.tokenCalls.Add(1)
			assert.Empty(t, r.Header.Get("Authorization"))
			assert.Equal(t, "routing=selected", r.URL.RawQuery)
			assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
			assert.NoError(t, r.ParseForm())
			assert.Equal(t, peer.clientID, r.PostForm.Get("client_id"))
			assert.Equal(t, peer.resource, r.PostForm.Get("resource"))
			peer.mutex.Lock()
			peer.forms = append(peer.forms, r.PostForm)
			if r.PostForm.Get("grant_type") == "authorization_code" {
				assert.Equal(t, peer.proofs[r.PostForm.Get("code")], oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")))
				assert.Len(t, r.PostForm.Get("code_verifier"), 43)
			}
			peer.mutex.Unlock()
			if peer.tokenStatus != 0 {
				w.WriteHeader(peer.tokenStatus)
			}
			body = peer.tokenBody
			if peer.tokenReply != nil {
				body = peer.tokenReply(count)
			}
		case "/mcp/a%2Fb":
			peer.mcpCalls.Add(1)
			if peer.mcpHandle != nil {
				if peer.mcpHandle(w, r) {
					return
				}
			} else {
				assert.Equal(t, "Bearer opaque-token", r.Header.Get("Authorization"))
			}
			encoded, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.NotContains(t, string(encoded), "opaque-token")
			assert.NotContains(t, string(encoded), "private-code")
			assert.NotContains(t, string(encoded), "private-refresh")
			var rpc jsonrpc.RawRequest
			assert.NoError(t, rpc.UnmarshalJSON(encoded))
			assert.Nil(t, ValidateHTTPRequest(r, encoded, nil))
			result := `{"resultType":"complete","content":[],"structuredContent":{"value":"ok"}}`
			if rpc.Method == "tools/list" {
				result = `{"resultType":"complete","tools":[{"name":"read","inputSchema":{"type":"object","additionalProperties":false},"annotations":{}}],"ttlMs":0,"cacheScope":"private"}`
			}
			response := &jsonrpc.Response{JSONRPC: rpcVersion, ID: rpc.ID, Result: json.RawMessage(result)}
			encoded, err = response.MarshalJSON()
			assert.NoError(t, err)
			body = string(encoded)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		_, err := io.WriteString(w, body)
		assert.NoError(t, err)
	}))
	t.Cleanup(peer.server.Close)
	peer.clientID = "registered-public"
	peer.resource = peer.server.URL + "/mcp/a%2Fb?tenant=selected"
	peer.issuer = peer.server.URL + "/issuer"
	peer.metadata = fmt.Sprintf(`{"resource":%q,"authorization_servers":[%q],"scopes_supported":["records:read"]}`, peer.resource, peer.issuer)
	peer.issuerBody = fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q,"authorization_endpoint":%q,"grant_types_supported":["authorization_code","refresh_token"],"token_endpoint_auth_methods_supported":["none"],"code_challenge_methods_supported":["S256"],"authorization_response_iss_parameter_supported":true}`, peer.issuer, peer.server.URL+"/token/a%2Fb?routing=selected", peer.server.URL+"/authorize?routing=selected")
	return peer
}

// transport constructs a public client with a host callback scoped to this peer.
func (p *browserOAuthPeer) transport(t *testing.T, authorize func(context.Context, string) (string, error)) *HTTPTransport {
	t.Helper()
	transport, err := NewAuthorizationCodeHTTPTransport(HTTPOptions{
		Endpoint: p.resource, Client: p.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"},
	}, AuthorizationCode{
		Issuer: p.issuer, ClientID: "registered-public", RedirectURI: "https://host.example/callback/a%2Fb?route=selected",
		Authorize: authorize,
	})
	require.NoError(t, err)
	return transport
}

// authorize models a trusted host's consent and returns an issuer redirect. It
// retains only test PKCE challenges so the token endpoint can verify each code.
func (p *browserOAuthPeer) authorize(t *testing.T) func(context.Context, string) (string, error) {
	t.Helper()
	return func(_ context.Context, address string) (string, error) {
		p.hostCalls.Add(1)
		parsed, err := url.Parse(address)
		if err != nil {
			return "", err
		}
		query := parsed.Query()
		assert.Equal(t, p.resource, query.Get("resource"))
		assert.Equal(t, "code", query.Get("response_type"))
		assert.Equal(t, "S256", query.Get("code_challenge_method"))
		assert.Empty(t, query.Get("code_verifier"))
		assert.Empty(t, query.Get("client_secret"))
		code := "private-code-" + query.Get("state")
		p.mutex.Lock()
		p.requests = append(p.requests, query)
		p.proofs[code] = query.Get("code_challenge")
		p.mutex.Unlock()
		callback, err := url.Parse(query.Get("redirect_uri"))
		if err != nil {
			return "", err
		}
		response := callback.Query()
		response.Set("code", code)
		response.Set("state", query.Get("state"))
		response.Set("iss", p.issuer)
		callback.RawQuery = response.Encode()
		return callback.String(), nil
	}
}

// TestAuthorizationCodeChallengeRecovery proves that rejected HTTP operations
// may obtain new credentials without granting permission to repeat a tool whose
// result was lost. Accepted requests reach the simulated tool only once.
