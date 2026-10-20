// Package codegen keeps the first query public and retains its typed page
// implementation. Both branches use their original codecs and provider methods;
// direct callers send a flat input accepted by exactly one branch.
package codegen

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// composePagingEntries uses the declared pair after both branch codecs exist.
// Common output and execution promises must agree; planning tags remain private
// to each branch and are never combined into a broader permission list.
func composePagingEntries(tools []*ToolData, entries []*toolEntry) error {
	byName := make(map[string]*toolEntry, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	for _, tool := range tools {
		if tool.Page == nil {
			continue
		}
		source, page := byName[tool.QualifiedName], byName[tool.Page.QualifiedName]
		if err := validatePagingComposition(source, page); err != nil {
			return err
		}
		schema, err := pagingExecutionSchema(source.Payload.ExecutionSchemaJSON, page.Payload.ExecutionSchemaJSON)
		if err != nil {
			return fmt.Errorf("compose %s: %w", source.Name, err)
		}
		source.Execution = &executionContractData{Schema: schema, Query: source.Payload, Page: page.Payload}
		if source.TextOnlyExecution != nil {
			schema, err := pagingExecutionSchema(source.TextOnlyExecution.ExecutionSchemaJSON, page.TextOnlyExecution.ExecutionSchemaJSON)
			if err != nil {
				return fmt.Errorf("compose text-only %s: %w", source.Name, err)
			}
			source.TextOnlyExecutionContract = &executionContractData{Schema: schema, Query: source.TextOnlyExecution, Page: page.TextOnlyExecution}
		}
		bounds := *source.Bounds
		paging := *source.Bounds.Paging
		paging.ContinueTool = source.Name
		paging.SourceTool = ""
		paging.RetainedFields = append([]string(nil), page.Bounds.Paging.RetainedFields...)
		bounds.Paging = &paging
		source.Bounds = &bounds
	}
	return nil
}

// validatePagingComposition rejects pairs whose shared public result or safety
// promise would change merely because their operation names were combined.
func validatePagingComposition(source, page *toolEntry) error {
	if !samePagingSchema(source.Result, page.Result) {
		return fmt.Errorf("paged tool %s and %s must return the same result contract", source.Name, page.Name)
	}
	if source.RequiresUI != page.RequiresUI || !reflect.DeepEqual(source.Confirmation, page.Confirmation) || source.TerminalRun != page.TerminalRun || source.Bookkeeping != page.Bookkeeping || source.ReplanOnTimeout != page.ReplanOnTimeout || !reflect.DeepEqual(source.Meta, page.Meta) {
		return fmt.Errorf("paged tool %s and %s must declare the same public execution policy", source.Name, page.Name)
	}
	if source.ResultReminder != page.ResultReminder || source.TextOnlyResultReminder != page.TextOnlyResultReminder || (source.TextOnlyExecution == nil) != (page.TextOnlyExecution == nil) {
		return fmt.Errorf("paged tool %s and %s must declare the same result and UI policy", source.Name, page.Name)
	}
	if len(source.ServerData) != len(page.ServerData) {
		return fmt.Errorf("paged tool %s and %s must declare the same server data", source.Name, page.Name)
	}
	for index, left := range source.ServerData {
		right := page.ServerData[index]
		if left.Kind != right.Kind || left.Audience != right.Audience || left.NativeImage != right.NativeImage || !samePagingSchema(left.Type, right.Type) {
			return fmt.Errorf("paged tool %s and %s must declare the same server data", source.Name, page.Name)
		}
	}
	return nil
}

// pagingExecutionSchema receives query and page schemas from the same builder.
// It keeps their closed input branches and places named definitions at the shared
// root. Separate name prefixes keep each branch's references and rules intact.
func pagingExecutionSchema(query, page []byte) ([]byte, error) {
	branches := make([]map[string]any, 2)
	definitions := make(map[string]any)
	for index, document := range [][]byte{query, page} {
		if err := json.Unmarshal(document, &branches[index]); err != nil {
			return nil, err
		}
		prefix := []string{"query__", "page__"}[index]
		rebasePagingReferences(branches[index], prefix, fmt.Sprintf("#/oneOf/%d", index))
		if branchDefinitions, exists := branches[index]["$defs"]; exists {
			for name, definition := range branchDefinitions.(map[string]any) {
				definitions[prefix+name] = definition
			}
		}
		delete(branches[index], "$defs")
	}
	dialect := branches[0]["$schema"]
	for _, branch := range branches {
		delete(branch, "$schema")
	}
	combined := map[string]any{"$schema": dialect, "type": "object", "oneOf": branches}
	if len(definitions) > 0 {
		combined["$defs"] = definitions
	}
	return json.Marshal(combined)
}

// rebasePagingReferences updates schema references after a branch moves into the
// combined document. Only schema children are visited; authored examples and
// default values remain data even when they contain a member named "$ref".
func rebasePagingReferences(value any, definitionPrefix, branchPrefix string) {
	schema, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, child := range schema {
		switch key {
		case "$ref":
			ref := child.(string)
			if strings.HasPrefix(ref, "#/$defs/") {
				schema[key] = "#/$defs/" + definitionPrefix + strings.TrimPrefix(ref, "#/$defs/")
			} else if strings.HasPrefix(ref, "#/") {
				schema[key] = branchPrefix + ref[1:]
			}
		case "$defs", "properties":
			for _, nested := range child.(map[string]any) {
				rebasePagingReferences(nested, definitionPrefix, branchPrefix)
			}
		case "items", "additionalProperties":
			rebasePagingReferences(child, definitionPrefix, branchPrefix)
		case "oneOf", "anyOf", "allOf":
			for _, nested := range child.([]any) {
				rebasePagingReferences(nested, definitionPrefix, branchPrefix)
			}
		}
	}
}

// samePagingSchema compares complete result declarations so one published result
// never promises values accepted by only one implementation.
func samePagingSchema(left, right *typeData) bool {
	if left == nil || right == nil {
		return left == right
	}
	var a, b any
	if err := json.Unmarshal(left.SchemaJSON, &a); err != nil {
		panic(fmt.Sprintf("invalid generated result schema: %v", err))
	}
	if err := json.Unmarshal(right.SchemaJSON, &b); err != nil {
		panic(fmt.Sprintf("invalid generated result schema: %v", err))
	}
	return reflect.DeepEqual(a, b)
}
