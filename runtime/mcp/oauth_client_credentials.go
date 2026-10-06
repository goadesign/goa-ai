// Package mcp obtains client-secret grants before sending MCP requests. Metadata
// and token responses use generated Goa clients and validation. One transport
// owns one resource, issuer and registered client; credentials never enter tool
// arguments, and failures before MCP dispatch do not imply tool execution.
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
	gentokenclient "goa.design/goa-ai/internal/mcpauth/gen/http/access_tokens/client"
	genissuerclient "goa.design/goa-ai/internal/mcpauth/gen/http/issuer_metadata/client"
	genresourceclient "goa.design/goa-ai/internal/mcpauth/gen/http/resource_metadata/client"
	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
	genresourcemetadata "goa.design/goa-ai/internal/mcpauth/gen/resource_metadata"
)

type (
	// ClientCredentials binds a preregistered client-secret grant to one issuer.
	// The protected resource is HTTPOptions.Endpoint. Each transport owns its
	// token independently; no registration or grant is shared with another client.
	ClientCredentials struct {
		// Issuer is the exact HTTPS authorization-server identifier.
		Issuer string
		// ClientID is the identifier registered with Issuer.
		ClientID string
		// ClientSecret is the secret registered with Issuer.
		ClientSecret string
		// Scopes are the permissions requested for this transport's resource.
		Scopes []string
	}
	// clientSecretGrant retains only this transport's configured grant and token.
	clientSecretGrant struct {
		client      *http.Client
		resource    *url.URL
		issuer      *url.URL
		credentials ClientCredentials
		lock        chan struct{}
		token       *genaccesstokens.SecretResult
		obtained    time.Time
	}
)

// NewClientCredentialsHTTPTransport constructs a resource-bound transport.
// Client must be an *http.Client or omitted; redirects are rejected on a copy
// so neither client secrets nor bearer tokens follow another endpoint. The
// issuer must explicitly advertise both Basic and request-body secret support.
// Discovery and token acquisition run with each calling request's context.
// Challenges, consent, PKCE and scope changes are not handled by this profile.
func NewClientCredentialsHTTPTransport(opts HTTPOptions, credentials ClientCredentials) (*HTTPTransport, error) {
	resource, err := authorizationURL(opts.Endpoint, false)
	if err != nil {
		return nil, fmt.Errorf("mcp: protected resource: %w", err)
	}
	issuer, err := authorizationURL(credentials.Issuer, true)
	if err != nil {
		return nil, fmt.Errorf("mcp: authorization issuer: %w", err)
	}
	if credentials.ClientID == "" || credentials.ClientSecret == "" {
		return nil, errors.New("mcp: client identifier and secret are required")
	}
	seen := make(map[string]bool, len(credentials.Scopes))
	for _, scope := range credentials.Scopes {
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
			return nil, errors.New("mcp: client-secret authorization requires a non-nil http.Client")
		}
	}
	configured := *client
	configured.CheckRedirect = rejectAuthorizationRedirect
	credentials.Scopes = slices.Clone(credentials.Scopes)
	caller.transport.next = &configured
	caller.transport.authorization = &clientSecretGrant{
		client:      &configured,
		resource:    resource,
		issuer:      issuer,
		credentials: credentials,
		lock:        make(chan struct{}, 1),
	}
	return caller.transport, nil
}

// authorizationURL rejects addresses that cannot identify the configured HTTPS
// resource or issuer without changing its spelling. Issuer identifiers also
// forbid a query component.
func authorizationURL(value string, issuer bool) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" ||
		parsed.String() != value || (issuer && (parsed.RawQuery != "" || parsed.ForceQuery)) {
		return nil, errors.New("an exactly serialized absolute HTTPS URL without user information or a fragment is required; issuers also forbid queries")
	}
	return parsed, nil
}

// validScopeToken accepts the visible ASCII characters defined for an OAuth
// scope, excluding spaces, double quotes and backslashes.
func validScopeToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e || character == '"' || character == '\\' {
			return false
		}
	}
	return true
}

// rejectAuthorizationRedirect stops before any credential-bearing request can
// be repeated at a different address. The caller receives a transport failure.
func rejectAuthorizationRedirect(_ *http.Request, _ []*http.Request) error {
	return errors.New("mcp: authorization redirects are not permitted")
}

