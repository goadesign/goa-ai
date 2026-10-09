// Package mcp obtains and stores OAuth credentials for one host account.
// Generated clients read metadata and tokens. The metadata's resource identifier
// selects what a token authorizes, called its audience. Each operation validates
// identities and loads that audience's record; storage serializes grant changes.
// Credentials reach only the configured MCP address, and failures retain no
// usable token after an uncertain exchange or save.
package mcp

import (
	"context"
	"crypto/rand"
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
	"go.opentelemetry.io/otel/trace"

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
		credentialBindings(string) [][]string
		recoversChallenges() bool
		acquire(context.Context, *http.Client, string, *genissuermetadata.ReadResult, []string, *genaccesstokens.BearerToken, []AuthorizationCredential) (*genaccesstokens.BearerToken, time.Time, error)
	}
	// authorizationClient fixes the host account, registration and request address.
	// Each operation copies it and loads credentials for its validated audience.
	// No token or registration enters tool input.
	authorizationClient struct {
		client     *http.Client
		resource   *url.URL
		audience   string
		issuer     *url.URL
		clientInfo ClientInfo
		scopes     []string
		grant      tokenGrant
		store      AuthorizationStore
		bindings   [][]string
		keys       []string
		state      *genaccesstokens.ResourceCredentialState
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
func newAuthorizationHTTPTransport(opts HTTPOptions, registration *ClientRegistration, scopes []string, store AuthorizationStore, grant tokenGrant) (*HTTPTransport, error) {
	resource, err := authorizationURL(opts.Endpoint, false)
	if err != nil {
		return nil, fmt.Errorf("mcp: protected resource: %w", err)
	}
	issuer := registration.issuer
	if store == nil {
		return nil, errors.New("mcp: an authorization store for this host user or application is required")
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
	owner := &authorizationClient{
		client: &configured, resource: resource, issuer: issuer,
		clientInfo: opts.ClientInfo, scopes: slices.Clone(scopes),
		grant: grant, store: store,
	}
	caller.transport.authorization = owner
	return caller.transport, nil
}

// prepare validates metadata for this operation and supplies a resource-bound
// token. Waiting for another grant respects cancellation; expired public-client
// tokens may use their private refresh credential before invoking host consent.
func (g *authorizationClient) prepare(request *http.Request, credentialQueries, resourceCredentials []string) (issuance string, err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(request.Context(), "mcp.oauth.prepare")
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
	if !matchesResourceAddress(g.resource, request.URL, credentialQueries, resourceCredentials) {
		return "", errors.New("mcp: authorized transport cannot send to another resource")
	}
	span.SetAttributes(attribute.String("oauth.issuer", g.issuer.String()), attribute.String("oauth.resource", g.resource.String()))
	if request.Header.Get("Authorization") != "" {
		return "", errors.New("mcp: built-in authorization cannot replace an existing authorization credential")
	}
	resource, err := discoverProtectedResource(ctx, g.client, g.resource, g.issuer)
	if err != nil {
		return "", err
	}
	var challenged []string
	probed := resource == nil
	if probed {
		resource, challenged, err = discoverResourceAuthorization(ctx, g.client, g.resource, g.issuer, g.clientInfo, nil)
		if err != nil {
			return "", err
		}
	}
	issuer, err := discoverAuthorizationIssuer(ctx, g.client, g.issuer)
	if err != nil {
		return "", err
	}
	if err := g.grant.validateIssuer(issuer); err != nil {
		return "", err
	}

	// Metadata selects the token audience before the store selects its record.
	// A new interactive grant releases that record to obtain initial challenge
	// scopes, then loads the record for the challenge's validated audience.
	for {
		span.SetAttributes(attribute.String("oauth.audience", resource.Resource))
		owner := g.credentialOwner(resource.Resource)
		needsChallenge := false
		err = withAuthorizationCredentials(ctx, owner.store, owner.keys, func(ctx context.Context, records []AuthorizationCredential) error {
			if err := owner.loadCredential(ctx, records[0]); err != nil {
				return err
			}
			_, retained := owner.state.State.AsReady()
			if !probed && !retained && owner.grant.recoversChallenges() {
				needsChallenge = true
				return nil
			}
			issuance, err = owner.prepareCredential(ctx, request, records, resource, issuer, challenged)
			return err
		})
		if err != nil || !needsChallenge {
			return issuance, err
		}
		resource, challenged, err = discoverResourceAuthorization(ctx, g.client, g.resource, g.issuer, g.clientInfo, resource)
		if err != nil {
			return "", err
		}
		probed = true
	}
}

// prepareCredential uses only the record currently held by the store. It saves
// any new credential before adding a bearer header and returning its issuance.
func (g *authorizationClient) prepareCredential(ctx context.Context, request *http.Request, records []AuthorizationCredential, resource *genresourcemetadata.ReadResult, issuer *genissuermetadata.ReadResult, challenged []string) (string, error) {
	_, retained := g.state.State.AsReady()

	known := unionScopes(g.state.Requested, g.state.Granted)
	scopes := g.requestScopes(resource)
	if g.grant.recoversChallenges() {
		// A saved grant keeps its permissions until the resource rejects an
		// operation. Newly advertised scopes do not require another consent.
		switch {
		case retained:
			scopes = known
		case len(challenged) > 0:
			scopes = unionScopes(known, challenged)
		default:
			scopes = unionScopes(known, scopes)
		}
	}
	if !g.reusableGrant() || !includesScopes(known, scopes) {
		ready, available := g.state.State.AsReady()
		var previous *genaccesstokens.BearerToken
		if available {
			previous = ready.Token
		}
		if !includesScopes(known, scopes) {
			previous = nil
		}
		if err := g.obtain(ctx, issuer, scopes, previous, records); err != nil {
			return "", err
		}
	}
	ready, _ := g.state.State.AsReady()
	request.Header.Set("Authorization", "Bearer "+ready.Token.AccessToken)
	return ready.Issuance, nil
}

// discoverAuthorizationIssuer reads OAuth and OpenID metadata in the specified order. The
// exact issuer and HTTPS token endpoint are checked before any grant executes.
func discoverAuthorizationIssuer(ctx context.Context, httpClient *http.Client, identifier *url.URL) (*genissuermetadata.ReadResult, error) {
	for _, address := range issuerMetadataAddresses(identifier) {
		client := genissuerclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: httpClient, address: address, operation: "issuer_metadata"}, nil, generatedJSONDecoder, false)
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
		if issuer.Issuer != identifier.String() {
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
func (g *authorizationClient) recover(request *http.Request, response *HTTPResponseError, sent string) (recovered bool, err error) {
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
	resource, challenge, err := challengedProtectedResource(ctx, g.client, g.resource, g.issuer, challenges)
	if err != nil {
		return false, err
	}
	span.SetAttributes(attribute.String("oauth.audience", resource.Resource))
	owner := g.credentialOwner(resource.Resource)
	err = withAuthorizationCredentials(ctx, owner.store, owner.keys, func(ctx context.Context, records []AuthorizationCredential) error {
		if err := owner.loadCredential(ctx, records[0]); err != nil {
			return err
		}
		recovered, err = owner.recoverCredential(ctx, request, response, challenge, sent, records)
		return err
	})
	if recovered {
		span.AddEvent("resource grant renewed")
	}
	return recovered, err
}

// recoverCredential compares stable issuance identifiers, so a record loaded
// in another process can satisfy a stale rejection without refreshing twice.
func (g *authorizationClient) recoverCredential(ctx context.Context, request *http.Request, response *HTTPResponseError, challenge oauthChallenge, sent string, records []AuthorizationCredential) (bool, error) {
	if response.StatusCode == http.StatusForbidden && (challenge.error != oauthInsufficientScope || len(challenge.scopes) == 0) {
		return false, nil
	}
	if response.StatusCode == http.StatusUnauthorized && challenge.error != "" && challenge.error != "invalid_token" && challenge.error != oauthInsufficientScope {
		return false, nil
	}
	ready, available := g.state.State.AsReady()
	scopes := unionScopes(g.state.Requested, g.state.Granted, challenge.scopes)
	if g.reusableGrant() && available && ready.Issuance != sent && includesScopes(unionScopes(g.state.Requested, g.state.Granted), scopes) {
		request.Header.Set("Authorization", "Bearer "+ready.Token.AccessToken)
		return true, nil
	}
	if challenge.error == oauthInsufficientScope && includesScopes(g.state.Requested, challenge.scopes) {
		return false, nil
	}
	issuer, err := discoverAuthorizationIssuer(ctx, g.client, g.issuer)
	if err != nil {
		return false, err
	}
	if err := g.grant.validateIssuer(issuer); err != nil {
		return false, err
	}
	var previous *genaccesstokens.BearerToken
	if available {
		previous = ready.Token
	}
	if !includesScopes(unionScopes(g.state.Requested, g.state.Granted), scopes) {
		previous = nil
	}
	if err := g.obtain(ctx, issuer, scopes, previous, records); err != nil {
		return false, err
	}
	ready, _ = g.state.State.AsReady()
	request.Header.Set("Authorization", "Bearer "+ready.Token.AccessToken)
	return true, nil
}

// obtain completes one grant and validates its lifetime before retaining it.
// Returned permissions remain issuer facts; the resource decides whether they
// permit an operation. Failed exchanges leave no access credential for the next call.
func (g *authorizationClient) obtain(ctx context.Context, issuer *genissuermetadata.ReadResult, scopes []string, previous *genaccesstokens.BearerToken, records []AuthorizationCredential) error {
	g.state.State = genaccesstokens.NewStatePending("exchange")
	g.state.Requested = slices.Clone(scopes)
	if err := g.saveCredential(ctx, records[0]); err != nil {
		return err
	}
	token, obtained, err := g.grant.acquire(ctx, g.client, g.audience, issuer, scopes, previous, records)
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
	g.obtained, g.state.Granted = obtained, granted
	g.state.State = genaccesstokens.NewStateReady(&genaccesstokens.ResourceCredentialReady{Token: token, Obtained: obtained.Format(time.RFC3339Nano), Issuance: rand.Text()})
	if err := g.saveCredential(ctx, records[0]); err != nil {
		return err
	}
	trace.SpanFromContext(ctx).AddEvent("resource access credential obtained and saved")
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
	ready, available := g.state.State.AsReady()
	return available && ready.Token.ExpiresIn != nil && int64(time.Since(g.obtained)/time.Second) < *ready.Token.ExpiresIn
}

// loadCredential validates the complete saved value and its exact owner before
// allowing reuse. An absent or pending record requires a fresh grant.
func (g *authorizationClient) loadCredential(ctx context.Context, record AuthorizationCredential) error {
	data, exists, err := record.Load()
	if err != nil {
		return authorizationFailure(ctx, "credential load", err)
	}
	if !exists {
		g.state = &genaccesstokens.ResourceCredentialState{Binding: slices.Clone(g.bindings[0]), State: genaccesstokens.NewStatePending("exchange")}
		g.obtained = time.Time{}
		return nil
	}
	state, err := genaccesstokens.DecodeResourceCredentialState(data)
	if err != nil {
		return authorizationFailure(ctx, "stored resource credential validation", err)
	}
	if !slices.Equal(state.Binding, g.bindings[0]) {
		return errors.New("mcp: stored resource credential belongs to another authorization owner")
	}
	obtained := time.Time{}
	if ready, available := state.State.AsReady(); available {
		obtained, err = time.Parse(time.RFC3339Nano, ready.Obtained)
		if err != nil {
			return authorizationFailure(ctx, "stored credential time", err)
		}
	}
	g.state, g.obtained = state, obtained
	return nil
}

// saveCredential encodes the private generated record and commits it before a
// consumed refresh credential or a new MCP bearer can escape this operation.
func (g *authorizationClient) saveCredential(ctx context.Context, record AuthorizationCredential) error {
	data, err := genaccesstokens.EncodeResourceCredentialState(g.state)
	if err != nil {
		return authorizationFailure(ctx, "resource credential encoding", err)
	}
	if err := record.Save(data); err != nil {
		return authorizationFailure(ctx, "credential save", err)
	}
	return nil
}

// credentialOwner keeps grant changes local to one operation and selects saved
// records for the validated token audience. The configured request address stays
// fixed; another audience cannot reuse this operation's token or refresh value.
func (g *authorizationClient) credentialOwner(audience string) *authorizationClient {
	owner := *g
	owner.audience = audience
	owner.bindings = g.grant.credentialBindings(audience)
	owner.keys = make([]string, len(owner.bindings))
	for n, binding := range owner.bindings {
		owner.keys[n] = authorizationCredentialKey(binding)
	}
	return &owner
}
