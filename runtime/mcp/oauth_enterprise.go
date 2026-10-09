// Package mcp exchanges a host user's existing identity credential for access to
// one MCP resource. The identity provider owns identity validation and issues a
// grant for the resource issuer. That issuer returns the only bearer token sent
// to MCP. Native Goa clients own both token forms and response validation.
package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
	genidentitygrants "goa.design/goa-ai/internal/mcpauth/gen/identity_grants"
	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
)

type (
	// EnterpriseIdentity owns credentials for one host user and identity-provider
	// registration. Share it across that user's resource transports, never users.
	// The host owns sign-in and validates every supplied SSO credential for this
	// registration and user before returning it from the constructor's callback.
	EnterpriseIdentity struct {
		registration *ClientRegistration
		client       *http.Client
		source       func(context.Context) (string, error)
		kind         enterpriseCredentialKind
		store        AuthorizationStore
		binding      []string
	}
	// EnterpriseAuthorization requests resource permissions through a user's
	// constructed identity and an independent resource-issuer registration.
	EnterpriseAuthorization struct {
		// Identity supplies this user's credentials at their identity provider.
		Identity *EnterpriseIdentity
		// Registration selects authentication at the resource authorization server.
		Registration *ClientRegistration
		// Scopes are the permissions requested for HTTPOptions.Endpoint.
		Scopes []string
	}
	// enterpriseCredentialKind selects the host's known SSO credential contract.
	enterpriseCredentialKind uint8
	// enterpriseGrant redeems identity grants through the shared resource-token owner.
	enterpriseGrant struct {
		identity     *EnterpriseIdentity
		registration *ClientRegistration
	}
)

const (
	oauthIdentityExchange    = "urn:ietf:params:oauth:grant-type:token-exchange"
	oauthIdentityRedemption  = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	oauthIdentityProfile     = "urn:ietf:params:oauth:grant-profile:id-jag"
	oauthIdentityGrantType   = "urn:ietf:params:oauth:token-type:id-jag"
	oauthIdentityIDTokenType = "urn:ietf:params:oauth:token-type:id_token" // #nosec G101 -- This public OAuth purpose identifier contains no credential.
	oauthIdentityRefreshType = "urn:ietf:params:oauth:token-type:refresh_token"
)

const (
	enterpriseIDToken enterpriseCredentialKind = iota
	enterpriseRefresh
	enterpriseSAML
)

// NewIDTokenEnterpriseIdentity constructs a user identity from the host's
// validated OpenID Connect ID token. Signed and encrypted tokens are accepted;
// the identity provider validates them before issuing an authorization grant.
func NewIDTokenEnterpriseIdentity(registration *ClientRegistration, client *http.Client, store AuthorizationStore, source func(context.Context) (string, error)) (*EnterpriseIdentity, error) {
	return newEnterpriseIdentity(registration, client, store, source, enterpriseIDToken)
}

// NewSAMLEnterpriseIdentity constructs a user identity from the host's validated
// SAML assertion. Goa AI encodes the assertion and exchanges it once for an
// identity-provider refresh credential shared by this user's MCP resources.
// The callback must obtain a fresh assertion whenever called, including after
// an uncertain bootstrap. It must never return a previously submitted assertion.
func NewSAMLEnterpriseIdentity(registration *ClientRegistration, client *http.Client, store AuthorizationStore, source func(context.Context) (string, error)) (*EnterpriseIdentity, error) {
	return newEnterpriseIdentity(registration, client, store, source, enterpriseSAML)
}

// NewRefreshTokenEnterpriseIdentity constructs a user identity from a refresh
// credential already owned by the host's SSO flow. Each resource renewal asks
// the host for its current credential and obtains a fresh identity grant.
func NewRefreshTokenEnterpriseIdentity(registration *ClientRegistration, client *http.Client, store AuthorizationStore, source func(context.Context) (string, error)) (*EnterpriseIdentity, error) {
	return newEnterpriseIdentity(registration, client, store, source, enterpriseRefresh)
}

// NewEnterpriseHTTPTransport constructs a resource-bound authorized transport.
// Identity-provider and resource registrations may differ. The existing transport
// retains final tokens and bounds recovery after explicit resource rejections.
func NewEnterpriseHTTPTransport(opts HTTPOptions, config EnterpriseAuthorization) (*HTTPTransport, error) {
	if config.Identity == nil || config.Identity.registration == nil {
		return nil, errors.New("mcp: a constructed enterprise user identity is required")
	}
	if err := validateClientRegistration(config.Registration); err != nil {
		return nil, err
	}
	grant := &enterpriseGrant{identity: config.Identity, registration: config.Registration}
	return newAuthorizationHTTPTransport(opts, config.Registration, config.Scopes, config.Identity.store, grant)
}