// prepare checks the request's exact resource, validates both metadata owners,
// and supplies a token before the caller dispatches any MCP operation. Waiting
// for another token exchange respects this request's cancellation independently.
func (g *clientSecretGrant) prepare(request *http.Request) (err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(request.Context(), "mcp.oauth.prepare")
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	span.SetAttributes(attribute.String("oauth.issuer", g.issuer.String()), attribute.String("oauth.resource", g.resource.String()))
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.URL.String() != g.resource.String() {
		return errors.New("mcp: authorized transport cannot send to another resource")
	}
	if request.Header.Get("Authorization") != "" {
		return errors.New("mcp: client-secret authorization cannot replace an existing authorization credential")
	}
	select {
	case g.lock <- struct{}{}:
		defer func() { <-g.lock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	issuer, err := g.discover(ctx)
	if err != nil {
		return err
	}
	// Metadata is checked for every operation. A changed issuer or revoked
	// authentication method cannot reuse a token cached from an earlier call.
	if g.token == nil || g.token.ExpiresIn == nil || int64(time.Since(g.obtained)/time.Second) >= *g.token.ExpiresIn {
		g.token = nil
		g.obtained = time.Now()
		g.token, err = g.exchange(ctx, issuer.TokenEndpoint)
		if err != nil {
			return err
		}
		span.AddEvent("access token obtained")
	}
	if g.token.ExpiresIn != nil && int64(time.Since(g.obtained)/time.Second) >= *g.token.ExpiresIn {
		return errors.New("mcp: authorization server returned an expired access token")
	}
	request.Header.Set("Authorization", "Bearer "+g.token.AccessToken)
	return nil
}

// discover reads the resource before its configured issuer. A document with a
// different identity stops the operation before any client secret is sent.
func (g *clientSecretGrant) discover(ctx context.Context) (*genissuermetadata.ReadResult, error) {
	resourceAddresses := resourceMetadataAddresses(g.resource)
	var resource *genresourcemetadata.ReadResult
	for _, address := range resourceAddresses {
		client := genresourceclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: g.client, address: address, operation: "resource_metadata"}, nil, authorizationDecoder, false)
		value, err := client.Read()(ctx, nil)
		if metadataMissing(err) {
			continue
		}
		if err != nil {
			return nil, authorizationFailure(ctx, "protected-resource metadata", err)
		}
		var ok bool
		resource, ok = value.(*genresourcemetadata.ReadResult)
		if !ok {
			return nil, errors.New("mcp: protected-resource metadata has an invalid result type")
		}
		break
	}
	if resource == nil {
		return nil, errors.New("mcp: protected-resource metadata was not found")
	}
	if resource.Resource != g.resource.String() || !slices.Contains(resource.AuthorizationServers, g.issuer.String()) {
		return nil, errors.New("mcp: protected-resource metadata does not bind the configured resource and issuer")
	}
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
			return nil, errors.New("mcp: issuer metadata has an invalid result type")
		}
		if issuer.Issuer != g.issuer.String() {
			return nil, errors.New("mcp: authorization metadata issuer does not match the configured issuer")
		}
		if _, err := authorizationURL(issuer.TokenEndpoint, false); err != nil {
			return nil, fmt.Errorf("mcp: token endpoint: %w", err)
		}
		if !slices.Contains(issuer.GrantTypesSupported, "client_credentials") || !slices.Contains(issuer.TokenEndpointAuthMethodsSupported, "client_secret_post") || !slices.Contains(issuer.TokenEndpointAuthMethodsSupported, "client_secret_basic") {
			return nil, errors.New("mcp: issuer must advertise client_credentials, client_secret_post and client_secret_basic")
		}
		return issuer, nil
	}
	return nil, errors.New("mcp: issuer metadata was not found")
}

// exchange sends the configured secret only to the validated issuer's token
// endpoint. A malformed response or missing requested permission returns an
// authorization failure before an MCP request can be sent.
func (g *clientSecretGrant) exchange(ctx context.Context, endpoint string) (*genaccesstokens.SecretResult, error) {
	address, err := authorizationURL(endpoint, false)
	if err != nil {
		return nil, err
	}
	client := gentokenclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: g.client, address: address, operation: "token_exchange"}, secretFormEncoder, authorizationDecoder, false)
	payload := &genaccesstokens.SecretPayload{
		ClientID:     g.credentials.ClientID,
		ClientSecret: g.credentials.ClientSecret,
		Resource:     g.resource.String(),
	}
	if len(g.credentials.Scopes) > 0 {
		scope := strings.Join(g.credentials.Scopes, " ")
		payload.Scope = &scope
	}
	value, err := client.Secret()(ctx, payload)
	if err != nil {
		return nil, authorizationFailure(ctx, "token exchange", err)
	}
	token, ok := value.(*genaccesstokens.SecretResult)
	if !ok {
		return nil, errors.New("mcp: token exchange has an invalid result type")
	}
	if token.Scope != nil {
		granted := strings.Split(*token.Scope, " ")
		for _, required := range g.credentials.Scopes {
			if !slices.Contains(granted, required) {
				return nil, errors.New("mcp: token response does not grant the configured permissions")
			}
		}
	}
	return token, nil
}
