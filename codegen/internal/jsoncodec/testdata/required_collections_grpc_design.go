// This design exchanges records over gRPC and forces a separate complete JSON
// envelope. Unsupported siblings must not remove that supported envelope codec.
package design

import (
	_ "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("collections", func() {})

var (
	Item = Type("Item", func() {
		Field(1, "label", String, "The item's text.")
		Required("label")
	})

	Node = Type("Node", func() {
		Field(1, "items", ArrayOf(Item), "The node's items, including no items.")
		Field(2, "labels", MapOf(String, String), "The node's labels, including no labels.")
		Field(3, "child", "Node", "An optional child.")
		Field(4, "children", ArrayOf("Node"), "Optional ordered children.")
		Field(5, "byName", MapOf(String, "Node"), "Optional named children.")
		Required("items", "labels")
	})

	Record = Type("Record", func() {
		Field(1, "items", ArrayOf(Item), "Required items; empty is valid.")
		Field(2, "labels", MapOf(String, String), "Required labels; empty is valid.")
		Field(5, "optionalItems", ArrayOf(Item), "Optional items.")
		Field(6, "optionalLabels", MapOf(String, String), "Optional labels.")
		Field(7, "note", String, "An optional note, which may be empty.")
		Field(8, "node", Node, "An optional recursive node.")
		Required("items", "labels")
	})

	Envelope = Type("Envelope", func() {
		Meta("type:generate:force", "collections")
		Field(1, "record", Record, "The complete record to retain.")
		Required("record")
	})

	AnySibling = Type("AnySibling", func() {
		Meta("type:generate:force", "collections")
		Attribute("value", Any, "An unsupported dynamic value.")
	})

	CustomSibling = Type("CustomSibling", func() {
		Meta("type:generate:force", "collections")
		Attribute("value", Bytes, "An unsupported custom Go value.", func() {
			Meta("struct:field:type", "json.RawMessage", "encoding/json")
		})
	})
)

var _ = Service("collections", func() {
	Description("Exchanges synthetic records to check generated representations.")
	Method("exchange", func() {
		Description("Copies a record through request and response representations.")
		Payload(Record)
		Result(Record)
		GRPC(func() {})
	})
})
