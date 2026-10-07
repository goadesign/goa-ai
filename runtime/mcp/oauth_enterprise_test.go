// These HTTPS peers independently verify both enterprise exchanges, including
// signed client authentication and the IdP's signed resource authorization grant.
// The existing MCP peer checks final bearer usage; SSO credentials stay at the IdP.
package mcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// enterpriseOAuthPeer checks the IdP and independent resource registrations.
	enterpriseOAuthPeer struct {
		resource             *browserOAuthPeer
		idp                  *httptest.Server
		issuer               string
		metadata             string
		registration         *ClientRegistration
		identityRegistration *ClientRegistration
		grantSigner          jose.Signer
		grantKey             ed25519.PublicKey
		mutex                sync.Mutex
		forms                []url.Values
		identityBody         string
		resources            map[string]*browserOAuthPeer
		refreshBody          string
		status               int
		sourceCalls          atomic.Int32
	}
)

func TestEnterpriseAuthenticationProfilesAndRenewal(t *testing.T) {
	profiles := []string{oauthPublicClient, oauthBasicClient, oauthSecretClient, oauthSignedClient}
	for _, identityAuthentication := range profiles {
		for _, resourceAuthentication := range profiles {
			for _, source := range []string{"id token", "refresh", "SAML"} {
				t.Run(identityAuthentication+"/"+resourceAuthentication+"/"+source, func(t *testing.T) {
					peer := newEnterpriseOAuthPeer(t, identityAuthentication, resourceAuthentication)
					identity := peer.identity(t, source)
					transport := peer.transport(t, identity)
					for round := range 2 {
						if round > 0 {
							setOAuthCredentialTime(t, transport, time.Now().Add(-2*time.Hour))
						}
						require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource.resource))
					}
					assert.EqualValues(t, 2, peer.resource.tokenCalls.Load())
					assert.EqualValues(t, 2, peer.resource.mcpCalls.Load())
					assert.Zero(t, peer.resource.hostCalls.Load())
					peer.mutex.Lock()
					defer peer.mutex.Unlock()
					if source == "SAML" {
						assert.EqualValues(t, 1, peer.sourceCalls.Load())
						require.Len(t, peer.forms, 3)
						assert.Equal(t, "openid offline_access", peer.forms[0].Get("scope"))
						assert.NotContains(t, peer.forms[0], "audience")
						assert.NotContains(t, peer.forms[0], "resource")
					} else {
						assert.EqualValues(t, 2, peer.sourceCalls.Load())
						require.Len(t, peer.forms, 2)
					}
				})
			}
		}
	}
}

