// These tests send native generated assertion forms through real HTTPS peers.
// The issuer verifies signatures and registration claims before issuing a token;
// the resource receives only that access token. Failure cases prove that signing
// problems stop before token exchange and do not become unknown MCP outcomes.
package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// controlledAssertionSigner preserves the constructed signer except for one
	// test-selected failure, cancellation or delay during its signing operation.
	controlledAssertionSigner struct {
		jose.Signer
		failure error
		cancel  context.CancelFunc
		delay   time.Duration
	}
)

func TestClientAssertionAuthenticatesBeforeDispatch(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	for _, discovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("discovered=%t", discovered), func(t *testing.T) {
			peer := newOAuthPeer(t)
			advertiseAssertion(peer, "RS256")
			registration := assertionRegistration(peer, signer)
			received := make(chan jwt.Claims, 2)
			peer.mutex.Lock()
			peer.verifyGrant = func(form url.Values) error {
				claims, err := verifyAssertionForm(form, peer.resource, registration, &key.PublicKey, jose.RS256)
				if err != nil {
					return err
				}
				received <- claims
				return nil
			}
			peer.mutex.Unlock()
			transport, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, registration)
			require.NoError(t, err)
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
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
			assert.EqualValues(t, 2, peer.mcpCalls.Load())
			claims := <-received
			assert.Equal(t, registration.AssertionIssuer, claims.Issuer)
			assert.Equal(t, registration.ClientID, claims.Subject)
			assert.Equal(t, jwt.Audience{registration.Audience}, claims.Audience)
			assert.NotEqual(t, peer.issuer, registration.Audience)
			assert.Equal(t, registration.Lifetime, claims.Expiry.Time().Sub(claims.IssuedAt.Time()))
			assert.NotEmpty(t, claims.ID)
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			assert.Contains(t, peer.addresses, "/token/a%2Fb?route=selected")
			require.Len(t, peer.forms, 1)
			assert.Equal(t, "records:read", peer.forms[0].Get("scope"))
			assert.NotContains(t, peer.forms[0], "client_id")
			assert.NotContains(t, peer.forms[0], "client_secret")
		})
	}
}

func TestClientAssertionChecksMetadataAndAlgorithms(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	require.NoError(t, err)
	cases := []struct {
		name   string
		change func(*oauthPeer)
	}{
		{"missing method", func(p *oauthPeer) {
			p.issuerBody = strings.ReplaceAll(p.issuerBody, "private_key_jwt", "client_secret_post")
		}},
		{"missing grant", func(p *oauthPeer) {
			p.issuerBody = strings.ReplaceAll(p.issuerBody, "client_credentials", "authorization_code")
		}},
		{"missing algorithms", func(p *oauthPeer) {
			p.issuerBody = strings.ReplaceAll(p.issuerBody, `,"token_endpoint_auth_signing_alg_values_supported":["RS256"]`, "")
		}},
		{"empty algorithms", func(p *oauthPeer) { p.issuerBody = strings.ReplaceAll(p.issuerBody, `["RS256"]`, `[]`) }},
		{"null algorithms", func(p *oauthPeer) { p.issuerBody = strings.ReplaceAll(p.issuerBody, `["RS256"]`, `null`) }},
		{"null algorithm", func(p *oauthPeer) { p.issuerBody = strings.ReplaceAll(p.issuerBody, `["RS256"]`, `[null]`) }},
		{"other algorithm", func(p *oauthPeer) { p.issuerBody = strings.ReplaceAll(p.issuerBody, "RS256", "RS512") }},
		{"symmetric algorithm", func(p *oauthPeer) { p.issuerBody = strings.ReplaceAll(p.issuerBody, "RS256", "HS256") }},
		{"insecure token URL", func(p *oauthPeer) {
			p.issuerBody = strings.ReplaceAll(p.issuerBody, p.server.URL+"/token", "http://other.example/token")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newOAuthPeer(t)
			advertiseAssertion(peer, "RS256")
			tc.change(peer)
			transport, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, assertionRegistration(peer, signer))
			require.NoError(t, err)
			err = callOAuthPeer(t.Context(), transport, peer.resource)
			require.Error(t, err)
			assert.Zero(t, peer.tokenCalls.Load())
			assert.Zero(t, peer.mcpCalls.Load())
			var unknown *OutcomeUnknownError
			assert.NotErrorAs(t, err, &unknown)
		})
	}
}

