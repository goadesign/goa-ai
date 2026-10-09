// These tests exercise HTTP authentication grammar at the untrusted header
// boundary. Separate schemes and realms must never become combined permissions,
// and malformed server values must not enter diagnostics or credential requests.
package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuthChallenges(t *testing.T) {
	cases := []struct {
		name   string
		fields []string
		count  int
		scope  []string
		url    string
	}{
		{"none", nil, 0, nil, ""},
		{"bare", []string{"Bearer"}, 1, nil, ""},
		{"case", []string{`bEaReR RESOURCE_METADATA="https://resource.example/custom/a%2Fb?tenant=x", ScOpE="read write"`}, 1, []string{"read", "write"}, "https://resource.example/custom/a%2Fb?tenant=x"},
		{"separate schemes", []string{`Basic realm="one,two", Bearer scope="read", Digest realm="three", nonce="a,b"`}, 1, []string{"read"}, ""},
		{"separate fields", []string{`Negotiate YWJjZA==`, `Bearer scope="read"`}, 1, []string{"read"}, ""},
		{"two realms", []string{`Bearer realm="one", scope="read", Bearer realm="two", scope="write"`}, 2, []string{"read"}, ""},
		{"quoted escapes", []string{`Basic realm="a\"b\\c", Bearer scope="read"`}, 1, []string{"read"}, ""},
		{"empty list members", []string{`, , Bearer scope = "read",,`}, 1, []string{"read"}, ""},
		{"punctuation", []string{`Bearer scope="urn:example:rating=G,PG-13 read!#$%&'()*+,-./:;<=>?@[]^_` + "`" + `{|}~"`}, 1, []string{"urn:example:rating=G,PG-13", "read!#$%&'()*+,-./:;<=>?@[]^_`{|}~"}, ""},
		{"unknown extension", []string{`Bearer extension="arbitrary,\"value", scope=read`}, 1, []string{"read"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			challenges, err := oauthChallenges(tc.fields)
			require.NoError(t, err)
			require.Len(t, challenges, tc.count)
			if tc.count == 0 {
				return
			}
			assert.Equal(t, tc.scope, challenges[0].scopes)
			if tc.url == "" {
				assert.Nil(t, challenges[0].metadata)
			} else {
				require.NotNil(t, challenges[0].metadata)
				assert.Equal(t, tc.url, challenges[0].metadata.String())
			}
		})
	}
}

func TestOAuthChallengesRejectMalformedHeaders(t *testing.T) {
	for _, field := range []string{
		`Bearer scope="private-canary`,
		`Bearer scope="private-canary", SCOPE="write"`,
		`Bearer scope=""`,
		`Bearer scope="read  private-canary"`,
		`Bearer scope=" read"`,
		`Bearer scope="read\"private-canary"`,
		`Bearer realm="private-canary" scope="read"`,
		`Bearer resource_metadata="http://private-canary.example/metadata"`,
		`Bearer resource_metadata="https://private-canary.example/metadata#fragment"`,
		`Bearer resource_metadata="https://secret@private-canary.example/metadata"`,
		`Bearer resource_metadata="https://private-canary.example/unescaped path"`,
		`Bearer resource_metadata=""`,
		`Bearer private-canary==`,
		`Bearer error="private-canary\"invalid"`,
		"Bearer scope=\"private-canary\n\"",
		"Bearer\tscope=private-canary",
		`Basic realm="private-canary`,
		`Bearer realm=private-canary, Digest nonce="invalid`,
	} {
		t.Run(field, func(t *testing.T) {
			challenges, err := oauthChallenges([]string{field})
			require.Error(t, err)
			assert.Nil(t, challenges)
			assert.NotContains(t, err.Error(), "private-canary")
		})
	}
}
