// Package anthropic lets Claude perform discovery on the provider. It prepares deferred
// declarations and reconciles current availability with retained native history.
// No catalog or conversation state survives outside the request and messages.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"goa.design/goa-ai/features/model/internal/claudebeta"
	"goa.design/goa-ai/features/model/internal/claudecaps"
	"goa.design/goa-ai/features/model/toolname"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	claudeSearch struct {
		enabled     bool
		definitions map[string]searchDefinition
		changes     []string
		seenCalls   map[string]struct{}
	}
)

// prepareClaudeSearch freezes current declarations, restores definitions needed
// by historical references, and emits only availability changes not already
// present in history. Historical definitions never enter provToCanon, which
// remains the authority for newly produced application calls.
func prepareClaudeSearch(ctx context.Context, req *model.Request, enc *encodedRequest) error {
	search := &claudeSearch{
		definitions: make(map[string]searchDefinition, len(enc.tools)),
		seenCalls:   make(map[string]struct{}),
	}
	hasDeferred := false
	current := make(map[string]struct{}, len(enc.tools))
	for i, definition := range req.Tools {
		tool := enc.tools[i].OfTool
		snapshot, err := snapshotSearchDefinition(definition, tool)
		if err != nil {
			return err
		}
		search.definitions[tool.Name] = snapshot
		current[tool.Name] = struct{}{}
		deferred := definition.Deferred
		if req.ToolChoice != nil && req.ToolChoice.Mode == model.ToolChoiceModeTool &&
			req.ToolChoice.Name == definition.Name {
			deferred = false
		}
		if deferred {
			tool.DeferLoading = sdk.Bool(true)
			search.enabled = true
			hasDeferred = true
		}
	}
	if req.ToolChoice != nil && (req.ToolChoice.Mode == model.ToolChoiceModeTool ||
		req.ToolChoice.Mode == model.ToolChoiceModeNone) {
		search.enabled = false
	}

	removed := make(map[string]bool)
	hasHistory := false
	hasChanges := false
	for _, message := range req.Messages {
		snapshots, err := searchRecordStrings(message.Meta, searchDefsKey)
		if err != nil {
			return err
		}
		changes, err := searchRecordStrings(message.Meta, searchChangesKey)
		if err != nil {
			return err
		}
		records, err := searchRecordStrings(message.Meta, searchPartsKey)
		if err != nil {
			return err
		}
		if len(snapshots)+len(changes)+len(records) == 0 {
			continue
		}
		if message.Role != model.ConversationRoleAssistant || len(records) == 0 {
			return errors.New("anthropic: search metadata requires an assistant message and ordered parts")
		}
		hasHistory = true
		messageDefinitions := make(map[string]searchDefinition, len(snapshots))
		for _, record := range snapshots {
			snapshot, err := decodeSearchDefinition(record)
			if err != nil {
				return err
			}
			if _, exists := messageDefinitions[snapshot.Name]; exists {
				return errors.New("anthropic: duplicate historical search definition")
			}
			messageDefinitions[snapshot.Name] = snapshot
			if previous, exists := search.definitions[snapshot.Name]; exists {
				if !sameSearchDefinition(previous, snapshot) {
					return fmt.Errorf("anthropic: retained search reference %q conflicts with another definition: %w",
						snapshot.Canonical, model.ErrToolSearchUnsupported)
				}
			} else {
				search.definitions[snapshot.Name] = snapshot
			}
		}
		for _, record := range changes {
			change, err := decodeSearchChange(record)
			if err != nil {
				return err
			}
			if _, exists := messageDefinitions[change.Tool.Name]; !exists {
				return fmt.Errorf("anthropic: availability change has no definition for %q", change.Tool.Name)
			}
			removed[change.Tool.Name] = change.Type == toolRemovalType
			hasChanges = true
		}
		if err := validateSearchSequence(records, messageDefinitions, search.seenCalls); err != nil {
			return err
		}
	}

	names := make([]string, 0, len(search.definitions))
	for name := range search.definitions {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		_, active := current[name]
		if !active {
			snapshot := search.definitions[name]
			input, err := toolInputSchema(ctx, snapshot.InputSchema)
			if err != nil {
				return err
			}
			tool := sdk.ToolUnionParamOfTool(input, name)
			tool.OfTool.Description = sdk.String(snapshot.Description)
			tool.OfTool.DeferLoading = sdk.Bool(true)
			if len(snapshot.InputExamples) > 0 {
				var examples []json.RawMessage
				if err := json.Unmarshal(snapshot.InputExamples, &examples); err != nil {
					return err
				}
				for _, example := range examples {
					input, err := toolExampleInput(rawjson.Message(example))
					if err != nil {
						return err
					}
					tool.OfTool.InputExamples = append(tool.OfTool.InputExamples, input)
				}
			}
			enc.tools = append(enc.tools, tool)
		}
		if removed[name] == !active {
			continue
		}
		change := searchChange{Type: toolRemovalType}
		if active {
			change.Type = toolAdditionType
		}
		change.Tool.Type = toolReferenceType
		change.Tool.Name = name
		record, err := json.Marshal(change)
		if err != nil {
			return err
		}
		search.changes = append(search.changes, string(record))
	}
	if hasChanges || len(search.changes) > 0 {
		if !claudecaps.ToolChangesSupported(enc.model) {
			return fmt.Errorf("anthropic: model %q cannot change availability of retained search tools: %w",
				enc.model, model.ErrToolSearchUnsupported)
		}
		enc.opts = append(enc.opts, option.WithHeaderAdd("anthropic-beta", claudebeta.ToolChanges))
	}
	if hasDeferred || hasHistory {
		if _, exists := search.definitions[claudeSearchName]; exists {
			return fmt.Errorf("anthropic: tool name %q is reserved for native discovery", claudeSearchName)
		}
		native := &sdk.ToolSearchToolRegex20251119Param{
			Type: sdk.ToolSearchToolRegex20251119TypeToolSearchToolRegex20251119,
		}
		enc.tools = append(enc.tools, sdk.ToolUnionParam{OfToolSearchToolRegex20251119: native})
	}
	if req.Cache != nil && req.Cache.AfterTools && len(enc.tools) > 0 {
		last := enc.tools[len(enc.tools)-1]
		if last.OfToolSearchToolRegex20251119 != nil {
			last.OfToolSearchToolRegex20251119.CacheControl = sdk.NewCacheControlEphemeralParam()
		} else {
			// A forced tool can be the only immediate declaration when search
			// is disabled. Cache only an immediate tool, never a deferred one.
			for i := len(enc.tools) - 1; i >= 0; i-- {
				if !enc.tools[i].OfTool.DeferLoading.Value {
					enc.tools[i].OfTool.CacheControl = sdk.NewCacheControlEphemeralParam()
					break
				}
			}
		}
	}
	enc.search = search
	return nil
}

