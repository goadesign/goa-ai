package codegen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// SchemaValidationError represents a validation error with structured details.
type SchemaValidationError struct {
	Path    string
	Message string
	Value   any
}

// SchemaValidationErrors collects multiple validation errors.
type SchemaValidationErrors struct {
	Errors []*SchemaValidationError
}

func (e *SchemaValidationError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

func (e *SchemaValidationErrors) Error() string {
	if len(e.Errors) == 0 {
		return "validation failed"
	}
	if len(e.Errors) == 1 {
		return e.Errors[0].Error()
	}
	msgs := make([]string, 0, len(e.Errors))
	for _, err := range e.Errors {
		msgs = append(msgs, err.Error())
	}
	return fmt.Sprintf("validation failed: %s", strings.Join(msgs, "; "))
}

func (e *SchemaValidationErrors) Add(path, message string, value any) {
	e.Errors = append(e.Errors, &SchemaValidationError{
		Path:    path,
		Message: message,
		Value:   value,
	})
}

func (e *SchemaValidationErrors) HasErrors() bool {
	return len(e.Errors) > 0
}

// validateAgainstSchema validates data against a JSON schema.
// This is a copy of the validation logic from the generated template for testing.
func validateAgainstSchema(schema []byte, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal payload for validation: %w", err)
	}

	var schemaObj map[string]any
	if err := json.Unmarshal(schema, &schemaObj); err != nil {
		return fmt.Errorf("parse payload schema: %w", err)
	}

	var dataObj any
	if err := json.Unmarshal(jsonData, &dataObj); err != nil {
		return fmt.Errorf("parse payload data: %w", err)
	}

	errs := &SchemaValidationErrors{}
	validateType(schemaObj, dataObj, "", errs)

	if errs.HasErrors() {
		return errs
	}
	return nil
}

func validateType(schema map[string]any, data any, path string, errs *SchemaValidationErrors) {
	if data == nil {
		if nullable, ok := schema["nullable"].(bool); ok && nullable {
			return
		}
		schemaType, _ := schema["type"].(string)
		if schemaType != "" && schemaType != "null" {
			errs.Add(path, fmt.Sprintf("expected %s, got null", schemaType), nil)
		}
		return
	}

	schemaType, _ := schema["type"].(string)

	switch schemaType {
	case "object":
		validateObject(schema, data, path, errs)
	case "array":
		validateArray(schema, data, path, errs)
	case "string":
		validateString(schema, data, path, errs)
	case "number", "integer":
		validateNumber(schema, data, path, schemaType, errs)
	case "boolean":
		if _, ok := data.(bool); !ok {
			errs.Add(path, fmt.Sprintf("expected boolean, got %T", data), data)
		}
	case "null":
		if data != nil {
			errs.Add(path, fmt.Sprintf("expected null, got %T", data), data)
		}
	case "":
		if oneOf, ok := schema["oneOf"].([]any); ok {
			validateOneOf(oneOf, data, path, errs)
		}
		if anyOf, ok := schema["anyOf"].([]any); ok {
			validateAnyOf(anyOf, data, path, errs)
		}
	}
}

func validateObject(schema map[string]any, data any, path string, errs *SchemaValidationErrors) {
	obj, ok := data.(map[string]any)
	if !ok {
		errs.Add(path, fmt.Sprintf("expected object, got %T", data), data)
		return
	}

	if required, ok := schema["required"].([]any); ok {
		for _, r := range required {
			fieldName, _ := r.(string)
			if _, exists := obj[fieldName]; !exists {
				fieldPath := joinPath(path, fieldName)
				errs.Add(fieldPath, "missing required field", nil)
			}
		}
	}

	props, _ := schema["properties"].(map[string]any)
	additionalProps := true
	if ap, ok := schema["additionalProperties"].(bool); ok {
		additionalProps = ap
	}

	for key, val := range obj {
		fieldPath := joinPath(path, key)
		if props != nil {
			if propSchema, ok := props[key].(map[string]any); ok {
				validateType(propSchema, val, fieldPath, errs)
				continue
			}
		}
		if !additionalProps {
			errs.Add(fieldPath, "additional property not allowed", val)
		}
	}
}

