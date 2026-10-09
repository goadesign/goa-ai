// Package design describes private OAuth storage records. The existing Goa-AI
// codecs validate saved records before the OAuth owner reuses their credentials.
// A pending record contains no reusable token after an uncertain grant exchange.
package design

import . "goa.design/goa/v3/dsl"

var resourceCredentialReady = Type("ResourceCredentialReady", func() {
	Description("One issued resource credential and the permissions retained by its owning client.")
	Field(1, "token", bearerToken, "Validated resource token returned by the issuer")
	Field(2, "obtained", String, "Token response time used with its exclusive expires_in lifetime", func() { Format(FormatDateTime) })
	Field(3, "issuance", String, "Private identifier for this issuance, retained across record loads", func() { MinLength(1) })
	Required("token", "obtained", "issuance")
})

var _ = Type("ResourceCredentialState", func() {
	Description("A saved resource token or an exchange whose previous refresh credential cannot safely be reused.")
	Meta("type:generate:force", "access_tokens")
	Field(1, "binding", ArrayOf(String), "Exact grant, issuer, client and resource identity", func() { MinLength(1); Elem(func() { MinLength(1) }) })
	Field(2, "requested", ArrayOf(String), "Permissions requested for this grant, retained across uncertain exchanges", oauthScopeTokens)
	Field(3, "granted", ArrayOf(String), "Previously granted permissions, retained for fresh authorization", oauthScopeTokens)
	OneOf("state", func() {
		Attribute("ready", resourceCredentialReady, "Completed issuer response saved before MCP dispatch")
		Attribute("pending", String, "An exchange may have consumed the preceding credential", func() { Enum("exchange") })
	})
	Required("binding", "state")
})

var identityCredentialReady = Type("IdentityCredentialReady", func() {
	Description("An identity-provider refresh credential obtained from one host SAML authorization.")
	Field(1, "credential", identityRefresh, "Validated identity refresh credential, never sent to MCP")
	Field(2, "obtained", String, "Identity response time used with its reported exclusive lifetime", func() { Format(FormatDateTime) })
	Required("credential", "obtained")
})

var _ = Type("IdentityCredentialState", func() {
	Description("A saved SAML bootstrap credential or an exchange requiring a fresh host assertion.")
	Meta("type:generate:force", "identity_grants")
	Field(1, "binding", ArrayOf(String), "Exact identity provider, registered client and credential purpose", func() { MinLength(1); Elem(func() { MinLength(1) }) })
	OneOf("state", func() {
		Attribute("ready", identityCredentialReady, "Completed identity refresh credential")
		Attribute("pending", String, "The preceding assertion may have been consumed", func() { Enum("exchange") })
	})
	Required("binding", "state")
})
