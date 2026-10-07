// Package mcp enforces the JWT algorithm parameters for resource RSA keys.
// JOSE supplies the exact signed bytes and signature to its verifier extension;
// Go's cryptographic primitives verify them. This adapter requires the PSS salt
// length specified by RFC 7518, which the SDK's default verifier does not enforce.
package mcp

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"

	"github.com/go-jose/go-jose/v4"
)

type (
	// resourceRSAVerifier checks signatures with one trusted public key. It does
	// not select keys or decode claims; the resource owner and JOSE do that.
	resourceRSAVerifier struct {
		key *rsa.PublicKey
	}
)

// VerifyPayload receives JOSE's signing input, signature and selected algorithm.
// It uses the required SHA-2 hash and accepts RSA-PSS only with a salt equal to
// that hash's length. Unsupported algorithms and invalid signatures fail.
func (v resourceRSAVerifier) VerifyPayload(payload, signature []byte, algorithm jose.SignatureAlgorithm) error {
	var (
		hash   crypto.Hash
		digest []byte
	)
	switch algorithm {
	case jose.RS256, jose.PS256:
		value := sha256.Sum256(payload)
		hash, digest = crypto.SHA256, value[:]
	case jose.RS384, jose.PS384:
		value := sha512.Sum384(payload)
		hash, digest = crypto.SHA384, value[:]
	case jose.RS512, jose.PS512:
		value := sha512.Sum512(payload)
		hash, digest = crypto.SHA512, value[:]
	case jose.EdDSA, jose.HS256, jose.HS384, jose.HS512, jose.ES256, jose.ES384, jose.ES512:
		return jose.ErrUnsupportedAlgorithm
	default:
		return jose.ErrUnsupportedAlgorithm
	}
	if algorithm == jose.PS256 || algorithm == jose.PS384 || algorithm == jose.PS512 {
		return rsa.VerifyPSS(v.key, hash, digest, signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	}
	return rsa.VerifyPKCS1v15(v.key, hash, digest, signature)
}
