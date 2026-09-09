// Package model derives replacement guidance from the advertised tool input
// contract. Guidance may repeat advertised schema text and validated examples,
// but never submitted values, dynamic map keys, array indexes, undeclared
// fields, or call IDs.
package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

const (
	advertisedToolInputCorrection    = "The previous tool call did not match its advertised input schema. Return a replacement tool call with valid arguments."
	malformedToolArgumentsCorrection = "The previous tool call arguments were not valid JSON. Return a replacement tool call whose arguments are one JSON object matching the advertised input schema."
)

type (
	// toolCorrectionCandidate is one instruction derived from advertised field
	// metadata and one structured JSON Schema failure.
	toolCorrectionCandidate struct {
		field       tools.FieldMetadata
		constraint  string
		unsupported bool
	}

	// fieldPathMatch binds each dynamic segment in field metadata to the
	// corresponding array index or map key in the submitted JSON path.
	fieldPathMatch struct {
		field    tools.FieldMetadata
		actual   []string
		bindings map[int]string
	}
)

// toolInputCorrection makes one rejection self-contained using the example
// already validated when this tool input was constructed. The example teaches
// argument structure, not the values or union branch the caller must choose.
// Advisory examples that do not fit are omitted whole; validation is unchanged.
func toolInputCorrection(err error, payload rawjson.Message, fields []tools.FieldMetadata, example rawjson.Message) string {
	text := toolFieldCorrection(err, payload, fields)
	const instruction = "\nExample illustrates structure; use values and a valid variant appropriate to the request:\n"
	if len(example) == 0 || len(text)+len(instruction)+len(example) > correction.MaxBytes {
		return text
	}
	return text + instruction + string(example)
}

// toolFieldCorrection reports independently identified field problems. Unknown
// or ambiguous problems do not erase useful guidance for other fields.
func toolFieldCorrection(err error, payload rawjson.Message, fields []tools.FieldMetadata) string {
	if len(fields) == 0 {
		return advertisedToolInputCorrection
	}
	var schemaErr *jsonschema.ValidationError
	if !errors.As(err, &schemaErr) {
		return advertisedToolInputCorrection
	}
	input, err := decodeCorrectionInput(payload)
	if err != nil {
		return advertisedToolInputCorrection
	}
	return formatToolCorrections(collectToolCorrectionCandidates(schemaErr, input, fields))
}

// collectToolCorrectionCandidates follows the selected branch of each union
// and groups of independently required constraints. Alternative schemas,
// candidate array matches and property-name checks must not become instructions
// to change every corresponding field value.
func collectToolCorrectionCandidates(
	err *jsonschema.ValidationError,
	input any,
	fields []tools.FieldMetadata,
) []toolCorrectionCandidate {
	switch err.ErrorKind.(type) {
	case *kind.OneOf:
		selected, ok := selectedUnionCause(err, input, fields)
		if !ok {
			return []toolCorrectionCandidate{{unsupported: true}}
		}
		return collectToolCorrectionCandidates(selected, input, fields)
	case *kind.Schema, *kind.Group, *kind.Reference, *kind.AllOf:
		var candidates []toolCorrectionCandidate
		for _, cause := range err.Causes {
			candidates = append(candidates, collectToolCorrectionCandidates(cause, input, fields)...)
		}
		return candidates
	}
	return toolCorrectionCandidatesForError(err, input, fields)
}

// selectedUnionCause returns only the validation failure for the branch named
// by the submitted discriminator. JSON Schema reports every branch when none
// matches, but the other branches do not describe the model's selected value.
func selectedUnionCause(
	err *jsonschema.ValidationError,
	input any,
	fields []tools.FieldMetadata,
) (*jsonschema.ValidationError, bool) {
	var selected int
	found := false
	for _, field := range fields {
		if len(field.DiscriminatorValues) == 0 || len(field.Path) != len(err.InstanceLocation)+1 {
			continue
		}
		path := append(slices.Clone(err.InstanceLocation), "")
		match, ok := matchFieldPath(field, path)
		if !ok {
			continue
		}
		last, fixed := field.Path[len(field.Path)-1].(tools.FixedField)
		if !fixed {
			continue
		}
		match.actual[len(match.actual)-1] = string(last)
		if !unionBranchesMatch(match, input) {
			continue
		}
		value, ok := jsonValueAt(input, match.actual)
		if !ok {
			return nil, false
		}
		discriminator, ok := value.(string)
		if !ok {
			return nil, false
		}
		index := slices.Index(field.DiscriminatorValues, discriminator)
		if index < 0 || found {
			return nil, false
		}
		selected = index
		found = true
	}
	if !found || selected >= len(err.Causes) {
		return nil, false
	}
	return err.Causes[selected], true
}