func TestEnterpriseDiscoveryCallerAndScopeNarrowing(t *testing.T) {
	peer := newEnterpriseOAuthPeer(t, oauthPublicClient, oauthBasicClient)
	peer.identityBody = peer.grantResponse(t, "records:read")
	identity := peer.identity(t, "id token")
	transport := peer.transport(t, identity)
	transport.authorization.scopes = []string{"records:read", "records:write"}
	caller, err := NewHTTPCaller(HTTPOptions{Endpoint: peer.resource.resource, Client: transport, ClientInfo: ClientInfo{Name: "host", Version: "1"}})
	require.NoError(t, err)
	response, err := caller.CallTool(t.Context(), CallRequest{Tool: "read", Payload: []byte(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":"ok"}`, string(response.StructuredContent))
	assert.Equal(t, []string{"records:read"}, transport.authorization.state.Granted)
	assert.Equal(t, []string{"records:read", "records:write"}, transport.authorization.state.Requested)
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	require.Len(t, peer.forms, 1)
	assert.Equal(t, "records:read records:write", peer.forms[0].Get("scope"))
	peer.resource.mutex.Lock()
	defer peer.resource.mutex.Unlock()
	require.Len(t, peer.resource.forms, 1)
	assert.Equal(t, "records:read", peer.resource.forms[0].Get("scope"))
}

func TestEnterpriseInvalidResponsesStopBeforeResourceExchange(t *testing.T) {
	for _, response := range []string{
		`{"issued_token_type":"urn:ietf:params:oauth:token-type:id-jag","access_token":"a.b.c","token_type":"Bearer"}`,
		`{"issued_token_type":"urn:ietf:params:oauth:token-type:access_token","access_token":"a.b.c","token_type":"N_A"}`,
		`{"issued_token_type":"urn:ietf:params:oauth:token-type:id-jag","access_token":"private-canary","token_type":"N_A"}`,
		`{"issued_token_type":"urn:ietf:params:oauth:token-type:id-jag","access_token":"a.b.c","token_type":"N_A","expires_in":0}`,
		`{"issued_token_type":"urn:ietf:params:oauth:token-type:id-jag","access_token":"a.b.c","token_type":"N_A","expires_in":null}`,
		`{"issued_token_type":"urn:ietf:params:oauth:token-type:id-jag","access_token":"a.b.c","token_type":"N_A","scope":"bad\npermission"}`,
	} {
		t.Run(response, func(t *testing.T) {
			peer := newEnterpriseOAuthPeer(t, oauthPublicClient, oauthSecretClient)
			peer.identityBody = response
			err := callOAuthPeer(t.Context(), peer.transport(t, peer.identity(t, "id token")), peer.resource.resource)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-canary")
			assert.Zero(t, peer.resource.tokenCalls.Load())
			assert.Zero(t, peer.resource.mcpCalls.Load())
			var unknown *OutcomeUnknownError
			assert.NotErrorAs(t, err, &unknown)
		})
	}
}

func TestEnterpriseSAMLBootstrapRequiresFreshAssertion(t *testing.T) {
	peer := newEnterpriseOAuthPeer(t, oauthPublicClient, oauthPublicClient)
	peer.refreshBody = `{"issued_token_type":"urn:ietf:params:oauth:token-type:refresh_token","access_token":"private-canary","token_type":"Bearer"}`
	transport := peer.transport(t, peer.identity(t, "SAML"))
	for range 2 {
		err := callOAuthPeer(t.Context(), transport, peer.resource.resource)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "private-canary")
	}
	assert.EqualValues(t, 2, peer.sourceCalls.Load())
	require.Len(t, peer.forms, 2)
	assert.NotEqual(t, peer.forms[0].Get("subject_token"), peer.forms[1].Get("subject_token"))
	assert.Zero(t, peer.resource.tokenCalls.Load())
}

func TestEnterpriseRejectsInvalidHostConfiguration(t *testing.T) {
	registration, err := NewPublicClientRegistration("https://identity.example", "registered")
	require.NoError(t, err)
	for _, constructor := range []func(*ClientRegistration, *http.Client, AuthorizationStore, func(context.Context) (string, error)) (*EnterpriseIdentity, error){NewIDTokenEnterpriseIdentity, NewSAMLEnterpriseIdentity, NewRefreshTokenEnterpriseIdentity} {
		_, err := constructor(registration, nil, NewMemoryAuthorizationStore(), func(context.Context) (string, error) { return "identity", nil })
		require.Error(t, err)
		_, err = constructor(registration, http.DefaultClient, NewMemoryAuthorizationStore(), nil)
		require.Error(t, err)
		_, err = constructor(&ClientRegistration{}, http.DefaultClient, NewMemoryAuthorizationStore(), func(context.Context) (string, error) { return "identity", nil })
		require.Error(t, err)
	}
	_, err = NewEnterpriseHTTPTransport(HTTPOptions{Endpoint: "https://resource.example/mcp"}, EnterpriseAuthorization{Identity: &EnterpriseIdentity{}, Registration: registration})
	assert.Error(t, err)
}

// newEnterpriseOAuthPeer creates separate HTTPS issuers and signed IdP grants.
// Each issuer independently checks its registered authentication and token purpose.
func newEnterpriseOAuthPeer(t *testing.T, identityAuthentication, resourceAuthentication string) *enterpriseOAuthPeer {
	t.Helper()
	peer := &enterpriseOAuthPeer{resource: newBrowserOAuthPeer(t)}
	var err error
	var privateKey ed25519.PrivateKey
	peer.grantKey, privateKey, err = ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	peer.grantSigner, err = jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: privateKey}, (&jose.SignerOptions{}).WithType("oauth-id-jag+jwt"))
	require.NoError(t, err)
	peer.resource.clientID = "resource:application +"
	resourceRegistration, verifyResource := enterpriseTestRegistration(t, peer.resource.issuer, peer.resource.clientID, resourceAuthentication)
	peer.registration = resourceRegistration
	peer.resource.issuerBody = enterpriseIssuerMetadata(peer.resource.issuer, peer.resource.server.URL+"/token/a%2Fb?routing=selected", resourceAuthentication)
	peer.resource.verifyClient = func(request *http.Request) error {
		if err := verifyResource(request); err != nil {
			return err
		}
		assert.Equal(t, oauthIdentityRedemption, request.PostForm.Get("grant_type"))
		assert.NotContains(t, request.PostForm, "refresh_token")
		return verifyEnterpriseTestGrant(t, request, peer.resource, peer.issuer, peer.grantKey)
	}
	var identityVerifier func(*http.Request) error
	peer.idp = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/.well-known/oauth-authorization-server/identity" {
			assert.Empty(t, r.Header.Get("Authorization"))
			_, err := io.WriteString(w, peer.metadata)
			assert.NoError(t, err)
			return
		}
		if r.URL.Path != "/identity/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		assert.NoError(t, r.ParseForm())
		assert.NoError(t, identityVerifier(r))
		assert.Equal(t, oauthIdentityExchange, r.PostForm.Get("grant_type"))
		peer.mutex.Lock()
		peer.forms = append(peer.forms, r.PostForm)
		peer.mutex.Unlock()
		var body string
		if r.PostForm.Get("subject_token_type") == "urn:ietf:params:oauth:token-type:saml2" {
			decoded, err := base64.RawURLEncoding.DecodeString(r.PostForm.Get("subject_token"))
			assert.NoError(t, err)
			assert.Regexp(t, `^<Assertion id="[0-9]+">host user & identity</Assertion>$`, string(decoded))
			assert.Equal(t, "urn:ietf:params:oauth:token-type:refresh_token", r.PostForm.Get("requested_token_type"))
			body = peer.refreshBody
		} else {
			assert.Equal(t, oauthIdentityGrantType, r.PostForm.Get("requested_token_type"))
			target, ok := peer.resources[r.PostForm.Get("audience")]
			if !assert.True(t, ok, "identity grant targets a registered resource issuer") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, target.resource, r.PostForm.Get("resource"))
			if r.PostForm.Get("subject_token_type") == "urn:ietf:params:oauth:token-type:id_token" {
				assert.Equal(t, "encrypted.header.key.ciphertext.tag", r.PostForm.Get("subject_token"))
			} else {
				assert.Equal(t, "urn:ietf:params:oauth:token-type:refresh_token", r.PostForm.Get("subject_token_type"))
				assert.Equal(t, "private-identity-refresh", r.PostForm.Get("subject_token"))
			}
			body = peer.identityBody
			if body == "" {
				body = peer.grantResponseFor(t, target, "")
			}
		}
		if peer.status != 0 {
			w.WriteHeader(peer.status)
		}
		_, err := io.WriteString(w, body)
		assert.NoError(t, err)
	}))
	t.Cleanup(peer.idp.Close)
	peer.issuer = peer.idp.URL + "/identity"
	identityRegistration, verifyIdentity := enterpriseTestRegistration(t, peer.issuer, "identity:application +", identityAuthentication)
	identityVerifier = verifyIdentity
	peer.metadata = enterpriseIssuerMetadata(peer.issuer, peer.idp.URL+"/identity/token", identityAuthentication)
	peer.resources = map[string]*browserOAuthPeer{peer.resource.issuer: peer.resource}
	peer.refreshBody = `{"issued_token_type":"urn:ietf:params:oauth:token-type:refresh_token","access_token":"private-identity-refresh","token_type":"N_A","expires_in":3600}`
	peer.resource.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"unused-resource-refresh"}`
	peer.identityRegistration = identityRegistration
	return peer
}

// grantResponse signs a fresh grant for the fixture's exact resource issuer.
// The resource peer verifies its signature and identity before issuing a token.
func (p *enterpriseOAuthPeer) grantResponse(t *testing.T, scope string) string {
	t.Helper()
	return p.grantResponseFor(t, p.resource, scope)
}

// grantResponseFor binds the signed grant to one configured resource and client.
func (p *enterpriseOAuthPeer) grantResponseFor(t *testing.T, target *browserOAuthPeer, scope string) string {
	t.Helper()
	serialized, err := jwt.Signed(p.grantSigner).Claims(jwt.Claims{Issuer: p.issuer, Subject: "host-user", Audience: jwt.Audience{target.issuer}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}).Claims(struct {
		ClientID string `json:"client_id"`
		Resource string `json:"resource"`
	}{ClientID: target.clientID, Resource: target.resource}).Serialize()
	require.NoError(t, err)
	scopeMember := ""
	if scope != "" {
		scopeMember = fmt.Sprintf(`,"scope":%q`, scope)
	}
	return fmt.Sprintf(`{"issued_token_type":%q,"access_token":%q,"token_type":"N_A","expires_in":60%s}`, oauthIdentityGrantType, serialized, scopeMember)
}

// identity constructs the host's known source credential kind for this user.
func (p *enterpriseOAuthPeer) identity(t *testing.T, source string) *EnterpriseIdentity {
	t.Helper()
	return p.identityWithStore(t, source, NewMemoryAuthorizationStore())
}

// identityWithStore reconstructs this host user's known source and account storage.
func (p *enterpriseOAuthPeer) identityWithStore(t *testing.T, source string, store AuthorizationStore) *EnterpriseIdentity {
	t.Helper()
	constructor := NewIDTokenEnterpriseIdentity
	credential := "encrypted.header.key.ciphertext.tag"
	switch source {
	case "refresh":
		constructor, credential = NewRefreshTokenEnterpriseIdentity, "private-identity-refresh"
	case "SAML":
		constructor, credential = NewSAMLEnterpriseIdentity, "<Assertion>host user & identity</Assertion>"
	}
	identity, err := constructor(p.identityRegistration, p.idp.Client(), store, func(context.Context) (string, error) {
		call := p.sourceCalls.Add(1)
		if source == "SAML" {
			return fmt.Sprintf("<Assertion id=%q>host user & identity</Assertion>", fmt.Sprint(call)), nil
		}
		return credential, nil
	})
	require.NoError(t, err)
	return identity
}

// transport constructs an independent resource-token owner for this identity.
func (p *enterpriseOAuthPeer) transport(t *testing.T, identity *EnterpriseIdentity) *HTTPTransport {
	t.Helper()
	transport, err := NewEnterpriseHTTPTransport(HTTPOptions{Endpoint: p.resource.resource, Client: p.resource.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, EnterpriseAuthorization{Identity: identity, Registration: p.registration, Scopes: []string{"records:read"}})
	require.NoError(t, err)
	return transport
}

// enterpriseIssuerMetadata advertises only the selected authentication. Enterprise
// profile advertisements are intentionally omitted to test explicit configuration.
func enterpriseIssuerMetadata(issuer, endpoint, authentication string) string {
	return fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q,"token_endpoint_auth_methods_supported":[%q],"token_endpoint_auth_signing_alg_values_supported":["EdDSA"]}`, issuer, endpoint, authentication)
}

