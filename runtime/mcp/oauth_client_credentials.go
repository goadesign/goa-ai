// Package mcp obtains client-secret grants before sending MCP requests. Metadata
// and token responses use generated Goa clients and validation. One transport
// owns one resource, issuer and registered client; credentials never enter tool
// arguments, and failures before MCP dispatch do not imply tool execution.
package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
	gentokenclient "goa.design/goa-ai/internal/mcpauth/gen/http/access_tokens/client"
	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
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
	// clientSecretGrant owns the secret registration selected at construction.
	clientSecretGrant struct {
		credentials ClientCredentials
	}
)

// NewClientCredentialsHTTPTransport constructs a resource-bound transport.
// Client must be an *http.Client or omitted; redirects are rejected on a copy
// so neither client secrets nor bearer tokens follow another endpoint. The
// issuer must explicitly advertise both Basic and request-body secret support.
// Discovery and token acquisition run with each calling request's context.
// Challenge-only metadata discovery is supported. Consent, PKCE and scope
// changes remain outside this client-secret profile; HTTP rejections are not retried.
func NewClientCredentialsHTTPTransport(opts HTTPOptions, credentials ClientCredentials) (*HTTPTransport, error) {
	if credentials.ClientID == "" || credentials.ClientSecret == "" {
		return nil, errors.New("mcp: client identifier and secret are required")
	}
	credentials.Scopes = slices.Clone(credentials.Scopes)
	return newAuthorizationHTTPTransport(opts, credentials.Issuer, credentials.Scopes, &clientSecretGrant{credentials: credentials})
}

// authorizationURL rejects addresses that cannot identify the configured HTTPS
// resource or issuer without changing its spelling. Issuer identifiers also
// forbid a query component.
func authorizationURL(value string, issuer bool) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != httpsScheme || parsed.Hostname() == "" ||
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

// validateIssuer checks the exact advertised grant and authentication methods
// before a secret registration or its cached token can be used.
func (g *clientSecretGrant) validateIssuer(issuer *genissuermetadata.ReadResult) error {
	if !slices.Contains(issuer.GrantTypesSupported, "client_credentials") || !slices.Contains(issuer.TokenEndpointAuthMethodsSupported, "client_secret_post") || !slices.Contains(issuer.TokenEndpointAuthMethodsSupported, "client_secret_basic") {
		return errors.New("mcp: issuer must advertise client_credentials, client_secret_post and client_secret_basic")
	}
	return nil
}

// acquire sends the configured secret only to the validated issuer's token
// endpoint. A malformed response returns an authorization failure before an MCP
// request can be sent; the resource server decides whether the token permits it.
func (g *clientSecretGrant) acquire(ctx context.Context, client *http.Client, resource string, issuer *genissuermetadata.ReadResult, scopes []string, _ *genaccesstokens.BearerToken) (*genaccesstokens.BearerToken, time.Time, error) {
	address, err := authorizationURL(issuer.TokenEndpoint, false)
	if err != nil {
		return nil, time.Time{}, err
	}
	generated := gentokenclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: client, address: address, operation: "token_exchange"}, secretFormEncoder, authorizationDecoder, false)
	payload := &genaccesstokens.SecretPayload{
		ClientID:     g.credentials.ClientID,
		ClientSecret: g.credentials.ClientSecret,
		Resource:     resource,
	}
	if len(scopes) > 0 {
		scope := strings.Join(scopes, " ")
		payload.Scope = &scope
	}
	obtained := time.Now()
	value, err := generated.Secret()(ctx, payload)
	if err != nil {
		return nil, time.Time{}, authorizationFailure(ctx, "token exchange", err)
	}
	token, ok := value.(*genaccesstokens.BearerToken)
	if !ok {
		return nil, time.Time{}, errors.New("mcp: token exchange has an invalid result type")
	}

	return token, obtained, nil
}

// recoversChallenges selects whether this grant may change credentials after a
// resource rejection. Browser consent supports recovery; machine grants abort.
func (g *clientSecretGrant) recoversChallenges() bool {
	return false
}
