// These tests sign synthetic access tokens and check the complete private
// signature-to-generated-decoder path. No test token or key is a live credential.
package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const resourceClaimsDocument = `{"iss":"https://issuer.example","sub":"subject","aud":"https://resource.example/mcp","exp":1900000000.75,"iat":1800000000.5,"jti":"synthetic-token","client_id":"registered-client","scope":"records:read records:write","extension":null}`

func TestJWTResourceVerifiesSignedClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	verifier, err := newJWTResourceVerifier("https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "trusted", Algorithm: "RS256"}},
	}, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err)
	now := time.Unix(1900000000, 500000000)
	cases := []struct {
		name, document string
		valid          bool
	}{
		{"standard", resourceClaimsDocument, true},
		{"several audiences", strings.Replace(resourceClaimsDocument, `"aud":"https://resource.example/mcp"`, `"aud":["https://other.example","https://resource.example/mcp"]`, 1), true},
		{"empty scopes allowed", strings.Replace(resourceClaimsDocument, `,"scope":"records:read records:write"`, "", 1), true},
		{"exact not before", strings.Replace(resourceClaimsDocument, `,"extension":null`, `,"nbf":1900000000.5`, 1), true},
		{"future not before", strings.Replace(resourceClaimsDocument, `,"extension":null`, `,"nbf":1900000000.501`, 1), false},
		{"already expired", strings.Replace(resourceClaimsDocument, "1900000000.75", "1900000000.49", 1), false},
		{"exact expiry", strings.Replace(resourceClaimsDocument, "1900000000.75", "1900000000.5", 1), false},
		{"wrong issuer", strings.Replace(resourceClaimsDocument, "https://issuer.example", "https://other.example", 1), false},
		{"wrong audience", strings.Replace(resourceClaimsDocument, "https://resource.example/mcp", "https://other.example", 1), false},
		{"empty audience", strings.Replace(resourceClaimsDocument, `"aud":"https://resource.example/mcp"`, `"aud":[]`, 1), false},
		{"number audience", strings.Replace(resourceClaimsDocument, `"aud":"https://resource.example/mcp"`, `"aud":123`, 1), false},
		{"mixed audience", strings.Replace(resourceClaimsDocument, `"aud":"https://resource.example/mcp"`, `"aud":["https://resource.example/mcp",123]`, 1), false},
		{"null audience element", strings.Replace(resourceClaimsDocument, `"aud":"https://resource.example/mcp"`, `"aud":["https://resource.example/mcp",null]`, 1), false},
		{"duplicate issuer", strings.Replace(resourceClaimsDocument, `"iss":"https://issuer.example"`, `"iss":"https://other.example","iss":"https://issuer.example"`, 1), false},
		{"duplicate extension", strings.Replace(resourceClaimsDocument, `"extension":null`, `"extension":null,"extension":true`, 1), false},
		{"unknown case cannot supply issuer", strings.Replace(resourceClaimsDocument, `"iss":`, `"ISS":`, 1), false},
		{"case distinct extension", strings.Replace(resourceClaimsDocument, `"extension":null`, `"ISS":"another issuer"`, 1), true},
		{"invalid scopes", strings.Replace(resourceClaimsDocument, "records:read records:write", "records:read  records:write", 1), false},
		{"null optional scopes", strings.Replace(resourceClaimsDocument, `"scope":"records:read records:write"`, `"scope":null`, 1), false},
		{"null audience", strings.Replace(resourceClaimsDocument, `"aud":"https://resource.example/mcp"`, `"aud":null`, 1), false},
		{"trailing document", resourceClaimsDocument + " {}", false},
		{"invalid UTF-8", strings.Replace(resourceClaimsDocument, "subject", string([]byte{0xff}), 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := signResourceJWT(t, key, jose.RS256, "trusted", "at+jwt", tc.document)
			claims, err := verifier.verify(t.Context(), token, now)
			if !tc.valid {
				require.ErrorIs(t, err, errInvalidAccessToken)
				assert.Nil(t, claims)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "subject", claims.Sub)
			assert.InDelta(t, 1800000000.5, claims.Iat, 0)
		})
	}
	for _, field := range []string{
		`"iss":"https://issuer.example",`, `"sub":"subject",`,
		`"aud":"https://resource.example/mcp",`, `"exp":1900000000.75,`,
		`"iat":1800000000.5,`, `"jti":"synthetic-token",`,
		`"client_id":"registered-client",`,
	} {
		t.Run("missing "+strings.Split(field, ":")[0], func(t *testing.T) {
			document := strings.Replace(resourceClaimsDocument, field, "", 1)
			token := signResourceJWT(t, key, jose.RS256, "trusted", "at+jwt", document)
			claims, err := verifier.verify(t.Context(), token, now)
			require.ErrorIs(t, err, errInvalidAccessToken)
			assert.Nil(t, claims)
		})
	}
	for _, tokenType := range []string{"at+jwt", "application/at+jwt", "at+JWT"} {
		t.Run(tokenType, func(t *testing.T) {
			_, err := verifier.verify(t.Context(), signResourceJWT(t, key, jose.RS256, "trusted", tokenType, resourceClaimsDocument), now)
			assert.NoError(t, err)
		})
	}
	for _, tokenType := range []string{"JWT", "id+jwt", "oauth-id-jag+jwt", ""} {
		t.Run("wrong purpose "+tokenType, func(t *testing.T) {
			_, err := verifier.verify(t.Context(), signResourceJWT(t, key, jose.RS256, "trusted", tokenType, resourceClaimsDocument), now)
			assert.ErrorIs(t, err, errInvalidAccessToken)
		})
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	for _, token := range []string{
		signResourceJWT(t, other, jose.RS256, "trusted", "at+jwt", resourceClaimsDocument),
		signResourceJWT(t, key, jose.RS256, "unknown", "at+jwt", resourceClaimsDocument),
		signResourceJWT(t, key, jose.PS256, "trusted", "at+jwt", resourceClaimsDocument),
		"not-a-token",
	} {
		_, err := verifier.verify(t.Context(), token, now)
		require.ErrorIs(t, err, errInvalidAccessToken)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = verifier.verify(ctx, signResourceJWT(t, key, jose.RS256, "trusted", "at+jwt", resourceClaimsDocument), now)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestJWTResourceCopiesTrustedKeys(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keys := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public, KeyID: "trusted", Algorithm: "EdDSA"}}}
	algorithms := []jose.SignatureAlgorithm{jose.EdDSA}
	verifier, err := newJWTResourceVerifier("https://issuer.example", "https://resource.example/mcp", keys, algorithms)
	require.NoError(t, err)
	clear(public)
	keys.Keys[0].KeyID = "changed"
	algorithms[0] = jose.HS256
	_, err = verifier.verify(t.Context(), signResourceJWT(t, private, jose.EdDSA, "trusted", "at+jwt", resourceClaimsDocument), time.Unix(1850000000, 0))
	assert.NoError(t, err)
}

