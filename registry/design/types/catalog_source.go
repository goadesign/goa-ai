// Registry source identities select an exact native revision or service registration.
// Consumers use the owner and resource to bind inputs to the selected declaration.
package types

import (
	"goa.design/goa-ai/runtime/toolregistry"
	. "goa.design/goa/v3/dsl"
)

var (
	// NativeCatalogSource selects all declarations of one immutable native revision.
	NativeCatalogSource = Type("NativeCatalogSource", func() {
		Field(1, "toolset", String, "Immutable native revision route.", func() { MinLength(1) })
		Required("toolset")
	})
	// ServiceCatalogSource selects one exact service registration.
	ServiceCatalogSource = Type("ServiceCatalogSource", func() {
		Field(1, "toolset", String, "Service registration route.", func() { MinLength(1) })
		Field(2, "registration_token", String, "Exact retained service registration.", func() { Pattern(toolregistry.RegistrationTokenPattern) })
		Required("toolset", "registration_token")
	})
	// CatalogSource pairs the resource scope and name authorized by the host with
	// one selected native revision or service registration.
	CatalogSource = Type("CatalogSource", func() {
		Field(1, "scope", String, "Owning scope supplied and authorized by the host.", func() { MinLength(1) })
		Field(2, "name", String, "Permanent resource name supplied and authorized by the host within that scope.", func() { MinLength(1) })
		OneOf("target", func() {
			TypeName("CatalogSourceTarget")
			Field(3, "native", NativeCatalogSource, "One immutable native revision.")
			Field(4, "service", ServiceCatalogSource, "One exact service registration.")
		})
		Required("scope", "name", "target")
	})
)