// enterpriseTestRegistration verifies native authentication against its own
// secret or signing key, rather than trusting the client's selected fields.
func enterpriseTestRegistration(t *testing.T, issuer, clientID, authentication string) (*ClientRegistration, func(*http.Request) error) {
	t.Helper()
	var registration *ClientRegistration
	var key ed25519.PublicKey
	var err error
	switch authentication {
	case oauthPublicClient:
		registration, err = NewPublicClientRegistration(issuer, clientID)
	case oauthBasicClient:
		registration, err = NewBasicClientRegistration(issuer, clientID, "private:secret +")
	case oauthSecretClient:
		registration, err = NewSecretClientRegistration(issuer, clientID, "private:secret +")
	case oauthSignedClient:
		var privateKey ed25519.PrivateKey
		key, privateKey, err = ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		var signer jose.Signer
		signer, err = jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: privateKey}, nil)
		require.NoError(t, err)
		registration, err = NewSignedClientRegistration(ClientAssertion{Issuer: issuer, ClientID: clientID, AssertionIssuer: clientID, Audience: issuer, Lifetime: time.Minute, Signer: signer})
	}
	require.NoError(t, err)
	return registration, func(request *http.Request) error {
		form := request.PostForm
		switch authentication {
		case oauthBasicClient:
			assert.NotContains(t, form, "client_id")
			assert.NotContains(t, form, "client_secret")
			return verifyRegistrationBasic(request, registration)
		case oauthPublicClient, oauthSecretClient:
			assert.Empty(t, request.Header.Get("Authorization"))
			assert.Equal(t, clientID, form.Get("client_id"))
			if authentication == oauthSecretClient {
				assert.Equal(t, registration.secret, form.Get("client_secret"))
			} else {
				assert.NotContains(t, form, "client_secret")
			}
		case oauthSignedClient:
			assert.Empty(t, request.Header.Get("Authorization"))
			assert.NotContains(t, form, "client_id")
			assert.NotContains(t, form, "client_secret")
			assert.Equal(t, "urn:ietf:params:oauth:client-assertion-type:jwt-bearer", form.Get("client_assertion_type"))
			token, err := jwt.ParseSigned(form.Get("client_assertion"), []jose.SignatureAlgorithm{jose.EdDSA})
			if err != nil {
				return err
			}
			var claims jwt.Claims
			if err := token.Claims(key, &claims); err != nil {
				return err
			}
			if claims.Issuer != clientID || claims.Subject != clientID || !claims.Audience.Contains(issuer) || claims.ID == "" {
				return errors.New("signed registration claims differ")
			}
		}
		assert.NotContains(t, request.URL.RawQuery, "private")
		return nil
	}
}

