// Package mcp completes browser authorization using Proof Key for Code Exchange
// (PKCE), which binds a code to a private verifier, and generated query decoding.
// The host handles browser consent for its own user or application; this
// file checks the exact redirect, state and issuer before any code is exchanged.
// Refresh credentials stay private to the same constructed issuer and resource.
package mcp

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
	gentokenclient "goa.design/goa-ai/internal/mcpauth/gen/http/access_tokens/client"
	gencallbackserver "goa.design/goa-ai/internal/mcpauth/gen/http/authorization_responses/server"
	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
	goahttp "goa.design/goa/v3/http"
)

type (
	// AuthorizationCode configures browser consent for one public client and
	// host user or application. Construct a separate transport for each user or
	// application, issuer and protected resource.
	AuthorizationCode struct {
		// Issuer is the exact HTTPS identifier of the selected authorization server.
		Issuer string
		// ClientID is the pre-registered identifier, or the HTTPS document URL
		// when constructing a client-metadata transport.
		ClientID string
		// RedirectURI is the registered HTTPS or HTTP loopback callback address.
		RedirectURI string
		// Scopes are the initial requested permissions. When empty, the resource's
		// advertised basic scopes supply the initial request. An initial challenge
		// takes priority when it supplies scopes for the current operation.
		Scopes []string
		// Authorize handles sign-in and consent for this transport's user or application.
		// It receives the complete authorization URL and returns the complete
		// redirect URL. It must respect cancellation and must not log either URL.
		Authorize func(context.Context, string) (string, error)
	}
	// SignedAuthorizationCode configures browser consent with signed client
	// authentication. The embedded registration supplies one issuer, client
	// identity, signer and permission set for both code exchange and refresh.
	SignedAuthorizationCode struct {
		// ClientAssertion is the registered authentication used at the token endpoint.
		ClientAssertion
		// RedirectURI is the registered HTTPS or HTTP loopback callback address.
		RedirectURI string
		// Authorize handles sign-in and consent and returns the complete redirect.
		// It must respect cancellation and must not log either URL.
		Authorize func(context.Context, string) (string, error)
	}
	// authorizationCodeGrant retains one client's registered redirect and
	// host callback. PKCE values and state exist only during the active exchange.
	authorizationCodeGrant struct {
		client   AuthorizationCode
		redirect *url.URL
		metadata *url.URL
		signed   *clientAssertionAuthentication
	}
)

// NewAuthorizationCodeHTTPTransport constructs a pre-registered public-client
// flow. It verifies advertised S256 support before asking the host for consent,
// retains tokens privately, and uses the active request context for refresh.
func NewAuthorizationCodeHTTPTransport(opts HTTPOptions, client AuthorizationCode) (*HTTPTransport, error) {
	return newAuthorizationCodeTransport(opts, client, nil, nil)
}

// NewClientMetadataHTTPTransport constructs a public client whose ClientID is
// its self-hosted HTTPS metadata document. The host publishes that document;
// this transport validates it and the issuer's support before user consent.
// Unsupported registration never falls back to legacy dynamic registration.
func NewClientMetadataHTTPTransport(opts HTTPOptions, client AuthorizationCode) (*HTTPTransport, error) {
	address, err := clientMetadataAddress(client.ClientID)
	if err != nil {
		return nil, err
	}
	return newAuthorizationCodeTransport(opts, client, address, nil)
}

// NewSignedAuthorizationCodeHTTPTransport constructs a pre-registered browser
// flow that signs fresh authentication for code exchange and token refresh.
// Consent, PKCE and credential ownership follow the same browser flow.
func NewSignedAuthorizationCodeHTTPTransport(opts HTTPOptions, client SignedAuthorizationCode) (*HTTPTransport, error) {
	return newSignedAuthorizationCodeTransport(opts, client, nil)
}

// NewSignedClientMetadataHTTPTransport constructs a signed browser client whose
// identifier is its HTTPS registration document. The document must declare
// private_key_jwt and exactly one public-key source before consent is requested.
func NewSignedClientMetadataHTTPTransport(opts HTTPOptions, client SignedAuthorizationCode) (*HTTPTransport, error) {
	address, err := clientMetadataAddress(client.ClientID)
	if err != nil {
		return nil, err
	}
	return newSignedAuthorizationCodeTransport(opts, client, address)
}

