// This file declares synthetic parameterized resources. The reader accepts the
// exact URI and owns the domain lookup; templates describe selectable addresses.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var (
	// TemplateText declares one text representation and its resource identifier.
	TemplateText = Type("TemplateText", func() {
		Attribute("uri", String, "Resource identifier", func() { Format(FormatURI) })
		Attribute("mimeType", String, "Content media type")
		Attribute("text", String, "Resource contents")
		Required("uri", "text")
	})
	// TemplateBlob declares binary bytes with a resource identifier.
	TemplateBlob = Type("TemplateBlob", func() {
		Attribute("uri", String, "Resource identifier", func() { Format(FormatURI) })
		Attribute("mimeType", String, "Content media type")
		Attribute("blob", Bytes, "Binary resource contents")
		Required("uri")
	})
	// TemplateItem declares exactly one selected resource representation.
	TemplateItem = Type("TemplateItem", func() {
		OneOf("content", "Selected resource representation", func() {
			Attribute("text", TemplateText, "Text resource")
			Attribute("blob", TemplateBlob, "Binary resource")
			TypeName("TemplateRepresentation")
			Meta("struct:field:name", "Selected")
		})
		Required("content")
	})
)

// refereeResourceTemplates binds lossy and overlapping discovery templates to
// one reader so the client URI reaches the owning service without inversion.
func refereeResourceTemplates() {
	Method("read_resource", func() {
		Description("Read synthetic parameterized resources by their exact URI")
		Payload(func() {
			Attribute("uri", String, "Exact requested resource identifier", func() {
				Format(FormatURI)
				Meta("struct:field:name", "Address")
			})
			Required("uri")
		})
		Result(func() {
			Attribute("contents", ArrayOfRequired(TemplateItem), "Ordered resource contents", func() {
				MinLength(1)
				Meta("struct:field:name", "Parts")
			})
			Required("contents")
		})
		ResourceTemplate("referee", "test://template/{id}/data", "text/plain")
		ResourceTemplate("prefix", "test://template/{id:3}/data", "text/plain")
		ResourceTemplate("binary", "test://binary/{id}", "application/octet-stream")
		ResourceTemplate("reserved", "test://reserved/{+path}{?fields*}", "text/plain")
	})
}