// verifyEnterpriseTestGrant verifies the IdP signature, purpose, lifetime and
// independent client/resource binding before the peer issues an access token.
func verifyEnterpriseTestGrant(t *testing.T, request *http.Request, target *browserOAuthPeer, issuer string, key ed25519.PublicKey) error {
	t.Helper()
	token, err := jwt.ParseSigned(request.PostForm.Get("assertion"), []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		return err
	}
	var claims jwt.Claims
	var binding struct {
		ClientID string `json:"client_id"`
		Resource string `json:"resource"`
	}
	if err := token.Claims(key, &claims, &binding); err != nil {
		return err
	}
	assert.Equal(t, "oauth-id-jag+jwt", token.Headers[0].ExtraHeaders[jose.HeaderType])
	assert.Equal(t, issuer, claims.Issuer)
	assert.Equal(t, jwt.Audience{target.issuer}, claims.Audience)
	assert.Equal(t, "host-user", claims.Subject)
	assert.Equal(t, target.clientID, binding.ClientID)
	assert.Equal(t, target.resource, binding.Resource)
	return claims.Validate(jwt.Expected{Issuer: issuer, Subject: "host-user", AnyAudience: jwt.Audience{target.issuer}, Time: time.Now()})
}

func TestEnterpriseSAMLIdentitySharedAcrossResources(t *testing.T) {
	peer := newEnterpriseOAuthPeer(t, oauthSignedClient, oauthBasicClient)
	second := newBrowserOAuthPeer(t)
	second.clientID = "another-resource-client"
	registration, verify := enterpriseTestRegistration(t, second.issuer, second.clientID, oauthSecretClient)
	second.issuerBody = enterpriseIssuerMetadata(second.issuer, second.server.URL+"/token/a%2Fb?routing=selected", oauthSecretClient)
	second.verifyClient = func(request *http.Request) error {
		if err := verify(request); err != nil {
			return err
		}
		assert.Equal(t, oauthIdentityRedemption, request.PostForm.Get("grant_type"))
		return verifyEnterpriseTestGrant(t, request, second, peer.issuer, peer.grantKey)
	}
	peer.resources[second.issuer] = second
	identity := peer.identity(t, "SAML")
	first := peer.transport(t, identity)
	another, err := NewEnterpriseHTTPTransport(HTTPOptions{Endpoint: second.resource, Client: second.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, EnterpriseAuthorization{Identity: identity, Registration: registration})
	require.NoError(t, err)
	require.NoError(t, callOAuthPeer(t.Context(), first, peer.resource.resource))
	require.NoError(t, callOAuthPeer(t.Context(), another, second.resource))
	assert.EqualValues(t, 1, peer.sourceCalls.Load())
	assert.Len(t, peer.forms, 3)
	assert.EqualValues(t, 1, peer.resource.tokenCalls.Load())
	assert.EqualValues(t, 1, second.tokenCalls.Load())
	assert.NotEqual(t, storedOAuthCredential(t, first).Issuance, storedOAuthCredential(t, another).Issuance)
	// A separately constructed identity cannot reuse the first owner's bootstrap.
	separate := peer.identity(t, "SAML")
	require.NoError(t, callOAuthPeer(t.Context(), peer.transport(t, separate), peer.resource.resource))
	assert.EqualValues(t, 2, peer.sourceCalls.Load())
	assert.Len(t, peer.forms, 5)
}

func TestEnterpriseExplicitRejectionUsesSharedRecovery(t *testing.T) {
	peer := newEnterpriseOAuthPeer(t, oauthBasicClient, oauthSignedClient)
	transport := peer.transport(t, peer.identity(t, "id token"))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource.resource))
	var requests atomic.Int32
	peer.resource.mcpHandle = func(w http.ResponseWriter, _ *http.Request) bool {
		if requests.Add(1) == 1 {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return true
		}
		return false
	}
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource.resource))
	assert.EqualValues(t, 2, requests.Load())
	assert.EqualValues(t, 2, peer.resource.tokenCalls.Load())
	assert.EqualValues(t, 2, peer.sourceCalls.Load())
	assert.Zero(t, peer.resource.hostCalls.Load())
}

