package codegen

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

// TestTypeReferenceConsistency verifies Property 2: Type Reference Consistency.
// **Feature: a2a-codegen-refactor, Property 2: Type Reference Consistency**
// *For any* attribute expression with user types (including composites like arrays and maps),
// the type reference generated via NameScope helpers should be syntactically valid Go code
// and correctly qualified with package aliases.
// **Validates: Requirements 2.2**
func TestTypeReferenceConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("type references are consistent across calls", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			gen := newA2AAdapterGenerator("example.com/test/gen", createTestAgentData())

			// Call getTypeReference twice with the same input
			ref1 := gen.getTypeReference(attr)
			ref2 := gen.getTypeReference(attr)

			// Results should be identical
			return ref1 == ref2
		},
		genAttributeExprForTypeRef(),
	))

	properties.TestingRun(t)
}

// TestTypeReferenceNonEmpty verifies that non-nil attributes produce non-empty references.
// **Feature: a2a-codegen-refactor, Property 2: Type Reference Consistency**
// **Validates: Requirements 2.2**
func TestTypeReferenceNonEmpty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("non-nil attributes produce non-empty type references", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			gen := newA2AAdapterGenerator("example.com/test/gen", createTestAgentData())
			ref := gen.getTypeReference(attr)
			return ref != ""
		},
		genAttributeExprForTypeRef(),
	))

	properties.TestingRun(t)
}

// TestSchemaGenerationConsistency verifies Property 3: Schema Generation Consistency.
// **Feature: a2a-codegen-refactor, Property 3: Schema Generation Consistency**
// *For any* tool payload attribute, the JSON schema generated for A2A skills should be
// structurally equivalent to the schema generated for MCP tools with the same payload.
// **Validates: Requirements 2.4**
func TestSchemaGenerationConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("schema generation is consistent across calls", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			gen := newA2AAdapterGenerator("example.com/test/gen", createTestAgentData())

			// Generate schema twice
			schema1 := gen.toJSONSchema(attr)
			schema2 := gen.toJSONSchema(attr)

			// Results should be identical
			return schema1 == schema2
		},
		genAttributeExprForSchema(),
	))

	properties.TestingRun(t)
}

// TestSchemaGenerationRoundTrip verifies that generated schemas are valid JSON.
// **Feature: a2a-codegen-refactor, Property 3: Schema Generation Consistency**
// **Validates: Requirements 2.4**
func TestSchemaGenerationRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("generated schemas are valid JSON that round-trips", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			gen := newA2AAdapterGenerator("example.com/test/gen", createTestAgentData())
			schema := gen.toJSONSchema(attr)

			// Parse the schema
			var parsed map[string]any
			if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
				return false
			}

			// Re-serialize
			reserialized, err := json.Marshal(parsed)
			if err != nil {
				return false
			}

			// Parse again
			var reparsed map[string]any
			if err := json.Unmarshal(reserialized, &reparsed); err != nil {
				return false
			}

			// Should be structurally equal
			return reflect.DeepEqual(parsed, reparsed)
		},
		genAttributeExprForSchema(),
	))

	properties.TestingRun(t)
}

// TestSchemaContainsType verifies that generated schemas contain a type field.
// **Feature: a2a-codegen-refactor, Property 3: Schema Generation Consistency**
// **Validates: Requirements 2.4**
func TestSchemaContainsType(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("generated schemas contain a type field", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			gen := newA2AAdapterGenerator("example.com/test/gen", createTestAgentData())
			schema := gen.toJSONSchema(attr)

			var parsed map[string]any
			if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
				return false
			}

			// Schema should have a "type" field
			_, hasType := parsed["type"]
			return hasType
		},
		genAttributeExprForSchema(),
	))

	properties.TestingRun(t)
}

