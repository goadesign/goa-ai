// Package runtime completes consumer arguments before planning activities save accepted work.
// The original model bytes remain separate from the complete executor input;
// workflow replacement and user responses reuse those values without rebinding.
package runtime

import (
	"fmt"
	"slices"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// prepareConsumerInput validates one activity's model input, adds generated typed
// values, and validates the complete provider input before it can be accepted.
func prepareConsumerInput(lookup toolSpecLookup, name tools.Ident, input rawjson.Message) (rawjson.Message, error) {
	spec, ok := lookup(name)
	if !ok {
		return nil, fmt.Errorf("tool %q has no input contract", name)
	}
	decoded, err := spec.Payload.Codec.FromJSON(input)
	if err != nil {
		return nil, planner.NewOutputContractError(fmt.Errorf("tool %q model input: %w", name, err))
	}
	complete := slices.Clone(input)
	if spec.PrepareInput != nil {
		var err error
		complete, err = spec.PrepareInput(decoded)
		if err != nil {
			return nil, engine.MarkActivityErrorNonRetryable(fmt.Errorf("tool %q complete input: %w", name, err))
		}
	}
	if _, err := spec.ExecutionPayloadCodec.FromJSON(complete); err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(fmt.Errorf("tool %q execution input: %w", name, err))
	}
	return slices.Clone(complete), nil
}

// prepareConsumerAwait copies a planner's wait and completes each tool input.
// Call IDs still belong to workflow compilation; this step preserves the exact
// model bytes before the activity result can be saved or published.
func prepareConsumerAwait(lookup toolSpecLookup, source *planner.Await) (*planner.Await, error) {
	if source == nil {
		return nil, nil
	}
	items := make([]planner.AwaitItem, len(source.Items))
	for index, item := range source.Items {
		switch item.Kind {
		case planner.AwaitItemKindClarification:
			value := *item.Clarification
			value.MissingFields = slices.Clone(value.MissingFields)
			value.ExampleJSON = slices.Clone(value.ExampleJSON)
			items[index] = planner.AwaitClarificationItem(&value)
		case planner.AwaitItemKindToolClarification:
			value := *item.ToolClarification
			complete, err := prepareConsumerInput(lookup, value.ToolName, value.Payload)
			if err != nil {
				return nil, err
			}
			value.ModelPayload = slices.Clone(value.Payload)
			value.Payload = complete
			items[index] = planner.AwaitToolClarificationItem(&value)
		case planner.AwaitItemKindQuestions:
			value := *item.Questions
			complete, err := prepareConsumerInput(lookup, value.ToolName, value.Payload)
			if err != nil {
				return nil, err
			}
			value.ModelPayload = slices.Clone(value.Payload)
			value.Payload = complete
			if value.Title != nil {
				title := *value.Title
				value.Title = &title
			}
			value.Questions = cloneAwaitQuestions(value.Questions)
			items[index] = planner.AwaitQuestionsItem(&value)
		case planner.AwaitItemKindExternalTools:
			value := *item.ExternalTools
			value.Items = slices.Clone(value.Items)
			for toolIndex := range value.Items {
				tool := &value.Items[toolIndex]
				complete, err := prepareConsumerInput(lookup, tool.Name, tool.Payload)
				if err != nil {
					return nil, err
				}
				tool.ModelPayload = slices.Clone(tool.Payload)
				tool.Payload = complete
			}
			items[index] = planner.AwaitExternalToolsItem(&value)
		default:
			return nil, fmt.Errorf("await item %d has unknown kind %q", index, item.Kind)
		}
	}
	return planner.NewAwait(items...), nil
}