func validateArray(schema map[string]any, data any, path string, errs *SchemaValidationErrors) {
	arr, ok := data.([]any)
	if !ok {
		errs.Add(path, fmt.Sprintf("expected array, got %T", data), data)
		return
	}

	if minItems, ok := schema["minItems"].(float64); ok {
		if float64(len(arr)) < minItems {
			errs.Add(path, fmt.Sprintf("array length %d is less than minimum %d", len(arr), int(minItems)), nil)
		}
	}

	if maxItems, ok := schema["maxItems"].(float64); ok {
		if float64(len(arr)) > maxItems {
			errs.Add(path, fmt.Sprintf("array length %d exceeds maximum %d", len(arr), int(maxItems)), nil)
		}
	}

	if items, ok := schema["items"].(map[string]any); ok {
		for i, item := range arr {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			if path == "" {
				itemPath = fmt.Sprintf("[%d]", i)
			}
			validateType(items, item, itemPath, errs)
		}
	}
}

func validateString(schema map[string]any, data any, path string, errs *SchemaValidationErrors) {
	str, ok := data.(string)
	if !ok {
		errs.Add(path, fmt.Sprintf("expected string, got %T", data), data)
		return
	}

	if minLen, ok := schema["minLength"].(float64); ok {
		if float64(len(str)) < minLen {
			errs.Add(path, fmt.Sprintf("string length %d is less than minimum %d", len(str), int(minLen)), str)
		}
	}

	if maxLen, ok := schema["maxLength"].(float64); ok {
		if float64(len(str)) > maxLen {
			errs.Add(path, fmt.Sprintf("string length %d exceeds maximum %d", len(str), int(maxLen)), str)
		}
	}

	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if eStr, ok := e.(string); ok && eStr == str {
				found = true
				break
			}
		}
		if !found {
			errs.Add(path, fmt.Sprintf("value %q is not in enum", str), str)
		}
	}

	if pattern, ok := schema["pattern"].(string); ok {
		matched, err := regexp.MatchString(pattern, str)
		if err != nil {
			errs.Add(path, fmt.Sprintf("invalid pattern %q: %v", pattern, err), str)
		} else if !matched {
			errs.Add(path, fmt.Sprintf("value %q does not match pattern %q", str, pattern), str)
		}
	}
}

func validateNumber(schema map[string]any, data any, path, schemaType string, errs *SchemaValidationErrors) {
	var num float64
	switch v := data.(type) {
	case float64:
		num = v
	case int:
		num = float64(v)
	case int64:
		num = float64(v)
	case float32:
		num = float64(v)
	default:
		errs.Add(path, fmt.Sprintf("expected %s, got %T", schemaType, data), data)
		return
	}

	if schemaType == "integer" {
		if num != float64(int64(num)) {
			errs.Add(path, fmt.Sprintf("expected integer, got %v", num), data)
		}
	}

	if min, ok := schema["minimum"].(float64); ok {
		if num < min {
			errs.Add(path, fmt.Sprintf("value %v is less than minimum %v", num, min), data)
		}
	}

	if max, ok := schema["maximum"].(float64); ok {
		if num > max {
			errs.Add(path, fmt.Sprintf("value %v exceeds maximum %v", num, max), data)
		}
	}

	if exMin, ok := schema["exclusiveMinimum"].(float64); ok {
		if num <= exMin {
			errs.Add(path, fmt.Sprintf("value %v must be greater than %v", num, exMin), data)
		}
	}

	if exMax, ok := schema["exclusiveMaximum"].(float64); ok {
		if num >= exMax {
			errs.Add(path, fmt.Sprintf("value %v must be less than %v", num, exMax), data)
		}
	}
}

func validateOneOf(oneOf []any, data any, path string, errs *SchemaValidationErrors) {
	validCount := 0
	for _, schema := range oneOf {
		schemaMap, ok := schema.(map[string]any)
		if !ok {
			continue
		}
		testErrs := &SchemaValidationErrors{}
		validateType(schemaMap, data, path, testErrs)
		if !testErrs.HasErrors() {
			validCount++
		}
	}
	if validCount != 1 {
		errs.Add(path, fmt.Sprintf("value must match exactly one schema in oneOf, matched %d", validCount), data)
	}
}

func validateAnyOf(anyOf []any, data any, path string, errs *SchemaValidationErrors) {
	for _, schema := range anyOf {
		schemaMap, ok := schema.(map[string]any)
		if !ok {
			continue
		}
		testErrs := &SchemaValidationErrors{}
		validateType(schemaMap, data, path, testErrs)
		if !testErrs.HasErrors() {
			return
		}
	}
	errs.Add(path, "value does not match any schema in anyOf", data)
}

func joinPath(base, field string) string {
	if base == "" {
		return field
	}
	return base + "." + field
}

