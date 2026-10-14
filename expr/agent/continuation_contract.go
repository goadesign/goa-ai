package agent

// Continuation fields reuse values from an earlier tool execution. These checks
// compare the declared value contracts before generation, so runtime code can
// copy named fields without interpreting schemas or changing their values.

import (
	"reflect"
	"slices"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type continuationAttributePair struct {
	source *goaexpr.AttributeExpr
	target *goaexpr.AttributeExpr
}

// validateContinuationPayloadMatch checks each continuation input against the
// source input. Fields omitted from execution JSON cannot be retained; fields
// hidden only from the model by consumer bindings remain available.
func validateContinuationPayloadMatch(source, continuation *ToolExpr, verr *eval.ValidationErrors) {
	if source.Args == nil || continuation.Args == nil {
		verr.Add(continuation, "continuation and source Args must be objects")
		return
	}
	sourceObject := goaexpr.AsObject(source.Args.Type)
	targetObject := goaexpr.AsObject(continuation.Args.Type)
	if sourceObject == nil || targetObject == nil {
		verr.Add(continuation, "continuation and source Args must be objects")
		return
	}
	if !continuationUniqueWireNames(sourceObject) || !continuationUniqueWireNames(targetObject) {
		verr.Add(continuation, "continuation and source Args must have distinct execution JSON field names")
		return
	}
	for _, field := range *targetObject {
		if field.Name == continuation.Bounds.Paging.CursorField || slices.Contains(continuation.InjectedFields, field.Name) || continuationExecutionHidden(field.Attribute) {
			continue
		}
		previous := sourceObject.Attribute(field.Name)
		if previous == nil || slices.Contains(source.InjectedFields, field.Name) || continuationExecutionHidden(previous) {
			verr.Add(continuation, "continuation field %q must exist in source tool %q execution input", field.Name, source.Name)
			continue
		}
		if continuationRequired(continuation.Args, field.Name) && !continuationRequired(source.Args, field.Name) {
			verr.Add(continuation, "required continuation field %q must be required in source tool %q", field.Name, source.Name)
		}
		if !continuationValueEqual(previous, field.Attribute, make(map[continuationAttributePair]bool)) {
			verr.Add(continuation, "continuation field %q must preserve source tool %q type, validation, defaults and nested JSON shape", field.Name, source.Name)
		}
	}
}

// continuationValueEqual compares effective attributes recursively. Named aliases
// may differ, but their accepted values and encoded JSON must remain identical.
func continuationValueEqual(source, target *goaexpr.AttributeExpr, seen map[continuationAttributePair]bool) bool {
	if source == nil || target == nil || source.Type == nil || target.Type == nil {
		return source == target
	}
	if !continuationCustomTypeEqual(source, target) || !continuationValidationEqual(source, target) || !reflect.DeepEqual(continuationDefault(source), continuationDefault(target)) {
		return false
	}
	pair := continuationAttributePair{source, target}
	if seen[pair] {
		return true
	}
	seen[pair] = true
	sourceType, targetType := continuationBaseType(source), continuationBaseType(target)
	switch left := sourceType.(type) {
	case goaexpr.Primitive:
		return left == targetType
	case *goaexpr.Object:
		right, ok := targetType.(*goaexpr.Object)
		if !ok || len(*left) != len(*right) || !continuationUniqueWireNames(left) || !continuationUniqueWireNames(right) {
			return false
		}
		for _, field := range *left {
			other := right.Attribute(field.Name)
			if other == nil || continuationExecutionHidden(field.Attribute) != continuationExecutionHidden(other) {
				return false
			}
			if !continuationExecutionHidden(field.Attribute) && !continuationValueEqual(field.Attribute, other, seen) {
				return false
			}
		}
		return true
	case *goaexpr.Array:
		right, ok := targetType.(*goaexpr.Array)
		return ok && left.NonNullableElems == right.NonNullableElems && continuationValueEqual(left.ElemType, right.ElemType, seen)
	case *goaexpr.Map:
		right, ok := targetType.(*goaexpr.Map)
		return ok && continuationValueEqual(left.KeyType, right.KeyType, seen) && continuationValueEqual(left.ElemType, right.ElemType, seen)
	case *goaexpr.Union:
		right, ok := targetType.(*goaexpr.Union)
		if !ok || left.Untagged != right.Untagged || left.Flatten != right.Flatten || left.GetTypeKey() != right.GetTypeKey() || left.GetValueKey() != right.GetValueKey() || len(left.Values) != len(right.Values) {
			return false
		}
		for _, branch := range left.Values {
			found := false
			for _, other := range right.Values {
				if branch.Name == other.Name {
					found = continuationValueEqual(branch.Attribute, other.Attribute, seen)
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func continuationBaseType(attribute *goaexpr.AttributeExpr) goaexpr.DataType {
	for {
		alias, ok := attribute.Type.(goaexpr.UserType)
		if !ok {
			return attribute.Type
		}
		attribute = alias.Attribute()
	}
}

func continuationDefault(attribute *goaexpr.AttributeExpr) any {
	for {
		if attribute.DefaultValue != nil {
			return attribute.DefaultValue
		}
		alias, ok := attribute.Type.(goaexpr.UserType)
		if !ok {
			return nil
		}
		attribute = alias.Attribute()
	}
}

func continuationValidationEqual(source, target *goaexpr.AttributeExpr) bool {
	left, right := goaexpr.EffectiveValidation(source), goaexpr.EffectiveValidation(target)
	if left == nil {
		left = &goaexpr.ValidationExpr{}
	}
	if right == nil {
		right = &goaexpr.ValidationExpr{}
	}
	leftRequired, rightRequired := left.Required, right.Required
	leftValues, rightValues := left.Values, right.Values
	left.Required, right.Required, left.Values, right.Values = nil, nil, nil, nil
	slices.Sort(leftRequired)
	slices.Sort(rightRequired)
	if !slices.Equal(slices.Compact(leftRequired), slices.Compact(rightRequired)) || !reflect.DeepEqual(left, right) || len(leftValues) != len(rightValues) {
		return false
	}
	for _, value := range leftValues {
		if !slices.ContainsFunc(rightValues, func(other any) bool { return reflect.DeepEqual(value, other) }) {
			return false
		}
	}
	return true
}

func continuationRequired(attribute *goaexpr.AttributeExpr, name string) bool {
	validation := goaexpr.EffectiveValidation(attribute)
	return validation != nil && slices.Contains(validation.Required, name)
}

// continuationUniqueWireNames rejects distinct authored fields that would encode
// under the same generated snake_case property and lose their separate values.
func continuationUniqueWireNames(object *goaexpr.Object) bool {
	names := make(map[string]bool, len(*object))
	for _, field := range *object {
		if continuationExecutionHidden(field.Attribute) {
			continue
		}
		name := codegen.SnakeCase(field.Name)
		if names[name] {
			return false
		}
		names[name] = true
	}
	return true
}

// validateContinuationCursor accepts the old optional full-input cursor and a
// required subset cursor. Generated execution input always requires its value.
func validateContinuationCursor(where string, attribute *goaexpr.AttributeExpr, name string, verr *eval.ValidationErrors, tool *ToolExpr) {
	field := attribute.Find(name)
	if field == nil || field.Type != goaexpr.String {
		verr.Add(tool, "%s must define a String field named %q", where, name)
	}
}

// continuationCustomTypeEqual preserves explicitly configured Go JSON codecs.
// Their concrete type and import must agree before retained values can be reused.
func continuationCustomTypeEqual(source, target *goaexpr.AttributeExpr) bool {
	leftName, leftImport := continuationCustomType(source)
	rightName, rightImport := continuationCustomType(target)
	return leftName == rightName && reflect.DeepEqual(leftImport, rightImport)
}

func continuationCustomType(attribute *goaexpr.AttributeExpr) (string, *codegen.ImportSpec) {
	for {
		name, imported := codegen.GetMetaType(attribute)
		if name != "" {
			return name, imported
		}
		alias, ok := attribute.Type.(goaexpr.UserType)
		if !ok {
			return "", nil
		}
		attribute = alias.Attribute()
	}
}

// continuationExecutionHidden reads the JSON tag used by generated execution
// codecs and reports whether the field is omitted from their encoded input.
func continuationExecutionHidden(attribute *goaexpr.AttributeExpr) bool {
	if tags := attribute.Meta["struct:tag:json"]; len(tags) > 0 {
		return strings.Split(tags[0], ",")[0] == "-"
	}
	if names := attribute.Meta["struct:tag:json:name"]; len(names) > 0 {
		return names[0] == "-"
	}
	return false
}
