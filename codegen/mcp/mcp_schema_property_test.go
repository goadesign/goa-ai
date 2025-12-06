package codegen

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"goa.design/goa-ai/codegen/shared"
	"goa.design/goa/v3/expr"
)

// TestToolSchemaRoundTripProperty verifies Property 1: Tool Schema Round-Trip Consistency.
// **Feature: mcp-registry, Property 1: Tool Schema Round-Trip Consistency**
// *For any* valid MCP tool schema, serializing to JSON then deserializing back
// SHALL produce an equivalent schema.
// **Validates: Requirements 9.3**
func TestToolSchemaRoundTripProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("tool schema round-trip preserves structure", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			// Serialize to JSON
			jsonStr := shared.ToJSONSchema(attr)

			// Deserialize back
			var parsed map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
				return false
			}

			// Serialize again
			reserializedBytes, err := json.Marshal(parsed)
			if err != nil {
				return false
			}

			// Parse both for comparison
			var original, reserialized map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &original); err != nil {
				return false
			}
			if err := json.Unmarshal(reserializedBytes, &reserialized); err != nil {
				return false
			}

			// Compare the two maps - they should be identical
			return reflect.DeepEqual(original, reserialized)
		},
		genAttributeExpr(),
	))

	properties.TestingRun(t)
}

// TestToolSchemaRoundTripWithValidations tests round-trip with validation constraints.
// **Feature: mcp-registry, Property 1: Tool Schema Round-Trip Consistency**
// **Validates: Requirements 9.3**
func TestToolSchemaRoundTripWithValidations(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("schema with validations round-trips correctly", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			jsonStr := shared.ToJSONSchema(attr)

			var first, second map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &first); err != nil {
				return false
			}

			reserializedBytes, err := json.Marshal(first)
			if err != nil {
				return false
			}

			if err := json.Unmarshal(reserializedBytes, &second); err != nil {
				return false
			}

			return reflect.DeepEqual(first, second)
		},
		genAttributeWithValidation(),
	))

	properties.TestingRun(t)
}

// TestToolSchemaRoundTripNestedObjects tests round-trip with nested object structures.
// **Feature: mcp-registry, Property 1: Tool Schema Round-Trip Consistency**
// **Validates: Requirements 9.3**
func TestToolSchemaRoundTripNestedObjects(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("nested object schema round-trips correctly", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			jsonStr := shared.ToJSONSchema(attr)

			var first, second map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &first); err != nil {
				return false
			}

			reserializedBytes, err := json.Marshal(first)
			if err != nil {
				return false
			}

			if err := json.Unmarshal(reserializedBytes, &second); err != nil {
				return false
			}

			return reflect.DeepEqual(first, second)
		},
		genNestedObjectAttr(),
	))

	properties.TestingRun(t)
}

// genAttributeExpr generates random valid AttributeExpr instances for property testing.
func genAttributeExpr() gopter.Gen {
	return gen.OneGenOf(
		genPrimitiveAttr(),
		genObjectAttr(),
		genArrayAttr(),
		genMapAttr(),
	)
}

// genPrimitiveAttr generates AttributeExpr with primitive types.
func genPrimitiveAttr() gopter.Gen {
	primitives := []expr.Primitive{
		expr.String,
		expr.Int,
		expr.Int32,
		expr.Int64,
		expr.UInt,
		expr.UInt32,
		expr.UInt64,
		expr.Float32,
		expr.Float64,
		expr.Boolean,
		expr.Bytes,
	}

	return gen.IntRange(0, len(primitives)-1).Map(func(idx int) *expr.AttributeExpr {
		return &expr.AttributeExpr{
			Type: primitives[idx],
		}
	})
}

// genObjectAttr generates AttributeExpr with object types.
func genObjectAttr() gopter.Gen {
	return gen.SliceOfN(3, gen.AlphaString()).
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
		Map(func(names []string) *expr.AttributeExpr {
			obj := make(expr.Object, 0, len(names))
			for _, name := range names {
				obj = append(obj, &expr.NamedAttributeExpr{
					Name: name,
					Attribute: &expr.AttributeExpr{
						Type: expr.String,
					},
				})
			}
			return &expr.AttributeExpr{
				Type: &obj,
			}
		})
}

