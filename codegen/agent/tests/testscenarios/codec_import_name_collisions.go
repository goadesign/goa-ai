// Package testscenarios defines small Goa designs used by generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// CodecImportNameCollisions defines tool packages whose generated files need
// both a fixed package and a user package with the same preferred name.
func CodecImportNameCollisions() func() {
	return func() {
		API("codec import name collisions", func() {})

		token := Type("Token", String, func() {
			Meta("struct:pkg:path", "strconv")
		})
		indexedPayload := Type("IndexedPayload", func() {
			Attribute("values", ArrayOfRequired(token), "Indexed values.")
			Required("values")
		})
		scalarPayload := Type("ScalarPayload", func() {
			Attribute("value", token, "One value.")
			Required("value")
		})
		customPayload := Type("CustomPayload", func() {
			Attribute("token", String, "A custom token.", func() {
				Meta("struct:field:type", "goa.Token", "generated.local/custom/goa", "goa")
			})
			Required("token")
		})
		sharedPackagePayload := Type("SharedPackagePayload", func() {
			Attribute("first", String, "The first custom value.", func() {
				Meta("struct:field:type", "alpha.Value", "generated.local/custom/shared", "alpha")
			})
			Attribute("second", String, "The second custom value.", func() {
				Meta("struct:field:type", "beta.Other", "generated.local/custom/shared", "beta")
			})
			Required("first", "second")
		})
		relocatedBranch := Type("RelocatedBranch", func() {
			Meta("struct:pkg:path", "errors")
			Attribute("name", String, "The selected name.")
			Required("name")
		})
		unionPayload := Type("UnionPayload", func() {
			OneOf("choice", func() {
				Attribute("relocated", relocatedBranch, "A relocated branch.")
				Attribute("text", String, "A text branch.")
			})
			Required("choice")
		})

		Service("alpha", func() {
			Completion("opaque", "Returns an opaque value.", func() {
				Return(Any, func() {
					Meta("struct:field:type", "errors.Value", "generated.local/custom/errors", "errors")
				})
			})
			Agent("scribe", "Checks generated codec imports.", func() {
				Use("indexed", func() {
					Tool("store", "Stores indexed values.", func() {
						Args(indexedPayload)
						Return(String)
					})
				})
				Use("scalar", func() {
					Tool("store", "Stores one value.", func() {
						Args(scalarPayload)
						Return(String)
					})
				})
				Use("custom", func() {
					Tool("store", "Stores a custom value.", func() {
						Args(customPayload)
						Return(String)
					})
				})
				Use("union", func() {
					Tool("store", "Stores one selected value.", func() {
						Args(unionPayload)
						Return(String)
					})
				})
				Use("shared", func() {
					Tool("store", "Stores values from one custom package.", func() {
						Args(sharedPackagePayload)
						Return(String)
					})
				})
			})
		})
	}
}
