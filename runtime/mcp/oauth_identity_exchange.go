// Package mcp sends enterprise token exchanges through native Goa clients.
// Constructed registrations select exact authentication, and constructed user
// identities select the source credential purpose. Generated responses keep
// identity grants and SAML refresh credentials separate from MCP bearer tokens.
package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
	genidentityclient "goa.design/goa-ai/internal/mcpauth/gen/http/identity_grants/client"
	genidentitygrants "goa.design/goa-ai/internal/mcpauth/gen/identity_grants"
	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
	goahttp "goa.design/goa/v3/http"
)

// exchangeIdentity sends the host's identity credential only to its IdP, asking
// for a grant bound to the exact resource issuer, MCP resource and permissions.
func (r *ClientRegistration) exchangeIdentity(ctx context.Context, client *http.Client, issuer *genissuermetadata.ReadResult, subjectType, subject, audience, resource string, scopes []string) (*genidentitygrants.IdentityGrant, time.Time, error) {
	address, assertion, err := r.tokenRequest(ctx, issuer)
	if err != nil {
		return nil, time.Time{}, err
	}
	generated := genidentityclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: client, address: address, operation: "identity_exchange"}, goahttp.RequestEncoder, authorizationDecoder, false)
	var scope *string
	if len(scopes) > 0 {
		permissions := strings.Join(scopes, " ")
		scope = &permissions
	}
	obtained := time.Now()
	var value any
	switch r.authentication {
	case oauthPublicClient:
		value, err = generated.Exchange()(ctx, &genidentitygrants.ExchangePayload{
			ClientID:         r.clientID,
			SubjectTokenType: subjectType,
			SubjectToken:     subject,
			Audience:         audience,
			Resource:         resource,
			Scope:            scope,
		})
	case oauthBasicClient:
		value, err = generated.BasicExchange()(ctx, &genidentitygrants.BasicExchangePayload{
			ClientID:         url.QueryEscape(r.clientID),
			ClientSecret:     url.QueryEscape(r.secret),
			SubjectTokenType: subjectType,
			SubjectToken:     subject,
			Audience:         audience,
			Resource:         resource,
			Scope:            scope,
		})
	case oauthSecretClient:
		value, err = generated.SecretExchange()(ctx, &genidentitygrants.SecretExchangePayload{
			ClientID:         r.clientID,
			ClientSecret:     r.secret,
			SubjectTokenType: subjectType,
			SubjectToken:     subject,
			Audience:         audience,
			Resource:         resource,
			Scope:            scope,
		})
	case oauthSignedClient:
		value, err = generated.SignedExchange()(ctx, &genidentitygrants.SignedExchangePayload{
			ClientAssertion:  assertion,
			SubjectTokenType: subjectType,
			SubjectToken:     subject,
			Audience:         audience,
			Resource:         resource,
			Scope:            scope,
		})
	}
	if err != nil {
		return nil, time.Time{}, authorizationFailure(ctx, "identity exchange", err)
	}
	grant, ok := value.(*genidentitygrants.IdentityGrant)
	if !ok {
		return nil, time.Time{}, errors.New("mcp: identity exchange has an invalid result type")
	}
	return grant, obtained, nil
}

// bootstrapIdentity exchanges one encoded SAML assertion for a private IdP
// refresh credential. This response can never become an MCP bearer token.
func (r *ClientRegistration) bootstrapIdentity(ctx context.Context, client *http.Client, issuer *genissuermetadata.ReadResult, subject string) (*genidentitygrants.IdentityRefresh, time.Time, error) {
	address, assertion, err := r.tokenRequest(ctx, issuer)
	if err != nil {
		return nil, time.Time{}, err
	}
	generated := genidentityclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: client, address: address, operation: "identity_bootstrap"}, goahttp.RequestEncoder, authorizationDecoder, false)
	obtained := time.Now()
	var value any
	switch r.authentication {
	case oauthPublicClient:
		value, err = generated.Saml()(ctx, &genidentitygrants.SamlPayload{
			ClientID:     r.clientID,
			SubjectToken: subject,
		})
	case oauthBasicClient:
		value, err = generated.BasicSaml()(ctx, &genidentitygrants.BasicSamlPayload{
			ClientID:     url.QueryEscape(r.clientID),
			ClientSecret: url.QueryEscape(r.secret),
			SubjectToken: subject,
		})
	case oauthSecretClient:
		value, err = generated.SecretSaml()(ctx, &genidentitygrants.SecretSamlPayload{
			ClientID:     r.clientID,
			ClientSecret: r.secret,
			SubjectToken: subject,
		})
	case oauthSignedClient:
		value, err = generated.SignedSaml()(ctx, &genidentitygrants.SignedSamlPayload{
			ClientAssertion: assertion,
			SubjectToken:    subject,
		})
	}
	if err != nil {
		return nil, time.Time{}, authorizationFailure(ctx, "identity bootstrap", err)
	}
	credential, ok := value.(*genidentitygrants.IdentityRefresh)
	if !ok {
		return nil, time.Time{}, errors.New("mcp: identity bootstrap has an invalid result type")
	}
	return credential, obtained, nil
}

// redeemIdentity authenticates the independent resource registration and sends
// one still-valid IdP grant. Only the returned resource token enters MCP traffic.
func (r *ClientRegistration) redeemIdentity(ctx context.Context, client *http.Client, issuer *genissuermetadata.ReadResult, resource string, grant *genidentitygrants.IdentityGrant, obtained time.Time, scopes []string) (*genaccesstokens.BearerToken, time.Time, error) {
	generated, assertion, err := r.tokenClient(ctx, client, issuer, "identity_redemption")
	if err != nil {
		return nil, time.Time{}, err
	}
	if grant.ExpiresIn != nil && int64(time.Since(obtained)/time.Second) >= *grant.ExpiresIn {
		return nil, time.Time{}, errors.New("mcp: identity provider returned an expired authorization grant")
	}
	var scope *string
	if len(scopes) > 0 {
		permissions := strings.Join(scopes, " ")
		scope = &permissions
	}
	issued := time.Now()
	var value any
	switch r.authentication {
	case oauthPublicClient:
		value, err = generated.Redeem()(ctx, &genaccesstokens.RedeemPayload{
			ClientID:  r.clientID,
			Assertion: grant.AccessToken,
			Resource:  resource,
			Scope:     scope,
		})
	case oauthBasicClient:
		value, err = generated.BasicRedeem()(ctx, &genaccesstokens.BasicRedeemPayload{
			ClientID:     url.QueryEscape(r.clientID),
			ClientSecret: url.QueryEscape(r.secret),
			Assertion:    grant.AccessToken,
			Resource:     resource,
			Scope:        scope,
		})
	case oauthSecretClient:
		value, err = generated.SecretRedeem()(ctx, &genaccesstokens.SecretRedeemPayload{
			ClientID:     r.clientID,
			ClientSecret: r.secret,
			Assertion:    grant.AccessToken,
			Resource:     resource,
			Scope:        scope,
		})
	case oauthSignedClient:
		value, err = generated.SignedRedeem()(ctx, &genaccesstokens.SignedRedeemPayload{
			ClientAssertion: assertion,
			Assertion:       grant.AccessToken,
			Resource:        resource,
			Scope:           scope,
		})
	}
	return registeredTokenResult(ctx, "identity redemption", value, issued, err)
}
