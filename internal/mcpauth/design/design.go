// Package design defines the metadata and token messages read by MCP's OAuth
// client. Goa generates typed HTTP clients and response validation; the runtime
// supplies exact discovered URLs and the token endpoint's form encoder.
package design

import . "goa.design/goa/v3/dsl"

var _ = API("mcp_authorization", func() {
	Description("Read protected-resource and issuer metadata and obtain resource-bound access tokens without exposing credentials to MCP tools.")
})

var _ = Service("resource_metadata", func() {
	Description("Identify the protected resource and the independent authorization servers trusted to issue its access tokens.")
	Method("read", func() {
		Description("Read the resource's metadata before choosing an issuer or sending a client credential.")
		Result(func() {
			Field(1, "resource", String, "Exact identifier of the protected resource", func() { Format(FormatURI) })
			Field(2, "authorization_servers", ArrayOf(String), "Issuers trusted by this resource", func() {
				MinLength(1)
				Elem(func() { Format(FormatURI) })
			})
			Field(3, "scopes_supported", ArrayOf(String), "Permissions supported by this resource")
			Required("resource", "authorization_servers")
		})
		HTTP(func() { GET("/resource") })
	})
})

var _ = Service("issuer_metadata", func() {
	Description("Identify an authorization server's token endpoint and the grants and client authentication methods it supports.")
	Method("read", func() {
		Description("Read issuer metadata and bind it to the exact configured issuer before sending a secret to its token endpoint.")
		Result(func() {
			Field(1, "issuer", String, "Exact identifier of the authorization server", func() { Format(FormatURI) })
			Field(2, "token_endpoint", String, "HTTPS endpoint that accepts token grants", func() { Format(FormatURI) })
			Field(3, "grant_types_supported", ArrayOf(String), "Token grants supported by the issuer")
			Field(4, "token_endpoint_auth_methods_supported", ArrayOf(String), "Client authentication methods accepted at the token endpoint")
			Required("issuer", "token_endpoint")
		})
		HTTP(func() { GET("/issuer") })
	})
})

var _ = Service("access_tokens", func() {
	Description("Obtain opaque resource-bound bearer tokens using explicitly advertised client authentication, without sending those credentials to the MCP server.")
	Method("secret", func() {
		Description("Exchange a preregistered client identifier and secret using request-body authentication for one resource and its configured permissions.")
		Payload(func() {
			Field(1, "client_id", String, "Client identifier registered with this issuer", func() { MinLength(1) })
			Field(2, "client_secret", String, "Secret registered with this issuer", func() { MinLength(1) })
			Field(3, "resource", String, "Exact resource for which the token is requested", func() { Format(FormatURI) })
			Field(4, "scope", String, "Space-separated permissions requested by the configured client", func() { Pattern(`^[\x21\x23-\x5b\x5d-\x7e]+( [\x21\x23-\x5b\x5d-\x7e]+)*$`) })
			Required("client_id", "client_secret", "resource")
		})
		Result(func() {
			Field(1, "access_token", String, "Opaque bearer token returned by the issuer", func() {
				MinLength(1)
				Pattern("^[A-Za-z0-9._~+/-]+=*$")
			})
			Field(2, "token_type", String, "Bearer token type, compared without case sensitivity", func() { Pattern("^[Bb][Ee][Aa][Rr][Ee][Rr]$") })
			Field(3, "expires_in", Int64, "Token lifetime in seconds from the token response", func() { Minimum(0) })
			Field(4, "scope", String, "Space-separated permissions granted by the issuer", func() { Pattern(`^[\x21\x23-\x5b\x5d-\x7e]+( [\x21\x23-\x5b\x5d-\x7e]+)*$`) })
			Required("access_token", "token_type")
		})
		HTTP(func() { POST("/token") })
	})
})
