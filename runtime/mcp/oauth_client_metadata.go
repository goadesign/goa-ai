// Package mcp checks self-hosted client registration before browser consent.
// Native Goa clients decode the selected authentication profile; this file
// checks exact client identity, callback membership and public-key registration.
// The authorization server resolves key URLs and verifies client assertions.
package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	genclientmetadata "goa.design/goa-ai/internal/mcpauth/gen/client_metadata"
	genclientmetadataclient "goa.design/goa-ai/internal/mcpauth/gen/http/client_metadata/client"
)

// clientMetadataAddress rejects identifiers that cannot name one exact HTTPS
// registration document. Public and signed registrations use the same rule.
func clientMetadataAddress(identifier string) (*url.URL, error) {
	address, err := authorizationURL(identifier, false)
	if err != nil || address.Path == "" {
		return nil, errors.New("mcp: client metadata identifier must be an exact HTTPS URL with a path")
	}
	for _, segment := range strings.Split(address.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("mcp: client metadata identifier cannot contain dot path segments")
		}
	}
	return address, nil
}

// validateBrowserRegistration checks relationships that field validation cannot
// express. A matching document returns whether refresh is registered; another
// identity, callback, grant or a shared-secret member stops before consent.
func validateBrowserRegistration(client AuthorizationCode, identifier string, redirects, grants, responses []string, secretPresent bool) (bool, error) {
	if identifier != client.ClientID || !slices.Contains(redirects, client.RedirectURI) ||
		!slices.Contains(grants, "authorization_code") || !slices.Contains(responses, "code") || secretPresent {
		return false, errors.New("mcp: client metadata does not bind this client and redirect")
	}
	return slices.Contains(grants, "refresh_token"), nil
}

// validateMetadata reads the selected registration through its native generated
// decoder. It returns refresh permission after checking the browser binding and,
// for signed authentication, exactly one source of public keys.
func (g *authorizationCodeGrant) validateMetadata(ctx context.Context, client *http.Client) (bool, error) {
	generated := genclientmetadataclient.NewClient(g.metadata.Scheme, g.metadata.Host, &authorizationDoer{client: client, address: g.metadata, operation: "client_metadata"}, nil, authorizationDecoder, false)
	if g.signed == nil {
		value, err := generated.Read()(ctx, nil)
		if err != nil {
			return false, authorizationFailure(ctx, "client metadata", err)
		}
		metadata, ok := value.(*genclientmetadata.ReadResult)
		if !ok {
			return false, errors.New("mcp: client metadata has an invalid result type")
		}
		return validateBrowserRegistration(g.client, metadata.ClientID, metadata.RedirectUris, metadata.GrantTypes, metadata.ResponseTypes, metadata.ClientSecret != nil || metadata.ClientSecretExpiresAt != nil)
	}
	value, err := generated.SignedRead()(ctx, nil)
	if err != nil {
		return false, authorizationFailure(ctx, "signed client metadata", err)
	}
	metadata, ok := value.(*genclientmetadata.SignedReadResult)
	if !ok {
		return false, errors.New("mcp: signed client metadata has an invalid result type")
	}
	if (metadata.JwksURI == nil) == (metadata.Jwks == nil) {
		return false, errors.New("mcp: signed client metadata requires exactly one public-key source")
	}
	if metadata.JwksURI != nil {
		if _, err := authorizationURL(*metadata.JwksURI, false); err != nil {
			return false, errors.New("mcp: client public-key address must identify an exact HTTPS URL")
		}
	} else {
		if len(metadata.Jwks.Keys) == 0 {
			return false, errors.New("mcp: inline client keys must contain public keys")
		}
		for _, key := range metadata.Jwks.Keys {
			if !key.Valid() || !key.IsPublic() {
				return false, errors.New("mcp: inline client keys must contain only valid public keys")
			}
		}
	}
	return validateBrowserRegistration(g.client, metadata.ClientID, metadata.RedirectUris, metadata.GrantTypes, metadata.ResponseTypes, metadata.ClientSecret != nil || metadata.ClientSecretExpiresAt != nil)
}
