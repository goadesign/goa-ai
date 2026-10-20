// Package registrycontract saves and restores calls to tools whose declarations
// are generated into the consuming agent.
//
// A planned call to such a tool keeps only the registration chosen for it: the
// registration token, the toolset envelope, the names of the called operation
// and its paging partner, and for an Agent tool the provider's worker route and
// configuration. The reader supplies the generated declarations for those names
// from its own build. Read checks them against the saved fingerprint and then
// rebuilds the same Resolution that a full saved copy would decode to. When the
// reader's build declares different operations, Read returns
// ErrCompiledContractChanged so each caller can apply its own rule for a tool
// that changed between builds.
package registrycontract

import (
	"errors"
	"fmt"
	"reflect"
	"slices"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

type (
	// CompiledDeclarations returns the reader's generated declaration for one
	// operation name. The declaration has no provider Agent target; a missing
	// name means the reader's build does not declare that operation.
	CompiledDeclarations func(name tools.Ident) (*genregistry.ToolSchema, bool)
)

const (
	serviceKind = "service"
	agentKind   = "agent"
)

// ErrCompiledContractChanged reports that a saved call names generated
// operations whose declarations differ in the reading build, or that the
// reading build no longer declares. The saved call cannot be interpreted with
// the reader's generated code.
var ErrCompiledContractChanged = errors.New("compiled tool declarations changed since the call was selected")

// ResolveCompiled checks one registry response for tools whose declarations
// are generated into the consumer, and marks the result so Select saves only
// the registration chosen for each call. The caller must already have checked
// that the generated declarations match the registered ones.
func ResolveCompiled(registered *genregistry.ResolvedToolset) (*Resolution, error) {
	resolved, err := Resolve(registered)
	if err != nil {
		return nil, err
	}
	resolved.compiled = true
	return resolved, nil
}

// ServiceRoute reads the registration token and registered toolset name that a
// saved service-tool call must use, without needing the tool's declaration. It
// accepts direct and compiled service selections, and rejects registered
// resources and Agent operations, which a service executor cannot run.
func ServiceRoute(binding *tools.RegistryBinding, name tools.Ident) (token, toolset string, err error) {
	selected, err := decodeSelection(binding)
	if err != nil {
		return "", "", err
	}
	if compiled, ok := selected.Selection.AsCompiledService(); ok {
		if !slices.Contains(compiled.Operations, string(name)) {
			return "", "", fmt.Errorf("saved compiled selection contains no tool %q", name)
		}
		return compiled.RegistrationToken, compiled.Name, nil
	}
	direct, ok := selected.Selection.AsDirect()
	if !ok {
		return "", "", errors.New("saved selection is not a generated service tool")
	}
	resolved, err := Resolve(direct)
	if err != nil {
		return "", "", err
	}
	declaration, err := resolved.Declaration(name)
	if err != nil {
		return "", "", err
	}
	if declaration.ConsumerContract.Kind != serviceKind {
		return "", "", errors.New("saved selection is not a generated service tool")
	}
	return direct.RegistrationToken, direct.Toolset.Name, nil
}

// RegistrationToken reads the registration a saved selection names without
// needing its declarations, so a caller can group saved calls by registration
// even when the reading build declares a compiled tool differently.
func RegistrationToken(binding *tools.RegistryBinding) (string, error) {
	selected, err := decodeSelection(binding)
	if err != nil {
		return "", err
	}
	if direct, ok := selected.Selection.AsDirect(); ok {
		return direct.RegistrationToken, nil
	}
	if registered, ok := selected.Selection.AsRegistered(); ok {
		return registered.Resolved.RegistrationToken, nil
	}
	if service, ok := selected.Selection.AsCompiledService(); ok {
		return service.RegistrationToken, nil
	}
	agent, _ := selected.Selection.AsCompiledAgent()
	return agent.Selection.RegistrationToken, nil
}

// SameSelection reports whether two saved bindings name the same registry and
// the same selection. It compares the decoded values, so it needs no
// declarations and gives the same answer in every build.
func SameSelection(a, b *tools.RegistryBinding) (bool, error) {
	left, err := decodeSelection(a)
	if err != nil {
		return false, err
	}
	right, err := decodeSelection(b)
	if err != nil {
		return false, err
	}
	return a.Registry == b.Registry && reflect.DeepEqual(left, right), nil
}

// compiledFingerprint returns the registry identity of a set of generated
// declarations with their provider Agent targets removed. Generated
// declarations never carry a target, and a bound declaration differs from its
// generated one only by that target, so both sides produce the same value.
func compiledFingerprint(toolset string, declarations []*genregistry.ToolSchema) (string, error) {
	unbound := make([]*genregistry.ToolSchema, len(declarations))
	for index, declaration := range declarations {
		copied := *declaration
		if declaration.ConsumerContract != nil {
			contract := *declaration.ConsumerContract
			contract.Agent = nil
			copied.ConsumerContract = &contract
		}
		unbound[index] = &copied
	}
	return toolcontract.Fingerprint(&genregistry.Toolset{Name: toolset, Tools: unbound})
}

// decodeSelection validates the saved registry name and decodes its typed union.
func decodeSelection(binding *tools.RegistryBinding) (*genregistry.RegistrySelection, error) {
	if binding == nil || binding.Registry == "" {
		return nil, errors.New("selected tool registry is required")
	}
	selected, err := genregistry.DecodeRegistrySelection(binding.Resolution)
	if err != nil {
		return nil, fmt.Errorf("decode selected registry tool: %w", err)
	}
	return selected, nil
}

// readCompiled rebuilds a compiled selection from the reader's generated
// declarations. It first compares their fingerprint with the saved one, then
// attaches the saved Agent target and toolset envelope, and finally runs the
// same validation as a registry response. agent is nil for service tools.
func readCompiled(selection *genregistry.CompiledToolSelection, agent *genregistry.AgentToolTarget, compiled CompiledDeclarations) (*Resolution, error) {
	if compiled == nil {
		return nil, errors.New("compiled tool selection requires the reader's generated declarations")
	}
	declarations := make([]*genregistry.ToolSchema, len(selection.Operations))
	for index, name := range selection.Operations {
		declaration, ok := compiled(tools.Ident(name))
		if !ok {
			return nil, fmt.Errorf("%w: operation %q is not declared by this build", ErrCompiledContractChanged, name)
		}
		declarations[index] = declaration
	}
	fingerprint, err := compiledFingerprint(selection.Name, declarations)
	if err != nil {
		return nil, err
	}
	if fingerprint != selection.DeclarationFingerprint {
		return nil, fmt.Errorf("%w: operations %v", ErrCompiledContractChanged, selection.Operations)
	}
	bound := make([]*genregistry.ToolSchema, len(declarations))
	for index, declaration := range declarations {
		copied := *declaration
		contract := *declaration.ConsumerContract
		contract.Agent = agent
		copied.ConsumerContract = &contract
		bound[index] = &copied
	}
	resolved, err := ResolveCompiled(&genregistry.ResolvedToolset{
		Toolset: &genregistry.Toolset{
			Name:         selection.Name,
			Description:  selection.Description,
			Version:      selection.Version,
			Tags:         selection.Tags,
			Tools:        bound,
			RegisteredAt: selection.RegisteredAt,
		},
		RegistrationToken: selection.RegistrationToken,
	})
	if err != nil {
		return nil, fmt.Errorf("restore compiled tool selection: %w", err)
	}
	return resolved, nil
}

// selectCompiled saves the registration chosen for the named operations: the
// token, the toolset envelope, their fingerprint, and for Agent operations the
// provider target. Every operation in one selection must share its kind and
// target, because the restored declarations all receive the saved target.
func (r *Resolution) selectCompiled(names []tools.Ident) (genregistry.RegistrySelectionKind, error) {
	declarations := make([]*genregistry.ToolSchema, len(names))
	operations := make([]string, len(names))
	for index, name := range names {
		declarations[index] = r.byName[name]
		operations[index] = string(name)
	}
	first := declarations[0].ConsumerContract
	for _, declaration := range declarations[1:] {
		contract := declaration.ConsumerContract
		if contract.Kind != first.Kind || (first.Agent == nil) != (contract.Agent == nil) ||
			(first.Agent != nil && *first.Agent != *contract.Agent) {
			return genregistry.RegistrySelectionKind{}, fmt.Errorf("compiled operations %v do not share one provider target", operations)
		}
	}
	toolset := r.Registered.Toolset
	fingerprint, err := compiledFingerprint(toolset.Name, declarations)
	if err != nil {
		return genregistry.RegistrySelectionKind{}, err
	}
	selection := &genregistry.CompiledToolSelection{
		RegistrationToken:      r.Registered.RegistrationToken,
		Name:                   toolset.Name,
		Description:            toolset.Description,
		Version:                toolset.Version,
		Tags:                   slices.Clone(toolset.Tags),
		RegisteredAt:           toolset.RegisteredAt,
		Operations:             operations,
		DeclarationFingerprint: fingerprint,
	}
	switch first.Kind {
	case serviceKind:
		return genregistry.NewRegistrySelectionKindCompiledService(selection), nil
	case agentKind:
		target := *first.Agent
		return genregistry.NewRegistrySelectionKindCompiledAgent(&genregistry.CompiledAgentToolSelection{
			Selection: selection, Agent: &target,
		}), nil
	default:
		return genregistry.RegistrySelectionKind{}, fmt.Errorf("compiled operation kind %q is not supported", first.Kind)
	}
}