// snapshotSearchDefinition retains the generated raw schema instead of the
// SDK's map serialization, whose object key order can vary between requests.
func snapshotSearchDefinition(definition *model.ToolDefinition, tool *sdk.ToolParam) (searchDefinition, error) {
	input := definition.Input.Contract()
	schema := input.Schema
	if len(tool.InputExamples) > 0 {
		schema = input.SchemaWithoutRootExample
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, schema); err != nil {
		return searchDefinition{}, fmt.Errorf("anthropic: snapshot tool schema: %w", err)
	}
	snapshot := searchDefinition{
		Canonical: definition.Name, Name: tool.Name, Description: tool.Description.Value, InputSchema: compact.Bytes(),
	}
	if len(tool.InputExamples) > 0 {
		examples, err := json.Marshal(tool.InputExamples)
		if err != nil {
			return searchDefinition{}, fmt.Errorf("anthropic: snapshot tool examples: %w", err)
		}
		snapshot.InputExamples = examples
	}
	return snapshot, nil
}

func decodeSearchDefinition(record string) (searchDefinition, error) {
	var definition searchDefinition
	if err := decodeSearchRecord([]byte(record), &definition); err != nil {
		return definition, err
	}
	if definition.Canonical == "" || definition.Name != toolname.Sanitize(definition.Canonical) ||
		definition.Description == "" {
		return definition, errors.New("anthropic: invalid historical search definition")
	}
	if _, err := decodeToolJSONObject(definition.InputSchema); err != nil {
		return definition, fmt.Errorf("anthropic: historical search schema: %w", err)
	}
	if len(definition.InputExamples) > 0 {
		var examples []map[string]json.RawMessage
		if err := json.Unmarshal(definition.InputExamples, &examples); err != nil || len(examples) == 0 {
			return definition, errors.New("anthropic: invalid historical search examples")
		}
	}
	return definition, nil
}

func sameSearchDefinition(a, b searchDefinition) bool {
	return a.Canonical == b.Canonical && a.Name == b.Name && a.Description == b.Description &&
		bytes.Equal(a.InputSchema, b.InputSchema) && bytes.Equal(a.InputExamples, b.InputExamples)
}

// validateSearchSequence checks each completed search and every returned tool
// reference. A record cannot authorize another server tool or unresolved name.
func validateSearchSequence(records []string, definitions map[string]searchDefinition, seenCalls map[string]struct{}) error {
	pending := make(map[string]bool)
	for _, record := range records {
		kind, err := searchRecordType(record)
		if err != nil {
			return err
		}
		switch kind {
		case nativeSearchCallType:
			call, err := decodeSearchCall(record)
			if err != nil {
				return err
			}
			if _, exists := seenCalls[call.ID]; exists {
				return errors.New("anthropic: duplicate native search call")
			}
			seenCalls[call.ID] = struct{}{}
			pending[call.ID] = true
		case nativeSearchResultType:
			result, err := decodeSearchResult(record)
			if err != nil {
				return err
			}
			if !pending[result.ToolUseID] {
				return errors.New("anthropic: search result has no pending call")
			}
			pending[result.ToolUseID] = false
			for _, reference := range result.Content.ToolReferences {
				if _, exists := definitions[reference.ToolName]; !exists {
					return fmt.Errorf("anthropic: search reference has no definition for %q", reference.ToolName)
				}
			}
		case semanticReference, thinkingReference:
			var reference searchPartReference
			if err := decodeSearchRecord([]byte(record), &reference); err != nil {
				return err
			}
			if reference.Index < 0 {
				return errors.New("anthropic: negative search part reference")
			}
		default:
			return fmt.Errorf("anthropic: unsupported search replay record %q", kind)
		}
	}
	for _, unresolved := range pending {
		if unresolved {
			return errors.New("anthropic: native search call has no result")
		}
	}
	return nil
}