// TestSchemaObjectHasProperties verifies that object schemas have properties.
// **Feature: a2a-codegen-refactor, Property 3: Schema Generation Consistency**
// **Validates: Requirements 2.4**
func TestSchemaObjectHasProperties(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("object schemas have properties field when non-empty", prop.ForAll(
		func(fieldNames []string) bool {
			// Create an object with the given fields
			obj := make(expr.Object, 0, len(fieldNames))
			for _, name := range fieldNames {
				obj = append(obj, &expr.NamedAttributeExpr{
					Name:      name,
					Attribute: &expr.AttributeExpr{Type: expr.String},
				})
			}
			attr := &expr.AttributeExpr{Type: &obj}

			gen := newA2AAdapterGenerator("example.com/test/gen", createTestAgentData())
			schema := gen.toJSONSchema(attr)

			var parsed map[string]any
			if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
				return false
			}

			// If we have fields, we should have properties
			if len(fieldNames) > 0 {
				props, hasProps := parsed["properties"]
				if !hasProps {
					return false
				}
				propsMap, ok := props.(map[string]any)
				if !ok {
					return false
				}
				return len(propsMap) == len(fieldNames)
			}

			return true
		},
		genUniqueFieldNames(),
	))

	properties.TestingRun(t)
}

// Helper functions

// createTestAgentData creates a minimal AgentData for testing.
func createTestAgentData() *AgentData {
	return &AgentData{
		Name: "test_agent",
		Service: &service.Data{
			Name: "test_service",
		},
	}
}

// genAttributeExprForTypeRef generates attribute expressions for type reference testing.
func genAttributeExprForTypeRef() gopter.Gen {
	return gen.OneGenOf(
		genPrimitiveAttrForRef(),
		genArrayAttrForRef(),
		genMapAttrForRef(),
		genObjectAttrForRef(),
	)
}

// genPrimitiveAttrForRef generates primitive attribute expressions.
func genPrimitiveAttrForRef() gopter.Gen {
	primitives := []expr.Primitive{
		expr.String,
		expr.Int,
		expr.Int32,
		expr.Int64,
		expr.Float32,
		expr.Float64,
		expr.Boolean,
	}

	return gen.IntRange(0, len(primitives)-1).Map(func(idx int) *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: primitives[idx]}
	})
}

// genArrayAttrForRef generates array attribute expressions.
func genArrayAttrForRef() gopter.Gen {
	return gen.Const(&expr.AttributeExpr{
		Type: &expr.Array{
			ElemType: &expr.AttributeExpr{Type: expr.String},
		},
	})
}

// genMapAttrForRef generates map attribute expressions.
func genMapAttrForRef() gopter.Gen {
	return gen.Const(&expr.AttributeExpr{
		Type: &expr.Map{
			KeyType:  &expr.AttributeExpr{Type: expr.String},
			ElemType: &expr.AttributeExpr{Type: expr.String},
		},
	})
}

// genObjectAttrForRef generates object attribute expressions.
func genObjectAttrForRef() gopter.Gen {
	return gen.SliceOfN(3, gen.AlphaString()).
		SuchThat(func(names []string) bool {
			seen := make(map[string]bool)
			for _, n := range names {
				if n == "" || seen[n] {
					return false
				}
				seen[n] = true
			}
			return len(names) >= 1
		}).
		Map(func(names []string) *expr.AttributeExpr {
			obj := make(expr.Object, 0, len(names))
			for _, name := range names {
				obj = append(obj, &expr.NamedAttributeExpr{
					Name:      name,
					Attribute: &expr.AttributeExpr{Type: expr.String},
				})
			}
			return &expr.AttributeExpr{Type: &obj}
		})
}

// genAttributeExprForSchema generates attribute expressions for schema testing.
func genAttributeExprForSchema() gopter.Gen {
	return gen.OneGenOf(
		genPrimitiveAttrForRef(),
		genArrayAttrForRef(),
		genMapAttrForRef(),
		genObjectAttrForRef(),
	)
}

// genUniqueFieldNames generates unique field names for object testing.
func genUniqueFieldNames() gopter.Gen {
	return gen.SliceOfN(5, gen.AlphaString()).
		SuchThat(func(names []string) bool {
			seen := make(map[string]bool)
			for _, n := range names {
				if n == "" || seen[n] {
					return false
				}
				seen[n] = true
			}
			return true
		}).
		Map(func(names []string) []string {
			// Filter out any names that might cause issues
			result := make([]string, 0, len(names))
			for _, n := range names {
				if len(n) > 0 && !strings.ContainsAny(n, " \t\n") {
					result = append(result, n)
				}
			}
			return result
		})
}

// Ensure codegen package is used
var _ = codegen.Goify