// TestSchemaValidationRejectsInvalidPayloadsProperty verifies Property 7:
// Schema Validation Rejects Invalid Payloads.
// **Feature: mcp-registry, Property 7: Schema Validation Rejects Invalid Payloads**
// *For any* tool with a JSON schema, payloads that violate the schema SHALL be
// rejected before invocation.
// **Validates: Requirements 2.2**
func TestSchemaValidationRejectsInvalidPayloadsProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("invalid payloads are rejected by schema validation", prop.ForAll(
		func(tc invalidPayloadTestCase) bool {
			err := validateAgainstSchema(tc.Schema, tc.Payload)
			// Property: invalid payloads MUST be rejected (err != nil)
			return err != nil
		},
		genInvalidPayloadTestCase(),
	))

	properties.TestingRun(t)
}

// TestSchemaValidationAcceptsValidPayloadsProperty verifies that valid payloads
// are accepted by schema validation.
// **Feature: mcp-registry, Property 7: Schema Validation Rejects Invalid Payloads**
// **Validates: Requirements 2.2**
func TestSchemaValidationAcceptsValidPayloadsProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("valid payloads are accepted by schema validation", prop.ForAll(
		func(tc validPayloadTestCase) bool {
			err := validateAgainstSchema(tc.Schema, tc.Payload)
			// Property: valid payloads MUST be accepted (err == nil)
			return err == nil
		},
		genValidPayloadTestCase(),
	))

	properties.TestingRun(t)
}

// TestSchemaValidationRejectsMissingRequiredFields verifies that missing required
// fields are rejected.
// **Feature: mcp-registry, Property 7: Schema Validation Rejects Invalid Payloads**
// **Validates: Requirements 2.2**
func TestSchemaValidationRejectsMissingRequiredFields(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("missing required fields are rejected", prop.ForAll(
		func(tc missingRequiredFieldTestCase) bool {
			err := validateAgainstSchema(tc.Schema, tc.Payload)
			// Property: payloads missing required fields MUST be rejected
			return err != nil
		},
		genMissingRequiredFieldTestCase(),
	))

	properties.TestingRun(t)
}

// TestSchemaValidationRejectsWrongTypes verifies that wrong types are rejected.
// **Feature: mcp-registry, Property 7: Schema Validation Rejects Invalid Payloads**
// **Validates: Requirements 2.2**
func TestSchemaValidationRejectsWrongTypes(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("wrong types are rejected", prop.ForAll(
		func(tc wrongTypeTestCase) bool {
			err := validateAgainstSchema(tc.Schema, tc.Payload)
			// Property: payloads with wrong types MUST be rejected
			return err != nil
		},
		genWrongTypeTestCase(),
	))

	properties.TestingRun(t)
}

// TestSchemaValidationRejectsConstraintViolations verifies that constraint
// violations are rejected.
// **Feature: mcp-registry, Property 7: Schema Validation Rejects Invalid Payloads**
// **Validates: Requirements 2.2**
func TestSchemaValidationRejectsConstraintViolations(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("constraint violations are rejected", prop.ForAll(
		func(tc constraintViolationTestCase) bool {
			err := validateAgainstSchema(tc.Schema, tc.Payload)
			// Property: payloads violating constraints MUST be rejected
			return err != nil
		},
		genConstraintViolationTestCase(),
	))

	properties.TestingRun(t)
}

// Test case types

type invalidPayloadTestCase struct {
	Schema  []byte
	Payload any
}

type validPayloadTestCase struct {
	Schema  []byte
	Payload any
}

type missingRequiredFieldTestCase struct {
	Schema  []byte
	Payload any
}

type wrongTypeTestCase struct {
	Schema  []byte
	Payload any
}

type constraintViolationTestCase struct {
	Schema  []byte
	Payload any
}

// Generators

func genInvalidPayloadTestCase() gopter.Gen {
	return gen.OneGenOf(
		genMissingRequiredFieldTestCase().Map(func(tc missingRequiredFieldTestCase) invalidPayloadTestCase {
			return invalidPayloadTestCase(tc)
		}),
		genWrongTypeTestCase().Map(func(tc wrongTypeTestCase) invalidPayloadTestCase {
			return invalidPayloadTestCase(tc)
		}),
		genConstraintViolationTestCase().Map(func(tc constraintViolationTestCase) invalidPayloadTestCase {
			return invalidPayloadTestCase(tc)
		}),
	)
}

func genValidPayloadTestCase() gopter.Gen {
	return gen.OneGenOf(
		genValidStringPayload(),
		genValidIntegerPayload(),
		genValidObjectPayload(),
		genValidArrayPayload(),
		genValidBooleanPayload(),
	)
}

