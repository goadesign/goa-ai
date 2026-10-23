// Package runtime compiles complete static service declarations into immutable
// selected-call records. Callers may reuse those records across activities;
// each activity still supplies its own policy, generated declarations and
// previous-registration reader before it can schedule a call.
package runtime

import (
	"context"
	"fmt"
	"slices"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// CompiledStaticService holds independently owned selections derived from a
	// complete matching expected and selected service declaration. It grants no
	// access or provider availability, and retains no caller or activity state.
	CompiledStaticService struct {
		route      string
		version    string
		selections map[tools.Ident]rawjson.Message
	}
)

// CompileStaticService checks the complete raw declarations with the same
// generated codecs and fingerprint rules as BindStaticService. Its immutable
// result keeps only each operation's selected registration and paging partner;
// later changes to either raw input cannot change the result. Callers must
// compile again when they select another declaration or registration.
func CompileStaticService(expected *genregistry.Toolset, selected *genregistry.ResolvedToolset) (*CompiledStaticService, error) {
	resolved, err := compileStaticServiceDeclarations(expected, selected)
	if err != nil {
		return nil, err
	}
	if err := validateStaticServiceKinds(resolved); err != nil {
		return nil, err
	}
	compiled := &CompiledStaticService{
		route:      resolved.Registered.Toolset.Name,
		selections: make(map[tools.Ident]rawjson.Message, len(resolved.Registered.Toolset.Tools)),
	}
	if resolved.Registered.Toolset.Version != nil {
		compiled.version = string(*resolved.Registered.Toolset.Version)
	}
	for _, declaration := range resolved.Registered.Toolset.Tools {
		name := tools.Ident(declaration.Name)
		binding, err := resolved.Select("", name)
		if err != nil {
			return nil, err
		}
		compiled.selections[name] = binding.Resolution
	}
	return compiled, nil
}

// BindCompiledStaticService attaches checked selections to this activity's
// named generated tools. It checks current consumption, declarations and local
// specifications, then installs an activity-owned result with readPrevious.
// That reader must still prove the old registration's owner before a new page;
// accepted calls retain their saved registration. The compiled value supplies
// no authority and must not replace a caller's current provider selection.
func (c *RegistryCatalog) BindCompiledStaticService(registry string, compiled *CompiledStaticService, readPrevious func(context.Context, string, string) (*genregistry.ResolvedToolset, error), names ...tools.Ident) error {
	if readPrevious == nil {
		return fmt.Errorf("static service binding requires a reader for previous registrations")
	}
	if len(names) == 0 {
		return fmt.Errorf("static service binding requires explicit tool names")
	}
	if _, err := c.runtime.registryConnection(registry); err != nil {
		return err
	}
	if compiled == nil || compiled.route == "" {
		return fmt.Errorf("static service binding requires a compiled service declaration")
	}
	if c.sources == nil || !c.sources.Allows(registry, compiled.route, compiled.version) {
		return fmt.Errorf("registry %q toolset %q is outside this agent's declared consumption", registry, compiled.route)
	}
	restored := make([]*registrycontract.Resolution, len(names))
	for index, name := range names {
		if slices.Contains(names[:index], name) {
			return fmt.Errorf("static service binding repeats tool %q", name)
		}
		if _, exists := c.selections[name]; exists {
			return fmt.Errorf("tool %q already has a selected registration", name)
		}
		spec, exists := c.definition.specByName[name]
		if !exists {
			return fmt.Errorf("tool %q is not static in this consuming definition", name)
		}
		selection, exists := compiled.selections[name]
		if !exists {
			return fmt.Errorf("registry selection contains no tool %q", name)
		}
		binding := &tools.RegistryBinding{Registry: registry, Resolution: selection}
		resolved, err := registrycontract.Read(binding, c.definition.registryDeclaration)
		if err != nil {
			return fmt.Errorf("tool %q cannot be restored from this agent's generated registry declarations; regenerate the agent: %w", name, err)
		}
		if err := validateStaticServiceSpec(spec, resolved); err != nil {
			return err
		}
		restored[index] = resolved
	}
	return c.installStaticServiceSelections(registry, restored, readPrevious, names)
}