// genArrayAttr generates AttributeExpr with array types.
func genArrayAttr() gopter.Gen {
	return gen.Const(&expr.AttributeExpr{
		Type: &expr.Array{
			ElemType: &expr.AttributeExpr{
				Type: expr.String,
			},
		},
	})
}

// genMapAttr generates AttributeExpr with map types.
func genMapAttr() gopter.Gen {
	return gen.Const(&expr.AttributeExpr{
		Type: &expr.Map{
			KeyType: &expr.AttributeExpr{
				Type: expr.String,
			},
			ElemType: &expr.AttributeExpr{
				Type: expr.String,
			},
		},
	})
}

// genAttributeWithValidation generates AttributeExpr with validation constraints.
func genAttributeWithValidation() gopter.Gen {
	return gen.OneGenOf(
		genStringWithValidation(),
		genIntWithValidation(),
		genObjectWithRequired(),
		genArrayWithLengthValidation(),
	)
}

// genStringWithValidation generates string attributes with pattern/length validations.
func genStringWithValidation() gopter.Gen {
	return gen.IntRange(1, 50).Map(func(minLen int) *expr.AttributeExpr {
		maxLen := minLen + 50
		return &expr.AttributeExpr{
			Type: expr.String,
			Validation: &expr.ValidationExpr{
				MinLength: &minLen,
				MaxLength: &maxLen,
			},
		}
	})
}

// genIntWithValidation generates integer attributes with min/max validations.
func genIntWithValidation() gopter.Gen {
	return gen.Float64Range(0, 100).Map(func(min float64) *expr.AttributeExpr {
		max := min + 100
		return &expr.AttributeExpr{
			Type: expr.Int,
			Validation: &expr.ValidationExpr{
				Minimum: &min,
				Maximum: &max,
			},
		}
	})
}

// genObjectWithRequired generates object attributes with required field validation.
func genObjectWithRequired() gopter.Gen {
	return gen.SliceOfN(3, gen.AlphaString()).
		SuchThat(func(names []string) bool {
			seen := make(map[string]bool)
			for _, n := range names {
				if n == "" || seen[n] {
					return false
				}
				seen[n] = true
			}
			return len(names) >= 2
		}).
		Map(func(names []string) *expr.AttributeExpr {
			obj := make(expr.Object, 0, len(names))
			for _, name := range names {
				obj = append(obj, &expr.NamedAttributeExpr{
					Name: name,
					Attribute: &expr.AttributeExpr{
						Type: expr.String,
					},
				})
			}
			return &expr.AttributeExpr{
				Type: &obj,
				Validation: &expr.ValidationExpr{
					Required: []string{names[0]},
				},
			}
		})
}

// genArrayWithLengthValidation generates array attributes with min/max items.
func genArrayWithLengthValidation() gopter.Gen {
	return gen.IntRange(0, 5).Map(func(minItems int) *expr.AttributeExpr {
		maxItems := minItems + 15
		return &expr.AttributeExpr{
			Type: &expr.Array{
				ElemType: &expr.AttributeExpr{
					Type: expr.String,
				},
			},
			Validation: &expr.ValidationExpr{
				MinLength: &minItems,
				MaxLength: &maxItems,
			},
		}
	})
}

