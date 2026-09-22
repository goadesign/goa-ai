// This file defines the generated metadata needed by consumers that discover
// tools after compilation. JSON schemas remain on ToolSchema; this contract
// carries the additional facts that schemas do not describe.
package design

import . "goa.design/goa/v3/dsl"

var ConsumerContract = Type("ConsumerContract", func() {
	Description("Generated execution and presentation facts for one registry tool. The registry includes this complete value in the registration fingerprint.")
	Field(1, "kind", String, "Execution kind fixed by the provider design. Dynamic consumers support service tools and agent tools with a declared executor and configuration. Control tools require compiled runtime integration.", func() {
		Enum("service", "agent", "control")
		Example("service")
	})
	Field(2, "title", String, "Human-readable title declared for the tool.", func() {
		MinLength(1)
		Example("Find records")
	})
	Field(3, "search", ToolSearchDocument, "Word counts generated from the tool's name, title, and description.")
	Field(4, "payload", ToolTypeMetadata, "Generated details of the model-facing argument type.")
	Field(5, "result", ToolTypeMetadata, "Generated details of the result type. Omitted only when the tool returns no value.")
	Field(6, "meta", MapOf(String, ArrayOf(String)), "Consumer-owned design annotations whose semantics belong to the application interpreting each key.")
	Field(7, "required_labels", ArrayOf(String), "Run labels required by provider-side injection for this tool.")
	Field(8, "bounds", ToolBounds, "Declared result limits and continuation relationship.")
	Field(9, "confirmation", ToolConfirmation, "Required user confirmation before this tool may execute.")
	Field(10, "server_data", ArrayOf(ToolServerData), "Closed set of server-only result payloads emitted by this tool.")
	Field(11, "result_reminder", String, "Model guidance emitted after this tool's result.")
	Field(12, "agent", AgentToolTarget, "Worker and immutable application configuration used by a dynamically registered Agent tool.", func() {
		Meta("struct:tag:json", "Agent,omitempty")
	})
	Required("kind", "title", "search", "payload")
})

var ToolSearchDocument = Type("ToolSearchDocument", func() {
	Description("Precomputed word frequencies for local tool retrieval.")
	Field(1, "length", Int, "Total number of words in this document, including repeats.", func() {
		Minimum(1)
		Example(12)
	})
	Field(2, "terms", MapOf(String, Int), "Lowercase words and their positive occurrence counts.", func() {
		MinLength(1)
		Elem(func() { Minimum(1) })
	})
	Required("length", "terms")
})

var ToolTypeMetadata = Type("ToolTypeMetadata", func() {
	Description("Precomputed schema variants, examples, and field details for one tool value.")
	Field(1, "name", String, "Generated type name used in diagnostics.")
	Field(2, "schema_without_root_example", Bytes, "Canonical schema with its root example omitted for model providers that carry examples separately.", func() {
		MinLength(1)
	})
	Field(3, "example_json", Bytes, "Canonical example JSON when the design supplies one.")
	Field(4, "fields", ArrayOf(ToolFieldMetadata), "Field paths, descriptions, and union requirements prepared by code generation.")
	Required("schema_without_root_example")
})

var ToolFieldMetadata = Type("ToolFieldMetadata", func() {
	Description("One generated field description, including the union branches in which it exists.")
	Field(1, "path", ArrayOf(ToolFieldPathSegment), "Path to this field. An omitted path identifies the root value.")
	Field(2, "json_type", String, "Single JSON type accepted by this field, when known.", func() {
		Enum("object", "array", "string", "number", "integer", "boolean", "null")
	})
	Field(3, "description", String, "Description already present in the tool schema.")
	Field(4, "branches", ArrayOf(ToolUnionBranch), "All branch selections required for this field to apply.")
	Field(5, "discriminator_values", ArrayOf(String), "Allowed branch names when this field selects a union branch.")
})

var ToolFieldPathSegment = Type("ToolFieldPathSegment", func() {
	Description("One fixed JSON property or one caller-selected array index or map key.")
	OneOf("segment", "Exactly one path segment kind.", func() {
		TypeName("ToolFieldSegment")
		Field(1, "field", String, "Exact JSON property name, including empty or punctuation-containing names.")
		Field(2, "element", ToolCollectionElement, "One caller-selected array index or map key.")
	})
	Required("segment")
})

var ToolCollectionElement = Type("ToolCollectionElement", func() {
	Description("Marks one array index or map key without prescribing its value.")
})

var ToolUnionBranch = Type("ToolUnionBranch", func() {
	Description("One discriminator value that makes a generated field applicable.")
	Field(1, "discriminator", ArrayOf(ToolFieldPathSegment), "Path to the union's discriminator property.", func() {
		MinLength(1)
	})
	Field(2, "value", String, "Branch name required at the discriminator.", func() {
		MinLength(1)
	})
	Required("discriminator", "value")
})

var ToolBounds = Type("ToolBounds", func() {
	Description("Declares that successful results include the runtime's canonical result bounds.")
	Field(1, "paging", ToolPaging, "Cursor-based continuation contract, when supported.")
})

var ToolPaging = Type("ToolPaging", func() {
	Description("Generated relationship between a query tool and its continuation.")
	Field(1, "continue_tool", String, "Qualified tool that advances this result; omitted when the tool advances itself.")
	Field(2, "source_tool", String, "Qualified query tool whose retained arguments a dedicated continuation advances.")
	Field(3, "replay_payload", Boolean, "Whether the runtime retains the source query's arguments and replaces only its cursor.")
	Field(4, "cursor_field", String, "JSON argument name containing the continuation cursor.", func() { MinLength(1) })
	Field(5, "next_cursor_field", String, "JSON result name containing the next cursor.", func() { MinLength(1) })
	Required("replay_payload", "cursor_field", "next_cursor_field")
})

var ToolConfirmation = Type("ToolConfirmation", func() {
	Description("Templates rendered against canonical JSON arguments before execution.")
	Field(1, "title", String, "Optional title displayed with the confirmation prompt.")
	Field(2, "prompt_template", String, "Go text/template reading JSON argument names to produce the confirmation prompt.", func() { MinLength(1) })
	Field(3, "denied_result_template", String, "Go text/template reading JSON argument names to produce a schema-valid denial result.", func() { MinLength(1) })
	Required("prompt_template", "denied_result_template")
})

var ToolServerData = Type("ToolServerData", func() {
	Description("One server-only result kind, its audience, and complete payload contract.")
	Field(1, "kind", String, "Unique kind emitted by this tool.", func() { MinLength(1) })
	Field(2, "audience", String, "Consumers allowed to receive this payload.", func() {
		Enum("timeline", "internal", "evidence")
	})
	Field(3, "description", String, "Description of the data carried by this kind.")
	Field(4, "schema", Bytes, "Canonical JSON schema for the item data.", func() { MinLength(1) })
	Field(5, "type", ToolTypeMetadata, "Generated examples and field details for the item data.")
	Required("kind", "audience", "schema", "type")
})

var AgentToolTarget = Type("AgentToolTarget", func() {
	Description("A native child Agent invocation. The worker is authorized at startup; the application owns the immutable configuration reference.")
	Field(1, "executor", String, "Identifier of the preconfigured Agent worker that accepts this configuration.", func() {
		MinLength(1)
		Example("generic.agent")
	})
	Field(2, "configuration", String, "Immutable application configuration reference retained with each accepted tool call.", func() {
		MinLength(1)
		Example("installer/revisions/7")
	})
	Required("executor", "configuration")
})
