// This file validates the referee's signed host identity before passing it to
// production enterprise authorization. The fixture supplies the trusted key,
// issuer, client audience and user; an MCP discovery response cannot change them.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"goa.design/goa-ai/runtime/mcp"
)

// refereeEnterpriseTransport gives the identity and resource issuers separate
// registrations. Their generated token clients exchange a verified host identity
// for the only bearer credential that reaches the MCP server.
func refereeEnterpriseTransport(
	endpoint string,
	client *http.Client,
	info mcp.ClientInfo,
	registration *mcp.ClientRegistration,
	configuration authorizationContext,
) (*mcp.HTTPTransport, error) {
	identityRegistration, err := mcp.NewPublicClientRegistration(configuration.IDPIssuer, configuration.IDPClientID)
	if err != nil {
		return nil, err
	}
	source, err := refereeIdentitySource(configuration)
	if err != nil {
		return nil, err
	}
	identity, err := mcp.NewIDTokenEnterpriseIdentity(identityRegistration, client, mcp.NewMemoryAuthorizationStore(), source)
	if err != nil {
		return nil, err
	}
	return mcp.NewEnterpriseHTTPTransport(mcp.HTTPOptions{
		Endpoint:   endpoint,
		Client:     client,
		ClientInfo: info,
	}, mcp.EnterpriseAuthorization{
		Identity:     identity,
		Registration: registration,
	})
}

// refereeIdentitySource checks the supplied key once and validates the token
// whenever production authorization requests the host's current identity.
// Errors describe the rejected contract without printing tokens or claims.
func refereeIdentitySource(configuration authorizationContext) (func(context.Context) (string, error), error) {
	if configuration.IDPIssuer == "" || configuration.IDPClientID == "" || configuration.IDPSubject == "" {
		return nil, errors.New("referee identity requires its configured issuer, client and user")
	}
	block, remaining := pem.Decode([]byte(configuration.IDPPublicKeyPEM))
	if block == nil || block.Type != "PUBLIC KEY" || len(remaining) != 0 {
		return nil, errors.New("referee identity requires one trusted public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("referee identity has an invalid public key")
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("referee identity requires a P-256 public key")
	}
	token, err := jwt.ParseSigned(configuration.IDPToken, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return nil, errors.New("referee identity requires a signed ES256 token")
	}
	var claims jwt.Claims
	if err := token.Claims(key, &claims); err != nil {
		return nil, errors.New("referee identity signature or claims are invalid")
	}
	if claims.Expiry == nil || claims.IssuedAt == nil {
		return nil, errors.New("referee identity requires issuance and expiry times")
	}
	return func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		now := time.Now()
		expected := jwt.Expected{
			Issuer:      configuration.IDPIssuer,
			Subject:     configuration.IDPSubject,
			AnyAudience: jwt.Audience{configuration.IDPClientID},
			Time:        now,
		}
		if err := claims.ValidateWithLeeway(expected, 0); err != nil || !now.Before(claims.Expiry.Time()) {
			return "", errors.New("referee identity does not match the configured host or validity")
		}
		return configuration.IDPToken, nil
	}, nil
}