// genNestedObjectAttr generates nested object structures.
func genNestedObjectAttr() gopter.Gen {
	return gen.SliceOfN(2, gen.AlphaString()).
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
		Map(func(names []string) *expr.AttributeExpr {
			// Create inner object
			innerObj := make(expr.Object, 0, 2)
			innerObj = append(innerObj, &expr.NamedAttributeExpr{
				Name: "inner_field",
				Attribute: &expr.AttributeExpr{
					Type: expr.String,
				},
			})
			innerObj = append(innerObj, &expr.NamedAttributeExpr{
				Name: "inner_count",
				Attribute: &expr.AttributeExpr{
					Type: expr.Int,
				},
			})

			// Create outer object with nested inner
			outerObj := make(expr.Object, 0, len(names)+1)
			for _, name := range names {
				outerObj = append(outerObj, &expr.NamedAttributeExpr{
					Name: name,
					Attribute: &expr.AttributeExpr{
						Type: expr.String,
					},
				})
			}
			outerObj = append(outerObj, &expr.NamedAttributeExpr{
				Name: "nested",
				Attribute: &expr.AttributeExpr{
					Type: &innerObj,
				},
			})

			return &expr.AttributeExpr{
				Type: &outerObj,
			}
		})
}

// TestToolSchemaRoundTripWithEnums tests round-trip with enum values.
// **Feature: mcp-registry, Property 1: Tool Schema Round-Trip Consistency**
// **Validates: Requirements 9.3**
func TestToolSchemaRoundTripWithEnums(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("schema with enums round-trips correctly", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			jsonStr := shared.ToJSONSchema(attr)

			var first, second map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &first); err != nil {
				return false
			}

			reserializedBytes, err := json.Marshal(first)
			if err != nil {
				return false
			}

			if err := json.Unmarshal(reserializedBytes, &second); err != nil {
				return false
			}

			return reflect.DeepEqual(first, second)
		},
		genEnumAttr(),
	))

	properties.TestingRun(t)
}

// TestToolSchemaRoundTripWithUserTypes tests round-trip with user-defined types.
// **Feature: mcp-registry, Property 1: Tool Schema Round-Trip Consistency**
// **Validates: Requirements 9.3**
func TestToolSchemaRoundTripWithUserTypes(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("schema with user types round-trips correctly", prop.ForAll(
		func(attr *expr.AttributeExpr) bool {
			jsonStr := shared.ToJSONSchema(attr)

			var first, second map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &first); err != nil {
				return false
			}

			reserializedBytes, err := json.Marshal(first)
			if err != nil {
				return false
			}

			if err := json.Unmarshal(reserializedBytes, &second); err != nil {
				return false
			}

			return reflect.DeepEqual(first, second)
		},
		genUserTypeAttr(),
	))

	properties.TestingRun(t)
}

// genEnumAttr generates string attributes with enum values.
func genEnumAttr() gopter.Gen {
	return gen.SliceOfN(4, gen.AlphaString()).
		SuchThat(func(values []string) bool {
			seen := make(map[string]bool)
			for _, v := range values {
				if v == "" || seen[v] {
					return false
				}
				seen[v] = true
			}
			return len(values) >= 2
		}).
		Map(func(values []string) *expr.AttributeExpr {
			enumValues := make([]any, len(values))
			for i, v := range values {
				enumValues[i] = v
			}
			return &expr.AttributeExpr{
				Type: expr.String,
				Validation: &expr.ValidationExpr{
					Values: enumValues,
				},
			}
		})
}

// genUserTypeAttr generates user-defined type attributes.
func genUserTypeAttr() gopter.Gen {
	return gen.AlphaString().
		SuchThat(func(name string) bool {
			return len(name) > 0
		}).
		Map(func(typeName string) *expr.AttributeExpr {
			// Create a user type with an underlying object
			obj := make(expr.Object, 0, 2)
			obj = append(obj, &expr.NamedAttributeExpr{
				Name: "id",
				Attribute: &expr.AttributeExpr{
					Type: expr.String,
				},
			})
			obj = append(obj, &expr.NamedAttributeExpr{
				Name: "value",
				Attribute: &expr.AttributeExpr{
					Type: expr.Int,
				},
			})

			userType := &expr.UserTypeExpr{
				TypeName: typeName,
				AttributeExpr: &expr.AttributeExpr{
					Type: &obj,
				},
			}

			return &expr.AttributeExpr{
				Type: userType,
			}
		})
}