// newEnterpriseIdentity binds an already-built trusted client and credential
// source to one user registration. Credential-bearing requests cannot redirect.
func newEnterpriseIdentity(registration *ClientRegistration, client *http.Client, store AuthorizationStore, source func(context.Context) (string, error), kind enterpriseCredentialKind) (*EnterpriseIdentity, error) {
	if err := validateClientRegistration(registration); err != nil {
		return nil, err
	}
	if client == nil || store == nil || source == nil {
		return nil, errors.New("mcp: enterprise identity requires a trusted HTTP client, authorization store and host credential source")
	}
	configured := *client
	configured.CheckRedirect = rejectAuthorizationRedirect
	purpose := oauthIdentityIDTokenType
	if kind == enterpriseRefresh {
		purpose = oauthIdentityRefreshType
	}
	if kind == enterpriseSAML {
		purpose = "saml_bootstrap"
	}
	return &EnterpriseIdentity{registration: registration, client: &configured, store: store, source: source, kind: kind, binding: []string{"identity", purpose, registration.issuer.String(), registration.clientID, registration.authentication}}, nil
}

// identityGrant serializes this user's identity exchanges. It validates the IdP
// registration, obtains or reuses SAML bootstrap, and returns one non-bearer grant
// for the exact resource issuer and MCP resource without retaining that grant.
func (i *EnterpriseIdentity) identityGrant(ctx context.Context, audience, resource string, scopes []string, record AuthorizationCredential) (grant *genidentitygrants.IdentityGrant, obtained time.Time, err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.oauth.identity_grant")
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	span.SetAttributes(attribute.String("oauth.issuer", i.registration.issuer.String()), attribute.String("oauth.resource", resource))
	grant, obtained, err = i.identityGrantCredential(ctx, audience, resource, scopes, record)
	if err == nil {
		span.AddEvent("identity authorization grant obtained")
	}
	return grant, obtained, err
}

// identityGrantCredential holds this user's identity record while obtaining one
// grant. Resource token storage is a separate record owned by the resource client.
func (i *EnterpriseIdentity) identityGrantCredential(ctx context.Context, audience, resource string, scopes []string, record AuthorizationCredential) (*genidentitygrants.IdentityGrant, time.Time, error) {
	issuer, err := discoverAuthorizationIssuer(ctx, i.client, i.registration.issuer)
	if err != nil {
		return nil, time.Time{}, err
	}
	if err := i.registration.validateIssuer(issuer); err != nil {
		return nil, time.Time{}, err
	}
	if len(issuer.IdentityChainingRequestedTokenTypesSupported) > 0 && !slices.Contains(issuer.IdentityChainingRequestedTokenTypesSupported, oauthIdentityGrantType) {
		return nil, time.Time{}, errors.New("mcp: identity provider advertises no identity authorization grant support")
	}
	if err := i.registration.validateEnterpriseMetadata(ctx, i.client, oauthIdentityExchange); err != nil {
		return nil, time.Time{}, err
	}
	credential, kind, err := i.subject(ctx, issuer, record)
	if err != nil {
		return nil, time.Time{}, err
	}
	grant, obtained, err := i.registration.exchangeIdentity(ctx, i.client, issuer, kind, credential, audience, resource, scopes)
	if err != nil {
		return nil, time.Time{}, err
	}
	return grant, obtained, nil
}

// subject obtains a current host credential or loads SAML bootstrap from this
// user's store. An expired or pending bootstrap asks for a fresh host assertion.
func (i *EnterpriseIdentity) subject(ctx context.Context, issuer *genissuermetadata.ReadResult, record AuthorizationCredential) (string, string, error) {
	if i.kind == enterpriseSAML {
		credential, err := i.samlSubject(ctx, issuer, record)
		return credential, oauthIdentityRefreshType, err
	}
	credential, err := i.source(ctx)
	if err != nil {
		return "", "", authorizationFailure(ctx, "host identity credential", err)
	}
	if credential == "" || strings.ContainsAny(credential, "\r\n") {
		return "", "", errors.New("mcp: host identity credential is empty or contains line breaks")
	}
	subjectType := oauthIdentityIDTokenType
	if i.kind == enterpriseRefresh {
		subjectType = oauthIdentityRefreshType
	}
	return credential, subjectType, nil
}

