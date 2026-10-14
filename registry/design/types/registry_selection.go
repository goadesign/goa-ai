// Saved registry selections retain the exact provider declaration. Direct imports
// keep original names; registered resources also retain the identity from their owner.
package types

import . "goa.design/goa/v3/dsl"

var (
	// RegisteredToolsetSelection pairs an owner-selected resource with its exact declaration.
	RegisteredToolsetSelection = Type("RegisteredToolsetSelection", func() {
		Field(1, "source", CatalogSource, "Owner-selected resource and exact registration target.")
		Field(2, "resolved", ResolvedToolset, "Original operation declarations and registration token.")
		Required("source", "resolved")
	})
	// RegistrySelection retains one of the two supported registry import contracts.
	RegistrySelection = Type("RegistrySelection", func() {
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
		OneOf("selection", func() {
			TypeName("RegistrySelectionKind")
			Field(1, "direct", ResolvedToolset, "Direct import using original operation names.")
			Field(2, "registered", RegisteredToolsetSelection, "Owner-selected resource with derived operation names.")
		})
		Required("selection")
	})
)
