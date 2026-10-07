// Package design specializes enterprise identity exchanges before generating
// HTTP clients. Identity grants and SAML bootstrap credentials have different
// required purposes; neither can be decoded as a resource bearer token.
package design

import (
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

const (
	identityTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	identityJWTBearer     = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	identityGrantType     = "urn:ietf:params:oauth:token-type:id-jag"
	identityRefreshType   = "urn:ietf:params:oauth:token-type:refresh_token"
	identityIDTokenType   = "urn:ietf:params:oauth:token-type:id_token"
	identitySAMLType      = "urn:ietf:params:oauth:token-type:saml2"
)

var identityGrant = Type("IdentityGrant", func() {
	Description("A signed authorization grant issued by the identity provider for redemption at the resource issuer; it is never an MCP bearer token.")
	Field(1, "issued_token_type", String, "Required identity authorization grant purpose", func() { Enum(identityGrantType) })
	Field(2, "access_token", String, "Signed compact identity authorization grant for the resource issuer", func() { Pattern(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`) })
	Field(3, "token_type", String, "No HTTP bearer usage is permitted for this grant", func() { Enum("N_A") })
	Field(4, "scope", String, "Permissions allowed by the identity provider for the requested MCP resource", oauthScope)
	Field(5, "expires_in", Int64, "Exclusive grant lifetime in seconds from its token exchange", func() { Minimum(0) })
	Field(6, "refresh_token", String, "Optional issuer credential that this identity-grant response does not retain", oauthVisibleValue)
	Required("issued_token_type", "access_token", "token_type")
})

var identityRefresh = Type("IdentityRefresh", func() {
	Description("An identity-provider refresh credential obtained from a SAML assertion; the user identity owner retains it across MCP resources.")
	Field(1, "issued_token_type", String, "Required identity refresh credential purpose", func() { Enum(identityRefreshType) })
	Field(2, "access_token", String, "Opaque identity refresh credential carried in the token-exchange response field", oauthVisibleValue)
	Field(3, "token_type", String, "No HTTP bearer usage is permitted for this identity credential", func() { Enum("N_A") })
	Field(4, "scope", String, "SSO permissions issued for identity refresh, separate from MCP permissions", oauthScope)
	Field(5, "expires_in", Int64, "Exclusive identity refresh lifetime in seconds when the issuer reports it", func() { Minimum(0) })
	Required("issued_token_type", "access_token", "token_type")
})

var identityExchange = Type("IdentityExchange", func() {
	Description("Request an identity grant for one exact resource issuer, MCP resource and permission set using this user's SSO credential.")
	Field(1, "grant_type", String, "Fixed OAuth token-exchange grant", func() {
		Default(identityTokenExchange)
		Enum(identityTokenExchange)
		Example(identityTokenExchange)
	})
	Field(2, "subject_token", String, "Host-validated SSO identity or refresh credential", oauthVisibleValue)
	Field(3, "audience", String, "Exact issuer identifier of the resource authorization server", func() { Format(FormatURI) })
	Field(4, "resource", String, "Exact MCP protected resource identifier", func() { Format(FormatURI) })
	Field(5, "scope", String, "Permissions requested for this MCP resource", oauthScope)
	Field(6, "subject_token_type", String, "The constructed identity owner supplies an ID token or identity refresh credential", func() { Enum(identityIDTokenType, identityRefreshType) })
	Field(7, "requested_token_type", String, "Fixed identity grant response purpose", func() {
		Default(identityGrantType)
		Enum(identityGrantType)
		Example(identityGrantType)
	})
	Required("subject_token", "subject_token_type", "audience", "resource")
})

var samlIdentityExchange = Type("SAMLIdentityExchange", func() {
	Description("Exchange a validated SAML assertion for an identity-provider refresh credential before obtaining MCP resource grants.")
	Field(1, "grant_type", String, "Fixed OAuth token-exchange grant", func() {
		Default(identityTokenExchange)
		Enum(identityTokenExchange)
		Example(identityTokenExchange)
	})
	Field(2, "subject_token", String, "Base64url-encoded SAML assertion already validated by the host", func() { Pattern(`^[A-Za-z0-9_-]+$`) })
	Field(5, "scope", String, "SSO permissions requested for reusable identity credentials", func() {
		Default("openid offline_access")
		oauthScope()
	})
	Field(7, "requested_token_type", String, "Fixed identity refresh response purpose", func() {
		Default(identityRefreshType)
		Enum(identityRefreshType)
		Example(identityRefreshType)
	})
	Required("subject_token")
})

var identityRedemption = Type("IdentityRedemption", func() {
	Description("Redeem the identity provider's signed authorization grant at the resource issuer using that issuer's independent client registration.")
	Field(1, "resource", String, "Exact MCP resource for which the access token is requested", func() { Format(FormatURI) })
	Field(2, "assertion", String, "Signed identity authorization grant from the identity provider", func() { Pattern(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`) })
	Field(3, "scope", String, "Permissions requested within the identity provider's granted permissions", oauthScope)
	Field(4, "grant_type", String, "Fixed JWT authorization grant", func() {
		Default(identityJWTBearer)
		Enum(identityJWTBearer)
		Example(identityJWTBearer)
	})
	Required("resource", "assertion")
})

var _ = Service("identity_grants", func() {
	Description("Obtain identity-provider authorization grants and SAML bootstrap credentials through separate native purposes and registered authentication profiles.")
	for _, profile := range oauthRegistrationProfiles {
		for _, grant := range []struct {
			name, subjectType string
			fields, result    expr.UserType
		}{
			{"exchange", "", identityExchange, identityGrant},
			{"saml", identitySAMLType, samlIdentityExchange, identityRefresh},
		} {
			Method(profile.prefix+grant.name, func() {
				Description("Authenticate this identity-provider registration and exchange the fixed SSO credential kind; return only the declared non-bearer purpose.")
				if profile.authentication == "client_secret_basic" {
					Security(registrationCredentials)
				}
				Payload(func() {
					Extend(grant.fields)
					if grant.subjectType != "" {
						Field(6, "subject_token_type", String, "Fixed host SSO credential kind for this exchange", func() {
							Default(grant.subjectType)
							Enum(grant.subjectType)
							Example(grant.subjectType)
						})
					}
					oauthClientAuthentication(profile.authentication, 8, 10, 9)
				})
				Result(grant.result)
				HTTP(func() {
					POST("/" + profile.prefix + grant.name)
					FormRequest()
				})
			})
		}
	}
})
