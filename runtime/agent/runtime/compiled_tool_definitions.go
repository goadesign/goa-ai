// Package runtime retains compiled model contracts with the tool specification
// that owns them. Static registrations compile both supported inputs once.
// Changed activity inputs compile their ordinary contract when installed, then
// their text-only contract only if the current planner may actually see it.
package runtime

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// compiledToolDefinition keeps the exact validators and codecs constructed
	// from one accepted tool specification. An activity's changed text-only input
	// is filled before the planner receives its context; excluded inputs stay unused.
	compiledToolDefinition struct {
		ordinary *model.ToolDefinition
		textOnly *model.ToolDefinition
	}
)

// prepareModelDefinitions receives the final activity choices after source
// selection, input binding, correction and policy checks. It compiles only
// visible changed text-only inputs, so later reads only copy owned definitions.
func (c *RegistryCatalog) prepareModelDefinitions(specs []tools.ToolSpec, policy compiledToolPolicy, unavailable []tools.Ident) error {
	if !policy.textOnly {
		return nil
	}
	for _, spec := range specs {
		if !plannerToolVisible(spec, policy, unavailable, c.runtime.toolConfirmation) {
			continue
		}
		compiled := c.definitions[spec.Name]
		if compiled.textOnly != nil {
			continue
		}
		definition, err := model.NewToolDefinitionFromSpec(spec.ForTextOnly())
		if err != nil {
			return fmt.Errorf("tool %q text-only model input: %w", spec.Name, err)
		}
		definition.Deferred = compiled.ordinary.Deferred
		compiled.textOnly = definition
		c.definitions[spec.Name] = compiled
	}
	return nil
}
