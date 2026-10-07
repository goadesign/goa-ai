// Package design defines the metadata and token messages used by MCP OAuth
// clients and resource servers. Goa generates form requests and typed response
// validation; the runtime supplies exact discovered URLs and strict decoding.
// Native decoders also validate browser responses and verified access-token claims.
package design

import (
	_ "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = Service("access_token_claims", func() {
	Description("Decode verified JWT access-token claims before the resource server checks their issuer, audience and validity times.")
	Method("decode", func() {
		Description("Validate the signed claims using exact JSON names and preserve fractional Unix timestamps.")
		Payload(func() {
			Field(1, "iss", String, "Exact authorization server identifier", func() { MinLength(1) })
			Field(2, "sub", String, "Subject identified by the authorization server", func() { MinLength(1) })
			// JWT audiences use an untagged string-or-array value. Goa's tagged
			// unions cannot represent it; the private SDK type rejects other shapes.
			Field(3, "aud", Any, "Resource identifiers decoded by the JWT library", func() {
				Meta("struct:field:type", "jwt.Audience", "github.com/go-jose/go-jose/v4/jwt", "jwt")
			})
			Field(4, "exp", Float64, "Exclusive token expiration in Unix seconds")
			Field(5, "iat", Float64, "Token issue time in Unix seconds")
			Field(6, "jti", String, "Token identifier assigned by the authorization server", func() { MinLength(1) })
			Field(7, "client_id", String, "Client to which the access token was issued", func() { MinLength(1) })
			Field(8, "nbf", Float64, "Inclusive first valid instant in Unix seconds")
			Field(9, "scope", String, "Space-separated permissions granted by the issuer", oauthScope)
			Required("iss", "sub", "aud", "exp", "iat", "jti", "client_id")
		})
		HTTP(func() { POST("/claims") })
	})
})

var _ = API("mcp_authorization", func() {
	Description("Read protected-resource and issuer metadata and obtain resource-bound access tokens without exposing credentials to MCP tools.")
})

var introspectionCredentials = BasicAuthSecurity("introspection_credentials")

var registrationCredentials = BasicAuthSecurity("registration_credentials")

var _ = Service("token_introspection", func() {
	Description("Ask a trusted authorization server whether an opaque access token is currently usable at this MCP resource, using a separate resource-server registration.")
	Method("read", func() {
		Description("Authenticate the resource server and submit one access token in a generated form, then decode the issuer's current activity, audience and granted permissions.")
		Security(introspectionCredentials)
		Payload(func() {
			Username("username", String, "Form-encoded client identifier registered for resource introspection")
			Password("password", String, "Form-encoded client secret registered for resource introspection")
			Field(1, "token", String, "Bearer access token submitted for verification", oauthBearerValue)
			Field(2, "token_type_hint", String, "Access-token lookup hint; the issuer still owns token-purpose verification", func() {
				Default("access_token")
				Enum("access_token")
				Example("access_token")
			})
			Required("username", "password", "token")
		})
		Result(func() {
			Field(1, "active", Boolean, "Whether this token is currently usable by the authenticated resource")
			// Introspection uses the same untagged audience value as signed tokens.
			// The concrete SDK type rejects numbers and mixed arrays privately.
			Field(2, "aud", Any, "Intended resource identifiers decoded by the JWT library", func() {
				Meta("struct:field:type", "jwt.Audience", "github.com/go-jose/go-jose/v4/jwt", "jwt")
			})
			Field(3, "scope", String, "Space-separated permissions granted for this resource", oauthScope)
			Field(4, "iss", String, "Exact authorization server identifier when supplied", func() { MinLength(1) })
			Field(5, "sub", String, "Subject identified by the authorization server when supplied", func() { MinLength(1) })
			Field(6, "client_id", String, "Client to which the access token was issued when supplied", func() { MinLength(1) })
			Field(7, "exp", Int64, "Exclusive token expiration in whole Unix seconds when supplied")
			Field(8, "nbf", Int64, "Inclusive first valid instant in whole Unix seconds when supplied")
			Field(9, "token_type", String, "Bearer token type when supplied", func() {
				Pattern("^[Bb][Ee][Aa][Rr][Ee][Rr]$")
			})
			Required("active")
		})
		HTTP(func() {
			POST("/introspect")
			FormRequest()
		})
	})
})

