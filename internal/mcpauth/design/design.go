// Package design defines the metadata and token messages read by MCP's OAuth
// client. Goa generates typed form requests and response validation; the runtime
// supplies exact discovered URLs and strict external-response decoding. Native
// query decoding also validates browser authorization responses before exchange.
package design

import . "goa.design/goa/v3/dsl"

var _ = API("mcp_authorization", func() {
	Description("Read protected-resource and issuer metadata and obtain resource-bound access tokens without exposing credentials to MCP tools.")
})

var bearerToken = Type("BearerToken", func() {
	Description("An opaque resource access token and the optional refresh credential issued in the same grant.")
	Field(1, "access_token", String, "Opaque bearer token returned by the issuer", func() {
		MinLength(1)
		Pattern("^[A-Za-z0-9._~+/-]+=*$")
	})
	Field(2, "token_type", String, "Bearer token type, compared without case sensitivity", func() { Pattern("^[Bb][Ee][Aa][Rr][Ee][Rr]$") })
	Field(3, "expires_in", Int64, "Access token lifetime in seconds from the token response", func() { Minimum(0) })
	Field(4, "scope", String, "Space-separated permissions granted by the issuer", oauthScope)
	Field(5, "refresh_token", String, "Private refresh credential; never sent to a resource server", func() {
		MinLength(1)
		Pattern(`^[\x20-\x7e]+$`)
	})
	Required("access_token", "token_type")
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
			Field(3, "scopes_supported", ArrayOf(String), "Permissions supported by this resource", oauthScopeTokens)
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
			Field(3, "grant_types_supported", ArrayOf(String), "Token grants supported by the issuer", func() { Default([]string{"authorization_code", "implicit"}) })
			Field(4, "token_endpoint_auth_methods_supported", ArrayOf(String), "Client authentication methods accepted at the token endpoint")
			Field(5, "authorization_endpoint", String, "HTTPS endpoint for user sign-in and consent", func() { Format(FormatURI) })
			Field(6, "code_challenge_methods_supported", ArrayOf(String), "Advertised PKCE methods used to protect authorization codes")
			Field(7, "authorization_response_iss_parameter_supported", Boolean, "Whether every authorization response must identify its issuer")
			Field(8, "client_id_metadata_document_supported", Boolean, "Whether this issuer accepts HTTPS client metadata documents")
			Field(9, "scopes_supported", ArrayOf(String), "Permissions that the issuer accepts, including optional offline access", oauthScopeTokens)
			Required("issuer", "token_endpoint")
		})
		HTTP(func() { GET("/issuer") })
	})
})