// samlSubject loads a generated identity record before reusing its credential.
// A new bootstrap records its pending state before requesting a fresh assertion,
// then saves the validated refresh credential before any resource grant can use it.
func (i *EnterpriseIdentity) samlSubject(ctx context.Context, issuer *genissuermetadata.ReadResult, record AuthorizationCredential) (string, error) {
	data, exists, err := record.Load()
	if err != nil {
		return "", authorizationFailure(ctx, "identity credential load", err)
	}
	state := &genidentitygrants.IdentityCredentialState{Binding: slices.Clone(i.binding), State: genidentitygrants.NewStatePending("exchange")}
	if exists {
		state, err = genidentitygrants.DecodeIdentityCredentialState(data)
		if err != nil {
			return "", authorizationFailure(ctx, "stored identity credential validation", err)
		}
		if !slices.Equal(state.Binding, i.binding) {
			return "", errors.New("mcp: stored identity credential belongs to another authorization owner")
		}
		if ready, available := state.State.AsReady(); available {
			obtained, err := time.Parse(time.RFC3339Nano, ready.Obtained)
			if err != nil {
				return "", authorizationFailure(ctx, "stored identity credential time", err)
			}
			if ready.Credential.ExpiresIn == nil || int64(time.Since(obtained)/time.Second) < *ready.Credential.ExpiresIn {
				return ready.Credential.AccessToken, nil
			}
		}
	}
	state.State = genidentitygrants.NewStatePending("exchange")
	if err := saveIdentityCredential(ctx, record, state); err != nil {
		return "", err
	}
	credential, err := i.source(ctx)
	if err != nil {
		return "", authorizationFailure(ctx, "fresh host SAML credential", err)
	}
	if credential == "" {
		return "", errors.New("mcp: fresh host SAML credential is empty")
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(credential))
	refresh, obtained, err := i.registration.bootstrapIdentity(ctx, i.client, issuer, encoded)
	if err != nil {
		return "", err
	}
	if refresh.ExpiresIn != nil && int64(time.Since(obtained)/time.Second) >= *refresh.ExpiresIn {
		return "", errors.New("mcp: identity provider returned an expired refresh credential")
	}
	state.State = genidentitygrants.NewStateReady(&genidentitygrants.IdentityCredentialReady{Credential: refresh, Obtained: obtained.Format(time.RFC3339Nano)})
	if err := saveIdentityCredential(ctx, record, state); err != nil {
		return "", err
	}
	return refresh.AccessToken, nil
}

// saveIdentityCredential commits only a generated, validated private record.
// Storage or encoding failures stop before its identity credential can be used.
func saveIdentityCredential(ctx context.Context, record AuthorizationCredential, state *genidentitygrants.IdentityCredentialState) error {
	data, err := genidentitygrants.EncodeIdentityCredentialState(state)
	if err != nil {
		return authorizationFailure(ctx, "identity credential encoding", err)
	}
	if err := record.Save(data); err != nil {
		return authorizationFailure(ctx, "identity credential save", err)
	}
	return nil
}

// validateIssuer checks registered resource authentication and mandatory metadata
// relationships when the optional identity-grant profile is advertised.
func (g *enterpriseGrant) validateIssuer(issuer *genissuermetadata.ReadResult) error {
	if slices.Contains(issuer.AuthorizationGrantProfilesSupported, oauthIdentityProfile) && !slices.Contains(issuer.GrantTypesSupported, oauthIdentityRedemption) {
		return errors.New("mcp: resource issuer's identity profile requires JWT authorization grants")
	}
	return g.registration.validateIssuer(issuer)
}

// acquire obtains a fresh IdP grant and redeems it at the resource issuer. Scope
// narrowing survives an omitted final scope; browser refresh is never substituted.
func (g *enterpriseGrant) acquire(ctx context.Context, client *http.Client, resource string, issuer *genissuermetadata.ReadResult, scopes []string, _ *genaccesstokens.BearerToken, records []AuthorizationCredential) (*genaccesstokens.BearerToken, time.Time, error) {
	if err := g.registration.validateEnterpriseMetadata(ctx, client, oauthIdentityRedemption); err != nil {
		return nil, time.Time{}, err
	}
	grant, obtained, err := g.identity.identityGrant(ctx, g.registration.issuer.String(), resource, scopes, records[1])
	if err != nil {
		return nil, time.Time{}, err
	}
	permissions := scopes
	if grant.Scope != nil {
		permissions = strings.Split(*grant.Scope, " ")
	}
	token, issued, err := g.registration.redeemIdentity(ctx, client, issuer, resource, grant, obtained, permissions)
	if err != nil {
		return nil, time.Time{}, err
	}
	if token.Scope == nil && grant.Scope != nil {
		retained := *grant.Scope
		token.Scope = &retained
	}
	return token, issued, nil
}

// recoversChallenges allows the existing transport to replace a rejected token
// once in a request round. Stream loss remains a separate execution-outcome check.
func (g *enterpriseGrant) recoversChallenges() bool {
	return true
}

// validateEnterpriseMetadata checks the configured grant in a registration
// document when one exists. Preregistered clients need no document discovery.
func (r *ClientRegistration) validateEnterpriseMetadata(ctx context.Context, client *http.Client, grant string) error {
	if r.metadata == nil {
		return nil
	}
	metadata, err := r.readMetadata(ctx, client)
	if err != nil {
		return err
	}
	if !slices.Contains(metadata.grants, grant) {
		return errors.New("mcp: client metadata does not register the enterprise grant")
	}
	return nil
}

// credentialBindings separates resource issuers and the host identity registration
// while retaining one user's storage namespace across process restarts.
func (g *enterpriseGrant) credentialBindings(resource string) [][]string {
	return [][]string{
		slices.Concat([]string{"resource", "enterprise", g.registration.issuer.String(), g.registration.clientID, g.registration.authentication}, g.identity.binding, []string{resource}),
		slices.Clone(g.identity.binding),
	}
}