func TestClientAssertionSigningFailuresStopBeforeExchange(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	require.NoError(t, err)
	for _, failure := range []string{"signing error", "cancellation", "expired while signing"} {
		t.Run(failure, func(t *testing.T) {
			peer := newOAuthPeer(t)
			advertiseAssertion(peer, "RS256")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			controlled := &controlledAssertionSigner{Signer: signer}
			switch failure {
			case "signing error":
				controlled.failure = errors.New("private signing diagnostic")
			case "cancellation":
				controlled.cancel = cancel
			case "expired while signing":
				controlled.delay = time.Second
			}
			registration := assertionRegistration(peer, controlled)
			registration.Lifetime = time.Second
			transport, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, registration)
			require.NoError(t, err)
			err = callOAuthPeer(ctx, transport, peer.resource)
			require.Error(t, err)
			if failure == "cancellation" {
				require.ErrorIs(t, err, context.Canceled)
			}
			assert.NotContains(t, err.Error(), "private signing diagnostic")
			assert.Zero(t, peer.tokenCalls.Load())
			assert.Zero(t, peer.mcpCalls.Load())
			var unknown *OutcomeUnknownError
			assert.NotErrorAs(t, err, &unknown)
		})
	}
}

func TestClientAssertionLifetimeDoesNotLimitAccessTokenReuse(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	require.NoError(t, err)
	peer := newOAuthPeer(t)
	advertiseAssertion(peer, "RS256")
	registration := assertionRegistration(peer, signer)
	registration.Lifetime = 2 * time.Second
	received := make(chan jwt.Claims, 2)
	peer.mutex.Lock()
	peer.verifyGrant = func(form url.Values) error {
		claims, err := verifyAssertionForm(form, peer.resource, registration, &key.PublicKey, jose.RS256)
		if err != nil {
			return err
		}
		received <- claims
		return nil
	}
	peer.mutex.Unlock()
	transport, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, registration)
	require.NoError(t, err)
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	claims := <-received
	// This assertion is expired, but the issuer's separate access-token lifetime
	// still permits the second MCP request without another signing operation.
	time.Sleep(time.Until(claims.Expiry.Time()))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.EqualValues(t, 1, peer.tokenCalls.Load())
	assert.EqualValues(t, 2, peer.mcpCalls.Load())
}

func TestClientAssertionUsesFreshIdentityForEachGrant(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	require.NoError(t, err)
	peer := newOAuthPeer(t)
	advertiseAssertion(peer, "RS256")
	peer.tokenBody = `{"access_token":"private-token","token_type":"Bearer"}`
	registration := assertionRegistration(peer, signer)
	received := make(chan jwt.Claims, 2)
	peer.mutex.Lock()
	peer.verifyGrant = func(form url.Values) error {
		claims, err := verifyAssertionForm(form, peer.resource, registration, &key.PublicKey, jose.RS256)
		if err != nil {
			return err
		}
		received <- claims
		return nil
	}
	peer.mutex.Unlock()
	transport, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, registration)
	require.NoError(t, err)
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
	first, second := <-received, <-received
	assert.NotEqual(t, first.ID, second.ID)
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
}

func TestClientAssertionSupportsAsymmetricSignerFamilies(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	edPublic, edKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cases := []struct {
		algorithm  jose.SignatureAlgorithm
		privateKey any
		publicKey  any
	}{
		{jose.RS256, rsaKey, &rsaKey.PublicKey},
		{jose.PS256, rsaKey, &rsaKey.PublicKey},
		{jose.ES256, ecKey, &ecKey.PublicKey},
		{jose.EdDSA, edKey, edPublic},
	}
	for _, tc := range cases {
		t.Run(string(tc.algorithm), func(t *testing.T) {
			peer := newOAuthPeer(t)
			advertiseAssertion(peer, string(tc.algorithm))
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: tc.algorithm, Key: tc.privateKey}, nil)
			require.NoError(t, err)
			registration := assertionRegistration(peer, signer)
			peer.mutex.Lock()
			peer.verifyGrant = func(form url.Values) error {
				_, err := verifyAssertionForm(form, peer.resource, registration, tc.publicKey, tc.algorithm)
				return err
			}
			peer.mutex.Unlock()
			transport, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, registration)
			require.NoError(t, err)
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
		})
	}
}

