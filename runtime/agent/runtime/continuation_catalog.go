package runtime

// New page requests use the current activity's selected provider. Saved query
// inputs and cursor bytes stay unchanged; only the owner may establish that an
// earlier registration and today's registration serve the same resource.

import (
	"context"
	"slices"

	"goa.design/goa-ai/runtime/agent/tools"
)

// bindContinuationActions chooses providers before new page calls are accepted.
// Accepted calls and their retries do not enter this path, so their exact saved
// registration remains the one used by execution and admission replay.
func (c *RegistryCatalog) bindContinuationActions(ctx context.Context, actions []continuationAction) ([]continuationAction, error) {
	selected := make([]continuationAction, 0, len(actions))
	for _, action := range actions {
		if action.registry == nil {
			selected = append(selected, action)
			continue
		}
		if _, _, err := historicalPagingChains(action.outputs); err != nil {
			return nil, err
		}
		sourceName := action.spec.Name
		if action.spec.Bounds.Paging.SourceTool != "" {
			sourceName = action.spec.Bounds.Paging.SourceTool
		}
		current, exists := c.selections[sourceName]
		if !exists || current.registry != action.registry.Registry {
			continue
		}

		source, sourceExists := current.resolution.Specs[sourceName]
		continuation, continuationExists := current.resolution.Specs[action.spec.Name]
		if !sourceExists || !continuationExists || source.Bounds == nil || source.Bounds.Paging == nil ||
			source.Bounds.Paging.ContinueTool != continuation.Name || continuation.Bounds == nil || continuation.Bounds.Paging == nil ||
			continuation.Bounds.Paging.ContinueTool != continuation.Name ||
			(sourceName != continuation.Name && continuation.Bounds.Paging.SourceTool != sourceName) {
			continue
		}
		if c.textOnly && (source.RequiresUI || continuation.RequiresUI || source.Confirmation != nil ||
			continuation.Confirmation != nil || source.TextOnly == nil || continuation.TextOnly == nil) {
			continue
		}
		compatible := true
		for _, output := range action.outputs {
			previous, err := c.previousContinuationSelection(ctx, output)
			if err != nil {
				return nil, err
			}
			if previous == nil {
				compatible = false
				continue
			}
			previousSource, sourceExists := previous.Specs[sourceName]
			previousPage, pageExists := previous.Specs[action.spec.Name]
			if !sourceExists || !pageExists || previousSource.IsAgentTool != source.IsAgentTool ||
				previousPage.IsAgentTool != continuation.IsAgentTool || previousSource.Bounds == nil || previousSource.Bounds.Paging == nil ||
				previousPage.Bounds == nil || previousPage.Bounds.Paging == nil ||
				!samePagingArguments(previousSource.Bounds.Paging, source.Bounds.Paging) ||
				!samePagingArguments(previousPage.Bounds.Paging, continuation.Bounds.Paging) {
				compatible = false
				continue
			}
			if previous.Source != nil {
				resource := current.resolution.Source
				if resource == nil || resource.Scope != previous.Source.Scope || resource.Name != previous.Source.Name ||
					resource.Target.Kind() != previous.Source.Target.Kind() {
					compatible = false
					continue
				}
			} else if _, static := c.definition.specByName[sourceName]; !static &&
				(current.resolution.Source != nil || current.resolution.Registered.Toolset.Name != previous.Registered.Toolset.Name) {
				// Direct imports use their declared registry route as resource identity.
				// Registered resources instead require their explicit owner and name.
				compatible = false
				continue
			}
		}
		if !compatible {
			continue
		}
		if spec, static := c.specs[sourceName]; static {
			source = spec
		}
		if spec, static := c.specs[continuation.Name]; static {
			continuation = spec
		}
		executable, err := historicalPagingExecutable(action.outputs, map[tools.Ident]tools.ToolSpec{
			source.Name: source, continuation.Name: continuation,
		})
		if err != nil {
			return nil, err
		}
		if !executable {
			continue
		}
		action.spec = continuation
		action.executablePayload, err = continuationPayload(continuation, action.state)
		if err != nil {
			return nil, err
		}
		action.registry, err = current.resolution.Select(current.registry, continuation.Name)
		if err != nil {
			return nil, err
		}
		selected = append(selected, action)
	}
	return selected, nil
}

// samePagingArguments checks which original fields and cursor a new page copies.
// Result fields may change because the current provider owns each new result.
func samePagingArguments(previous, current *tools.PagingSpec) bool {
	return previous.SourceTool == current.SourceTool && previous.ContinueTool == current.ContinueTool &&
		previous.CursorField == current.CursorField && slices.Equal(
		slices.Sorted(slices.Values(previous.RetainedFields)),
		slices.Sorted(slices.Values(current.RetainedFields)),
	)
}
