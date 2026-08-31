// Package model derives replacement guidance at the generated input-validation
// boundary. It uses schema-owned field metadata and never reads rejected values
// back out of provider payloads.
package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/tools"
)

const (
	replacementInstruction           = "Return a replacement tool call whose arguments match the generated input schema."
	malformedToolArgumentsCorrection = "The previous tool call arguments were not valid JSON. Return a replacement tool call whose arguments are one JSON object matching the advertised input schema."
	unknownFieldCorrection           = "Remove properties that are not defined by the generated input schema."
	missingFieldConstraint           = "missing_field"
	invalidTypeConstraint            = "invalid_field_type"
	invalidEnumConstraint            = "invalid_enum_value"
	unknownFieldConstraint           = "unknown_field"
)

// generatedToolCallCorrection converts generated codec issues into stable,
// bounded instructions. A caller-authored schema has no generated field
// metadata, so its validation failures remain terminal.
func generatedToolCallCorrection(
	toolName tools.Ident,
	fieldJSONTypes map[string]string,
	err error,
) string {
	if len(fieldJSONTypes) == 0 {
		return ""
	}
	var validationErr *tools.ValidationError
	if !errors.As(err, &validationErr) {
		return ""
	}
	facts := correctionFacts(validationErr.Issues(), fieldJSONTypes)
	if len(facts) == 0 {
		return ""
	}

	intro := fmt.Sprintf("The generated input contract rejected a call to tool %q.", toolName)
	if len(intro)+1+len(replacementInstruction) > correction.MaxBytes {
		intro = "The generated input contract rejected a tool call."
	}
	var guidance strings.Builder
	guidance.WriteString(intro)
	for _, fact := range facts {
		if guidance.Len()+1+len(fact)+1+len(replacementInstruction) > correction.MaxBytes {
			break
		}
		guidance.WriteByte('\n')
		guidance.WriteString(fact)
	}
	guidance.WriteByte('\n')
	guidance.WriteString(replacementInstruction)
	return guidance.String()
}

// correctionFacts keeps only schema-owned field names and constraints. An
// unknown-property issue produces one generic instruction because its property
// name came from rejected provider JSON rather than the generated schema.
func correctionFacts(issues []*tools.FieldIssue, fieldJSONTypes map[string]string) []string {
	facts := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.Constraint == unknownFieldConstraint {
			facts = append(facts, unknownFieldCorrection)
			continue
		}
		field, expectedType, known := schemaOwnedField(issue.Field, fieldJSONTypes)
		if !known {
			continue
		}
		switch issue.Constraint {
		case missingFieldConstraint:
			if len(issue.Allowed) > 0 {
				facts = append(facts, requiredEnumCorrection(field, issue.Allowed))
				continue
			}
			facts = append(facts, fmt.Sprintf(
				"Field %q is required and must contain a JSON %s.",
				field,
				expectedType,
			))
		case invalidTypeConstraint:
			facts = append(facts, fmt.Sprintf(
				"Field %q must contain a JSON %s.",
				field,
				expectedType,
			))
		case invalidEnumConstraint:
			facts = append(facts, enumCorrection(field, issue.Allowed))
		default:
			facts = append(facts, fmt.Sprintf(
				"Field %q must satisfy its generated JSON schema constraints.",
				field,
			))
		}
	}
	slices.Sort(facts)
	return slices.Compact(facts)
}

// schemaOwnedField maps a codec-reported dotted path or JSON Pointer back to
// one generated metadata key. Dynamic map keys become the generated "*"
// placeholder, and ambiguous paths produce no correction.
func schemaOwnedField(issueField string, fieldJSONTypes map[string]string) (string, string, bool) {
	if expectedType, ok := fieldJSONTypes[issueField]; ok {
		return issueField, expectedType, true
	}
	keys := make([]string, 0, len(fieldJSONTypes))
	for field := range fieldJSONTypes {
		keys = append(keys, field)
	}
	slices.Sort(keys)
	for _, wildcard := range []bool{false, true} {
		var matched string
		for _, field := range keys {
			if strings.Contains(field, "*") != wildcard {
				continue
			}
			_, ok := tools.LookupFieldMetadata(
				map[string]struct{}{field: {}},
				issueField,
			)
			if !ok {
				continue
			}
			if matched != "" {
				return "", "", false
			}
			matched = field
		}
		if matched != "" {
			return matched, fieldJSONTypes[matched], true
		}
	}
	return "", "", false
}

// requiredEnumCorrection describes a missing generated discriminator or other
// required enum without exposing the submitted payload.
func requiredEnumCorrection(field string, allowed []string) string {
	legal := slices.Clone(allowed)
	slices.Sort(legal)
	line := fmt.Sprintf("Field %q is required and must be one of %q.", field, legal)
	if len(line) <= correction.MaxBytes/4 {
		return line
	}
	return fmt.Sprintf(
		"Field %q is required and must use a legal enum value from the generated input schema.",
		field,
	)
}

// enumCorrection lists every generated legal value when the list remains
// compact. Large generated enums refer the model back to the advertised schema
// rather than dropping an arbitrary suffix of legal values.
func enumCorrection(field string, allowed []string) string {
	if len(allowed) == 0 {
		return fmt.Sprintf(
			"Field %q must use one of the enum values declared by the generated input schema.",
			field,
		)
	}
	legal := slices.Clone(allowed)
	slices.Sort(legal)
	line := fmt.Sprintf("Field %q must be one of %q.", field, legal)
	if len(line) <= correction.MaxBytes/4 {
		return line
	}
	return fmt.Sprintf("Field %q must use a legal enum value from the generated input schema.", field)
}