// newSignedAuthorizationCodeTransport validates the signing registration and
// installs it in the shared browser grant; no second token owner is constructed.
func newSignedAuthorizationCodeTransport(opts HTTPOptions, client SignedAuthorizationCode, metadata *url.URL) (*HTTPTransport, error) {
	if err := validateClientAssertion(client.ClientAssertion); err != nil {
		return nil, err
	}
	return newAuthorizationCodeTransport(opts, AuthorizationCode{
		Issuer: client.Issuer, ClientID: client.ClientID, Scopes: client.Scopes,
		RedirectURI: client.RedirectURI, Authorize: client.Authorize,
	}, metadata, &clientAssertionAuthentication{credentials: client.ClientAssertion})
}

// newAuthorizationCodeTransport checks host configuration once and constructs
// the selected registration before exposing the shared transport to callers.
func newAuthorizationCodeTransport(opts HTTPOptions, client AuthorizationCode, metadata *url.URL, signed *clientAssertionAuthentication) (*HTTPTransport, error) {
	if client.ClientID == "" || client.Authorize == nil {
		return nil, errors.New("mcp: a registered client and host authorization callback are required")
	}
	redirect, err := authorizationRedirect(client.RedirectURI)
	if err != nil {
		return nil, err
	}
	client.Scopes = slices.Clone(client.Scopes)
	return newAuthorizationHTTPTransport(opts, client.Issuer, client.Scopes, &authorizationCodeGrant{client: client, redirect: redirect, metadata: metadata, signed: signed})
}

// authorizationRedirect validates the host's registered callback and excludes
// OAuth response parameters from its static query. Exact fixed query values may
// identify the host route, but cannot replace the runtime's state or issuer.
func authorizationRedirect(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" || parsed.String() != value {
		return nil, errors.New("mcp: authorization redirect must be an exact absolute URL without user information or a fragment")
	}
	loopback := parsed.Hostname() == "localhost"
	if ip := net.ParseIP(parsed.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if parsed.Scheme != httpsScheme && (parsed.Scheme != "http" || !loopback) {
		return nil, errors.New("mcp: authorization redirect must use HTTPS or HTTP loopback")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, errors.New("mcp: authorization redirect has invalid query encoding")
	}
	for _, name := range []string{"code", "error", "state", "iss", "error_description", "error_uri"} {
		if query.Has(name) {
			return nil, errors.New("mcp: registered redirect cannot contain OAuth response parameters")
		}
	}
	return parsed, nil
}

// authorizationRequest preserves the issuer's routing query and adds runtime-
// owned OAuth fields. Existing reserved fields are rejected instead of overwritten;
// the host receives one unambiguous request for the exact configured resource.
func authorizationRequest(endpoint string, client AuthorizationCode, resource string, scopes []string, verifier, state string) (string, error) {
	address, err := authorizationURL(endpoint, false)
	if err != nil {
		return "", err
	}
	query, err := url.ParseQuery(address.RawQuery)
	if err != nil {
		return "", errors.New("mcp: authorization endpoint has invalid query encoding")
	}
	for _, name := range []string{"response_type", "client_id", "redirect_uri", "resource", "scope", "code_challenge", "code_challenge_method", "state"} {
		if query.Has(name) {
			return "", errors.New("mcp: authorization endpoint contains a reserved OAuth request parameter")
		}
	}
	query.Set("response_type", "code")
	query.Set("client_id", client.ClientID)
	query.Set("redirect_uri", client.RedirectURI)
	query.Set("resource", resource)
	query.Set("code_challenge_method", "S256")
	query.Set("code_challenge", oauth2.S256ChallengeFromVerifier(verifier))
	query.Set("state", state)
	if len(scopes) > 0 {
		query.Set("scope", strings.Join(scopes, " "))
	}
	address.RawQuery = query.Encode()
	return address.String(), nil
}

