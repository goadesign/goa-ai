// Package design specializes OAuth token forms by grant and client registration.
// Shared expressions describe the grant fields. Evaluating each authentication
// profile adds only its required fields and native security binding, so generated
// clients send one complete form without interpreting credential field presence.
package design

import (
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

var oauthRegistrationProfiles = []struct {
	machine, prefix, authentication string
}{
	{"", "", "none"},
	{"basic", "basic_", "client_secret_basic"},
	{"secret", "secret_", "client_secret_post"},
	{"assertion", "signed_", "private_key_jwt"},
}

var machineExchange = Type("MachineExchange", func() {
	Description("Request an access token for one resource and permission set using a confidential application registration.")
	Field(1, "resource", String, "Exact protected resource identifier", func() { Format(FormatURI) })
	Field(3, "scope", String, "Space-separated permissions requested for the resource", oauthScope)
	Field(4, "grant_type", String, "Fixed client-credentials grant", func() {
		Default("client_credentials")
		Enum("client_credentials")
		Example("client_credentials")
	})
	Required("resource")
})

var codeExchange = Type("CodeExchange", func() {
	Description("Exchange one validated authorization code using its original PKCE verifier, redirect and resource.")
	Field(2, "code", String, "Authorization code from the validated callback", oauthVisibleValue)
	Field(3, "code_verifier", String, "Private verifier protecting this code exchange", func() { Pattern(`^[A-Za-z0-9._~-]{43,128}$`) })
	Field(4, "redirect_uri", String, "Exact registered callback used during authorization", func() { Format(FormatURI) })
	Field(5, "resource", String, "Exact protected resource identifier", func() { Format(FormatURI) })
	Field(6, "grant_type", String, "Fixed authorization-code grant", func() {
		Default("authorization_code")
		Enum("authorization_code")
		Example("authorization_code")
	})
	Required("code", "code_verifier", "redirect_uri", "resource")
})

var refreshExchange = Type("RefreshExchange", func() {
	Description("Replace an access token using the private refresh credential bound to its original resource and registration.")
	Field(2, "refresh_token", String, "Private refresh credential from the original grant", oauthVisibleValue)
	Field(3, "resource", String, "Exact protected resource identifier", func() { Format(FormatURI) })
	Field(4, "grant_type", String, "Fixed refresh grant", func() {
		Default("refresh_token")
		Enum("refresh_token")
		Example("refresh_token")
	})
	Required("refresh_token", "resource")
})

var _ = Service("access_tokens", func() {
	Description("Obtain resource-bound bearer tokens through separately generated registration authentication and grant contracts; credentials never enter MCP requests.")
	for _, profile := range oauthRegistrationProfiles {
		if profile.machine != "" {
			Method(profile.machine, func() {
				Description("Request a machine access token for the exact resource using this registration's required authentication.")
				if profile.authentication == "client_secret_basic" {
					Security(registrationCredentials)
				}
				Payload(func() {
					Extend(machineExchange)
					oauthClientAuthentication(profile.authentication, 2, 6, 5)
				})
				Result(bearerToken)
				HTTP(func() {
					POST("/" + profile.machine)
					FormRequest()
				})
			})
		}
		for _, grant := range []struct {
			name                            string
			fields                          expr.UserType
			identityTag, secretTag, typeTag int
		}{
			{"code", codeExchange, 1, 7, 7},
			{"refresh", refreshExchange, 1, 5, 5},
			{"redeem", identityRedemption, 5, 7, 6},
		} {
			Method(profile.prefix+grant.name, func() {
				Description("Complete the selected user grant with this registration's required authentication and the original resource binding.")
				if profile.authentication == "client_secret_basic" {
					Security(registrationCredentials)
				}
				Payload(func() {
					Extend(grant.fields)
					oauthClientAuthentication(profile.authentication, grant.identityTag, grant.secretTag, grant.typeTag)
				})
				Result(bearerToken)
				HTTP(func() {
					POST("/" + profile.prefix + grant.name)
					FormRequest()
				})
			})
		}
	}
})

// oauthClientAuthentication adds exactly the configured registration's fields.
// Basic uses native security headers; the other profiles use generated forms.
func oauthClientAuthentication(authentication string, identityTag, secretTag, assertionTypeTag int) {
	switch authentication {
	case "client_secret_basic":
		Username("client_id", String, "Individually form-encoded client identifier for the Basic header", func() { MinLength(1) })
		Password("client_secret", String, "Individually form-encoded secret for the Basic header", func() { MinLength(1) })
		Required("client_id", "client_secret")
	case "private_key_jwt":
		oauthClientAssertion(identityTag, assertionTypeTag)
	case "client_secret_post":
		Field(identityTag, "client_id", String, "Registered client identifier", func() { MinLength(1) })
		Field(secretTag, "client_secret", String, "Secret registered with this authorization server", func() { MinLength(1) })
		Required("client_id", "client_secret")
	case "none":
		Field(identityTag, "client_id", String, "Registered public client identifier", func() { MinLength(1) })
		Required("client_id")
	}
}
