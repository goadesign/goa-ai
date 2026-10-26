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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

type (
	// CompiledDeclarations returns the reader's pre-compiled generated
	// declaration for one operation name. A missing name means the reader's
	// build does not declare that operation.
	CompiledDeclarations func(name tools.Ident) (*CompiledDeclaration, bool)

	// CompiledDeclaration holds one generated tool declaration together with its
	// pre-encoded admission record, identity fingerprint, and compiled portable
	// ToolSpec. An AgentDefinition constructs it once per static tool so reading
	// a saved compiled selection never re-marshals ConsumerContract JSON or
	// re-compiles JSON schemas and regexes.
	CompiledDeclaration struct {
		declaration *genregistry.ToolSchema
		admission   internaladmission.ToolSchema
		fingerprint string
		kind        string
		spec        tools.ToolSpec
	}
)

const (
	serviceKind = "service"
	agentKind   = "agent"
)

var (
	// ErrCompiledContractChanged reports that a saved call names generated
	// operations whose declarations differ in the reading build, or that the
	// reading build no longer declares. The saved call cannot be interpreted with
	// the reader's generated code.
	ErrCompiledContractChanged = errors.New("compiled tool declarations changed since the call was selected")

	syntheticCompiledAgentTarget = &genregistry.AgentToolTarget{
		Executor:      "compiled.agent",
		Workflow:      "compiled.workflow",
		Queue:         "compiled.queue",
		Configuration: "compiled.configuration",
	}
)

// CompileDeclaration validates and pre-compiles one generated tool declaration.
// The declaration must be unbound (no provider Agent target) and have a
// qualified tool name.
func CompileDeclaration(declaration *genregistry.ToolSchema) (*CompiledDeclaration, error) {
	if declaration == nil || declaration.ConsumerContract == nil {
		return nil, errors.New("compiled tool declaration is required")
	}
	if declaration.ConsumerContract.Agent != nil {
		return nil, fmt.Errorf("generated registry declaration for %q must not select a provider", declaration.Name)
	}
	unbound := *declaration
	contract := *declaration.ConsumerContract
	unbound.ConsumerContract = &contract
	admission, err := unboundAdmissionToolSchema(&unbound)
	if err != nil {
		return nil, err
	}
	compileInput := unbound
	if contract.Kind == agentKind {
		agentContract := contract
		agentContract.Agent = syntheticCompiledAgentTarget
		compileInput.ConsumerContract = &agentContract
	}
	if err := genregistry.ValidateToolSchemaValue(&compileInput); err != nil {
		return nil, fmt.Errorf("registry resolution: %w", err)
	}
	spec, err := toolcontract.Compile(&compileInput)
	if err != nil {
		return nil, err
	}
	if spec.Name.Toolset() == "" || spec.Name.Tool() == "" {
		return nil, fmt.Errorf("registry tool %q must be qualified", spec.Name)
	}
	if contract.Kind != agentKind {
		spec.IsAgentTool = false
		spec.AgentID = ""
	}
	return &CompiledDeclaration{
		declaration: &unbound,
		admission:   admission,
		fingerprint: internaladmission.SchemaFingerprint(internaladmission.Schema{
			Name:  unbound.Name,
			Tools: []internaladmission.ToolSchema{admission},
		}),
		kind: contract.Kind,
		spec: spec,
	}, nil
}

