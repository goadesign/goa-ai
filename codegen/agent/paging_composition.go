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

// pagingExecutionSchema embeds each closed schema and rebases local references
// so nested types still resolve inside their original query or page branch.
func pagingExecutionSchema(query, page []byte) ([]byte, error) {
	branches := make([]any, 2)
	for index, document := range [][]byte{query, page} {
		if err := json.Unmarshal(document, &branches[index]); err != nil {
			return nil, err
		}
		rebasePagingReferences(branches[index], fmt.Sprintf("#/oneOf/%d", index))
	}
	return json.Marshal(map[string]any{"type": "object", "oneOf": branches})
}

// rebasePagingReferences keeps each embedded schema reference pointed at its
// original branch after the two schemas share one root document.
func rebasePagingReferences(value any, prefix string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				if ref, ok := child.(string); ok && strings.HasPrefix(ref, "#/") {
					typed[key] = prefix + ref[1:]
				}
			} else {
				rebasePagingReferences(child, prefix)
			}
		}
	case []any:
		for _, child := range typed {
			rebasePagingReferences(child, prefix)
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
