// Package mcp obtains machine grants before sending MCP requests. Metadata
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
	"time"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
)

type (
	// ClientCredentials requests machine permissions using a constructed confidential
	// registration. Resource records remain separate for each host account and resource.
	ClientCredentials struct {
		// Registration selects the exact issuer and registered authentication.
		Registration *ClientRegistration
		// Scopes are the permissions requested for this transport's resource.
		Scopes []string
		// Store owns this application's private credentials and serialized rotation.
		Store AuthorizationStore
	}
	// clientCredentialsGrant uses one registration without owning its authentication.
	clientCredentialsGrant struct {
		registration *ClientRegistration
	}
)

// NewClientCredentialsHTTPTransport constructs a resource-bound machine transport.
// The issuer must advertise the selected authentication and client_credentials.
// Redirects are rejected; resource rejections do not repeat machine grants.
func NewClientCredentialsHTTPTransport(opts HTTPOptions, config ClientCredentials) (*HTTPTransport, error) {
	if err := validateClientRegistration(config.Registration); err != nil {
		return nil, err
	}
	if config.Registration.authentication == oauthPublicClient {
		return nil, errors.New("mcp: machine grants require confidential client authentication")
	}
	return newAuthorizationHTTPTransport(opts, config.Registration, config.Scopes, config.Store, &clientCredentialsGrant{registration: config.Registration})
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

// validateIssuer checks the machine grant and the exact registered authentication
// before the shared credential owner acquires or reuses a resource token.
func (g *clientCredentialsGrant) validateIssuer(issuer *genissuermetadata.ReadResult) error {
	if !slices.Contains(issuer.GrantTypesSupported, "client_credentials") {
		return errors.New("mcp: issuer must advertise client_credentials")
	}
	return g.registration.validateIssuer(issuer)
}

// acquire sends a native machine request using this registration's authentication.
// Only the returned resource token reaches MCP; registration credentials stay here.
func (g *clientCredentialsGrant) acquire(ctx context.Context, client *http.Client, resource string, issuer *genissuermetadata.ReadResult, scopes []string, _ *genaccesstokens.BearerToken, _ []AuthorizationCredential) (*genaccesstokens.BearerToken, time.Time, error) {
	if g.registration.metadata != nil {
		metadata, err := g.registration.readMetadata(ctx, client)
		if err != nil {
			return nil, time.Time{}, err
		}
		if !slices.Contains(metadata.grants, "client_credentials") {
			return nil, time.Time{}, errors.New("mcp: client metadata does not register client_credentials")
		}
	}
	return g.registration.machine(ctx, client, issuer, resource, scopes)
}

// recoversChallenges keeps machine authorization rejections terminal.
func (g *clientCredentialsGrant) recoversChallenges() bool {
	return false
}

// credentialBindings separates machine grants from other uses of a registration.
func (g *clientCredentialsGrant) credentialBindings(resource string) [][]string {
	return [][]string{{"resource", "client_credentials", g.registration.issuer.String(), g.registration.clientID, g.registration.authentication, resource}}
}
