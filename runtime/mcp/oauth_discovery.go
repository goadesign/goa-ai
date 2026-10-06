// Package mcp discovers protected-resource metadata through generated HTTP
// clients. Well-known discovery and challenge URLs use the same exact decoding,
// resource checks and issuer binding; neither path sends a client credential.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"

	genresourceclient "goa.design/goa-ai/internal/mcpauth/gen/http/resource_metadata/client"
	genresourcemetadata "goa.design/goa-ai/internal/mcpauth/gen/resource_metadata"
)

// discoverProtectedResource reads specified metadata locations. If neither
// well-known address exists, it asks only server/discover for a challenge; it
// never calls a domain tool to discover credentials or permission requirements.
func discoverProtectedResource(ctx context.Context, client *http.Client, resource, issuer *url.URL, info ClientInfo) (*genresourcemetadata.ReadResult, []string, error) {
	for _, address := range resourceMetadataAddresses(resource) {
		metadata, err := readProtectedResource(ctx, client, address)
		if metadataMissing(err) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		bound, err := bindProtectedResource(metadata, resource, issuer)
		return bound, nil, err
	}
	transport := NewHTTPTransport(client, info, nil, InputSupport{}, HTTPRetryPolicy{})
	var ignored json.RawMessage
	err := transport.call(ctx, resource.String(), "server/discover", map[string]any{}, &ignored)
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	var response *HTTPResponseError
	if !errors.As(err, &response) || response.StatusCode != http.StatusUnauthorized {
		return nil, nil, errors.New("mcp: resource metadata is absent and discovery did not return an authorization challenge")
	}
	challenges, err := oauthChallenges(response.WWWAuthenticate)
	if err != nil {
		return nil, nil, err
	}
	metadata, challenge, err := challengedProtectedResource(ctx, client, resource, issuer, challenges)
	return metadata, challenge.scopes, err
}

// challengedProtectedResource prioritizes advertised metadata over well-known
// locations. Several HTTP realms remain separate; only a document that binds
// the configured resource and issuer supplies this operation's permissions.
func challengedProtectedResource(ctx context.Context, client *http.Client, resource, issuer *url.URL, challenges []oauthChallenge) (*genresourcemetadata.ReadResult, oauthChallenge, error) {
	advertised := false
	for _, challenge := range challenges {
		if challenge.metadata == nil {
			continue
		}
		advertised = true
		metadata, err := readProtectedResource(ctx, client, challenge.metadata)
		if err != nil {
			return nil, oauthChallenge{}, err
		}
		if metadata.Resource == resource.String() && slices.Contains(metadata.AuthorizationServers, issuer.String()) {
			return metadata, challenge, nil
		}
	}
	if advertised {
		return nil, oauthChallenge{}, errors.New("mcp: no challenged metadata binds the configured resource and issuer")
	}
	if len(challenges) != 1 {
		return nil, oauthChallenge{}, errors.New("mcp: authorization challenge does not identify one resource realm")
	}
	for _, address := range resourceMetadataAddresses(resource) {
		metadata, err := readProtectedResource(ctx, client, address)
		if metadataMissing(err) {
			continue
		}
		if err != nil {
			return nil, oauthChallenge{}, err
		}
		bound, err := bindProtectedResource(metadata, resource, issuer)
		return bound, challenges[0], err
	}
	return nil, oauthChallenge{}, errors.New("mcp: challenged resource metadata was not found")
}

// readProtectedResource uses Goa's generated response type and validations at
// the exact discovered URL. Only HTTP 404 retains its location-absent meaning;
// other failures return bounded errors without endpoint or response content.
func readProtectedResource(ctx context.Context, client *http.Client, address *url.URL) (*genresourcemetadata.ReadResult, error) {
	generated := genresourceclient.NewClient(address.Scheme, address.Host, &authorizationDoer{client: client, address: address, operation: "resource_metadata"}, nil, authorizationDecoder, false)
	value, err := generated.Read()(ctx, nil)
	if err != nil {
		return nil, authorizationFailure(ctx, "protected-resource metadata", err)
	}
	metadata, ok := value.(*genresourcemetadata.ReadResult)
	if !ok {
		return nil, errors.New("mcp: protected-resource metadata has an invalid result type")
	}
	return metadata, nil
}

// bindProtectedResource checks identities after well-known discovery. A wrong
// resource or issuer fails before any client secret is sent to a token endpoint.
func bindProtectedResource(metadata *genresourcemetadata.ReadResult, resource, issuer *url.URL) (*genresourcemetadata.ReadResult, error) {
	if metadata.Resource != resource.String() || !slices.Contains(metadata.AuthorizationServers, issuer.String()) {
		return nil, errors.New("mcp: protected-resource metadata does not bind the configured resource and issuer")
	}
	return metadata, nil
}
