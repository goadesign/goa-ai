// These HTTPS peers distinguish the fixed MCP request address from the token
// audience selected by validated resource metadata. Grants, saved permissions
// and refresh credentials remain isolated when the audience changes.
package mcp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
)

func TestAuthorizationCodeOriginAudienceStorageAndRefresh(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.missingMetadata = true
	peer.resourceID = peer.server.URL
	peer.rootMetadata = fmt.Sprintf(`{"resource":%q,"authorization_servers":[%q],"scopes_supported":["records:read"]}`, peer.resourceID, peer.issuer)
	peer.initialChallenge = `Bearer scope="records:read"`
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh","scope":"records:read records:write"}`
	store := NewMemoryAuthorizationStore()
	first := peer.transportWithStore(t, peer.authorize(t), store)
	require.NoError(t, callOAuthPeer(t.Context(), first, peer.resource))
	restarted := peer.transportWithStore(t, peer.authorize(t), store)
	require.NoError(t, callOAuthPeer(t.Context(), restarted, peer.resource))
	assert.EqualValues(t, 1, peer.hostCalls.Load())
	assert.EqualValues(t, 1, peer.tokenCalls.Load())

	// Expire only the saved origin grant. A restarted client must refresh that
	// grant with the same audience, without sending the token to the origin URL.
	binding := first.authorization.grant.credentialBindings(peer.resourceID)[0]
	require.NoError(t, withTestAuthorizationCredential(t.Context(), store, binding, func(record AuthorizationCredential) error {
		data, exists, err := record.Load()
		require.NoError(t, err)
		require.True(t, exists)
		state, err := genaccesstokens.DecodeResourceCredentialState(data)
		require.NoError(t, err)
		ready, available := state.State.AsReady()
		require.True(t, available)
		ready.Obtained = time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
		state.State = genaccesstokens.NewStateReady(ready)
		data, err = genaccesstokens.EncodeResourceCredentialState(state)
		require.NoError(t, err)
		return record.Save(data)
	}))
	require.NoError(t, callOAuthPeer(t.Context(), restarted, peer.resource))
	assert.EqualValues(t, 1, peer.hostCalls.Load())
	assert.EqualValues(t, 2, peer.tokenCalls.Load())

	// Endpoint metadata now identifies a separate token audience. Its first
	// consent cannot copy the origin's extra granted permission or refresh token.
	peer.missingMetadata = false
	peer.resourceID = peer.resource
	require.NoError(t, callOAuthPeer(t.Context(), restarted, peer.resource))
	assert.EqualValues(t, 2, peer.hostCalls.Load())
	assert.EqualValues(t, 3, peer.tokenCalls.Load())
	assert.EqualValues(t, 4, peer.mcpCalls.Load())
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	require.Len(t, peer.forms, 3)
	assert.Equal(t, peer.server.URL, peer.forms[0].Get("resource"))
	assert.Equal(t, peer.server.URL, peer.forms[1].Get("resource"))
	assert.Equal(t, "refresh_token", peer.forms[1].Get("grant_type"))
	assert.Equal(t, peer.resource, peer.forms[2].Get("resource"))
	assert.Equal(t, "authorization_code", peer.forms[2].Get("grant_type"))
	assert.Empty(t, peer.forms[2].Get("refresh_token"))
	require.Len(t, peer.requests, 2)
	assert.Equal(t, "records:read", peer.requests[1].Get("scope"))
}

func TestOAuthMetadataAudienceMatchesItsLocation(t *testing.T) {
	for _, test := range []struct {
		name, replacement string
		root              bool
	}{
		{name: "endpoint document cannot claim origin"},
		{name: "root document cannot claim endpoint", root: true, replacement: "/mcp/a%2Fb?tenant=blue"},
		{name: "root document cannot claim sibling", root: true, replacement: "/sibling"},
		{name: "root document cannot claim another origin", root: true, replacement: "https://other.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := newOAuthPeer(t)
			identifier := peer.server.URL + test.replacement
			if strings.HasPrefix(test.replacement, "https://") {
				identifier = test.replacement
			}
			peer.metadata = strings.Replace(peer.metadata, peer.resource, identifier, 1)
			if test.root {
				peer.missingPaths = []string{"/.well-known/oauth-protected-resource/mcp/a%2Fb"}
			}
			transport := peer.transport(t, "client", "secret", nil)
			require.ErrorContains(t, callOAuthPeer(t.Context(), transport, peer.resource), "does not bind")
			assert.Zero(t, peer.tokenCalls.Load())
			assert.Zero(t, peer.mcpCalls.Load())
		})
	}
}