func genValidStringPayload() gopter.Gen {
	return gen.AlphaString().
		SuchThat(func(s string) bool { return len(s) >= 1 && len(s) <= 100 }).
		Map(func(s string) validPayloadTestCase {
			schema := []byte(`{"type":"string","minLength":1,"maxLength":100}`)
			return validPayloadTestCase{Schema: schema, Payload: s}
		})
}

func genValidIntegerPayload() gopter.Gen {
	return gen.IntRange(0, 100).Map(func(n int) validPayloadTestCase {
		schema := []byte(`{"type":"integer","minimum":0,"maximum":100}`)
		return validPayloadTestCase{Schema: schema, Payload: float64(n)}
	})
}

func genValidObjectPayload() gopter.Gen {
	return gen.AlphaString().
		SuchThat(func(s string) bool { return len(s) >= 1 }).
		Map(func(name string) validPayloadTestCase {
			schema := []byte(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
			payload := map[string]any{"name": name}
			return validPayloadTestCase{Schema: schema, Payload: payload}
		})
}

func genValidArrayPayload() gopter.Gen {
	return gen.SliceOfN(3, gen.AlphaString()).Map(func(items []string) validPayloadTestCase {
		schema := []byte(`{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":10}`)
		payload := make([]any, len(items))
		for i, item := range items {
			payload[i] = item
		}
		return validPayloadTestCase{Schema: schema, Payload: payload}
	})
}

func genValidBooleanPayload() gopter.Gen {
	return gen.Bool().Map(func(b bool) validPayloadTestCase {
		schema := []byte(`{"type":"boolean"}`)
		return validPayloadTestCase{Schema: schema, Payload: b}
	})
}

func genMissingRequiredFieldTestCase() gopter.Gen {
	return gen.OneConstOf(
		missingRequiredFieldTestCase{
			Schema:  []byte(`{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name","age"]}`),
			Payload: map[string]any{"name": "test"},
		},
		missingRequiredFieldTestCase{
			Schema:  []byte(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
			Payload: map[string]any{},
		},
		missingRequiredFieldTestCase{
			Schema:  []byte(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"},"c":{"type":"string"}},"required":["a","b","c"]}`),
			Payload: map[string]any{"a": "x", "c": "z"},
		},
	)
}

func genWrongTypeTestCase() gopter.Gen {
	return gen.OneConstOf(
		wrongTypeTestCase{
			Schema:  []byte(`{"type":"string"}`),
			Payload: 123,
		},
		wrongTypeTestCase{
			Schema:  []byte(`{"type":"integer"}`),
			Payload: "not a number",
		},
		wrongTypeTestCase{
			Schema:  []byte(`{"type":"boolean"}`),
			Payload: "true",
		},
		wrongTypeTestCase{
			Schema:  []byte(`{"type":"array","items":{"type":"string"}}`),
			Payload: "not an array",
		},
		wrongTypeTestCase{
			Schema:  []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`),
			Payload: []any{"not", "an", "object"},
		},
		wrongTypeTestCase{
			Schema:  []byte(`{"type":"object","properties":{"count":{"type":"integer"}}}`),
			Payload: map[string]any{"count": "not an integer"},
		},
	)
}

func genConstraintViolationTestCase() gopter.Gen {
	return gen.OneConstOf(
		constraintViolationTestCase{
			Schema:  []byte(`{"type":"string","minLength":5}`),
			Payload: "abc",
		},
		constraintViolationTestCase{
			Schema:  []byte(`{"type":"string","maxLength":3}`),
			Payload: "toolong",
		},
		constraintViolationTestCase{
			Schema:  []byte(`{"type":"integer","minimum":10}`),
			Payload: float64(5),
		},
		constraintViolationTestCase{
			Schema:  []byte(`{"type":"integer","maximum":10}`),
			Payload: float64(15),
		},
		constraintViolationTestCase{
			Schema:  []byte(`{"type":"array","minItems":2}`),
			Payload: []any{"one"},
		},
		constraintViolationTestCase{
			Schema:  []byte(`{"type":"array","maxItems":2}`),
			Payload: []any{"one", "two", "three"},
		},
		constraintViolationTestCase{
			Schema:  []byte(`{"type":"string","enum":["a","b","c"]}`),
			Payload: "d",
		},
		constraintViolationTestCase{
			Schema:  []byte(`{"type":"number","exclusiveMinimum":10}`),
			Payload: float64(10),
		},
		constraintViolationTestCase{
			Schema:  []byte(`{"type":"number","exclusiveMaximum":10}`),
			Payload: float64(10),
		},
	)
}