func TestEnterpriseMetadataRelationships(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*enterpriseOAuthPeer)
	}{
		{"advertised profile without redemption", func(p *enterpriseOAuthPeer) {
			p.resource.issuerBody = strings.TrimSuffix(p.resource.issuerBody, "}") + `,"authorization_grant_profiles_supported":["urn:ietf:params:oauth:grant-profile:id-jag"]}`
		}},
		{"IdP advertises another purpose", func(p *enterpriseOAuthPeer) {
			p.metadata = strings.TrimSuffix(p.metadata, "}") + `,"identity_chaining_requested_token_types_supported":["urn:ietf:params:oauth:token-type:access_token"]}`
		}},
		{"wrong IdP", func(p *enterpriseOAuthPeer) {
			p.metadata = strings.Replace(p.metadata, p.issuer, "https://different.example", 1)
		}},
		{"null profile", func(p *enterpriseOAuthPeer) {
			p.resource.issuerBody = strings.TrimSuffix(p.resource.issuerBody, "}") + `,"authorization_grant_profiles_supported":null}`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newEnterpriseOAuthPeer(t, oauthPublicClient, oauthPublicClient)
			tc.change(peer)
			err := callOAuthPeer(t.Context(), peer.transport(t, peer.identity(t, "id token")), peer.resource.resource)
			require.Error(t, err)
			assert.Zero(t, peer.sourceCalls.Load())
			assert.Zero(t, peer.resource.tokenCalls.Load())
			assert.Zero(t, peer.resource.mcpCalls.Load())
		})
	}
}