var bearerToken = Type("BearerToken", func() {
	Description("An opaque resource access token and the optional refresh credential issued in the same grant.")
	Field(1, "access_token", String, "Opaque bearer token returned by the issuer", oauthBearerValue)
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
			Field(4, "token_endpoint_auth_methods_supported", ArrayOf(String), "Client authentication methods accepted at the token endpoint; omission selects HTTP Basic under RFC 8414", func() { Default([]string{"client_secret_basic"}) })
			Field(5, "authorization_endpoint", String, "HTTPS endpoint for user sign-in and consent", func() { Format(FormatURI) })
			Field(6, "code_challenge_methods_supported", ArrayOf(String), "Advertised PKCE methods used to protect authorization codes")
			Field(7, "authorization_response_iss_parameter_supported", Boolean, "Whether every authorization response must identify its issuer")
			Field(8, "client_id_metadata_document_supported", Boolean, "Whether this issuer accepts HTTPS client metadata documents")
			Field(9, "scopes_supported", ArrayOf(String), "Permissions that the issuer accepts, including optional offline access", oauthScopeTokens)
			Field(10, "token_endpoint_auth_signing_alg_values_supported", ArrayOf(String), "Signature algorithms accepted for signed client authentication; required only by a signed client profile", func() {
				Elem(func() { MinLength(1) })
			})
			Field(11, "identity_chaining_requested_token_types_supported", ArrayOf(String), "Optional token purposes supported by identity chaining", func() { Elem(func() { MinLength(1) }) })
			Field(12, "authorization_grant_profiles_supported", ArrayOf(String), "Optional authorization profiles; advertised identity grants require JWT-bearer support", func() { Elem(func() { MinLength(1) }) })
			Required("issuer", "token_endpoint")
		})
		HTTP(func() { GET("/issuer") })
	})
})

var clientMetadata = Type("ClientMetadata", func() {
	Description("Shared registration identity and permitted grant fields; each metadata operation declares its own required authentication method.")
	Field(1, "client_id", String, "Exact HTTPS URL hosting this client document", func() { Format(FormatURI) })
	Field(2, "client_name", String, "Client name shown by the issuer during consent", func() { MinLength(1) })
	// The external metadata contract requires this property even when a client
	// uses only grants without callbacks. Browser flow checks exact membership.
	Field(3, "redirect_uris", ArrayOf(String), "Registered callbacks owned by the client host", func() {
		Elem(func() { Format(FormatURI) })
	})
	Field(5, "grant_types", ArrayOf(String), "Grants registered for this application, including browser or enterprise exchanges", func() {
		Default([]string{"authorization_code"})
	})
	Field(6, "response_types", ArrayOf(String), "Registered authorization responses", func() {
		Default([]string{"code"})
	})
	Field(7, "client_secret", String, "Forbidden shared-secret registration member checked by the client")
	Field(8, "client_secret_expires_at", Int64, "Forbidden shared-secret registration member checked by the client")
	Field(11, "authorization_grant_profiles_supported", ArrayOf(String), "Optional registered authorization profiles; identity grants require both exchange and redemption grants", func() { Elem(func() { MinLength(1) }) })
	Required("client_id", "client_name", "redirect_uris")
})

var _ = Service("client_metadata", func() {
	Description("Read a client's self-hosted registration before using its HTTPS document URL as the client identifier; each operation requires the configured authentication profile.")
	for _, profile := range []struct {
		name, authentication string
		signed               bool
	}{
		{"read", "none", false},
		{"signed_read", "private_key_jwt", true},
	} {
		Method(profile.name, func() {
			Description("Check the document identity, registered callbacks and exact token authentication before beginning browser consent.")
			Result(func() {
				Extend(clientMetadata)
				Field(4, "token_endpoint_auth_method", String, "Registered authentication required at the token endpoint", func() { Enum(profile.authentication) })
				Required("token_endpoint_auth_method")
				if profile.signed {
					Field(9, "jwks_uri", String, "HTTPS address of the client's registered public keys", func() { Format(FormatURI) })
					// Public keys have algorithm-specific flat fields. The JOSE library
					// decodes these standard shapes without a Goa union discriminator.
					Field(10, "jwks", Any, "Inline public keys decoded by the JOSE library", func() {
						Meta("struct:field:type", "*jose.JSONWebKeySet", "github.com/go-jose/go-jose/v4", "jose")
					})
				}
			})
			HTTP(func() { GET("/" + profile.name) })
		})
	}
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

// oauthClientAssertion adds the signed authentication fields to the selected
// grant. Generated forms contain one assertion and its fixed protocol type.
func oauthClientAssertion(assertionTag, typeTag int) {
	Field(assertionTag, "client_assertion", String, "Signed compact JWT identifying the registered client", func() {
		Pattern(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
	})
	Field(typeTag, "client_assertion_type", String, "JWT client authentication selected by this operation", func() {
		Default("urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		Enum("urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		Example("urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	})
	Required("client_assertion")
}

// oauthBearerValue applies the bearer header grammar to issued and submitted
// access tokens. Goa rejects malformed values before they cross either boundary.
func oauthBearerValue() {
	MinLength(1)
	Pattern("^[A-Za-z0-9._~+/-]+=*$")
}

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
