// This file supplies the synthetic host integration for the referee's
// registered browser, machine and enterprise scenarios. Production OAuth code
// owns discovery, PKCE, callback validation, token exchange and MCP authorization.
// This driver supplies registered identities, validated host credentials, browser
// interaction and an explicitly trusted test CA.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
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
		IDPClientID      string `json:"idp_client_id"`
		IDPIssuer        string `json:"idp_issuer"`
		IDPToken         string `json:"idp_id_token"`
		IDPPublicKeyPEM  string `json:"idp_public_key_pem"`
		IDPSubject       string `json:"idp_subject"`
		Authentication   string `json:"token_endpoint_auth_method"`
		MetadataAddress  string `json:"metadata_address"`
	}
)

const (
	preregisteredScenario = "auth/pre-registration"
	basicMachineScenario  = "auth/client-credentials-basic"
	signedMachineScenario = "auth/client-credentials-jwt"
	enterpriseScenario    = "auth/enterprise-managed-authorization"
	metadataScenario      = "auth/basic-cimd"
	offlineAccessScenario = "auth/offline-access-scope"
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
	if configuration.Name == metadataScenario {
		if err := routeRefereeMetadata(httpTransport, configuration); err != nil {
			return err
		}
	}
	defer httpTransport.CloseIdleConnections()
	browser := &http.Client{Transport: httpTransport, CheckRedirect: stopAtAuthorizationRedirect}
	registration, err := refereeClientRegistration(configuration)
	if err != nil {
		return err
	}
	info := mcp.ClientInfo{Name: "goa-ai-conformance", Version: "1"}
	var transport *mcp.HTTPTransport
	switch configuration.Name {
	case preregisteredScenario,
		"auth/token-endpoint-auth-basic", "auth/token-endpoint-auth-post", "auth/token-endpoint-auth-none",
		"auth/iss-supported", "auth/iss-not-advertised", "auth/iss-supported-missing",
		"auth/iss-wrong-issuer", "auth/iss-unexpected", "auth/iss-normalized",
		"auth/metadata-issuer-mismatch", "auth/resource-mismatch",
		"auth/scope-from-www-authenticate", "auth/scope-from-scopes-supported",
		"auth/scope-omitted-when-undefined", "auth/scope-step-up", "auth/scope-retry-limit",
		metadataScenario, offlineAccessScenario, "auth/offline-access-not-supported",
		"auth/metadata-default", "auth/metadata-var1", "auth/metadata-var2", "auth/metadata-var3",
		"auth/authorization-server-migration":
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
	case enterpriseScenario:
		transport, err = refereeEnterpriseTransport(endpoint, browser, info, registration, configuration)
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
	switch configuration.Authentication {
	case "none":
		if configuration.Name == metadataScenario || configuration.Name == offlineAccessScenario {
			return mcp.NewPublicClientMetadataRegistration(configuration.Issuer, configuration.ClientID)
		}
		return mcp.NewPublicClientRegistration(configuration.Issuer, configuration.ClientID)
	case "client_secret_basic":
		return mcp.NewBasicClientRegistration(configuration.Issuer, configuration.ClientID, configuration.ClientSecret)
	case "client_secret_post":
		return mcp.NewSecretClientRegistration(configuration.Issuer, configuration.ClientID, configuration.ClientSecret)
	case "":
		// The original registered scenarios select their fixed profile below.
	default:
		return nil, errors.New("unsupported referee authentication method")
	}
	switch configuration.Name {
	case preregisteredScenario, basicMachineScenario, enterpriseScenario:
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

// routeRefereeMetadata connects the referee's fixed document host to its
// explicitly configured local HTTPS server. The URL, Host and TLS identity stay
// unchanged, so the production document reader still verifies the named host.
func routeRefereeMetadata(transport *http.Transport, configuration authorizationContext) error {
	if configuration.ClientID != "https://conformance-test.local/client-metadata.json" {
		return errors.New("referee metadata identity does not match its fixed host")
	}
	host, port, err := net.SplitHostPort(configuration.MetadataAddress)
	if err != nil || host != "localhost" || port == "" {
		return errors.New("referee metadata destination must name its local HTTPS server")
	}
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "conformance-test.local:443" {
			address = configuration.MetadataAddress
		}
		return dial(ctx, network, address)
	}
	return nil
}