// ResolveCompiled checks one registry response for tools whose declarations
// are generated into the consumer, and marks the result so Select saves only
// the registration chosen for each call. The caller must already have checked
// that the generated declarations match the registered ones.
func ResolveCompiled(registered *genregistry.ResolvedToolset) (*Resolution, error) {
	resolved, err := Resolve(registered)
	if err != nil {
		return nil, err
	}
	unbound := make(map[tools.Ident]internaladmission.ToolSchema, len(registered.Toolset.Tools))
	for _, declaration := range registered.Toolset.Tools {
		entry, err := unboundAdmissionToolSchema(declaration)
		if err != nil {
			return nil, err
		}
		unbound[tools.Ident(declaration.Name)] = entry
	}
	resolved.unbound = unbound
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

// Declaration returns the unbound generated ToolSchema owned by d.
func (d *CompiledDeclaration) Declaration() *genregistry.ToolSchema {
	return d.declaration
}

// Same reports whether d and other have the exact same registry declaration
// identity.
func (d *CompiledDeclaration) Same(other *CompiledDeclaration) bool {
	return d != nil && other != nil && d.fingerprint == other.fingerprint
}

// compiledFingerprint returns the registry identity of a set of generated
// declarations with their provider Agent targets removed.
func compiledFingerprint(toolset string, declarations []*genregistry.ToolSchema) (string, error) {
	tools := make([]internaladmission.ToolSchema, len(declarations))
	for index, declaration := range declarations {
		entry, err := unboundAdmissionToolSchema(declaration)
		if err != nil {
			return "", err
		}
		tools[index] = entry
	}
	return internaladmission.SchemaFingerprint(internaladmission.Schema{
		Name:  toolset,
		Tools: tools,
	}), nil
}

// unboundAdmissionToolSchema encodes declaration's ConsumerContract with its
// provider Agent target cleared so bound and unbound declarations produce the
// same admission record.
func unboundAdmissionToolSchema(declaration *genregistry.ToolSchema) (internaladmission.ToolSchema, error) {
	contract := *declaration.ConsumerContract
	contract.Agent = nil
	encodedContract, err := json.Marshal(&contract)
	if err != nil {
		return internaladmission.ToolSchema{}, fmt.Errorf("encode tool %q consumer contract: %w", declaration.Name, err)
	}
	return internaladmission.ToolSchema{
		Name:                   declaration.Name,
		Description:            declaration.Description,
		Tags:                   slices.Clone(declaration.Tags),
		PayloadSchema:          slices.Clone(declaration.PayloadSchema),
		ExecutionPayloadSchema: slices.Clone(declaration.ExecutionPayloadSchema),
		ResultSchema:           slices.Clone(declaration.ResultSchema),
		SidecarSchema:          slices.Clone(declaration.SidecarSchema),
		ConsumerContract:       encodedContract,
	}, nil
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

// readCompiled rebuilds a compiled selection from the reader's pre-compiled
// generated declarations. It first compares their fingerprint with the saved
// one, then validates the selection envelope and optional Agent target, and
// clones the pre-compiled contracts for the restored operations.
func readCompiled(selection *genregistry.CompiledToolSelection, agent *genregistry.AgentToolTarget, compiled CompiledDeclarations) (*Resolution, error) {
	if compiled == nil {
		return nil, errors.New("compiled tool selection requires the reader's generated declarations")
	}
	entries := make([]*CompiledDeclaration, len(selection.Operations))
	admissionTools := make([]internaladmission.ToolSchema, len(selection.Operations))
	for index, name := range selection.Operations {
		entry, ok := compiled(tools.Ident(name))
		if !ok || entry == nil {
			return nil, fmt.Errorf("%w: operation %q is not declared by this build", ErrCompiledContractChanged, name)
		}
		entries[index] = entry
		admissionTools[index] = entry.admission
	}
	fingerprint := internaladmission.SchemaFingerprint(internaladmission.Schema{
		Name:  selection.Name,
		Tools: admissionTools,
	})
	if fingerprint != selection.DeclarationFingerprint {
		return nil, fmt.Errorf("%w: operations %v", ErrCompiledContractChanged, selection.Operations)
	}
	if err := genregistry.ValidateResolvedToolsetValue(&genregistry.ResolvedToolset{
		Toolset: &genregistry.Toolset{
			Name:         selection.Name,
			Description:  selection.Description,
			Version:      selection.Version,
			Tags:         selection.Tags,
			Tools:        []*genregistry.ToolSchema{},
			RegisteredAt: selection.RegisteredAt,
		},
		RegistrationToken: selection.RegistrationToken,
	}); err != nil {
		return nil, fmt.Errorf("restore compiled tool selection: registry resolution: %w", err)
	}
	if agent != nil {
		if err := genregistry.ValidateAgentToolTargetValue(agent); err != nil {
			return nil, fmt.Errorf("restore compiled tool selection: registry resolution: %w", err)
		}
	}
	bound := make([]*genregistry.ToolSchema, len(entries))
	specs := make(map[tools.Ident]tools.ToolSpec, len(entries))
	byName := make(map[tools.Ident]*genregistry.ToolSchema, len(entries))
	unbound := make(map[tools.Ident]internaladmission.ToolSchema, len(entries))
	for index, entry := range entries {
		declaration := entry.declaration
		if (entry.kind == agentKind) != (agent != nil) {
			return nil, fmt.Errorf("restore compiled tool selection: tool %q agent target mismatch", declaration.Name)
		}
		copied := *declaration
		contract := *declaration.ConsumerContract
		contract.Agent = agent
		copied.ConsumerContract = &contract
		bound[index] = &copied
		spec := entry.spec
		if agent != nil {
			spec.IsAgentTool = true
			spec.AgentID = agent.Executor
		} else {
			spec.IsAgentTool = false
			spec.AgentID = ""
		}
		if _, exists := specs[spec.Name]; exists {
			return nil, fmt.Errorf("restore compiled tool selection: registry resolution repeats tool %q", spec.Name)
		}
		specs[spec.Name] = spec
		byName[spec.Name] = &copied
		unbound[spec.Name] = entry.admission
	}
	for _, spec := range specs {
		if err := validatePaging(specs, spec); err != nil {
			return nil, fmt.Errorf("restore compiled tool selection: %w", err)
		}
	}
	return &Resolution{
		Registered: &genregistry.ResolvedToolset{
			Toolset: &genregistry.Toolset{
				Name:         selection.Name,
				Description:  selection.Description,
				Version:      selection.Version,
				Tags:         selection.Tags,
				Tools:        bound,
				RegisteredAt: selection.RegisteredAt,
			},
			RegistrationToken: selection.RegistrationToken,
		},
		Specs:    specs,
		byName:   byName,
		unbound:  unbound,
		compiled: true,
	}, nil
}

// selectCompiled saves the registration chosen for the named operations: the
// token, the toolset envelope, their fingerprint, and for Agent operations the
// provider target. Every operation in one selection must share its kind and
// target, because the restored declarations all receive the saved target.
func (r *Resolution) selectCompiled(names []tools.Ident) (genregistry.RegistrySelectionKind, error) {
	declarations := make([]*genregistry.ToolSchema, len(names))
	admissionTools := make([]internaladmission.ToolSchema, len(names))
	operations := make([]string, len(names))
	for index, name := range names {
		declarations[index] = r.byName[name]
		admissionTools[index] = r.unbound[name]
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
	fingerprint := internaladmission.SchemaFingerprint(internaladmission.Schema{
		Name:  toolset.Name,
		Tools: admissionTools,
	})
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
