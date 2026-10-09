// These HTTPS peers use the existing browser fixture to verify signed client
// authentication without duplicating consent, PKCE, refresh or MCP behavior.
// Invalid registrations stop before the host or token endpoint receives a call.
package mcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// signedBrowserConfig keeps the test's authentication expectations alongside
	// its callback so the peer can independently verify the emitted signed claims.
	signedBrowserConfig struct {
		ClientAssertion
		RedirectURI string
		Authorize   func(context.Context, string) (string, error)
	}
)

func TestSignedAuthorizationCodeAndRefresh(t *testing.T) {
	for _, profile := range []string{"registered", "metadata URL", "metadata inline"} {
		for _, discovered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/discovered=%t", profile, discovered), func(t *testing.T) {
				peer, registration, publicKey := signedBrowserOAuthPeer(t)
				if profile != "registered" {
					registration.ClientID = peer.server.URL + "/client/document.json"
					peer.clientID = registration.ClientID
					peer.clientMetadata = signedBrowserMetadata(t, registration, publicKey, profile == "metadata inline")
				}
				var identifiers []string
				peer.verifyClient = func(request *http.Request) error {
					form := request.PostForm
					assert.Empty(t, request.Header.Get("Authorization"))
					assert.NotContains(t, form, "client_id")
					assert.NotContains(t, form, "client_secret")
					assert.Equal(t, "urn:ietf:params:oauth:client-assertion-type:jwt-bearer", form.Get("client_assertion_type"))
					token, err := jwt.ParseSigned(form.Get("client_assertion"), []jose.SignatureAlgorithm{jose.EdDSA})
					if err != nil {
						return err
					}
					var claims jwt.Claims
					if err := token.Claims(publicKey, &claims); err != nil {
						return err
					}
					assert.Equal(t, registration.ClientID, claims.Subject)
					assert.Equal(t, registration.AssertionIssuer, claims.Issuer)
					assert.Equal(t, jwt.Audience{registration.Audience}, claims.Audience)
					assert.Equal(t, registration.Lifetime, claims.Expiry.Time().Sub(claims.IssuedAt.Time()))
					assert.NotEmpty(t, claims.ID)
					identifiers = append(identifiers, claims.ID)
					return nil
				}
				transport := signedBrowserTransport(t, peer, registration, profile != "registered")
				peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh-one","scope":"records:read"}`
				if discovered {
					caller, err := NewHTTPCaller(HTTPOptions{Endpoint: peer.resource, Client: transport, ClientInfo: ClientInfo{Name: "host", Version: "1"}})
					require.NoError(t, err)
					_, err = caller.CallTool(t.Context(), CallRequest{Tool: "read", Payload: []byte(`{}`)})
					require.NoError(t, err)
				} else {
					wrapped := NewHTTPTransport(transport, ClientInfo{Name: "generated", Version: "1"}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
					require.NoError(t, callOAuthPeer(t.Context(), wrapped, peer.resource))
				}
				for _, reply := range []string{
					`{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh-two"}`,
					`{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600}`,
				} {
					setOAuthCredentialTime(t, transport, time.Now().Add(-2 * time.Hour))
					peer.tokenBody = reply
					require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
				}
				assert.EqualValues(t, 1, peer.hostCalls.Load())
				assert.EqualValues(t, 3, peer.tokenCalls.Load())
				peer.mutex.Lock()
				defer peer.mutex.Unlock()
				require.Len(t, peer.forms, 3)
				require.Len(t, identifiers, 3)
				assert.NotEqual(t, identifiers[0], identifiers[1])
				assert.NotEqual(t, identifiers[1], identifiers[2])
				assert.Equal(t, "authorization_code", peer.forms[0].Get("grant_type"))
				assert.Equal(t, "refresh_token", peer.forms[1].Get("grant_type"))
				assert.Equal(t, "private-refresh-one", peer.forms[1].Get("refresh_token"))
				assert.Equal(t, "private-refresh-two", peer.forms[2].Get("refresh_token"))
				assert.Equal(t, "private-refresh-two", *storedOAuthCredential(t, transport).Token.RefreshToken)
				for _, form := range peer.forms[1:] {
					assert.NotContains(t, form, "code")
					assert.NotContains(t, form, "code_verifier")
				}
			})
		}
	}
}

