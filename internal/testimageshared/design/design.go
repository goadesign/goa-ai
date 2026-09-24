// Package design declares two generated producers of the same retained image
// descriptor. Runtime tests compare their serialized contracts and verify that
// producer-specific Go names do not prevent historical decoding.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("shared-image-source-fixture", func() {})

var source = Type("RetainedImage", func() {
	Description("Exact retained image descriptor.")
	Attribute("id", String, "Exact retained image.", func() { MinLength(1) })
	Required("id")
})

var _ = Service("images", func() {
	Method("view", func() {
		Payload(func() {
			Attribute("id", String, "Selected image.")
			Required("id")
		})
		Result(func() {
			Attribute("source", source, "Exact selected image.")
			Required("source")
		})
	})
	Agent("reader", "Reads selected images.", func() {
		Use("pictures", func() {
			Tool("view", "View selected image.", func() {
				BindTo("images", "view")
				ServerData("fixture.shared.v1", source, func() {
					AudienceEvidence()
					NativeImage()
					FromMethodResultField("source")
				})
			})
			Tool("inspect", "Inspect selected image.", func() {
				BindTo("images", "view")
				ServerData("fixture.shared.v1", source, func() {
					AudienceEvidence()
					NativeImage()
					FromMethodResultField("source")
				})
			})
		})
	})
})