var _ = Service("client_metadata", func() {
	Description("Read a public client's self-hosted registration before using its HTTPS document URL as the client identifier.")
	Method("read", func() {
		Description("Check the client's document identity, public authentication and registered redirects before beginning browser consent.")
		Result(func() {
			Field(1, "client_id", String, "Exact HTTPS URL hosting this client document", func() { Format(FormatURI) })
			Field(2, "client_name", String, "Client name shown by the issuer during consent", func() { MinLength(1) })
			Field(3, "redirect_uris", ArrayOf(String), "Registered callbacks owned by the client host", func() {
				MinLength(1)
				Elem(func() { Format(FormatURI) })
			})
			Field(4, "token_endpoint_auth_method", String, "Public-client token authentication", func() { Enum("none") })
			Field(5, "grant_types", ArrayOf(String), "Registered grants including authorization code and optional refresh", func() {
				Default([]string{"authorization_code"})
			})
			Field(6, "response_types", ArrayOf(String), "Registered authorization responses", func() {
				Default([]string{"code"})
			})
			Field(7, "client_secret", String, "Forbidden shared-secret registration member checked by the client")
			Field(8, "client_secret_expires_at", Int64, "Forbidden shared-secret registration member checked by the client")
			Required("client_id", "client_name", "redirect_uris", "token_endpoint_auth_method")
		})
		HTTP(func() { GET("/client") })
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
			Field(4, "scope", String, "Space-separated permissions requested by the configured client", oauthScope)
			Field(5, "grant_type", String, "Client-credentials grant selected by this operation", func() {
				Default("client_credentials")
				Enum("client_credentials")
				Example("client_credentials")
			})
			Required("client_id", "client_secret", "resource")
		})
		Result(bearerToken)
		HTTP(func() {
			POST("/token")
			FormRequest()
		})
	})
	Method("code", func() {
		Description("Exchange one validated browser authorization code using the private PKCE verifier and the original resource and redirect.")
		Payload(func() {
			Field(1, "client_id", String, "Public client identifier registered with the selected issuer", func() { MinLength(1) })
			Field(2, "code", String, "Authorization code from the validated redirect", oauthVisibleValue)
			Field(3, "code_verifier", String, "Private PKCE verifier for this authorization exchange", func() { Pattern(`^[A-Za-z0-9._~-]{43,128}$`) })
			Field(4, "redirect_uri", String, "Exact redirect used in the authorization request", func() { Format(FormatURI) })
			Field(5, "resource", String, "Exact resource for which the token is requested", func() { Format(FormatURI) })
			Field(6, "grant_type", String, "Authorization-code grant selected by this operation", func() {
				Default("authorization_code")
				Enum("authorization_code")
				Example("authorization_code")
			})
			Required("client_id", "code", "code_verifier", "redirect_uri", "resource")
		})
		Result(bearerToken)
		HTTP(func() {
			POST("/code")
			FormRequest()
		})
	})
	Method("refresh", func() {
		Description("Replace an expired public-client access token using the refresh credential bound to the same issuer and resource.")
		Payload(func() {
			Field(1, "client_id", String, "Public client identifier of the original grant", func() { MinLength(1) })
			Field(2, "refresh_token", String, "Private refresh credential from the original grant", oauthVisibleValue)
			Field(3, "resource", String, "Exact resource of the original grant", func() { Format(FormatURI) })
			Field(4, "grant_type", String, "Refresh grant selected by this operation", func() {
				Default("refresh_token")
				Enum("refresh_token")
				Example("refresh_token")
			})
			Required("client_id", "refresh_token", "resource")
		})
		Result(bearerToken)
		HTTP(func() {
			POST("/refresh")
			FormRequest()
		})
	})
})

var _ = Service("authorization_responses", func() {
	Description("Validate browser redirect query fields before the client checks their state, issuer and code-or-error relationship.")
	Method("receive", func() {
		Description("Decode one host-delivered redirect using the same Goa query validation as a mounted HTTP callback.")
		Payload(func() {
			Field(1, "code", String, "Authorization code returned by the selected issuer", oauthVisibleValue)
			Field(2, "error", String, "OAuth error from the selected issuer", func() { Pattern(`^[\x20\x21\x23-\x5b\x5d-\x7e]+$`) })
			Field(3, "state", String, "Client-generated value identifying this authorization exchange", func() { MinLength(1) })
			Field(4, "issuer", String, "Exact authorization server identifier when supplied", func() { Format(FormatURI) })
			Required("state")
		})
		HTTP(func() {
			GET("/callback")
			Param("code")
			Param("error")
			Param("state")
			Param("issuer:iss")
		})
	})
})

// oauthScope applies the OAuth scope grammar wherever an external grant carries
// permissions. Generated response validators enforce this one declared rule.
func oauthScope() {
	Pattern(`^[\x21\x23-\x5b\x5d-\x7e]+( [\x21\x23-\x5b\x5d-\x7e]+)*$`)
}

// oauthVisibleValue accepts a nonempty OAuth value sent in a form or query.
// Spaces are valid within this opaque value; encoding preserves them as data.
func oauthVisibleValue() {
	MinLength(1)
	Pattern(`^[\x20-\x7e]+$`)
}

// oauthScopeTokens validates each advertised permission as one OAuth scope token.
// Generated metadata decoding rejects whitespace and malformed permissions.
func oauthScopeTokens() {
	Elem(func() { Pattern(`^[\x21\x23-\x5b\x5d-\x7e]+$`) })
}
