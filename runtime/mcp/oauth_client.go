// Package mcp owns resource-bound OAuth client state for all implemented grants.
// Generated HTTP clients decode metadata and token results; this owner checks
// their identities, serializes grant changes, and supplies credentials only to
// the configured MCP resource with each operation's cancellation context.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
	genissuerclient "goa.design/goa-ai/internal/mcpauth/gen/http/issuer_metadata/client"
	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
	genresourcemetadata "goa.design/goa-ai/internal/mcpauth/gen/resource_metadata"
)

type (
	// tokenGrant checks issuer metadata and obtains a bearer token using one
	// registered client. The shared owner checks these rules before reusing a token.
	tokenGrant interface {
		validateIssuer(*genissuermetadata.ReadResult) error
		recoversChallenges() bool
		acquire(context.Context, *http.Client, string, *genissuermetadata.ReadResult, []string, *genaccesstokens.BearerToken) (*genaccesstokens.BearerToken, time.Time, error)
	}
	// authorizationClient retains credentials for one host user or application,
	// issuer and resource. No token or registration enters tool input.
	authorizationClient struct {
		client     *http.Client
		resource   *url.URL
		issuer     *url.URL
		clientInfo ClientInfo
		scopes     []string
		grant      tokenGrant
		lock       chan struct{}
		token      *genaccesstokens.BearerToken
		granted    []string
		requested  []string
		obtained   time.Time
	}
)

const (
	httpsScheme            = "https"
	oauthInsufficientScope = "insufficient_scope"
)

// newAuthorizationHTTPTransport checks the configured resource, issuer and
// scopes. It creates one private credential owner that generated and discovered
// callers use to send authorized requests.
func newAuthorizationHTTPTransport(opts HTTPOptions, issuerID string, scopes []string, grant tokenGrant) (*HTTPTransport, error) {
	resource, err := authorizationURL(opts.Endpoint, false)
	if err != nil {
		return nil, fmt.Errorf("mcp: protected resource: %w", err)
	}
	issuer, err := authorizationURL(issuerID, true)
	if err != nil {
		return nil, fmt.Errorf("mcp: authorization issuer: %w", err)
	}
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if !validScopeToken(scope) || seen[scope] {
			return nil, errors.New("mcp: configured scopes must be distinct OAuth scope tokens")
		}
		seen[scope] = true
	}
	caller, err := NewHTTPCaller(opts)
	if err != nil {
		return nil, err
	}
	client := http.DefaultClient
	if opts.Client != nil {
		var ok bool
		client, ok = opts.Client.(*http.Client)
		if !ok || client == nil {
			return nil, errors.New("mcp: built-in authorization requires a non-nil http.Client")
		}
	}
	configured := *client
	configured.CheckRedirect = rejectAuthorizationRedirect
	caller.transport.next = &configured
	caller.transport.authorization = &authorizationClient{
		client: &configured, resource: resource, issuer: issuer,
		clientInfo: opts.ClientInfo, scopes: slices.Clone(scopes),
		grant: grant, lock: make(chan struct{}, 1),
	}
	return caller.transport, nil
}

// prepare validates metadata for this operation and supplies a resource-bound
// token. Waiting for another grant respects cancellation; expired public-client
// tokens may use their private refresh credential before invoking host consent.
func (g *authorizationClient) prepare(request *http.Request, credentialQueries, resourceCredentials []string) (credential *genaccesstokens.BearerToken, err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(request.Context(), "mcp.oauth.prepare")
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !matchesResourceAddress(g.resource, request.URL, credentialQueries, resourceCredentials) {
		return nil, errors.New("mcp: authorized transport cannot send to another resource")
	}
	span.SetAttributes(attribute.String("oauth.issuer", g.issuer.String()), attribute.String("oauth.resource", g.resource.String()))
	if request.Header.Get("Authorization") != "" {
		return nil, errors.New("mcp: built-in authorization cannot replace an existing authorization credential")
	}
	select {
	case g.lock <- struct{}{}:
		defer func() { <-g.lock }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	resource, challenged, err := discoverProtectedResource(ctx, g.client, g.resource, g.issuer, g.clientInfo)
	if err != nil {
		return nil, err
	}
	issuer, err := g.discoverIssuer(ctx)
	if err != nil {
		return nil, err
	}
	if err := g.grant.validateIssuer(issuer); err != nil {
		return nil, err
	}
	scopes := g.requestScopes(resource)
	if g.grant.recoversChallenges() {
		if len(challenged) > 0 {
			scopes = challenged
		}
		scopes = unionScopes(g.requested, g.granted, scopes)
	}
	known := unionScopes(g.requested, g.granted)
	if !g.reusableGrant() || !includesScopes(known, scopes) {
		previous := g.token
		if !includesScopes(known, scopes) {
			previous = nil
		}
		if err := g.obtain(ctx, issuer, scopes, previous); err != nil {
			return nil, err
		}
		span.AddEvent("access token obtained")
	}
	request.Header.Set("Authorization", "Bearer "+g.token.AccessToken)
	return g.token, nil
}

// discoverIssuer reads OAuth and OpenID metadata in the specified order. The
// exact issuer and HTTPS token endpoint are checked before any grant executes.
func (g *authorizationClient) discoverIssuer(ctx context.Context) (*genissuermetadata.ReadResult, error) {
	for _, address := range issuerMetadataAddresses(g.issuer) {
		client := genissuerclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: g.client, address: address, operation: "issuer_metadata"}, nil, authorizationDecoder, false)
		value, err := client.Read()(ctx, nil)
		if metadataMissing(err) {
			continue
		}
		if err != nil {
			return nil, authorizationFailure(ctx, "issuer metadata", err)
		}
		issuer, ok := value.(*genissuermetadata.ReadResult)
		if !ok {
			return nil, errors.New("mcp: authorization metadata has an invalid result type")
		}
		if issuer.Issuer != g.issuer.String() {
			return nil, errors.New("mcp: authorization metadata issuer does not match the configured issuer")
		}
		if _, err := authorizationURL(issuer.TokenEndpoint, false); err != nil {
			return nil, fmt.Errorf("mcp: token endpoint: %w", err)
		}
		return issuer, nil
	}
	return nil, errors.New("mcp: issuer metadata was not found")
}

