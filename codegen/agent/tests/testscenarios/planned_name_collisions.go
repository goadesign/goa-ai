// Package testscenarios provides Goa designs that exercise complete agent code generation.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ToolSpecNameCollisions defines relocated tool types whose package names
// match generated JSON metadata and validation helpers in the codec file.
func ToolSpecNameCollisions() func() {
	return func() {
		API("tool spec name collisions", func() {})

		var InspectInput = Type("InspectInput", func() {
			Attribute("descriptions", String, "Description metadata input.", func() {
				Meta("struct:field:type", "inspectPayloadFieldDescs.Value", "generated.local/custom/field_descriptions", "inspectPayloadFieldDescs")
			})
			Attribute("json_types", String, "JSON type metadata input.", func() {
				Meta("struct:field:type", "inspectPayloadFieldJSONTypes.Value", "generated.local/custom/field_json_types", "inspectPayloadFieldJSONTypes")
			})
			Attribute("validator", String, "JSON validator input.", func() {
				Meta("struct:field:type", "validateInspectPayloadJSON.Value", "generated.local/custom/json_validator", "validateInspectPayloadJSON")
			})
			Attribute("validation", String, "Validation description input.", func() {
				Meta("struct:field:type", "enrichInspectPayloadValidationError.Value", "generated.local/custom/validation_description", "enrichInspectPayloadValidationError")
			})
			Attribute("field_type", String, "Invalid field type input.", func() {
				Meta("struct:field:type", "invalidInspectPayloadFieldTypeError.Value", "generated.local/custom/field_type_error", "invalidInspectPayloadFieldTypeError")
			})
			Required("descriptions", "json_types", "validator", "validation", "field_type")
		})

		Service("alpha", func() {
			Agent("scribe", "Checks generated JSON contracts.", func() {
				Use("helpers", func() {
					Tool("inspect", "Checks one input.", func() {
						Args(InspectInput)
						Return(String)
					})
				})
			})
		})
	}
}

// CompletionNameCollisions defines completion names that request the same Go
// names as another completion's generated helper functions.
func CompletionNameCollisions() func() {
	return func() {
		API("completion name collisions", func() {})
		var Result = Type("CompletionCollisionResult", func() {
			Attribute("text", String, "Generated completion text.")
			Required("text")
			Example(map[string]any{"text": "Generated text."})
		})

		Service("tasks", func() {
			for _, name := range []string{
				"draft",
				"draft_example",
				"complete_draft",
				"stream_complete_draft",
			} {
				Completion(name, "Generates collision test output.", func() {
					Return(Result)
				})
			}
			Agent("writer", "Writes completion examples.", func() {})
		})
	}
}

// MCPBootstrapNameCollisions defines two MCP routes whose service names map to
// the same Go identifier in generated starter code.
func MCPBootstrapNameCollisions() func() {
	return func() {
		API("MCP bootstrap name collisions", func() {})
		for _, service := range []string{"calc-api", "calc_api"} {
			Service(service, func() {
				MCP("core", "1.0.0")
				JSONRPC(func() { POST("/rpc") })
				Method("ping", func() {
					Result(String)
					Tool("ping", "Returns a value.")
				})
			})
		}
		var First = Toolset("first", FromMCP("calc-api", "core"))
		var Second = Toolset("second", FromMCP("calc_api", "core"))
		Service("alpha", func() {
			Agent("scribe", "Uses colliding MCP routes.", func() {
				Use(First)
				Use(Second)
			})
		})
	}
}

// BootstrapImportNameCollisions defines an agent whose package requests the
// same import name as the Go context package used by starter bootstrap code.
func BootstrapImportNameCollisions() func() {
	return func() {
		API("bootstrap import name collisions", func() {})
		Service("calc", func() {
			MCP("core", "1.0.0")
			JSONRPC(func() { POST("/rpc") })
			Method("ping", func() {
				Result(String)
				Tool("ping", "Returns a value.")
			})
		})
		var CalcCore = Toolset(FromMCP("calc", "core"))
		Service("alpha", func() {
			Agent("context", "Uses an import name owned by the standard library.", func() {
				Use(CalcCore)
			})
		})
	}
}