func TestClientAssertionRejectsSymmetricAuthentication(t *testing.T) {
	peer := newOAuthPeer(t)
	advertiseAssertion(peer, "HS256")
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: make([]byte, 32)}, nil)
	require.NoError(t, err)
	transport, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, assertionRegistration(peer, signer))
	require.NoError(t, err)
	require.Error(t, callOAuthPeer(t.Context(), transport, peer.resource))
	assert.Zero(t, peer.tokenCalls.Load())
	assert.Zero(t, peer.mcpCalls.Load())
}

func TestClientAssertionKeepsMachineRejectionsTerminal(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	require.NoError(t, err)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			peer := newOAuthPeer(t)
			advertiseAssertion(peer, "RS256")
			peer.mcpStatus = status
			transport, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, assertionRegistration(peer, signer))
			require.NoError(t, err)
			err = callOAuthPeer(t.Context(), transport, peer.resource)
			var response *HTTPResponseError
			require.ErrorAs(t, err, &response)
			assert.Equal(t, status, response.StatusCode)
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
			assert.EqualValues(t, 1, peer.mcpCalls.Load())
		})
	}
}

func TestClientAssertionIssuerRejectsWrongRegistration(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	require.NoError(t, err)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	otherSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: otherKey}, nil)
	require.NoError(t, err)
	cases := []struct {
		name   string
		change func(*ClientAssertion)
	}{
		{"unregistered key", func(c *ClientAssertion) { c.Signer = otherSigner }},
		{"wrong audience", func(c *ClientAssertion) { c.Audience = "other-audience" }},
		{"wrong assertion issuer", func(c *ClientAssertion) { c.AssertionIssuer = "other-signer" }},
		{"wrong subject", func(c *ClientAssertion) { c.ClientID = "other-client" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newOAuthPeer(t)
			advertiseAssertion(peer, "RS256")
			registered := assertionRegistration(peer, signer)
			credentials := registered
			tc.change(&credentials)
			peer.mutex.Lock()
			peer.verifyGrant = func(form url.Values) error {
				_, err := verifyAssertionForm(form, peer.resource, registered, &key.PublicKey, jose.RS256)
				return err
			}
			peer.mutex.Unlock()
			transport, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, credentials)
			require.NoError(t, err)
			err = callOAuthPeer(t.Context(), transport, peer.resource)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "eyJ")
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
			assert.Zero(t, peer.mcpCalls.Load())
			var unknown *OutcomeUnknownError
			assert.NotErrorAs(t, err, &unknown)
		})
	}
}

func TestIssuerSigningMetadataIsOptionalForSecretClients(t *testing.T) {
	for _, field := range []string{"", `,"token_endpoint_auth_signing_alg_values_supported":[]`, `,"token_endpoint_auth_signing_alg_values_supported":["RS256"]`} {
		t.Run(field, func(t *testing.T) {
			peer := newOAuthPeer(t)
			peer.issuerBody = strings.TrimSuffix(peer.issuerBody, "}") + field + "}"
			transport := peer.transport(t, "registered", "private-secret", nil)
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
			assert.EqualValues(t, 1, peer.mcpCalls.Load())
		})
	}
}

