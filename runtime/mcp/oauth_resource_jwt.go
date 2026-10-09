// Package mcp verifies signed OAuth access tokens before MCP dispatch. The host
// supplies trusted public keys, issuer, resource and algorithms. The JWT library
// verifies signatures, and Goa's private decoder validates the signed claims;
// token contents and decoder diagnostics never enter errors or traces.
package mcp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	genclaims "goa.design/goa-ai/internal/mcpauth/gen/access_token_claims"
	genclaimssrv "goa.design/goa-ai/internal/mcpauth/gen/http/access_token_claims/server"
	goahttp "goa.design/goa/v3/http"
)

type (
	// jwtResourceVerifier holds a snapshot of the host's accepted signing keys.
	// A token cannot replace these keys or select another issuer or resource.
	jwtResourceVerifier struct {
		issuer, resource string
		keys             []jose.JSONWebKey
		algorithms       []jose.SignatureAlgorithm
	}
)

var errInvalidAccessToken = errors.New("mcp: invalid access token")

// newJWTResourceVerifier validates configuration and copies public key material.
// Mutating the host's key set after construction cannot change accepted tokens.
func newJWTResourceVerifier(issuer, resource string, keys jose.JSONWebKeySet, algorithms []jose.SignatureAlgorithm) (*jwtResourceVerifier, error) {
	if _, err := authorizationURL(issuer, true); err != nil {
		return nil, errors.New("mcp: JWT resource issuer must be an exact HTTPS identifier")
	}
	if _, err := authorizationURL(resource, false); err != nil {
		return nil, errors.New("mcp: JWT resource must be an exact HTTPS identifier")
	}
	if len(keys.Keys) == 0 || len(algorithms) == 0 {
		return nil, errors.New("mcp: JWT resource requires trusted public keys and signing algorithms")
	}
	for _, algorithm := range algorithms {
		if !slices.Contains(oauthAsymmetricAlgorithms, algorithm) {
			return nil, errors.New("mcp: JWT resource requires asymmetric signing algorithms")
		}
	}
	verifier := &jwtResourceVerifier{
		issuer: issuer, resource: resource,
		algorithms: slices.Clone(algorithms),
		keys:       make([]jose.JSONWebKey, 0, len(keys.Keys)),
	}
	for _, key := range keys.Keys {
		if !key.Valid() || !key.IsPublic() || (key.Use != "" && key.Use != "sig") {
			return nil, errors.New("mcp: JWT resource keys must be valid public signing keys")
		}
		// RFC 7518 requires at least 2048 bits for each RSA signing key.
		// This constraint protects one key, not a key set or request lifetime.
		if public, ok := key.Key.(*rsa.PublicKey); ok && public.N.BitLen() < 2048 {
			return nil, errors.New("mcp: JWT resource RSA keys require at least 2048 bits")
		}
		if key.Algorithm != "" && !slices.Contains(algorithms, jose.SignatureAlgorithm(key.Algorithm)) {
			return nil, errors.New("mcp: JWT resource key algorithm is outside the accepted algorithms")
		}
		encoded, err := x509.MarshalPKIXPublicKey(key.Key)
		if err != nil {
			return nil, errors.New("mcp: JWT resource public key could not be copied")
		}
		public, err := x509.ParsePKIXPublicKey(encoded)
		if err != nil {
			return nil, errors.New("mcp: JWT resource public key could not be decoded")
		}
		verifier.keys = append(verifier.keys, jose.JSONWebKey{
			Key: public, KeyID: key.KeyID, Algorithm: key.Algorithm, Use: "sig",
		})
	}
	return verifier, nil
}

// resourceTokenTimeValid checks the signed validity interval without rounding
// fractional seconds or adding a clock allowance. The first valid instant is
// inclusive; the expiration instant is exclusive for each authorization check.
func resourceTokenTimeValid(now time.Time, expires float64, notBefore *float64) bool {
	seconds := float64(now.Unix()) + float64(now.Nanosecond())/float64(time.Second)
	return seconds < expires && (notBefore == nil || seconds >= *notBefore)
}

// decodeAccessTokenClaims gives a verified payload to Goa's generated request
// decoder. The shared OAuth JSON decoder rejects duplicate names, null declared
// values, wrong letter case and trailing data before generated validation runs.
func decodeAccessTokenClaims(ctx context.Context, payload []byte) (*genclaims.DecodePayload, error) {
	request := &http.Request{
		Method: http.MethodPost,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(bytes.NewReader(payload)),
	}
	decode := genclaimssrv.DecodeDecodeRequest(goahttp.NewMuxer(), func(request *http.Request) goahttp.Decoder {
		return generatedJSONDecoder(&http.Response{Body: request.Body})
	})
	return decode(request.WithContext(ctx))
}

// verify checks a compact token against trusted keys before decoding its claims.
// Expiration is exclusive and not-before is inclusive, including fractions of
// a second. Invalid tokens produce one safe error regardless of their contents.
func (v *jwtResourceVerifier) verify(ctx context.Context, token string, now time.Time) (claims *genclaims.DecodePayload, err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.oauth.resource.verify")
	defer span.End()
	span.SetAttributes(attribute.String("oauth.token.profile", "rfc9068"))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	signed, err := jose.ParseSignedCompact(token, v.algorithms)
	if err != nil {
		return nil, errInvalidAccessToken
	}
	header := signed.Signatures[0].Protected
	tokenType, ok := header.ExtraHeaders[jose.HeaderType].(string)
	if !ok || (!strings.EqualFold(tokenType, "at+jwt") && !strings.EqualFold(tokenType, "application/at+jwt")) {
		return nil, errInvalidAccessToken
	}
	// Only configured keys participate. Embedded token keys and token-supplied
	// key URLs cannot authorize a signature or cause a network request.
	var payload []byte
	for _, key := range v.keys {
		if (header.KeyID != "" && key.KeyID != header.KeyID) || (key.Algorithm != "" && key.Algorithm != header.Algorithm) {
			continue
		}
		verificationKey := key.Key
		if public, ok := key.Key.(*rsa.PublicKey); ok {
			verificationKey = resourceRSAVerifier{key: public}
		}
		payload, err = signed.Verify(verificationKey)
		if err == nil {
			break
		}
	}
	if payload == nil || err != nil {
		return nil, errInvalidAccessToken
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	claims, err = decodeAccessTokenClaims(ctx, payload)
	if err != nil || claims.Iss != v.issuer || !slices.Contains(claims.Aud, v.resource) {
		return nil, errInvalidAccessToken
	}
	if !resourceTokenTimeValid(now, claims.Exp, claims.Nbf) {
		return nil, errInvalidAccessToken
	}
	span.AddEvent("access token verified")
	return claims, nil
}

// verifyGrant checks the signed token and keeps only identity, permissions and
// its validity interval for the shared resource guard and native Goa callbacks.
func (v *jwtResourceVerifier) verifyGrant(ctx context.Context, token string) (*verifiedResourceGrant, error) {
	claims, err := v.verify(ctx, token, time.Now())
	if err != nil {
		return nil, err
	}
	grant := &verifiedResourceGrant{
		principal: ResourcePrincipal{Issuer: claims.Iss, Subject: claims.Sub, ClientID: claims.ClientID},
		expires:   claims.Exp, notBefore: claims.Nbf,
	}
	if claims.Scope != nil {
		grant.scopes = strings.Split(*claims.Scope, " ")
	}
	return grant, nil
}
