// Package mcp checks self-hosted client registration before any token grant.
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

type (
	// clientRegistrationMetadata retains only relationships used to authorize grants.
	// Wire decoding and authentication selection remain in generated contracts.
	clientRegistrationMetadata struct {
		redirects []string
		grants    []string
		responses []string
	}
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

// validateMetadata checks the browser callback and grant against the registration
// document. A machine registration may have no redirects; a browser must match one.
func (g *authorizationCodeGrant) validateMetadata(ctx context.Context, client *http.Client) (bool, error) {
	metadata, err := g.client.Registration.readMetadata(ctx, client)
	if err != nil {
		return false, err
	}
	if !slices.Contains(metadata.redirects, g.client.RedirectURI) || !slices.Contains(metadata.grants, "authorization_code") || !slices.Contains(metadata.responses, "code") {
		return false, errors.New("mcp: client metadata does not bind this browser grant and redirect")
	}
	return slices.Contains(metadata.grants, "refresh_token"), nil
}

// readMetadata uses the registration's native decoder and checks its exact identity,
// forbidden secrets and public keys. It returns grant relationships, never keys or tokens.
func (r *ClientRegistration) readMetadata(ctx context.Context, client *http.Client) (*clientRegistrationMetadata, error) {
	generated := genclientmetadataclient.NewClient(r.metadata.Scheme, r.metadata.Host, &authorizationDoer{client: client, address: r.metadata, operation: "client_metadata"}, nil, authorizationDecoder, false)
	if r.signed == nil {
		value, err := generated.Read()(ctx, nil)
		if err != nil {
			return nil, authorizationFailure(ctx, "client metadata", err)
		}
		metadata, ok := value.(*genclientmetadata.ReadResult)
		if !ok {
			return nil, errors.New("mcp: client metadata has an invalid result type")
		}
		if metadata.ClientID != r.clientID || metadata.ClientSecret != nil || metadata.ClientSecretExpiresAt != nil {
			return nil, errors.New("mcp: client metadata has another identity or forbidden secret")
		}
		return &clientRegistrationMetadata{redirects: metadata.RedirectUris, grants: metadata.GrantTypes, responses: metadata.ResponseTypes}, nil
	}
	value, err := generated.SignedRead()(ctx, nil)
	if err != nil {
		return nil, authorizationFailure(ctx, "signed client metadata", err)
	}
	metadata, ok := value.(*genclientmetadata.SignedReadResult)
	if !ok {
		return nil, errors.New("mcp: signed client metadata has an invalid result type")
	}
	if metadata.ClientID != r.clientID || metadata.ClientSecret != nil || metadata.ClientSecretExpiresAt != nil {
		return nil, errors.New("mcp: client metadata has another identity or forbidden secret")
	}
	if (metadata.JwksURI == nil) == (metadata.Jwks == nil) {
		return nil, errors.New("mcp: signed client metadata requires exactly one public-key source")
	}
	if metadata.JwksURI != nil {
		if _, err := authorizationURL(*metadata.JwksURI, false); err != nil {
			return nil, errors.New("mcp: client public-key address must identify an exact HTTPS URL")
		}
	} else {
		if len(metadata.Jwks.Keys) == 0 {
			return nil, errors.New("mcp: inline client keys must contain public keys")
		}
		for _, key := range metadata.Jwks.Keys {
			if !key.Valid() || !key.IsPublic() {
				return nil, errors.New("mcp: inline client keys must contain only valid public keys")
			}
		}
	}
	return &clientRegistrationMetadata{redirects: metadata.RedirectUris, grants: metadata.GrantTypes, responses: metadata.ResponseTypes}, nil
}
