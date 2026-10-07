// These HTTPS peers verify that application registration composes across grants
// without sharing resource tokens or substituting another authentication method.
package mcp

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientRegistrationRejectsInvalidHostConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, issuer, clientID, secret string }{
		{"insecure issuer", "http://issuer.example", "registered", "secret"},
		{"issuer query", "https://issuer.example?", "registered", "secret"},
		{"issuer fragment", "https://issuer.example#", "registered", "secret"},
		{"missing client", "https://issuer.example", "", "secret"},
		{"missing secret", "https://issuer.example", "registered", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewBasicClientRegistration(tc.issuer, tc.clientID, tc.secret)
			require.Error(t, err)
			_, err = NewSecretClientRegistration(tc.issuer, tc.clientID, tc.secret)
			require.Error(t, err)
		})
	}
	for _, registration := range []*ClientRegistration{nil, {}} {
		_, err := NewClientCredentialsHTTPTransport(HTTPOptions{Endpoint: "https://resource.example/mcp"}, ClientCredentials{Store: NewMemoryAuthorizationStore(), Registration: registration})
		require.Error(t, err)
		_, err = NewAuthorizationCodeHTTPTransport(HTTPOptions{Endpoint: "https://resource.example/mcp"}, AuthorizationCode{Store: NewMemoryAuthorizationStore(), Registration: registration})
		require.Error(t, err)
	}
	public, err := NewPublicClientRegistration("https://issuer.example", "registered")
	require.NoError(t, err)
	_, err = NewClientCredentialsHTTPTransport(HTTPOptions{Endpoint: "https://resource.example/mcp"}, ClientCredentials{Store: NewMemoryAuthorizationStore(), Registration: public})
	assert.ErrorContains(t, err, "confidential")
}

func TestClientRegistrationMachineSecretProfilesAndTokenIsolation(t *testing.T) {
	for _, method := range []string{"client_secret_basic", "client_secret_post"} {
		t.Run(method, func(t *testing.T) {
			peer := newOAuthPeer(t)
			peer.issuerBody = strings.Replace(peer.issuerBody, `"client_secret_basic","client_secret_post"`, `"`+method+`"`, 1)
			constructor := NewBasicClientRegistration
			if method == "client_secret_post" {
				constructor = NewSecretClientRegistration
			}
			registration, err := constructor(peer.issuer, "registered:client +", "private:secret +")
			require.NoError(t, err)
			peer.verifyAuthentication = func(request *http.Request) error {
				if method == "client_secret_basic" {
					return verifyRegistrationBasic(request, registration)
				}
				assert.Empty(t, request.Header.Get("Authorization"))
				return nil
			}
			peer.verifyGrant = func(form url.Values) error {
				assert.Equal(t, "client_credentials", form.Get("grant_type"))
				assert.Equal(t, peer.resource, form.Get("resource"))
				if method == "client_secret_basic" {
					assert.NotContains(t, form, "client_id")
					assert.NotContains(t, form, "client_secret")
				} else {
					assert.Equal(t, registration.clientID, form.Get("client_id"))
					assert.Equal(t, registration.secret, form.Get("client_secret"))
				}
				return nil
			}
			for range 2 {
				transport, err := NewClientCredentialsHTTPTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, ClientCredentials{Store: NewMemoryAuthorizationStore(), Registration: registration})
				require.NoError(t, err)
				for range 2 {
					require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
				}
			}
			assert.EqualValues(t, 2, peer.tokenCalls.Load())
			assert.EqualValues(t, 4, peer.mcpCalls.Load())
		})
	}
}

func TestClientRegistrationBrowserSecretProfilesAndRefresh(t *testing.T) {
	for _, method := range []string{"client_secret_basic", "client_secret_post"} {
		t.Run(method, func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			peer.clientID = "registered:client +"
			peer.issuerBody = strings.Replace(peer.issuerBody, `"none"`, `"`+method+`"`, 1)
			constructor := NewBasicClientRegistration
			if method == "client_secret_post" {
				constructor = NewSecretClientRegistration
			}
			registration, err := constructor(peer.issuer, peer.clientID, "private:secret +")
			require.NoError(t, err)
			peer.verifyClient = func(request *http.Request) error {
				if method == "client_secret_basic" {
					if err := verifyRegistrationBasic(request, registration); err != nil {
						return err
					}
					assert.NotContains(t, request.PostForm, "client_id")
					assert.NotContains(t, request.PostForm, "client_secret")
				} else {
					assert.Empty(t, request.Header.Get("Authorization"))
					assert.Equal(t, registration.clientID, request.PostForm.Get("client_id"))
					assert.Equal(t, registration.secret, request.PostForm.Get("client_secret"))
				}
				return nil
			}
			peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh"}`
			transport, err := NewAuthorizationCodeHTTPTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, AuthorizationCode{Store: NewMemoryAuthorizationStore(), Registration: registration, RedirectURI: "https://host.example/callback/a%2Fb?route=selected", Authorize: peer.authorize(t)})
			require.NoError(t, err)
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			setOAuthCredentialTime(t, transport, time.Now().Add(-2 * time.Hour))
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			assert.EqualValues(t, 1, peer.hostCalls.Load())
			assert.EqualValues(t, 2, peer.tokenCalls.Load())
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			require.Len(t, peer.forms, 2)
			assert.Equal(t, "refresh_token", peer.forms[1].Get("grant_type"))
			assert.Equal(t, "private-refresh", peer.forms[1].Get("refresh_token"))
		})
	}
}

// verifyRegistrationBasic checks separately encoded credentials before the peer
// parses its generated form. The header must identify the exact registration.
func verifyRegistrationBasic(request *http.Request, registration *ClientRegistration) error {
	identity, secret, ok := request.BasicAuth()
	if !ok || identity != url.QueryEscape(registration.clientID) || secret != url.QueryEscape(registration.secret) {
		return errors.New("wrong registered Basic credentials")
	}
	return nil
}
