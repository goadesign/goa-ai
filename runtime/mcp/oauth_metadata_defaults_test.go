// These tests send issuer metadata through the generated HTTPS decoder and
// then perform a registered machine grant. RFC 8414 gives an omitted token
// authentication list one exact meaning; explicit lists keep their own meaning.
package mcp

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuthIssuerAuthenticationDefault(t *testing.T) {
	for _, test := range []struct {
		name    string
		methods string
		post    bool
		want    string
	}{
		{name: "omitted allows Basic"},
		{name: "explicit Basic", methods: `,"token_endpoint_auth_methods_supported":["client_secret_basic"]`},
		{name: "explicit empty denies Basic", methods: `,"token_endpoint_auth_methods_supported":[]`, want: "registered authentication"},
		{name: "explicit public denies Basic", methods: `,"token_endpoint_auth_methods_supported":["none"]`, want: "registered authentication"},
		{name: "omitted denies POST secret", post: true, want: "registered authentication"},
		{name: "explicit null rejected", methods: `,"token_endpoint_auth_methods_supported":null`, want: "issuer metadata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := newOAuthPeer(t)
			peer.issuerBody = fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q,"grant_types_supported":["client_credentials"]%s}`, peer.issuer, peer.server.URL+"/token/a%2Fb?route=selected", test.methods)
			construct := NewBasicClientRegistration
			if test.post {
				construct = NewSecretClientRegistration
			}
			registration, err := construct(peer.issuer, "client", "secret")
			require.NoError(t, err)
			if !test.post {
				peer.verifyAuthentication = func(request *http.Request) error {
					return verifyRegistrationBasic(request, registration)
				}
				peer.verifyGrant = func(form url.Values) error {
					assert.NotContains(t, form, "client_id")
					assert.NotContains(t, form, "client_secret")
					return nil
				}
			}
			transport, err := NewClientCredentialsHTTPTransport(HTTPOptions{Endpoint: peer.resource, Client: peer.server.Client(), ClientInfo: ClientInfo{Name: "host", Version: "1"}}, ClientCredentials{
				Registration: registration,
				Store:        NewMemoryAuthorizationStore(),
				Scopes:       []string{"records:read"},
			})
			require.NoError(t, err)
			err = callOAuthPeer(t.Context(), transport, peer.resource)
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
				assert.Zero(t, peer.tokenCalls.Load())
				assert.Zero(t, peer.mcpCalls.Load())
				return
			}
			require.NoError(t, err)
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
			assert.EqualValues(t, 1, peer.mcpCalls.Load())
		})
	}
}