// toolCorrectionCandidatesForError converts one validator leaf into a safe
// instruction or an unsupported marker that participates in ambiguity checks.
func toolCorrectionCandidatesForError(
	err *jsonschema.ValidationError,
	input any,
	fields []tools.FieldMetadata,
) []toolCorrectionCandidate {
	switch failure := err.ErrorKind.(type) {
	case *kind.Required:
		candidates := make([]toolCorrectionCandidate, 0, len(failure.Missing))
		for _, field := range failure.Missing {
			path := append(slices.Clone(err.InstanceLocation), field)
			candidates = append(candidates, toolCorrectionCandidateForPath(
				path,
				"is required.",
				input,
				fields,
			))
		}
		return candidates
	case *kind.Type:
		candidate := toolCorrectionCandidateForPath(
			err.InstanceLocation,
			"",
			input,
			fields,
		)
		if candidate.unsupported || !slices.Contains(failure.Want, candidate.field.JSONType) {
			candidate.unsupported = true
			return []toolCorrectionCandidate{candidate}
		}
		candidate.constraint = fmt.Sprintf("must contain a JSON %s.", candidate.field.JSONType)
		return []toolCorrectionCandidate{candidate}
	case *kind.MinItems:
		candidate := toolCorrectionCandidateForPath(err.InstanceLocation, "", input, fields)
		if candidate.unsupported || candidate.field.JSONType != "array" {
			candidate.unsupported = true
			return []toolCorrectionCandidate{candidate}
		}
		candidate.constraint = fmt.Sprintf("must contain at least %d items.", failure.Want)
		return []toolCorrectionCandidate{candidate}
	case *kind.MaxItems:
		candidate := toolCorrectionCandidateForPath(err.InstanceLocation, "", input, fields)
		if candidate.unsupported || candidate.field.JSONType != "array" {
			candidate.unsupported = true
			return []toolCorrectionCandidate{candidate}
		}
		candidate.constraint = fmt.Sprintf("must contain at most %d items.", failure.Want)
		return []toolCorrectionCandidate{candidate}
	case *kind.Enum:
		candidate := toolCorrectionCandidateForPath(
			err.InstanceLocation,
			"",
			input,
			fields,
		)
		if candidate.unsupported || len(candidate.field.DiscriminatorValues) > 0 {
			candidate.unsupported = true
			return []toolCorrectionCandidate{candidate}
		}
		values, marshalErr := json.Marshal(failure.Want)
		if marshalErr != nil {
			candidate.unsupported = true
			return []toolCorrectionCandidate{candidate}
		}
		candidate.constraint = fmt.Sprintf("must contain one of these JSON values: %s.", values)
		return []toolCorrectionCandidate{candidate}
	case *kind.AdditionalProperties:
		candidate := toolCorrectionCandidateForPath(
			err.InstanceLocation,
			"",
			input,
			fields,
		)
		if len(failure.Properties) == 0 || candidate.unsupported || candidate.field.JSONType != jsonObjectType {
			candidate.unsupported = true
			return []toolCorrectionCandidate{candidate}
		}
		candidate.constraint = "contains an undeclared field."
		return []toolCorrectionCandidate{candidate}
	default:
		return []toolCorrectionCandidate{{unsupported: true}}
	}
}

// toolCorrectionCandidateForPath resolves one validator path against the field
// records whose union branches match the submitted value.
func toolCorrectionCandidateForPath(
	path []string,
	constraint string,
	input any,
	fields []tools.FieldMetadata,
) toolCorrectionCandidate {
	var selected *tools.FieldMetadata
	selectedDynamicSegments := 0
	for _, field := range fields {
		match, ok := matchFieldPath(field, path)
		if !ok || !unionBranchesMatch(match, input) {
			continue
		}
		dynamicSegments := dynamicFieldCount(field.Path)
		if selected != nil && dynamicSegments > selectedDynamicSegments {
			continue
		}
		if selected == nil || dynamicSegments < selectedDynamicSegments {
			copy := field
			selected = &copy
			selectedDynamicSegments = dynamicSegments
			continue
		}
		if selected != nil && (selected.JSONType != field.JSONType ||
			selected.Description != field.Description ||
			tools.FieldPathString(selected.Path) != tools.FieldPathString(field.Path)) {
			return toolCorrectionCandidate{unsupported: true}
		}
	}
	if selected == nil {
		return toolCorrectionCandidate{unsupported: true}
	}
	return toolCorrectionCandidate{
		field:      *selected,
		constraint: constraint,
	}
}