// validateIssuer checks the selected client's advertised grant, PKCE and token
// authentication before consent or cache reuse. Unsupported profiles fail rather
// than assume default endpoints or try a different credential placement.
func (g *authorizationCodeGrant) validateIssuer(issuer *genissuermetadata.ReadResult) error {
	if issuer.AuthorizationEndpoint == nil || !slices.Contains(issuer.CodeChallengeMethodsSupported, "S256") || !slices.Contains(issuer.GrantTypesSupported, "authorization_code") {
		return errors.New("mcp: browser issuer must advertise authorization_code and S256 with an authorization endpoint")
	}
	if g.signed != nil {
		if err := validateAssertionAuthentication(issuer); err != nil {
			return err
		}
	} else if !slices.Contains(issuer.TokenEndpointAuthMethodsSupported, "none") {
		return errors.New("mcp: public-client issuer must advertise none authentication")
	}
	if g.metadata != nil && (issuer.ClientIDMetadataDocumentSupported == nil || !*issuer.ClientIDMetadataDocumentSupported) {
		return errors.New("mcp: issuer does not support client metadata documents")
	}
	if _, err := authorizationURL(*issuer.AuthorizationEndpoint, false); err != nil {
		return errors.New("mcp: authorization endpoint must identify an exact HTTPS URL")
	}
	return nil
}

// acquire refreshes an existing grant only when the issuer advertises refresh
// support. Otherwise it performs a new host-consented PKCE exchange. A rejected
// refresh returns its failure; it does not silently open another consent flow.
func (g *authorizationCodeGrant) acquire(ctx context.Context, client *http.Client, resource string, issuer *genissuermetadata.ReadResult, scopes []string, previous *genaccesstokens.BearerToken) (*genaccesstokens.BearerToken, time.Time, error) {
	canRefresh := true
	if g.metadata != nil {
		registeredRefresh, err := g.validateMetadata(ctx, client)
		if err != nil {
			return nil, time.Time{}, err
		}
		canRefresh = registeredRefresh
	}
	address, err := authorizationURL(issuer.TokenEndpoint, false)
	if err != nil {
		return nil, time.Time{}, err
	}
	if canRefresh && previous != nil && previous.RefreshToken != nil && slices.Contains(issuer.GrantTypesSupported, "refresh_token") {
		generated := gentokenclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: client, address: address, operation: "token_refresh"}, goahttp.RequestEncoder, authorizationDecoder, false)
		obtained := time.Now()
		var value any
		if g.signed != nil {
			assertion, signingErr := g.signed.assertion(ctx, issuer)
			if signingErr != nil {
				return nil, time.Time{}, signingErr
			}
			obtained = time.Now()
			value, err = generated.SignedRefresh()(ctx, &genaccesstokens.SignedRefreshPayload{
				ClientAssertion: assertion, RefreshToken: *previous.RefreshToken, Resource: resource,
			})
		} else {
			value, err = generated.Refresh()(ctx, &genaccesstokens.RefreshPayload{
				ClientID: g.client.ClientID, RefreshToken: *previous.RefreshToken, Resource: resource,
			})
		}
		if err != nil {
			return nil, time.Time{}, authorizationFailure(ctx, "token refresh", err)
		}
		token, ok := value.(*genaccesstokens.BearerToken)
		if !ok {
			return nil, time.Time{}, errors.New("mcp: refresh response has an invalid result type")
		}
		// A rotated refresh credential replaces the original. When the issuer
		// omits it, RFC 6749 retains the original grant's refresh credential.
		if token.RefreshToken == nil {
			token.RefreshToken = previous.RefreshToken
		}
		if token.Scope == nil {
			token.Scope = previous.Scope
		}
		return token, obtained, nil
	}
	verifier := oauth2.GenerateVerifier()
	state := rand.Text()
	authorization, err := authorizationRequest(*issuer.AuthorizationEndpoint, g.client, resource, scopes, verifier, state)
	if err != nil {
		return nil, time.Time{}, err
	}
	callback, err := g.client.Authorize(ctx, authorization)
	if err != nil {
		return nil, time.Time{}, authorizationFailure(ctx, "host authorization", err)
	}
	if ctx.Err() != nil {
		return nil, time.Time{}, ctx.Err()
	}
	code, err := g.authorizationResponse(ctx, callback, state, issuer)
	if err != nil {
		return nil, time.Time{}, err
	}
	generated := gentokenclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: client, address: address, operation: "token_exchange"}, goahttp.RequestEncoder, authorizationDecoder, false)
	obtained := time.Now()
	var value any
	if g.signed != nil {
		assertion, signingErr := g.signed.assertion(ctx, issuer)
		if signingErr != nil {
			return nil, time.Time{}, signingErr
		}
		obtained = time.Now()
		value, err = generated.SignedCode()(ctx, &genaccesstokens.SignedCodePayload{
			ClientAssertion: assertion, Code: code, CodeVerifier: verifier,
			RedirectURI: g.client.RedirectURI, Resource: resource,
		})
	} else {
		value, err = generated.Code()(ctx, &genaccesstokens.CodePayload{
			ClientID: g.client.ClientID, Code: code, CodeVerifier: verifier,
			RedirectURI: g.client.RedirectURI, Resource: resource,
		})
	}
	if err != nil {
		return nil, time.Time{}, authorizationFailure(ctx, "code exchange", err)
	}
	token, ok := value.(*genaccesstokens.BearerToken)
	if !ok {
		return nil, time.Time{}, errors.New("mcp: code exchange has an invalid result type")
	}
	return token, obtained, nil
}