func TestJWTResourceAsymmetricAlgorithms(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	edPublic, edPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	for _, tc := range []struct {
		algorithm       jose.SignatureAlgorithm
		public, private any
	}{
		{jose.RS256, &rsaKey.PublicKey, rsaKey},
		{jose.PS256, &rsaKey.PublicKey, rsaKey},
		{jose.ES256, &ecKey.PublicKey, ecKey},
		{jose.EdDSA, edPublic, edPrivate},
	} {
		t.Run(string(tc.algorithm), func(t *testing.T) {
			verifier, err := newJWTResourceVerifier("https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{
				Keys: []jose.JSONWebKey{{Key: tc.public, Algorithm: string(tc.algorithm)}},
			}, []jose.SignatureAlgorithm{tc.algorithm})
			require.NoError(t, err)
			_, err = verifier.verify(t.Context(), signResourceJWT(t, tc.private, tc.algorithm, "", "at+jwt", resourceClaimsDocument), time.Unix(1850000000, 0))
			assert.NoError(t, err)
		})
	}
}

func TestJWTResourceRejectsUnsafeConfiguration(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, issuer, resource string
		keys                   jose.JSONWebKeySet
		algorithms             []jose.SignatureAlgorithm
	}{
		{"no keys", "https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{}, []jose.SignatureAlgorithm{jose.EdDSA}},
		{"no algorithms", "https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public}}}, nil},
		{"shared MAC", "https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public}}}, []jose.SignatureAlgorithm{jose.HS256}},
		{"private key", "https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: private}}}, []jose.SignatureAlgorithm{jose.EdDSA}},
		{"encryption key", "https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public, Use: "enc"}}}, []jose.SignatureAlgorithm{jose.EdDSA}},
		{"wrong key algorithm", "https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public, Algorithm: "RS256"}}}, []jose.SignatureAlgorithm{jose.EdDSA}},
		{"issuer query", "https://issuer.example?tenant=blue", "https://resource.example/mcp", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public}}}, []jose.SignatureAlgorithm{jose.EdDSA}},
		{"insecure resource", "https://issuer.example", "http://resource.example/mcp", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public}}}, []jose.SignatureAlgorithm{jose.EdDSA}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newJWTResourceVerifier(tc.issuer, tc.resource, tc.keys, tc.algorithms)
			assert.Error(t, err)
		})
	}
}

// signResourceJWT signs exact test bytes so malformed JSON still has a valid
// signature. Verification must reject the claims rather than repair their shape.
func signResourceJWT(t *testing.T, key any, algorithm jose.SignatureAlgorithm, keyID, tokenType, document string) string {
	t.Helper()
	options := (&jose.SignerOptions{}).WithType(jose.ContentType(tokenType))
	if keyID != "" {
		options.WithHeader(jose.HeaderKey("kid"), keyID)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: algorithm, Key: key}, options)
	require.NoError(t, err)
	signed, err := signer.Sign([]byte(document))
	require.NoError(t, err)
	token, err := signed.CompactSerialize()
	require.NoError(t, err)
	return token
}
