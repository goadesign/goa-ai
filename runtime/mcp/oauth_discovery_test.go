// These HTTPS peers publish OAuth metadata through challenge-only addresses.
// The same generated decoder must bind resource and issuer before any secret
// exchange, without probing a domain tool or broadening requested permissions.
package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientCredentialsChallengeOnlyDiscovery(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		t.Run(fmt.Sprintf("alternate-realm=%t", alternate), func(t *testing.T) {
			peer := newOAuthPeer(t)
			peer.missingPaths = []string{"/.well-known/oauth-protected-resource/mcp/a%2Fb", "/.well-known/oauth-protected-resource"}
			address := peer.server.URL + "/challenge-metadata/a%2Fb?chosen=blue"
			peer.challenges = []string{`Basic realm="legacy,realm", Bearer resource_metadata="` + address + `", scope="records:read records:write"`}
			if alternate {
				peer.challenges = append([]string{`Bearer realm="other", scope="unrelated"`}, peer.challenges...)
			}
			transport := peer.transport(t, "client", "secret", []string{"records:read"})
			require.NoError(t, callOAuthPeer(t.Context(), transport, peer.resource))
			assert.EqualValues(t, 1, peer.probeCalls.Load())
			assert.EqualValues(t, 2, peer.mcpCalls.Load())
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
			peer.mutex.Lock()
			defer peer.mutex.Unlock()
			assert.Equal(t, []string{
				"/.well-known/oauth-protected-resource/mcp/a%2Fb?tenant=blue",
				"/.well-known/oauth-protected-resource", "/mcp/a%2Fb?tenant=blue",
				"/challenge-metadata/a%2Fb?chosen=blue", "/.well-known/oauth-authorization-server/tenant/a%2Fb",
				"/token/a%2Fb?route=selected", "/mcp/a%2Fb?tenant=blue",
			}, peer.addresses)
			assert.Equal(t, "records:read", peer.forms[0].Get("scope"))
		})
	}
}

func TestClientCredentialsRejectsChallengeDiscoveryBeforeGrant(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*oauthPeer)
	}{
		{"missing Bearer", func(p *oauthPeer) { p.challenges = []string{`Basic realm="other"`} }},
		{"missing URL", func(p *oauthPeer) { p.challenges = []string{`Bearer realm="owner"`} }},
		{"insecure URL", func(p *oauthPeer) {
			p.challenges = []string{`Bearer resource_metadata="http://untrusted.example/metadata"`}
		}},
		{"malformed", func(p *oauthPeer) { p.challenges = []string{`Bearer resource_metadata="unterminated`} }},
		{"duplicate", func(p *oauthPeer) { p.challenges = []string{`Bearer scope="read", SCOPE="write"`} }},
		{"wrong resource", func(p *oauthPeer) { p.metadata = strings.Replace(p.metadata, p.resource, p.server.URL+"/another", 1) }},
		{"wrong issuer", func(p *oauthPeer) { p.metadata = strings.Replace(p.metadata, p.issuer, p.server.URL+"/another", 1) }},
		{"missing challenged document", func(p *oauthPeer) { p.missingPaths = append(p.missingPaths, "/challenge-metadata/a%2Fb") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newOAuthPeer(t)
			peer.missingPaths = []string{"/.well-known/oauth-protected-resource/mcp/a%2Fb", "/.well-known/oauth-protected-resource"}
			peer.challenges = []string{`Bearer resource_metadata="` + peer.server.URL + `/challenge-metadata/a%2Fb"`}
			tc.configure(peer)
			transport := peer.transport(t, "client", "secret", nil)
			require.Error(t, callOAuthPeer(t.Context(), transport, peer.resource))
			assert.EqualValues(t, 1, peer.probeCalls.Load())
			assert.EqualValues(t, 1, peer.mcpCalls.Load())
			assert.Zero(t, peer.tokenCalls.Load())
		})
	}
}

func TestChallengeDiscoveryKeepsCancellation(t *testing.T) {
	peer := newOAuthPeer(t)
	resource, err := authorizationURL(peer.resource, false)
	require.NoError(t, err)
	issuer, err := authorizationURL(peer.issuer, true)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = discoverProtectedResource(ctx, peer.server.Client(), resource, issuer, ClientInfo{Name: "host", Version: "1"})
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, peer.tokenCalls.Load())
	assert.Zero(t, peer.mcpCalls.Load())
}
