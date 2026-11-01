// Package tools exposes field details attached to tool and completion schemas.
// Fixed JSON property names stay distinct from array indexes and map keys, so
// callers never need to parse dotted path strings.
package tools

import (
	"fmt"
	"strconv"
	"strings"
)

type (
	// FieldPathSegment is one fixed or caller-chosen part of a JSON field path.
	// Use FixedField for object properties and DynamicField for array indexes and
	// map keys.
	FieldPathSegment interface {
		fieldPathSegment()
	}

	// FixedField is one JSON object property whose name is fixed by the schema.
	FixedField string

	// DynamicField is one array index or map key supplied in the JSON value.
	DynamicField struct{}

	// UnionBranch identifies how a submitted JSON value selects a field's branch.
	// TaggedUnionBranch uses a declared string property; UntaggedUnionBranch uses
	// the value's JSON kind. Paths retain each array index or map key separately.
	UnionBranch interface {
		unionBranch()
	}

	// TaggedUnionBranch selects a branch through its declared discriminator.
	TaggedUnionBranch struct {
		// Discriminator is the path to the union's branch-name property.
		Discriminator []FieldPathSegment
		// Value is the branch name that makes the field applicable.
		Value string
	}

	// UntaggedUnionBranch selects a branch by the submitted value's JSON kind.
	UntaggedUnionBranch struct {
		// Path locates the union value. An empty path identifies the root value.
		Path []FieldPathSegment
		// JSONKind is string, number, boolean, array or object. Integer branches
		// use number because the discriminator is the JSON kind, not its range.
		JSONKind string
		// Index is this branch's zero-based position in the advertised oneOf.
		// It selects the matching structured validator diagnostic after the JSON
		// kind determines which branch applies.
		Index int
	}

	// FieldMetadata describes one field advertised in a JSON schema.
	// Branches is empty for fields outside unions. Every branch requirement must
	// match before the field applies to a submitted value.
	FieldMetadata struct {
		// Path locates the field without conflating fixed names with dynamic keys.
		Path []FieldPathSegment
		// JSONType is the single JSON type accepted for the field, when known.
		JSONType string
		// Description is the text already advertised in the JSON schema.
		Description string
		// Branches lists the selected union branches that contain this field.
		Branches []UnionBranch
		// DiscriminatorValues lists the valid branch names when this field is a
		// union discriminator. It is empty for ordinary fields.
		DiscriminatorValues []string
	}
)

// CloneFieldMetadata returns an independent copy of field details.
func CloneFieldMetadata(fields []FieldMetadata) []FieldMetadata {
	if len(fields) == 0 {
		return nil
	}
	cloned := make([]FieldMetadata, len(fields))
	for index, field := range fields {
		cloned[index] = field
		cloned[index].Path = append([]FieldPathSegment(nil), field.Path...)
		cloned[index].DiscriminatorValues = append([]string(nil), field.DiscriminatorValues...)
		cloned[index].Branches = make([]UnionBranch, len(field.Branches))
		for branchIndex, branch := range field.Branches {
			switch branch := branch.(type) {
			case TaggedUnionBranch:
				branch.Discriminator = append([]FieldPathSegment(nil), branch.Discriminator...)
				cloned[index].Branches[branchIndex] = branch
			case UntaggedUnionBranch:
				branch.Path = append([]FieldPathSegment(nil), branch.Path...)
				cloned[index].Branches[branchIndex] = branch
			default:
				panic(fmt.Sprintf("unknown generated union requirement %T", branch))
			}
		}
	}
	return cloned
}

func (TaggedUnionBranch) unionBranch() {}

func (UntaggedUnionBranch) unionBranch() {}

// LookupFieldMetadata returns the one field that matches a dotted field path or
// a slash-separated JSON Pointer. DynamicField matches exactly one array index
// or map key. Several matching union records are accepted only when they
// advertise the same type and description.
func LookupFieldMetadata(fields []FieldMetadata, path string) (FieldMetadata, bool) {
	actual, ok := fieldPathSegments(path)
	if !ok {
		return FieldMetadata{}, false
	}
	var matched FieldMetadata
	found := false
	dynamicSegments := 0
	for _, field := range fields {
		if !fieldPathMatches(field.Path, actual) {
			continue
		}
		candidateDynamicSegments := countDynamicFields(field.Path)
		if !found || candidateDynamicSegments < dynamicSegments {
			matched = field
			found = true
			dynamicSegments = candidateDynamicSegments
			continue
		}
		if candidateDynamicSegments > dynamicSegments {
			continue
		}
		if matched.JSONType != field.JSONType || matched.Description != field.Description {
			return FieldMetadata{}, false
		}
	}
	return matched, found
}

// FieldPathString renders a field path for model and user guidance. Common
// property names use dots. Names containing dots or stars use quoted brackets,
// while dynamic array indexes and map keys use an unquoted star.
func FieldPathString(path []FieldPathSegment) string {
	if len(path) == 0 {
		return "$payload"
	}
	var out strings.Builder
	for _, segment := range path {
		switch value := segment.(type) {
		case FixedField:
			name := string(value)
			if simpleFieldName(name) {
				if out.Len() > 0 {
					out.WriteByte('.')
				}
				out.WriteString(name)
				continue
			}
			out.WriteByte('[')
			out.WriteString(strconv.Quote(name))
			out.WriteByte(']')
		case DynamicField:
			if out.Len() > 0 {
				out.WriteByte('.')
			}
			out.WriteByte('*')
		}
	}
	return out.String()
}

func fieldPathMatches(pattern []FieldPathSegment, actual []string) bool {
	if len(pattern) != len(actual) {
		return false
	}
	for index, segment := range pattern {
		switch value := segment.(type) {
		case FixedField:
			if string(value) != actual[index] {
				return false
			}
		case DynamicField:
		default:
			return false
		}
	}
	return true
}

func countDynamicFields(path []FieldPathSegment) int {
	count := 0
	for _, segment := range path {
		if _, dynamic := segment.(DynamicField); dynamic {
			count++
		}
	}
	return count
}

func fieldPathSegments(path string) ([]string, bool) {
	if path == "$payload" || path == "" {
		return nil, true
	}
	if !strings.HasPrefix(path, "/") {
		return strings.Split(path, "."), true
	}
	encoded := strings.Split(path[1:], "/")
	segments := make([]string, len(encoded))
	for index, token := range encoded {
		decoded, ok := decodeJSONPointerToken(token)
		if !ok {
			return nil, false
		}
		segments[index] = decoded
	}
	return segments, true
}

func decodeJSONPointerToken(token string) (string, bool) {
	if !strings.Contains(token, "~") {
		return token, true
	}
	var decoded strings.Builder
	decoded.Grow(len(token))
	for index := 0; index < len(token); index++ {
		if token[index] != '~' {
			decoded.WriteByte(token[index])
			continue
		}
		if index+1 == len(token) {
			return "", false
		}
		index++
		switch token[index] {
		case '0':
			decoded.WriteByte('~')
		case '1':
			decoded.WriteByte('/')
		default:
			return "", false
		}
	}
	return decoded.String(), true
}

func simpleFieldName(name string) bool {
	if name == "" || name == "*" {
		return false
	}
	return !strings.ContainsAny(name, ".[]\"\\")
}

func (FixedField) fieldPathSegment() {}

func (DynamicField) fieldPathSegment() {}