// requestScopes selects explicitly configured scopes, or the resource's basic
// advertised scopes for initial authorization. It does not union catalog tools.
func (g *authorizationClient) requestScopes(resource *genresourcemetadata.ReadResult) []string {
	if len(g.scopes) > 0 {
		return g.scopes
	}
	return resource.ScopesSupported
}

// includesScopes checks exact scope tokens. Scope hierarchies belong to the
// resource server's authorization policy, not client-side string guesses.
func includesScopes(granted, required []string) bool {
	for _, scope := range required {
		if !slices.Contains(granted, scope) {
			return false
		}
	}
	return true
}

// recover checks an explicit resource rejection before obtaining credentials.
// Browser clients may refresh or request additional consent; machine clients
// return the rejection. Concurrent operations reuse a credential already changed
// by another operation instead of rotating the same refresh token twice.
func (g *authorizationClient) recover(request *http.Request, response *HTTPResponseError, sent *genaccesstokens.BearerToken) (recovered bool, err error) {
	if !g.grant.recoversChallenges() || (response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden) {
		return false, nil
	}
	ctx, span := otel.Tracer("goa-ai/mcp").Start(request.Context(), "mcp.oauth.recover")
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	challenges, err := oauthChallenges(response.WWWAuthenticate)
	if err != nil {
		return false, err
	}
	if len(challenges) == 0 {
		return false, nil
	}
	select {
	case g.lock <- struct{}{}:
		defer func() { <-g.lock }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	_, challenge, err := challengedProtectedResource(ctx, g.client, g.resource, g.issuer, challenges)
	if err != nil {
		return false, err
	}
	if response.StatusCode == http.StatusForbidden && (challenge.error != oauthInsufficientScope || len(challenge.scopes) == 0) {
		return false, nil
	}
	if response.StatusCode == http.StatusUnauthorized && challenge.error != "" && challenge.error != "invalid_token" && challenge.error != oauthInsufficientScope {
		return false, nil
	}
	scopes := unionScopes(g.requested, g.granted, challenge.scopes)
	if g.reusableGrant() && g.token != sent && includesScopes(unionScopes(g.requested, g.granted), scopes) {
		request.Header.Set("Authorization", "Bearer "+g.token.AccessToken)
		return true, nil
	}
	if challenge.error == oauthInsufficientScope && includesScopes(g.requested, challenge.scopes) {
		return false, nil
	}
	issuer, err := g.discoverIssuer(ctx)
	if err != nil {
		return false, err
	}
	if err := g.grant.validateIssuer(issuer); err != nil {
		return false, err
	}
	previous := g.token
	if !includesScopes(unionScopes(g.requested, g.granted), scopes) {
		previous = nil
	}
	if err := g.obtain(ctx, issuer, scopes, previous); err != nil {
		return false, err
	}
	request.Header.Set("Authorization", "Bearer "+g.token.AccessToken)
	span.AddEvent("resource grant renewed")
	return true, nil
}

// obtain completes one grant and validates its lifetime before retaining it.
// Returned permissions remain issuer facts; the resource decides whether they
// permit an operation. Failed exchanges leave no access credential for the next call.
func (g *authorizationClient) obtain(ctx context.Context, issuer *genissuermetadata.ReadResult, scopes []string, previous *genaccesstokens.BearerToken) error {
	g.token = nil
	token, obtained, err := g.grant.acquire(ctx, g.client, g.resource.String(), issuer, scopes, previous)
	if err != nil {
		return err
	}
	granted := slices.Clone(scopes)
	if token.Scope != nil {
		granted = strings.Split(*token.Scope, " ")
	}
	if token.ExpiresIn != nil && int64(time.Since(obtained)/time.Second) >= *token.ExpiresIn {
		return errors.New("mcp: authorization server returned an expired access token")
	}
	g.token, g.obtained, g.granted, g.requested = token, obtained, granted, slices.Clone(scopes)
	return nil
}

// unionScopes retains prior requested and granted permissions when a resource
// challenges one operation. Exact scope tokens keep their first-seen order.
func unionScopes(sets ...[]string) []string {
	var result []string
	for _, set := range sets {
		for _, scope := range set {
			if !slices.Contains(result, scope) {
				result = append(result, scope)
			}
		}
	}
	return result
}

// reusableGrant accepts only a retained access token with a known remaining
// lifetime. A stale concurrent rejection cannot reuse an already-expired grant.
func (g *authorizationClient) reusableGrant() bool {
	return g.token != nil && g.token.ExpiresIn != nil && int64(time.Since(g.obtained)/time.Second) < *g.token.ExpiresIn
}
