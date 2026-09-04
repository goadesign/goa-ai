// Package model derives replacement guidance from the advertised tool input
// contract. Guidance may repeat advertised schema text, but never submitted
// values, dynamic map keys, array indexes, undeclared fields, or call IDs.
package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

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
		depth       int
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

// toolInputCorrection returns one specific advertised-field instruction when
// the schema identifies it without ambiguity. Other failures keep the generic
// replacement instruction.
func toolInputCorrection(err error, payload rawjson.Message, fields []tools.FieldMetadata) string {
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
	failures := collectToolCorrectionCandidates(schemaErr, input, fields)
	candidate, ok := deepestToolCorrectionCandidate(failures)
	if !ok {
		return advertisedToolInputCorrection
	}
	text := fmt.Sprintf(
		"Field %q %s",
		tools.FieldPathString(candidate.field.Path),
		candidate.constraint,
	)
	if candidate.field.Description != "" {
		text += fmt.Sprintf(" Field description: %q.", candidate.field.Description)
	}
	text += " Return a replacement tool call with valid arguments."
	if len(text) > correction.MaxBytes {
		return advertisedToolInputCorrection
	}
	return text
}

// collectToolCorrectionCandidates follows the selected branch of each union
// and records every deepest validator failure, including failures for which no
// specific safe instruction exists.
func collectToolCorrectionCandidates(
	err *jsonschema.ValidationError,
	input any,
	fields []tools.FieldMetadata,
) []toolCorrectionCandidate {
	if _, unionFailure := err.ErrorKind.(*kind.OneOf); unionFailure {
		selected, ok := selectedUnionCause(err, input, fields)
		if !ok {
			return []toolCorrectionCandidate{{depth: len(err.InstanceLocation), unsupported: true}}
		}
		return collectToolCorrectionCandidates(selected, input, fields)
	}
	if len(err.Causes) > 0 {
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
		return []toolCorrectionCandidate{{depth: len(err.InstanceLocation), unsupported: true}}
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
			return toolCorrectionCandidate{depth: len(path), unsupported: true}
		}
	}
	if selected == nil {
		return toolCorrectionCandidate{depth: len(path), unsupported: true}
	}
	return toolCorrectionCandidate{
		field:      *selected,
		constraint: constraint,
		depth:      len(path),
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

// deepestToolCorrectionCandidate returns one unique instruction at the
// deepest invalid field. An unsupported failure at that depth makes the safe
// result generic. Repeated reports of the same instruction count once.
func deepestToolCorrectionCandidate(candidates []toolCorrectionCandidate) (toolCorrectionCandidate, bool) {
	deepest := -1
	unique := make(map[string]toolCorrectionCandidate)
	unsupported := false
	for _, candidate := range candidates {
		switch {
		case candidate.depth > deepest:
			deepest = candidate.depth
			clear(unique)
			unsupported = candidate.unsupported
		case candidate.depth < deepest:
			continue
		default:
			unsupported = unsupported || candidate.unsupported
		}
		if candidate.unsupported {
			continue
		}
		key := tools.FieldPathString(candidate.field.Path) + "\x00" +
			candidate.constraint + "\x00" + candidate.field.Description
		unique[key] = candidate
	}
	if unsupported || len(unique) != 1 {
		return toolCorrectionCandidate{}, false
	}
	for _, candidate := range unique {
		return candidate, true
	}
	panic("model: unreachable empty correction candidate")
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
