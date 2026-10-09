// Package mcp authenticates registered machine and browser clients with signed OAuth
// assertions. The shared credential owner selects the issuer and caches only
// resource access tokens. Goa owns form requests and issuer metadata; JOSE owns
// signing and compact JSON Web Token (JWT) encoding. Assertions never enter
// MCP request bodies.
package mcp

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
)

type (
	// ClientAssertion configures signed authentication for one registered client.
	// Grants supply their own permissions and resource; this configuration only
	// describes authentication registered with one issuer.
	ClientAssertion struct {
		// Issuer is the exact HTTPS authorization-server identifier.
		Issuer string
		// ClientID becomes the assertion subject registered with Issuer.
		ClientID string
		// AssertionIssuer becomes the assertion issuer agreed during registration.
		// It identifies the entity signing for ClientID, not the authorization server.
		AssertionIssuer string
		// Audience is the authorization-server identity agreed during registration.
		// RFC 7523 permits a token endpoint but does not require that identity.
		Audience string
		// Lifetime is the registered validity period of one assertion, in positive
		// whole seconds. Expiration is exclusive and does not limit access-token reuse.
		Lifetime time.Duration
		// Signer is the host's constructed JOSE signer for its registered private key.
		// Its key implementation owns any signing timeout. Its options must not
		// override the algorithm header; the issuer must advertise the actual algorithm.
		Signer jose.Signer
	}
	// clientAssertionAuthentication signs the host's registration independently
	// of the grant. Browser and machine exchanges receive the same checked JWT.
	clientAssertionAuthentication struct {
		credentials ClientAssertion
	}
)

var oauthAsymmetricAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.PS256, jose.PS384, jose.PS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.EdDSA,
}

// validateClientAssertion checks a host's signing registration before either a
// machine or browser grant can send authentication to an authorization server.
func validateClientAssertion(credentials ClientAssertion) error {
	if credentials.ClientID == "" || credentials.AssertionIssuer == "" || credentials.Audience == "" {
		return errors.New("mcp: registered client, assertion issuer and audience are required")
	}
	if credentials.Lifetime < time.Second || credentials.Lifetime%time.Second != 0 {
		return errors.New("mcp: assertion lifetime must be positive whole seconds")
	}
	if credentials.Signer == nil {
		return errors.New("mcp: assertion signer is required")
	}
	if _, overridden := credentials.Signer.Options().ExtraHeaders[jose.HeaderKey("alg")]; overridden {
		return errors.New("mcp: assertion signer must not override its algorithm header")
	}
	return nil
}

// validateAssertionAuthentication checks signed authentication independently of
// the grant, so browser and machine flows use the same advertised requirements.
func validateAssertionAuthentication(issuer *genissuermetadata.ReadResult) error {
	if !slices.Contains(issuer.TokenEndpointAuthMethodsSupported, oauthSignedClient) || len(issuer.TokenEndpointAuthSigningAlgValuesSupported) == 0 {
		return errors.New("mcp: issuer must advertise private_key_jwt and signing algorithms")
	}
	return nil
}

// assertion creates registered client-authentication claims and a fresh random
// identifier. It checks the actual signed header against issuer metadata and
// returns no assertion after cancellation, expiration or signing failure.
func (g *clientAssertionAuthentication) assertion(ctx context.Context, issuer *genissuermetadata.ReadResult) (assertion string, err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.oauth.assertion.sign")
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	issued := time.Now().Truncate(time.Second)
	expires := issued.Add(g.credentials.Lifetime)
	assertion, err = jwt.Signed(g.credentials.Signer).Claims(jwt.Claims{
		Issuer:   g.credentials.AssertionIssuer,
		Subject:  g.credentials.ClientID,
		Audience: jwt.Audience{g.credentials.Audience},
		IssuedAt: jwt.NewNumericDate(issued),
		Expiry:   jwt.NewNumericDate(expires),
		ID:       rand.Text(),
	}).Serialize()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", errors.New("mcp: client assertion signing failed")
	}
	token, err := jwt.ParseSigned(assertion, oauthAsymmetricAlgorithms)
	if err != nil || !slices.Contains(issuer.TokenEndpointAuthSigningAlgValuesSupported, token.Headers[0].Algorithm) {
		return "", errors.New("mcp: client assertion requires an asymmetric algorithm advertised by the issuer")
	}
	if !time.Now().Before(expires) {
		return "", errors.New("mcp: client assertion expired during signing")
	}
	span.SetAttributes(attribute.String("oauth.signing_algorithm", token.Headers[0].Algorithm))
	return assertion, nil
}
