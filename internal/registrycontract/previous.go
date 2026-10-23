// Package registrycontract reads declarations omitted by saved compiled
// selections. The owner authenticates the resource; this file checks the exact
// saved route, token, target and selected-operation fingerprint before use.
package registrycontract

import (
	"fmt"
	"reflect"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// ReadPrevious validates a saved selection against its owner's exact retained
// registration. It never substitutes current declarations or changes the binding.
func ReadPrevious(binding *tools.RegistryBinding, owned *genregistry.ResolvedToolset) (*Resolution, error) {
	owner, err := Resolve(owned)
	if err != nil {
		return nil, err
	}
	selected, err := decodeSelection(binding)
	if err != nil {
		return nil, err
	}
	var route, token string
	var target *genregistry.AgentToolTarget
	if service, ok := selected.Selection.AsCompiledService(); ok {
		route, token = service.Name, service.RegistrationToken
	} else if native, ok := selected.Selection.AsCompiledAgent(); ok {
		route, token, target = native.Selection.Name, native.Selection.RegistrationToken, native.Agent
	} else if direct, ok := selected.Selection.AsDirect(); ok {
		route, token = direct.Toolset.Name, direct.RegistrationToken
	} else {
		return nil, fmt.Errorf("previous static registration requires a direct or compiled selection")
	}
	if owned.Toolset.Name != route || owned.RegistrationToken != token {
		return nil, fmt.Errorf("previous registration returned another route or token")
	}
	lookup := func(name tools.Ident) (*genregistry.ToolSchema, bool) {
		declaration, exists := owner.byName[name]
		if !exists {
			return nil, false
		}
		copied := *declaration
		contract := *declaration.ConsumerContract
		contract.Agent = nil
		copied.ConsumerContract = &contract
		return &copied, true
	}
	prior, err := Read(binding, lookup)
	if err != nil {
		return nil, err
	}
	declarations := make([]*genregistry.ToolSchema, len(prior.Registered.Toolset.Tools))
	for index, declaration := range prior.Registered.Toolset.Tools {
		actual, exists := owner.byName[tools.Ident(declaration.Name)]
		if !exists {
			return nil, fmt.Errorf("previous registration omitted operation %q", declaration.Name)
		}
		if actual.ConsumerContract.Kind != declaration.ConsumerContract.Kind ||
			!reflect.DeepEqual(actual.ConsumerContract.Agent, declaration.ConsumerContract.Agent) ||
			target != nil && !reflect.DeepEqual(actual.ConsumerContract.Agent, target) {
			return nil, fmt.Errorf("previous registration returned another execution target for %q", declaration.Name)
		}
		declarations[index] = actual
	}
	expected, err := compiledFingerprint(route, prior.Registered.Toolset.Tools)
	if err != nil {
		return nil, err
	}
	actual, err := compiledFingerprint(route, declarations)
	if err != nil {
		return nil, err
	}
	if expected != actual {
		return nil, fmt.Errorf("%w: previous selected operations", ErrCompiledContractChanged)
	}
	return prior, nil
}
