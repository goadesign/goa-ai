// This file supplies the synthetic host integration for the referee's
// registered browser and machine scenarios. Production OAuth code owns discovery,
// PKCE,
// callback validation, token exchange and MCP authorization; this driver only
// supplies its registered identity, browser callback and trusted test CA.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-jose/go-jose/v4"

	"goa.design/goa-ai/runtime/mcp"
)

type (
	// authorizationContext supplies a fixture-owned issuer and synthetic registration.
	authorizationContext struct {
		Name             string `json:"name"`
		ClientID         string `json:"client_id"`
		ClientSecret     string `json:"client_secret"`
		Issuer           string `json:"issuer"`
		PrivateKeyPEM    string `json:"private_key_pem"`
		SigningAlgorithm string `json:"signing_algorithm"`
	}
)

const (
	preregisteredScenario = "auth/pre-registration"
	basicMachineScenario  = "auth/client-credentials-basic"
	signedMachineScenario = "auth/client-credentials-jwt"
)

// exerciseAuthorization calls the referee's tool through production OAuth.
// The outer referee bounds the driver process and retains its check results.
func exerciseAuthorization(endpoint string) error {
	var configuration authorizationContext
	if err := json.Unmarshal([]byte(os.Getenv("MCP_CONFORMANCE_CONTEXT")), &configuration); err != nil {
		return fmt.Errorf("decode referee authorization context: %w", err)
	}
	// The local test operator selects the CA file; the referee never supplies this path.
	certificate, err := os.ReadFile(os.Getenv("MCP_CONFORMANCE_CA_FILE")) // #nosec G703 -- trusted local test configuration
	if err != nil {
		return fmt.Errorf("read referee CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return errors.New("referee CA contains no certificate")
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return errors.New("referee host requires the standard HTTP transport")
	}
	httpTransport := baseTransport.Clone()
	httpTransport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	defer httpTransport.CloseIdleConnections()
	browser := &http.Client{Transport: httpTransport, CheckRedirect: stopAtAuthorizationRedirect}
	registration, err := refereeClientRegistration(configuration)
	if err != nil {
		return err
	}
	info := mcp.ClientInfo{Name: "goa-ai-conformance", Version: "1"}
	var transport *mcp.HTTPTransport
	switch configuration.Name {
	case preregisteredScenario:
		transport, err = mcp.NewAuthorizationCodeHTTPTransport(mcp.HTTPOptions{Endpoint: endpoint, Client: browser, ClientInfo: info}, mcp.AuthorizationCode{
			Registration: registration,
			RedirectURI:  "http://127.0.0.1:3000/callback",
			Store:        mcp.NewMemoryAuthorizationStore(),
			Authorize:    refereeBrowserAuthorization(browser),
		})
	case basicMachineScenario, signedMachineScenario:
		transport, err = mcp.NewClientCredentialsHTTPTransport(mcp.HTTPOptions{Endpoint: endpoint, Client: browser, ClientInfo: info}, mcp.ClientCredentials{
			Registration: registration,
			Store:        mcp.NewMemoryAuthorizationStore(),
		})
	default:
		return errors.New("unsupported referee authorization context")
	}
	if err != nil {
		return err
	}
	caller, err := mcp.NewHTTPCaller(mcp.HTTPOptions{Endpoint: endpoint, Client: transport, ClientInfo: info})
	if err != nil {
		return err
	}
	result, err := caller.CallTool(context.Background(), mcp.CallRequest{Tool: "test-tool"})
	if err != nil {
		return err
	}
	if result.InputRequired != nil || len(result.Content) != 1 {
		return errors.New("authorized tool did not return one completed content block")
	}
	return nil
}

// refereeBrowserAuthorization follows the synthetic sign-in URL and returns
// its complete redirect. The production grant verifies its state and issuer.
func refereeBrowserAuthorization(client *http.Client) func(context.Context, string) (string, error) {
	return func(ctx context.Context, address string) (string, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return "", err
		}
		response, err := client.Do(request)
		if err != nil {
			return "", err
		}
		if err := response.Body.Close(); err != nil {
			return "", err
		}
		if response.StatusCode != http.StatusFound {
			return "", errors.New("referee browser did not return an authorization redirect")
		}
		return response.Header.Get("Location"), nil
	}
}

func stopAtAuthorizationRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// refereeClientRegistration binds fixture credentials to their configured issuer.
// The JWT scenario supplies one PKCS#8 P-256 private key for signed authentication.
func refereeClientRegistration(configuration authorizationContext) (*mcp.ClientRegistration, error) {
	switch configuration.Name {
	case preregisteredScenario, basicMachineScenario:
		return mcp.NewBasicClientRegistration(configuration.Issuer, configuration.ClientID, configuration.ClientSecret)
	case signedMachineScenario:
		if configuration.SigningAlgorithm != "ES256" {
			return nil, errors.New("referee signed registration must select ES256")
		}
		block, remaining := pem.Decode([]byte(configuration.PrivateKeyPEM))
		if block == nil || block.Type != "PRIVATE KEY" || len(remaining) != 0 {
			return nil, errors.New("referee signed registration requires one PKCS#8 key")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("referee signed registration has an invalid private key")
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, nil)
		if err != nil {
			return nil, err
		}
		return mcp.NewSignedClientRegistration(mcp.ClientAssertion{
			Issuer:          configuration.Issuer,
			ClientID:        configuration.ClientID,
			AssertionIssuer: configuration.ClientID,
			Audience:        configuration.Issuer,
			// This fixture validity applies to one client assertion, not its access token.
			Lifetime: time.Minute,
			Signer:   signer,
		})
	default:
		return nil, errors.New("unsupported referee client registration")
	}
}
