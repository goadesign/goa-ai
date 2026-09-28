// This design imports only the DSLs. Normal generation discovers both tool
// codecs and the complete original codecs in the shared types package.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

const includeNative = true
const reverseTools = false

var _ = API("codec", func() {})

var DocumentID = Type("DocumentID", String, func() {
	Meta("struct:pkg:path", "types")
	Meta("type:generate:force")
	Meta("openapi:generate", "false")
	Pattern(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
})

var ImageSource = Type("ImageSource", func() {
	Meta("struct:pkg:path", "types")
	Meta("type:generate:force")
	Meta("openapi:generate", "false")
	Attribute("sourceDocumentId", DocumentID, "Document that owns the image.")
	Attribute("contentSHA256", String, "Digest of the image bytes.", func() {
		Pattern(`^[a-f0-9]{64}$`)
	})
	Attribute("format", String, "Image format.", func() {
		Enum("png", "jpeg", "gif", "webp")
	})
	Attribute("sizeBytes", Int64, "Encoded image size.", func() { Minimum(1) })
	Required("sourceDocumentId", "contentSHA256", "format", "sizeBytes")
	Example(map[string]any{
		"sourceDocumentId": "12345678-1234-4234-8234-123456789abc",
		"contentSHA256":    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"format":           "png", "sizeBytes": 1,
	})
})

var Detail = Type("Detail", func() {
	Meta("struct:pkg:path", "types")
	Attribute("detailText", String)
	Required("detailText")
	Example(map[string]any{"detailText": "selected"})
})

var Node = Type("Node", func() {
	Meta("struct:pkg:path", "types")
	Attribute("displayName", String)
	Attribute("ownerId", DocumentID)
	Attribute("nextNode", "Node")
	OneOf("choiceData", func() {
		Attribute("detail", Detail)
		Attribute("text", String)
	})
	Required("displayName")
	Example(map[string]any{"displayName": "leaf"})
})

var NodeAlias = Type("NodeAlias", ArrayOf(Node), func() {
	Meta("struct:pkg:path", "types")
	MinLength(1)
})

// Its ordinary transport requests the same preferred name as native Node.
var NodeNativeImage = Type("NodeNativeImage", func() {
	Meta("struct:pkg:path", "types")
	Attribute("collisionValue", String)
	Required("collisionValue")
})

var Graph = Type("Graph", func() {
	Meta("struct:pkg:path", "types")
	Meta("type:generate:force")
	Attribute("firstNode", Node)
	Attribute("secondNode", Node)
	Attribute("nodeList", ArrayOf(Node), func() { MinLength(1) })
	Attribute("nodeMap", MapOf(String, Node))
	Attribute("aliasNode", NodeAlias)
	Attribute("collisionNode", NodeNativeImage)
	Required("firstNode", "secondNode", "nodeList", "nodeMap", "aliasNode", "collisionNode")
	Example(map[string]any{
		"firstNode":     map[string]any{"displayName": "first"},
		"secondNode":    map[string]any{"displayName": "second"},
		"nodeList":      []any{map[string]any{"displayName": "list"}},
		"nodeMap":       map[string]any{"selected": map[string]any{"displayName": "map"}},
		"aliasNode":     []any{map[string]any{"displayName": "alias"}},
		"collisionNode": map[string]any{"collisionValue": "ordinary"},
	})
})

var Citation = Type("Citation", func() {
	Attribute("fileId", String)
	Attribute("details", Detail)
	Required("fileId", "details")
})

// Unsupported complete originals stay skipped; their supported child remains
// discoverable without an import of any runtime codec package in this design.
var DynamicOriginal = Type("DynamicOriginal", func() {
	Meta("struct:pkg:path", "unsupported")
	Meta("type:generate:force")
	Attribute("dynamicValue", Any)
	Attribute("child", Detail)
})

var CustomOriginal = Type("CustomOriginal", func() {
	Meta("struct:pkg:path", "unsupported")
	Meta("type:generate:force")
	Attribute("customValue", Bytes, func() {
		Meta("struct:field:type", "json.RawMessage", "encoding/json")
	})
	Attribute("child", Detail)
})

var ReadInput = Type("ReadInput", func() {
	Attribute("requestKey", String)
	Required("requestKey")
})

var ReadOutput = Type("ReadOutput", func() {
	Attribute("ready", Boolean)
	Attribute("image", ImageSource)
	Required("ready", "image")
})

var _ = Service("sample", func() {
	Method("Read", func() {
		Payload(ReadInput)
		Result(ReadOutput)
	})
	Agent("reader", "Reads synthetic image sources.", func() {
		Use("pictures", func() {
			ordinary := func() {
				Tool("ordinary", "Preserve ordinary JSON names.", func() {
					Args(ImageSource)
					Return(ImageSource)
					ServerData("fixture.unmarked", ImageSource, func() { AudienceEvidence() })
					ServerData("fixture.citation", Citation, func() { AudienceEvidence() })
				})
				Tool("graph", "Return a finite synthetic graph.", func() {
					Args(Graph)
					Return(Graph)
					ServerData("fixture.graph.ordinary", Graph, func() { AudienceEvidence() })
				})
			}
			native := func() {
				if !includeNative {
					return
				}
				Tool("view_image", "Read a synthetic image source.", func() {
					Args(ReadInput)
					Return(func() {
						Attribute("ready", Boolean)
						Required("ready")
					})
					BindTo("sample", "Read")
					ServerData("fixture.image", ImageSource, func() {
						AudienceEvidence()
						NativeImage()
						FromMethodResultField("image")
					})
				})
				Tool("native_graph", "Retain a finite synthetic graph.", func() {
					Args(Graph)
					Return(Graph)
					ServerData("fixture.graph", Graph, func() {
						AudienceEvidence()
						NativeImage()
					})
					ServerData("fixture.inline", func() {
						Attribute("displayName", String)
						Attribute("childNode", Node)
						Required("displayName", "childNode")
					}, func() {
						AudienceEvidence()
						NativeImage()
					})
				})
			}
			if reverseTools {
				native()
				ordinary()
			} else {
				ordinary()
				native()
			}
		})
	})
})