func dynamicFieldCount(path []tools.FieldPathSegment) int {
	count := 0
	for _, segment := range path {
		if _, dynamic := segment.(tools.DynamicField); dynamic {
			count++
		}
	}
	return count
}

// matchFieldPath compares typed generated segments with a validator path and
// records the submitted value for every dynamic segment.
func matchFieldPath(field tools.FieldMetadata, actual []string) (fieldPathMatch, bool) {
	if len(field.Path) != len(actual) {
		return fieldPathMatch{}, false
	}
	match := fieldPathMatch{
		field:    field,
		actual:   slices.Clone(actual),
		bindings: make(map[int]string),
	}
	for index, segment := range field.Path {
		switch value := segment.(type) {
		case tools.FixedField:
			if string(value) != actual[index] && actual[index] != "" {
				return fieldPathMatch{}, false
			}
		case tools.DynamicField:
			match.bindings[index] = actual[index]
		default:
			return fieldPathMatch{}, false
		}
	}
	return match, true
}

// unionBranchesMatch resolves each generated discriminator path with the
// dynamic indexes or keys bound while matching the invalid field.
func unionBranchesMatch(match fieldPathMatch, input any) bool {
	for _, branch := range match.field.Branches {
		path := make([]string, len(branch.Discriminator))
		for index, segment := range branch.Discriminator {
			switch value := segment.(type) {
			case tools.FixedField:
				path[index] = string(value)
			case tools.DynamicField:
				bound, ok := match.bindings[index]
				if !ok {
					return false
				}
				path[index] = bound
			default:
				return false
			}
		}
		value, ok := jsonValueAt(input, path)
		if !ok || value != branch.Value {
			return false
		}
	}
	return true
}

// formatToolCorrections sorts and deduplicates complete field instructions.
// Different instructions for one displayed path are withheld: replacing array
// indexes and map keys with * can hide which selected union member each needs.
// Oversized instructions are omitted whole, with an explicit notice; descriptions
// and enum values are never cut into partial text or JSON.
func formatToolCorrections(candidates []toolCorrectionCandidate) string {
	const replacement = " Return a replacement tool call with valid arguments."
	const omission = " Other schema errors are not detailed here."
	byPath := make(map[string]string)
	ambiguous := make(map[string]bool)
	omitted := false
	for _, candidate := range candidates {
		if candidate.unsupported {
			omitted = true
			continue
		}
		path := tools.FieldPathString(candidate.field.Path)
		text := fmt.Sprintf("Field %q %s", path, candidate.constraint)
		if candidate.field.Description != "" {
			text += fmt.Sprintf(" Field description: %q.", candidate.field.Description)
		}
		if previous, found := byPath[path]; found && previous != text {
			ambiguous[path] = true
			omitted = true
		}
		byPath[path] = text
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		if !ambiguous[path] {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	blocks := make([]string, 0, len(paths))
	for _, path := range paths {
		blocks = append(blocks, byPath[path])
	}
	suffix := replacement
	if omitted {
		suffix = omission + suffix
	}
	text := strings.Join(blocks, "\n")
	if len(text)+len(suffix) > correction.MaxBytes {
		suffix = omission + replacement
		kept := blocks[:0]
		size := len(suffix)
		for _, block := range blocks {
			separator := 0
			if len(kept) > 0 {
				separator = 1
			}
			if size+separator+len(block) <= correction.MaxBytes {
				kept = append(kept, block)
				size += separator + len(block)
			}
		}
		text = strings.Join(kept, "\n")
	}
	if text == "" {
		return advertisedToolInputCorrection
	}
	return text + suffix
}

func decodeCorrectionInput(payload rawjson.Message) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var input any
	if err := decoder.Decode(&input); err != nil {
		return nil, err
	}
	return input, nil
}

func jsonValueAt(input any, path []string) (any, bool) {
	value := input
	for _, segment := range path {
		switch current := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = current[segment]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(current) {
				return nil, false
			}
			value = current[index]
		default:
			return nil, false
		}
	}
	return value, true
}
