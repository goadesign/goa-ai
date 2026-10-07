// Package mcp constructs immutable OAuth application registrations. Registrations
// choose one issuer and authentication method; grants own user, resource, scope
// and token state separately. Sharing a registration never shares access tokens.
package mcp

import (
	"errors"
	"fmt"
	"net/url"
	"slices"

	genissuermetadata "goa.design/goa-ai/internal/mcpauth/gen/issuer_metadata"
)

type (
	// ClientRegistration identifies an application registered with one issuer.
	// Construct it with an explicit authentication constructor. It can be shared
	// across transports; each transport retains its own user and resource tokens.
	ClientRegistration struct {
		issuer         *url.URL
		clientID       string
		authentication string
		secret         string
		signed         *clientAssertionAuthentication
		metadata       *url.URL
	}
)

const (
	oauthPublicClient = "none"
	oauthBasicClient  = "client_secret_basic"
	oauthSecretClient = "client_secret_post"
	oauthSignedClient = "private_key_jwt"
)

// NewPublicClientRegistration constructs an application that has no client secret.
func NewPublicClientRegistration(issuer, clientID string) (*ClientRegistration, error) {
	return newClientRegistration(issuer, clientID, oauthPublicClient, "")
}

// NewBasicClientRegistration constructs an application using client_secret_basic.
// Native Goa security sends separately form-encoded identity and secret in the
// Basic header; neither credential is duplicated in the token request body.
func NewBasicClientRegistration(issuer, clientID, secret string) (*ClientRegistration, error) {
	return newClientRegistration(issuer, clientID, oauthBasicClient, secret)
}

// NewSecretClientRegistration constructs an application using client_secret_post.
// Generated forms send its identity and secret only to its issuer's token endpoint.
func NewSecretClientRegistration(issuer, clientID, secret string) (*ClientRegistration, error) {
	return newClientRegistration(issuer, clientID, oauthSecretClient, secret)
}

// NewSignedClientRegistration constructs an application using private_key_jwt.
// Each token request receives fresh authentication from the registered signer.
func NewSignedClientRegistration(config ClientAssertion) (*ClientRegistration, error) {
	if err := validateClientAssertion(config); err != nil {
		return nil, err
	}
	registration, err := newClientRegistration(config.Issuer, config.ClientID, oauthSignedClient, "")
	if err != nil {
		return nil, err
	}
	registration.signed = &clientAssertionAuthentication{credentials: config}
	return registration, nil
}

// NewPublicClientMetadataRegistration constructs a public application whose
// identity is its HTTPS metadata document. Grants validate the document and
// issuer support before requesting credentials; dynamic registration is not used.
func NewPublicClientMetadataRegistration(issuer, identifier string) (*ClientRegistration, error) {
	registration, err := NewPublicClientRegistration(issuer, identifier)
	if err != nil {
		return nil, err
	}
	registration.metadata, err = clientMetadataAddress(identifier)
	if err != nil {
		return nil, err
	}
	return registration, nil
}

// NewSignedClientMetadataRegistration constructs a signed application whose
// ClientID is its HTTPS metadata document. The document must register signed
// authentication and exactly one public-key source before any grant is requested.
func NewSignedClientMetadataRegistration(config ClientAssertion) (*ClientRegistration, error) {
	registration, err := NewSignedClientRegistration(config)
	if err != nil {
		return nil, err
	}
	registration.metadata, err = clientMetadataAddress(config.ClientID)
	if err != nil {
		return nil, err
	}
	return registration, nil
}

// newClientRegistration checks host input once. Its private fields prevent grant
// callers from changing an issuer or authentication method after construction.
func newClientRegistration(issuer, clientID, authentication, secret string) (*ClientRegistration, error) {
	address, err := authorizationURL(issuer, true)
	if err != nil {
		return nil, fmt.Errorf("mcp: authorization issuer: %w", err)
	}
	if clientID == "" {
		return nil, errors.New("mcp: registered client identifier is required")
	}
	if (authentication == oauthBasicClient || authentication == oauthSecretClient) && secret == "" {
		return nil, errors.New("mcp: registered client secret is required")
	}
	return &ClientRegistration{issuer: address, clientID: clientID, authentication: authentication, secret: secret}, nil
}

// validateClientRegistration rejects missing or unconstructed host input before
// a grant can use the private registration fields as established invariants.
func validateClientRegistration(registration *ClientRegistration) error {
	if registration == nil || registration.issuer == nil {
		return errors.New("mcp: a constructed client registration is required")
	}
	return nil
}

// validateIssuer requires the configured authentication method and metadata
// registration support. It never infers another method or relocates credentials.
func (r *ClientRegistration) validateIssuer(issuer *genissuermetadata.ReadResult) error {
	if !slices.Contains(issuer.TokenEndpointAuthMethodsSupported, r.authentication) {
		return errors.New("mcp: issuer does not advertise the registered authentication method")
	}
	if r.signed != nil {
		if err := validateAssertionAuthentication(issuer); err != nil {
			return err
		}
	}
	if r.metadata != nil && (issuer.ClientIDMetadataDocumentSupported == nil || !*issuer.ClientIDMetadataDocumentSupported) {
		return errors.New("mcp: issuer does not support client metadata documents")
	}
	return nil
}