func TestSignedClientMetadataRejectsBeforeConsent(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"public registration", `"private_key_jwt"`, `"none"`},
		{"no keys", `,"jwks_uri":"https://client.example/keys"`, ""},
		{"two key sources", `,"jwks_uri":"https://client.example/keys"`, `,"jwks_uri":"https://client.example/keys","jwks":{"keys":[]}`},
		{"empty inline keys", `,"jwks_uri":"https://client.example/keys"`, `,"jwks":{"keys":[]}`},
		{"insecure keys", `https://client.example/keys`, `http://client.example/keys`},
		{"wrong identity", `"client_id":"CLIENT"`, `"client_id":"https://other.example/client"`},
		{"wrong callback", `"redirect_uris":["REDIRECT"]`, `"redirect_uris":["https://other.example/callback"]`},
		{"empty callbacks", `"redirect_uris":["REDIRECT"]`, `"redirect_uris":[]`},
		{"shared secret", `"client_name":"Signed browser"`, `"client_name":"Signed browser","client_secret":"private-canary"`},
		{"null keys", `,"jwks_uri":"https://client.example/keys"`, `,"jwks":null`},
		{"symmetric keys", `,"jwks_uri":"https://client.example/keys"`, `,"jwks":{"keys":[{"kty":"oct","k":"cHJpdmF0ZQ"}]}`},
		{"invalid keys", `,"jwks_uri":"https://client.example/keys"`, `,"jwks":{"keys":[{"kty":"OKP","crv":"Ed25519","x":"c2hvcnQ"}]}`},
		{"null key list", `,"jwks_uri":"https://client.example/keys"`, `,"jwks":{"keys":null}`},
		{"duplicate key list", `,"jwks_uri":"https://client.example/keys"`, `,"jwks":{"keys":[],"keys":[]}`},
		{"private keys", `,"jwks_uri":"https://client.example/keys"`, `,"jwks":{"keys":[{"kty":"OKP","crv":"Ed25519","x":"11qYAYKxCrfVS_7TyW7Jls5scHZXVB0fHfwEdROBJJI","d":"nWGxne_9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A"}]}`},
		{"wrong grants", `"authorization_code"`, `"client_credentials"`},
		{"wrong responses", `"code"`, `"token"`},
		{"duplicate member", `"client_name":"Signed browser"`, `"client_name":"Signed browser","client_name":"Other"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer, registration, key := signedBrowserOAuthPeer(t)
			registration.ClientID = peer.server.URL + "/client/document.json"
			peer.clientID = registration.ClientID
			document := signedBrowserMetadata(t, registration, key, false)
			old := strings.NewReplacer("CLIENT", registration.ClientID, "REDIRECT", registration.RedirectURI).Replace(tc.old)
			peer.clientMetadata = strings.Replace(document, old, tc.replacement, 1)
			require.NotEqual(t, document, peer.clientMetadata)
			transport := signedBrowserTransport(t, peer, registration, true)
			err := callOAuthPeer(t.Context(), transport, peer.resource)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-canary")
			assert.Zero(t, peer.hostCalls.Load())
			assert.Zero(t, peer.tokenCalls.Load())
			assert.Zero(t, peer.mcpCalls.Load())
		})
	}
}

func TestSignedBrowserIssuerRequiresSignedAuthentication(t *testing.T) {
	for _, tc := range []struct{ old, replacement string }{
		{`"private_key_jwt"`, `"none"`},
		{`,"token_endpoint_auth_signing_alg_values_supported":["EdDSA"]`, ""},
	} {
		peer, registration, _ := signedBrowserOAuthPeer(t)
		peer.issuerBody = strings.Replace(peer.issuerBody, tc.old, tc.replacement, 1)
		transport := signedBrowserTransport(t, peer, registration, false)
		require.Error(t, callOAuthPeer(t.Context(), transport, peer.resource))
		assert.Zero(t, peer.hostCalls.Load())
		assert.Zero(t, peer.tokenCalls.Load())
	}
}

func TestSignedMetadataMachineWithoutBrowserRedirects(t *testing.T) {
	peer, config, publicKey := signedBrowserOAuthPeer(t)
	config.ClientID = peer.server.URL + "/client/document.json"
	peer.clientID = config.ClientID
	peer.clientMetadata = fmt.Sprintf(`{"client_id":%q,"client_name":"Machine client","redirect_uris":[],"token_endpoint_auth_method":"private_key_jwt","grant_types":["client_credentials"],"jwks_uri":"https://client.example/keys"}`, config.ClientID)
	peer.issuerBody = strings.Replace(peer.issuerBody, `"authorization_code","refresh_token"`, `"client_credentials"`, 1)
	peer.verifyClient = func(request *http.Request) error {
		assert.Empty(t, request.Header.Get("Authorization"))
		assert.NotContains(t, request.PostForm, "client_id")
		assert.NotContains(t, request.PostForm, "client_secret")
		assert.Equal(t, "client_credentials", request.PostForm.Get("grant_type"))
		token, err := jwt.ParseSigned(request.PostForm.Get("client_assertion"), []jose.SignatureAlgorithm{jose.EdDSA})
		if err != nil {
			return err
		}
		var claims jwt.Claims
		if err := token.Claims(publicKey, &claims); err != nil {
			return err
		}
		assert.Equal(t, config.ClientID, claims.Subject)
		return nil
	}
	registration, err := NewSignedClientMetadataRegistration(config.ClientAssertion)
	require.NoError(t, err)
	transport, err := NewClientCredentialsHTTPTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, ClientCredentials{Store: NewMemoryAuthorizationStore(), Registration: registration})
	require.NoError(t, err)
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.Zero(t, peer.hostCalls.Load())
	assert.EqualValues(t, 1, peer.tokenCalls.Load())
}

// signedBrowserOAuthPeer adds a real signing registration to the ordinary
// browser peer, while retaining its existing PKCE and callback checks.
func signedBrowserOAuthPeer(t *testing.T) (*browserOAuthPeer, signedBrowserConfig, ed25519.PublicKey) {
	t.Helper()
	peer := newBrowserOAuthPeer(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: privateKey}, (&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	peer.issuerBody = strings.Replace(peer.issuerBody, `"none"`, `"private_key_jwt"`, 1)
	peer.issuerBody = strings.TrimSuffix(peer.issuerBody, "}") + `,"token_endpoint_auth_signing_alg_values_supported":["EdDSA"],"client_id_metadata_document_supported":true}`
	return peer, signedBrowserConfig{
		ClientAssertion: ClientAssertion{Issuer: peer.issuer, ClientID: peer.clientID, AssertionIssuer: "registered-signer", Audience: "registered-audience", Lifetime: time.Minute, Signer: signer},
		RedirectURI:     "https://host.example/callback/a%2Fb?route=selected", Authorize: peer.authorize(t),
	}, publicKey
}

// signedBrowserMetadata publishes either inline public keys or their registered
// HTTPS address. Neither variant includes the private signing key.
func signedBrowserMetadata(t *testing.T, registration signedBrowserConfig, key ed25519.PublicKey, inline bool) string {
	t.Helper()
	keys := `,"jwks_uri":"https://client.example/keys"`
	if inline {
		encoded, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key, KeyID: "client", Use: "sig", Algorithm: "EdDSA"}}})
		require.NoError(t, err)
		keys = `,"jwks":` + string(encoded)
	}
	return fmt.Sprintf(`{"client_id":%q,"client_name":"Signed browser","redirect_uris":[%q],"token_endpoint_auth_method":"private_key_jwt","grant_types":["authorization_code","refresh_token"],"response_types":["code"]%s}`, registration.ClientID, registration.RedirectURI, keys)
}

// signedBrowserTransport constructs only the selected registration profile;
// every returned transport uses the existing shared credential owner.
func signedBrowserTransport(t *testing.T, peer *browserOAuthPeer, registration signedBrowserConfig, metadata bool) *HTTPTransport {
	t.Helper()
	constructor := NewSignedClientRegistration
	if metadata {
		constructor = NewSignedClientMetadataRegistration
	}
	client, err := constructor(registration.ClientAssertion)
	require.NoError(t, err)
	transport, err := NewAuthorizationCodeHTTPTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, AuthorizationCode{Store: NewMemoryAuthorizationStore(), Registration: client, RedirectURI: registration.RedirectURI, Authorize: registration.Authorize})
	require.NoError(t, err)
	return transport
}