func TestEnterpriseCancellationAndSafeSourceErrors(t *testing.T) {
	for _, cancelSource := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelSource), func(t *testing.T) {
			peer := newEnterpriseOAuthPeer(t, oauthPublicClient, oauthPublicClient)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			identity, err := NewIDTokenEnterpriseIdentity(peer.identityRegistration, peer.idp.Client(), NewMemoryAuthorizationStore(), func(context.Context) (string, error) {
				if cancelSource {
					cancel()
					return "encrypted.header.key.ciphertext.tag", nil
				}
				return "", errors.New("private-canary")
			})
			require.NoError(t, err)
			err = callOAuthPeer(ctx, peer.transport(t, identity), peer.resource.resource)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-canary")
			if cancelSource {
				require.ErrorIs(t, err, context.Canceled)
			}
			assert.Empty(t, peer.forms)
			assert.Zero(t, peer.resource.tokenCalls.Load())
		})
	}
}

func TestEnterpriseIdentityRefreshLifetime(t *testing.T) {
	peer := newEnterpriseOAuthPeer(t, oauthPublicClient, oauthPublicClient)
	peer.refreshBody = `{"issued_token_type":"urn:ietf:params:oauth:token-type:refresh_token","access_token":"private-identity-refresh","token_type":"N_A","expires_in":0}`
	transport := peer.transport(t, peer.identity(t, "SAML"))
	for range 2 {
		require.Error(t, callOAuthPeer(t.Context(), transport, peer.resource.resource))
	}
	assert.EqualValues(t, 2, peer.sourceCalls.Load())
	require.Len(t, peer.forms, 2)
	assert.NotEqual(t, peer.forms[0].Get("subject_token"), peer.forms[1].Get("subject_token"))
	assert.Zero(t, peer.resource.tokenCalls.Load())
}