func TestClientAssertionRequiresExplicitRegistration(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	require.NoError(t, err)
	overridden, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("alg", "RS512"))
	require.NoError(t, err)
	cases := []struct {
		name   string
		change func(*ClientAssertion)
	}{
		{"missing client", func(c *ClientAssertion) { c.ClientID = "" }},
		{"missing assertion issuer", func(c *ClientAssertion) { c.AssertionIssuer = "" }},
		{"missing audience", func(c *ClientAssertion) { c.Audience = "" }},
		{"missing signer", func(c *ClientAssertion) { c.Signer = nil }},
		{"algorithm override", func(c *ClientAssertion) { c.Signer = overridden }},
		{"zero lifetime", func(c *ClientAssertion) { c.Lifetime = 0 }},
		{"negative lifetime", func(c *ClientAssertion) { c.Lifetime = -time.Second }},
		{"subsecond lifetime", func(c *ClientAssertion) { c.Lifetime = time.Millisecond }},
		{"fractional lifetime", func(c *ClientAssertion) { c.Lifetime = time.Second + time.Nanosecond }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			credentials := ClientAssertion{Issuer: "https://issuer.example", ClientID: "registered", AssertionIssuer: "registered-signer", Audience: "registered-audience", Lifetime: time.Minute, Signer: signer}
			tc.change(&credentials)
			_, err := newSignedMachineTestTransport(HTTPOptions{Endpoint: "https://resource.example/mcp", ClientInfo: ClientInfo{Name: "host", Version: "1"}}, credentials)
			require.Error(t, err)
		})
	}
	// Registration may permit a longer assertion; there is no framework maximum
	// that turns a per-assertion period into an operation or run lifetime rule.
	_, err = newSignedMachineTestTransport(HTTPOptions{Endpoint: "https://resource.example/mcp", ClientInfo: ClientInfo{Name: "host", Version: "1"}}, ClientAssertion{Issuer: "https://issuer.example", ClientID: "registered", AssertionIssuer: "registered-signer", Audience: "registered-audience", Lifetime: 24 * time.Hour, Signer: signer})
	require.NoError(t, err)
}

// Sign returns the controlled failure or completes the original signing call
// after the test's cancellation or delay has occurred.
func (s *controlledAssertionSigner) Sign(payload []byte) (*jose.JSONWebSignature, error) {
	if s.failure != nil {
		return nil, s.failure
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.Signer.Sign(payload)
}

// advertiseAssertion declares only signed machine authentication and the
// selected algorithms so a successful grant cannot depend on secret fallback.
func advertiseAssertion(peer *oauthPeer, algorithms ...string) {
	values := make([]string, len(algorithms))
	for index, algorithm := range algorithms {
		values[index] = fmt.Sprintf("%q", algorithm)
	}
	peer.issuerBody = fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q,"grant_types_supported":["client_credentials"],"token_endpoint_auth_methods_supported":["private_key_jwt"],"token_endpoint_auth_signing_alg_values_supported":[%s]}`, peer.issuer, peer.server.URL+"/token/a%2Fb?route=selected", strings.Join(values, ","))
}

// assertionRegistration supplies intentionally distinct registration identities
// so tests reject deriving the assertion issuer or audience from discovery.
func assertionRegistration(peer *oauthPeer, signer jose.Signer) ClientAssertion {
	return ClientAssertion{Issuer: peer.issuer, ClientID: "registered-client", AssertionIssuer: "registered-signing-entity", Audience: "https://authorization.example/identity", Lifetime: time.Minute, Signer: signer}
}

// verifyAssertionForm checks the native form and cryptographically verifies
// registered claims before the synthetic issuer can return an access token.
func verifyAssertionForm(form url.Values, resource string, registration ClientAssertion, key any, algorithm jose.SignatureAlgorithm) (jwt.Claims, error) {
	var claims jwt.Claims
	if len(form) != 5 || form.Get("grant_type") != "client_credentials" || form.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" || form.Get("resource") != resource || form.Get("scope") != "records:read" {
		return claims, errors.New("wrong assertion grant form")
	}
	token, err := jwt.ParseSigned(form.Get("client_assertion"), []jose.SignatureAlgorithm{algorithm})
	if err != nil {
		return claims, err
	}
	if err := token.Claims(key, &claims); err != nil {
		return claims, err
	}
	if claims.Issuer != registration.AssertionIssuer || claims.Subject != registration.ClientID || len(claims.Audience) != 1 || claims.Audience[0] != registration.Audience || claims.ID == "" || claims.IssuedAt == nil || claims.Expiry == nil || !time.Now().Before(claims.Expiry.Time()) {
		return claims, errors.New("wrong registered assertion claims")
	}
	return claims, nil
}

// newSignedMachineTestTransport constructs registration separately from its grant,
// keeping the real factory errors visible to configuration boundary tests.
func newSignedMachineTestTransport(opts HTTPOptions, config ClientAssertion) (*HTTPTransport, error) {
	registration, err := NewSignedClientRegistration(config)
	if err != nil {
		return nil, err
	}
	return NewClientCredentialsHTTPTransport(opts, ClientCredentials{Registration: registration, Scopes: []string{"records:read"}})
}
