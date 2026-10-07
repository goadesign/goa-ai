// Package mcp sends OAuth grants through the native contract selected by a
// constructed registration. Goa encodes forms and Basic headers and validates
// responses. This file owns method selection and fresh signed authentication;
// it never constructs JSON or chooses authentication from field presence.
package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
	gentokenclient "goa.design/goa-ai/internal/mcpauth/gen/http/access_tokens/client"
	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
	goahttp "goa.design/goa/v3/http"
)

// registeredTokenResult checks the generated endpoint's result type and hides
// issuer response details on failure. Only a validated bearer token is returned.
func registeredTokenResult(ctx context.Context, operation string, value any, obtained time.Time, err error) (*genaccesstokens.BearerToken, time.Time, error) {
	if err != nil {
		return nil, time.Time{}, authorizationFailure(ctx, operation, err)
	}
	token, ok := value.(*genaccesstokens.BearerToken)
	if !ok {
		return nil, time.Time{}, errors.New("mcp: token response has an invalid result type")
	}
	return token, obtained, nil
}

// tokenClient checks the exact token address and signs fresh authentication when
// required. The returned native client sends only to that address without redirects.
func (r *ClientRegistration) tokenClient(ctx context.Context, client *http.Client, issuer *genissuermetadata.ReadResult, operation string) (*gentokenclient.Client, string, error) {
	address, err := authorizationURL(issuer.TokenEndpoint, false)
	if err != nil {
		return nil, "", err
	}
	var assertion string
	if r.signed != nil {
		assertion, err = r.signed.assertion(ctx, issuer)
		if err != nil {
			return nil, "", err
		}
	}
	generated := gentokenclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: client, address: address, operation: operation}, goahttp.RequestEncoder, authorizationDecoder, false)
	return generated, assertion, nil
}

// machine sends resource permissions with exactly the configured confidential
// authentication. The grant constructor has already rejected public registrations.
func (r *ClientRegistration) machine(ctx context.Context, client *http.Client, issuer *genissuermetadata.ReadResult, resource string, scopes []string) (*genaccesstokens.BearerToken, time.Time, error) {
	generated, assertion, err := r.tokenClient(ctx, client, issuer, "token_exchange")
	if err != nil {
		return nil, time.Time{}, err
	}
	var scope *string
	if len(scopes) > 0 {
		permissions := strings.Join(scopes, " ")
		scope = &permissions
	}
	obtained := time.Now()
	var value any
	switch r.authentication {
	case oauthBasicClient:
		value, err = generated.Basic()(ctx, &genaccesstokens.BasicPayload{ClientID: url.QueryEscape(r.clientID), ClientSecret: url.QueryEscape(r.secret), Resource: resource, Scope: scope})
	case oauthSecretClient:
		value, err = generated.Secret()(ctx, &genaccesstokens.SecretPayload{ClientID: r.clientID, ClientSecret: r.secret, Resource: resource, Scope: scope})
	case oauthSignedClient:
		value, err = generated.Assertion()(ctx, &genaccesstokens.AssertionPayload{ClientAssertion: assertion, Resource: resource, Scope: scope})
	}
	return registeredTokenResult(ctx, "token exchange", value, obtained, err)
}

// code exchanges a checked callback code using its original PKCE verifier and
// resource. Registration authentication is independent of browser consent.
func (r *ClientRegistration) code(ctx context.Context, client *http.Client, issuer *genissuermetadata.ReadResult, resource, code, verifier, redirect string) (*genaccesstokens.BearerToken, time.Time, error) {
	generated, assertion, err := r.tokenClient(ctx, client, issuer, "token_exchange")
	if err != nil {
		return nil, time.Time{}, err
	}
	obtained := time.Now()
	var value any
	switch r.authentication {
	case oauthPublicClient:
		value, err = generated.Code()(ctx, &genaccesstokens.CodePayload{ClientID: r.clientID, Code: code, CodeVerifier: verifier, RedirectURI: redirect, Resource: resource})
	case oauthBasicClient:
		value, err = generated.BasicCode()(ctx, &genaccesstokens.BasicCodePayload{ClientID: url.QueryEscape(r.clientID), ClientSecret: url.QueryEscape(r.secret), Code: code, CodeVerifier: verifier, RedirectURI: redirect, Resource: resource})
	case oauthSecretClient:
		value, err = generated.SecretCode()(ctx, &genaccesstokens.SecretCodePayload{ClientID: r.clientID, ClientSecret: r.secret, Code: code, CodeVerifier: verifier, RedirectURI: redirect, Resource: resource})
	case oauthSignedClient:
		value, err = generated.SignedCode()(ctx, &genaccesstokens.SignedCodePayload{ClientAssertion: assertion, Code: code, CodeVerifier: verifier, RedirectURI: redirect, Resource: resource})
	}
	return registeredTokenResult(ctx, "code exchange", value, obtained, err)
}

// refresh replaces a resource token with the same registration and private
// refresh credential. The grant owner applies rotation and omitted-field rules.
func (r *ClientRegistration) refresh(ctx context.Context, client *http.Client, issuer *genissuermetadata.ReadResult, resource, refresh string) (*genaccesstokens.BearerToken, time.Time, error) {
	generated, assertion, err := r.tokenClient(ctx, client, issuer, "token_refresh")
	if err != nil {
		return nil, time.Time{}, err
	}
	obtained := time.Now()
	var value any
	switch r.authentication {
	case oauthPublicClient:
		value, err = generated.Refresh()(ctx, &genaccesstokens.RefreshPayload{ClientID: r.clientID, RefreshToken: refresh, Resource: resource})
	case oauthBasicClient:
		value, err = generated.BasicRefresh()(ctx, &genaccesstokens.BasicRefreshPayload{ClientID: url.QueryEscape(r.clientID), ClientSecret: url.QueryEscape(r.secret), RefreshToken: refresh, Resource: resource})
	case oauthSecretClient:
		value, err = generated.SecretRefresh()(ctx, &genaccesstokens.SecretRefreshPayload{ClientID: r.clientID, ClientSecret: r.secret, RefreshToken: refresh, Resource: resource})
	case oauthSignedClient:
		value, err = generated.SignedRefresh()(ctx, &genaccesstokens.SignedRefreshPayload{ClientAssertion: assertion, RefreshToken: refresh, Resource: resource})
	}
	return registeredTokenResult(ctx, "token refresh", value, obtained, err)
}