// authorizationResponse checks the callback's exact route and fixed query, then
// uses Goa's generated query decoder. State and issuer are checked before a code
// is returned or an issuer-authored error can influence the caller.
func (g *authorizationCodeGrant) authorizationResponse(ctx context.Context, callback, state string, issuer *genissuermetadata.ReadResult) (string, error) {
	address, err := url.Parse(callback)
	if err != nil || address.User != nil || address.Fragment != "" || address.Opaque != "" || address.String() != callback {
		return "", errors.New("mcp: host returned an invalid authorization redirect")
	}
	actual, expected := *address, *g.redirect
	actual.RawQuery, actual.ForceQuery = "", false
	expected.RawQuery, expected.ForceQuery = "", false
	if actual.String() != expected.String() {
		return "", errors.New("mcp: authorization response arrived at another redirect")
	}
	query, err := url.ParseQuery(address.RawQuery)
	if err != nil {
		return "", errors.New("mcp: authorization response has invalid query encoding")
	}
	fixed := g.redirect.Query()
	for key, values := range fixed {
		if !slices.Equal(query[key], values) {
			return "", errors.New("mcp: authorization response changed the registered redirect query")
		}
	}
	for _, name := range []string{"code", "error", "state", "iss", "error_description", "error_uri"} {
		if values, present := query[name]; present && len(values) != 1 {
			return "", errors.New("mcp: authorization response repeats a protocol parameter")
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, callback, nil)
	if err != nil {
		return "", errors.New("mcp: authorization redirect cannot be decoded")
	}
	payload, err := gencallbackserver.DecodeReceiveRequest(goahttp.NewMuxer(), nil)(request)
	if err != nil {
		return "", errors.New("mcp: authorization response failed generated query validation")
	}
	if payload.State != state {
		return "", errors.New("mcp: authorization response does not match this exchange")
	}
	if payload.Issuer != nil {
		if *payload.Issuer != issuer.Issuer {
			return "", errors.New("mcp: authorization response issuer does not match this exchange")
		}
	} else if issuer.AuthorizationResponseIssParameterSupported != nil && *issuer.AuthorizationResponseIssParameterSupported {
		return "", errors.New("mcp: authorization response omitted its required issuer")
	}
	if (payload.Code == nil) == (payload.Error == nil) {
		return "", errors.New("mcp: authorization response must contain one code or error")
	}
	if payload.Error != nil {
		return "", errors.New("mcp: user authorization was declined by the selected issuer")
	}
	return *payload.Code, nil
}

// recoversChallenges selects whether this grant may change credentials after a
// resource rejection. Browser consent supports recovery; machine grants abort.
func (g *authorizationCodeGrant) recoversChallenges() bool {
	return true
}
