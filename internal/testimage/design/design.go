// Package design declares a deterministic image-source fixture. Its generated
// executor and codecs are exercised by runtime tests without a model or storage
// network connection.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("image-source-fixture", func() {
	Title("Native image source fixture")
})

var source = Type("ImageSource", func() {
	Description("Immutable image identity supplied by the fixture's retained owner.")
	Attribute("id", String, "Exact retained image identity.", func() { MinLength(1) })
	Attribute("format", String, "Accepted image encoding.", func() { Enum("png") })
	Attribute("size", Int64, "Accepted image byte length.", func() { Minimum(1) })
	Attribute("sha256", String, "SHA-256 of the accepted bytes.", func() {
		Pattern("^[a-f0-9]{64}$")
	})
	Required("id", "format", "size", "sha256")
})

var selection = Type("ImageSelection", func() {
	Attribute("id", String, "Exact image selected for inspection.", func() { MinLength(1) })
	Required("id")
})

var visibleResult = Type("ImageSelected", func() {
	Attribute("id", String, "Exact selected image identity.")
	Required("id")
})

var _ = Service("images", func() {
	Method("view", func() {
		Payload(selection)
		Result(func() {
			Attribute("id", String, "Exact selected image identity.")
			Attribute("source", source, "Server-owned immutable image descriptor.")
			Required("id", "source")
		})
	})
	Agent("observer", "Inspects explicitly selected native images.", func() {
		RunPolicy(func() {
			History(func() {
				CompressAtMaxInputTokens(200)
				KeepMaxTurns(1)
			})
		})
		Use("pictures", func() {
			Tool("view", "Inspect one selected retained image.", func() {
				Args(selection)
				Return(visibleResult)
				BindTo("images", "view")
				ServerData("fixture.image.v1", source, func() {
					AudienceEvidence()
					NativeImage()
					FromMethodResultField("source")
				})
			})
		})
	})
})