func TestEnterpriseClientMetadataProfileRelationships(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprintf("valid=%t", valid), func(t *testing.T) {
			peer := newEnterpriseOAuthPeer(t, oauthPublicClient, oauthPublicClient)
			peer.resource.clientID = peer.resource.server.URL + "/client/document.json"
			registration, err := NewPublicClientMetadataRegistration(peer.resource.issuer, peer.resource.clientID)
			require.NoError(t, err)
			peer.registration = registration
			grants := []string{oauthIdentityRedemption}
			if valid {
				grants = append(grants, oauthIdentityExchange)
			}
			encoded, err := json.Marshal(grants)
			require.NoError(t, err)
			peer.resource.issuerBody = strings.TrimSuffix(peer.resource.issuerBody, "}") + `,"client_id_metadata_document_supported":true}`
			peer.resource.clientMetadata = fmt.Sprintf(`{"client_id":%q,"client_name":"Enterprise host","redirect_uris":[],"token_endpoint_auth_method":"none","grant_types":%s,"authorization_grant_profiles_supported":["urn:ietf:params:oauth:grant-profile:id-jag"]}`, peer.resource.clientID, encoded)
			peer.resource.verifyClient = func(request *http.Request) error {
				assert.Equal(t, peer.resource.clientID, request.PostForm.Get("client_id"))
				return verifyEnterpriseTestGrant(t, request, peer.resource, peer.issuer, peer.grantKey)
			}
			err = callOAuthPeer(t.Context(), peer.transport(t, peer.identity(t, "id token")), peer.resource.resource)
			if valid {
				require.NoError(t, err)
				assert.EqualValues(t, 1, peer.sourceCalls.Load())
			} else {
				require.Error(t, err)
				assert.Zero(t, peer.sourceCalls.Load())
				assert.Zero(t, peer.resource.tokenCalls.Load())
			}
		})
	}
}
