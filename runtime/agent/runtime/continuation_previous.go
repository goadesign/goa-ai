// Package runtime reads exact previous registrations through the current
// resource's owner. Proven declarations live only on this activity's catalog;
// stored call/result bytes and accepted execution selections stay unchanged.
package runtime

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// hasCurrentPagingPair checks open chains before discovery or previous-owner
// reads. Finished queries need no provider metadata; local tools must retain
// their paging declaration before discovery is useful.
func (r *Runtime) hasCurrentPagingPair(agentID agent.Ident, outputs []*planner.ToolOutput) (bool, error) {
	chains, _, err := historicalPagingChains(outputs)
	if err != nil {
		return false, err
	}
	for _, chain := range chains {
		head := chain[len(chain)-1]
		if head.Bounds == nil || head.Bounds.NextCursor == nil {
			continue
		}
		if chain[0].Registry != nil {
			return true, nil
		}
		spec, exists := r.agentTools(agentID).spec(chain[0].Name)
		if exists && spec.Bounds != nil && spec.Bounds.Paging != nil {
			return true, nil
		}
	}
	return false, nil
}

// continuationActions supplies accepted old declarations to candidate building
// and then applies today's provider, input and policy checks to each new page.
func (c *RegistryCatalog) continuationActions(ctx context.Context, input *resolvedPlanActivityInput, outputs []*planner.ToolOutput) ([]continuationAction, error) {
	chains, keys, err := historicalPagingChains(outputs)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(chains))
	for key, chain := range chains {
		head := chain[len(chain)-1]
		if head.Bounds == nil || head.Bounds.NextCursor == nil {
			continue
		}
		if chain[0].Registry == nil {
			selected[key] = true
			continue
		}
		current, exists := c.selections[chain[0].Name]
		if !exists || current.registry != chain[0].Registry.Registry {
			continue
		}
		source, exists := current.resolution.Specs[chain[0].Name]
		if !exists || source.Bounds == nil || source.Bounds.Paging == nil {
			continue
		}
		page, exists := current.resolution.Specs[source.Bounds.Paging.ContinueTool]
		if !exists {
			continue
		}
		if _, static := c.definition.specByName[chain[0].Name]; static {
			if _, exists := c.definition.specByName[page.Name]; !exists {
				continue
			}
		}
		policy := compileToolPolicy(input.Policy)
		if !policy.allowsTool(page.Name, toolPolicyFactsFromSpec(page)) {
			continue
		}
		// Read every member before omitting this chain. An unavailable resource
		// must not hide another member's malformed binding or owner read failure.
		compatible := true
		for _, output := range chain {
			prior, err := c.previousContinuationSelection(ctx, output)
			if err != nil {
				return nil, err
			}
			if prior == nil {
				compatible = false
			}
		}
		selected[key] = compatible
	}
	enriched := make([]*planner.ToolOutput, 0, len(outputs))
	for _, output := range outputs {
		if output.Failure != nil {
			enriched = append(enriched, output)
			continue
		}
		if !selected[keys[output]] {
			continue
		}
		if output.Registry == nil {
			enriched = append(enriched, output)
			continue
		}
		if _, static := c.definition.specByName[output.Name]; !static {
			// Discovered resources already retain their owner and full declarations.
			enriched = append(enriched, output)
			continue
		}
		prior, err := c.previousContinuationSelection(ctx, output)
		if err != nil {
			return nil, err
		}
		// Full declarations are only an activity-local view for generic paging code.
		// The accepted output and its compact binding remain untouched in storage.
		full, err := registrycontract.Resolve(prior.Registered)
		if err != nil {
			return nil, err
		}
		binding, err := full.Select(output.Registry.Registry, output.Name)
		if err != nil {
			return nil, err
		}
		copied := *output
		copied.Registry = binding
		c.previousSelections[previousSelectionKey(binding)] = prior
		enriched = append(enriched, &copied)
	}
	actions, err := c.runtime.availableContinuationActions(input.AgentID, enriched, input.RunContext.TextOnly)
	if err != nil {
		return nil, err
	}
	return c.runtime.bindContinuationCandidates(ctx, input, c, actions)
}

// previousContinuationSelection returns one exact owner-proved selection for
// this activity. Identical selections share metadata only within its actor and
// invocation; another activity always performs its own owner authorization.
// A nil result means the saved resource has no matching current page provider.
// Its accepted result remains in history and grants no permission for new work.
func (c *RegistryCatalog) previousContinuationSelection(ctx context.Context, output *planner.ToolOutput) (selection *registrycontract.Resolution, readErr error) {
	key := previousSelectionKey(output.Registry)
	if prior, exists := c.previousSelections[key]; exists {
		return prior, nil
	}
	current, exists := c.selections[output.Name]
	if !exists || current.registry != output.Registry.Registry {
		return nil, fmt.Errorf("previous page has no current resource for %q", output.Name)
	}
	if _, static := c.definition.specByName[output.Name]; !static {
		return registrycontract.Read(output.Registry, c.registryDeclaration)
	}
	ctx, span := c.runtime.tracer.Start(ctx, "registry.read_previous_selection")
	span.SetAttributes(attribute.String("registry.name", output.Registry.Registry), attribute.String("tool.name", output.Name.String()))
	defer func() {
		if readErr != nil {
			span.RecordError(readErr)
			span.SetStatus(codes.Error, "previous selection read failed")
		}
		span.End()
	}()
	selected, err := genregistry.DecodeRegistrySelection(output.Registry.Resolution)
	if err != nil {
		return nil, err
	}
	var route, token string
	var target *genregistry.AgentToolTarget
	if service, ok := selected.Selection.AsCompiledService(); ok {
		route, token = service.Name, service.RegistrationToken
	} else if native, ok := selected.Selection.AsCompiledAgent(); ok {
		route, token, target = native.Selection.Name, native.Selection.RegistrationToken, native.Agent
	} else {
		prior, err := registrycontract.Read(output.Registry, c.registryDeclaration)
		if err != nil {
			return nil, err
		}
		if prior.Source != nil {
			return nil, fmt.Errorf("registered resource cannot replace static page %q", output.Name)
		}
		route, token = prior.Registered.Toolset.Name, prior.Registered.RegistrationToken
		declaration, err := prior.Declaration(output.Name)
		if err != nil {
			return nil, err
		}
		target = declaration.ConsumerContract.Agent
	}
	currentSpec, exists := current.resolution.Specs[output.Name]
	if !exists {
		return nil, fmt.Errorf("previous page operation %q is unavailable", output.Name)
	}
	if currentSpec.IsAgentTool != (target != nil) {
		return nil, fmt.Errorf("previous page operation %q changed execution kind", output.Name)
	}
	var owned *genregistry.ResolvedToolset
	if currentSpec.IsAgentTool {
		owned, err = current.readPreviousAgent(ctx, route, token, target)
	} else {
		owned, err = current.readPrevious(ctx, route, token)
	}
	if err != nil {
		return nil, fmt.Errorf("read previous registration for %q: %w", output.Name, err)
	}
	if owned == nil {
		return nil, nil
	}
	prior, err := registrycontract.ReadPrevious(output.Registry, owned)
	if err != nil {
		return nil, err
	}
	c.previousSelections[key] = prior
	return prior, nil
}

// previousSelectionKey keeps different registries and exact saved selections
// separate even when they contain equal cursors or operation names.
func previousSelectionKey(binding *tools.RegistryBinding) string {
	return binding.Registry + "\x00" + string(binding.Resolution)
}
