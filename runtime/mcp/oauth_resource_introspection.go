// Package mcp verifies opaque access tokens through a trusted issuer's HTTPS
// introspection endpoint. Goa owns the authenticated form and response decoding.
// The shared resource server checks scopes and supplies native auth callbacks;
// every check asks the issuer anew, so no saved grant conceals revocation.
package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	genintrospectclient "goa.design/goa-ai/internal/mcpauth/gen/http/token_introspection/client"
	genintrospectsrv "goa.design/goa-ai/internal/mcpauth/gen/http/token_introspection/server"
	genintrospect "goa.design/goa-ai/internal/mcpauth/gen/token_introspection"
	goahttp "goa.design/goa/v3/http"
)

type (
	// IntrospectionResource configures authenticated RFC 7662 access-token checks
	// for one MCP resource. Its trusted issuer must report active only for access
	// tokens currently usable by this registered resource, including revocation.
	IntrospectionResource struct {
		// Issuer is the trusted authorization server's exact HTTPS identifier.
		Issuer string
		// Resource is the exact HTTPS audience required in active responses.
		Resource string
		// Endpoint is the trusted HTTPS introspection address, never token supplied.
		Endpoint string
		// ClientID is the resource-server client registered for introspection.
		ClientID string
		// ClientSecret authenticates that registration using HTTP Basic.
		ClientSecret string
		// Client is the host's HTTP client; nil selects http.DefaultClient.
		// A private copy rejects redirects. The host owns network timeouts.
		Client *http.Client
	}
	// introspectionResourceVerifier owns one exact issuer endpoint and a separate
	// resource-server registration. Tokens cannot change any of those settings.
	introspectionResourceVerifier struct {
		client           *genintrospect.Client
		issuer, resource string
		username         string
		password         string
	}
)

// NewIntrospectionResourceServer constructs the shared resource owner with an
// explicit authenticated introspection profile. Active responses must name this
// audience. Identity and times remain optional as defined by RFC 7662. Invalid
// tokens receive 401; issuer failures receive 503 without an invalid-token claim.
func NewIntrospectionResourceServer(config IntrospectionResource) (*ResourceServer, error) {
	if _, err := authorizationURL(config.Issuer, true); err != nil {
		return nil, errors.New("mcp: introspection issuer must be an exact HTTPS identifier")
	}
	resource, err := authorizationURL(config.Resource, false)
	if err != nil {
		return nil, errors.New("mcp: introspection resource must be an exact HTTPS identifier")
	}
	endpoint, err := authorizationURL(config.Endpoint, false)
	if err != nil {
		return nil, errors.New("mcp: introspection endpoint must be an exact HTTPS address")
	}
	if config.ClientID == "" || config.ClientSecret == "" {
		return nil, errors.New("mcp: introspection requires a registered resource client identifier and secret")
	}
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	configured := *client
	configured.CheckRedirect = rejectAuthorizationRedirect
	generated := genintrospectclient.NewClient(endpoint.Scheme, endpoint.Host,
		&authorizationDoer{client: &configured, address: endpoint, operation: "token_introspection"},
		goahttp.RequestEncoder, authorizationDecoder, false)
	verifier := &introspectionResourceVerifier{
		client: genintrospect.NewClient(generated.Read()),
		issuer: config.Issuer, resource: config.Resource,
		// OAuth Basic credentials are form encoded individually before Goa's
		// native encoder constructs the Basic header; neither enters the form.
		username: url.QueryEscape(config.ClientID), password: url.QueryEscape(config.ClientSecret),
	}
	return &ResourceServer{
		verify: verifier.verifyGrant, issuer: config.Issuer, resource: config.Resource,
		metadata: resourceMetadataAddresses(resource)[0],
	}, nil
}

// verifyGrant validates the bearer grammar through Goa before submitting the
// exact token. The issuer supplies current activity; the resource checks audience,
// any supplied issuer and times. Failures expose no credentials or issuer body.
func (v *introspectionResourceVerifier) verifyGrant(ctx context.Context, token string) (grant *verifiedResourceGrant, err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.oauth.resource.introspect")
	defer span.End()
	span.SetAttributes(attribute.String("oauth.token.profile", "rfc7662"))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := genintrospectsrv.ValidateReadRequestBody(&genintrospectsrv.ReadRequestBody{Token: &token}); err != nil {
		return nil, errInvalidAccessToken
	}
	result, err := v.client.Read(ctx, &genintrospect.ReadPayload{
		Username: v.username, Password: v.password, Token: token, TokenTypeHint: "access_token",
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errResourceAuthorizationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !result.Active || !slices.Contains(result.Aud, v.resource) || (result.Iss != nil && *result.Iss != v.issuer) {
		return nil, errInvalidAccessToken
	}
	// The issuer owns active even when it omits timestamps. Supplied whole-second
	// limits are checked after the network response: expiration is exclusive,
	// and the first valid instant is inclusive for this authorization decision.
	now := time.Now().Unix()
	if (result.Exp != nil && now >= *result.Exp) || (result.Nbf != nil && now < *result.Nbf) {
		return nil, errInvalidAccessToken
	}
	grant = &verifiedResourceGrant{principal: ResourcePrincipal{Issuer: v.issuer}}
	if result.Sub != nil {
		grant.principal.Subject = *result.Sub
	}
	if result.ClientID != nil {
		grant.principal.ClientID = *result.ClientID
	}
	if result.Scope != nil {
		grant.scopes = strings.Split(*result.Scope, " ")
	}
	span.AddEvent("active resource access token verified")
	return grant, nil
}
