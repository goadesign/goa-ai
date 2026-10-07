// Package mcp authenticates registered machine clients with signed OAuth
// assertions. The shared credential owner selects the issuer and caches only
// resource access tokens. Goa owns form requests and issuer metadata; JOSE owns
// signing and compact JSON Web Token (JWT) encoding. Assertions never enter
// MCP request bodies.
package mcp

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
	gentokenclient "goa.design/goa-ai/internal/mcpauth/gen/http/access_tokens/client"
	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
	goahttp "goa.design/goa/v3/http"
)

type (
	// ClientAssertion configures signed authentication for one registered client.
	// Construct a separate transport for each issuer, resource and application.
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
		// Scopes are the permissions requested for HTTPOptions.Endpoint.
		Scopes []string
	}
	// clientAssertionGrant signs fresh authentication for each token acquisition.
	// The existing credential owner serializes calls and retains the access token.
	clientAssertionGrant struct {
		credentials ClientAssertion
	}
)

var oauthAsymmetricAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.PS256, jose.PS384, jose.PS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.EdDSA,
}

// NewClientAssertionHTTPTransport constructs a resource-bound machine transport.
// Goa AI creates assertion claims; the host supplies registered identities,
// validity and its already-built signer. Metadata must advertise private_key_jwt
// and the actual asymmetric signing algorithm. Redirects and authentication
// fallback are rejected; an MCP 401 or 403 is returned without a new grant.
func NewClientAssertionHTTPTransport(opts HTTPOptions, credentials ClientAssertion) (*HTTPTransport, error) {
	if credentials.ClientID == "" || credentials.AssertionIssuer == "" || credentials.Audience == "" {
		return nil, errors.New("mcp: registered client, assertion issuer and audience are required")
	}
	if credentials.Lifetime < time.Second || credentials.Lifetime%time.Second != 0 {
		return nil, errors.New("mcp: assertion lifetime must be positive whole seconds")
	}
	if credentials.Signer == nil {
		return nil, errors.New("mcp: assertion signer is required")
	}
	if _, overridden := credentials.Signer.Options().ExtraHeaders[jose.HeaderKey("alg")]; overridden {
		return nil, errors.New("mcp: assertion signer must not override its algorithm header")
	}
	credentials.Scopes = slices.Clone(credentials.Scopes)
	return newAuthorizationHTTPTransport(opts, credentials.Issuer, credentials.Scopes, &clientAssertionGrant{credentials: credentials})
}

// validateIssuer checks machine grants and signed authentication before the
// credential owner can acquire or reuse this registration's access token.
func (g *clientAssertionGrant) validateIssuer(issuer *genissuermetadata.ReadResult) error {
	if !slices.Contains(issuer.GrantTypesSupported, "client_credentials") ||
		!slices.Contains(issuer.TokenEndpointAuthMethodsSupported, "private_key_jwt") ||
		len(issuer.TokenEndpointAuthSigningAlgValuesSupported) == 0 {
		return errors.New("mcp: issuer must advertise client_credentials, private_key_jwt and signing algorithms")
	}
	return nil
}

// acquire signs one new assertion and sends it only to the validated issuer's
// token endpoint. Client identity travels inside the JWT, not in a second form
// field. The caller receives a validated resource access token or a safe error.
func (g *clientAssertionGrant) acquire(ctx context.Context, client *http.Client, resource string, issuer *genissuermetadata.ReadResult, scopes []string, _ *genaccesstokens.BearerToken) (*genaccesstokens.BearerToken, time.Time, error) {
	address, err := authorizationURL(issuer.TokenEndpoint, false)
	if err != nil {
		return nil, time.Time{}, err
	}
	assertion, err := g.assertion(ctx, issuer)
	if err != nil {
		return nil, time.Time{}, err
	}
	generated := gentokenclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: client, address: address, operation: "token_exchange"}, goahttp.RequestEncoder, authorizationDecoder, false)
	payload := &genaccesstokens.AssertionPayload{
		ClientAssertion: assertion,
		Resource:        resource,
	}
	if len(scopes) > 0 {
		scope := strings.Join(scopes, " ")
		payload.Scope = &scope
	}
	obtained := time.Now()
	value, err := generated.Assertion()(ctx, payload)
	if err != nil {
		return nil, time.Time{}, authorizationFailure(ctx, "token exchange", err)
	}
	token, ok := value.(*genaccesstokens.BearerToken)
	if !ok {
		return nil, time.Time{}, errors.New("mcp: token exchange has an invalid result type")
	}
	return token, obtained, nil
}

// recoversChallenges keeps machine authorization rejections terminal rather
// than requesting user consent or repeating an unchanged registration.
func (g *clientAssertionGrant) recoversChallenges() bool {
	return false
}

// assertion creates registered client-authentication claims and a fresh random
// identifier. It checks the actual signed header against issuer metadata and
// returns no assertion after cancellation, expiration or signing failure.
func (g *clientAssertionGrant) assertion(ctx context.Context, issuer *genissuermetadata.ReadResult) (assertion string, err error) {
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
