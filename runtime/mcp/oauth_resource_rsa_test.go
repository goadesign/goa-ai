// These tests sign real RSA access tokens around the JWT algorithm boundaries.
// A constraint applies to each public key or each PSS signature independently;
// valid keys and signatures remain valid when the resource has multiple keys.
package mcp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// resourcePSSSaltSigner creates an exact salt length through JOSE's signer
	// extension. Test signatures must reach the resource verifier unchanged.
	resourcePSSSaltSigner struct {
		key       *rsa.PrivateKey
		algorithm jose.SignatureAlgorithm
		hash      crypto.Hash
		salt      int
	}
)

func TestJWTResourceRSAKeySize(t *testing.T) {
	var keys []jose.JSONWebKey
	for _, bits := range []int{1024, 2047, 2048, 2049} {
		key, err := rsa.GenerateKey(rand.Reader, bits)
		require.NoError(t, err)
		assert.Equal(t, bits, key.N.BitLen())
		verifier, err := newJWTResourceVerifier("https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{{Key: &key.PublicKey}},
		}, []jose.SignatureAlgorithm{jose.RS256})
		if bits < 2048 {
			require.ErrorContains(t, err, "at least 2048 bits")
			assert.Nil(t, verifier)
			continue
		}
		require.NoError(t, err)
		_, err = verifier.verify(t.Context(), signResourceJWT(t, key, jose.RS256, "", "at+jwt", resourceClaimsDocument), time.Unix(1850000000, 0))
		require.NoError(t, err)
		keys = append(keys, jose.JSONWebKey{Key: &key.PublicKey})
	}
	_, err := newJWTResourceVerifier("https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{Keys: keys}, []jose.SignatureAlgorithm{jose.RS256})
	assert.NoError(t, err)
}

func TestJWTResourcePSSSaltLength(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	for _, tc := range []struct {
		algorithm jose.SignatureAlgorithm
		hash      crypto.Hash
	}{
		{jose.PS256, crypto.SHA256},
		{jose.PS384, crypto.SHA384},
		{jose.PS512, crypto.SHA512},
	} {
		t.Run(string(tc.algorithm), func(t *testing.T) {
			verifier, err := newJWTResourceVerifier("https://issuer.example", "https://resource.example/mcp", jose.JSONWebKeySet{
				Keys: []jose.JSONWebKey{{Key: &key.PublicKey}},
			}, []jose.SignatureAlgorithm{tc.algorithm})
			require.NoError(t, err)
			for _, salt := range []int{tc.hash.Size() - 1, tc.hash.Size(), tc.hash.Size() + 1} {
				signer := resourcePSSSaltSigner{key: key, algorithm: tc.algorithm, hash: tc.hash, salt: salt}
				token := signResourceJWT(t, signer, tc.algorithm, "", "at+jwt", resourceClaimsDocument)
				claims, err := verifier.verify(t.Context(), token, time.Unix(1850000000, 0))
				if salt == tc.hash.Size() {
					require.NoError(t, err)
					assert.Equal(t, "subject", claims.Sub)
				} else {
					require.ErrorIs(t, err, errInvalidAccessToken)
					assert.Nil(t, claims)
				}
			}
		})
	}
}

// Public returns the public part of this synthetic signing key to the SDK.
func (s resourcePSSSaltSigner) Public() *jose.JSONWebKey {
	return &jose.JSONWebKey{Key: &s.key.PublicKey}
}

// Algs limits the synthetic signer to its declared test algorithm.
func (s resourcePSSSaltSigner) Algs() []jose.SignatureAlgorithm {
	return []jose.SignatureAlgorithm{s.algorithm}
}

// SignPayload signs JOSE's supplied bytes with the selected test salt length.
func (s resourcePSSSaltSigner) SignPayload(payload []byte, algorithm jose.SignatureAlgorithm) ([]byte, error) {
	if algorithm != s.algorithm {
		return nil, jose.ErrUnsupportedAlgorithm
	}
	digest := s.hash.New()
	if _, err := digest.Write(payload); err != nil {
		return nil, err
	}
	return rsa.SignPSS(rand.Reader, s.key, s.hash, digest.Sum(nil), &rsa.PSSOptions{SaltLength: s.salt})
}
