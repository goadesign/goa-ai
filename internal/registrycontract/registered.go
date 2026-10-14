// Package registrycontract derives imported tool names from owner-selected resource identity.
// Provider declarations remain unchanged so saved calls execute their original
// operations, even after a later catalog read selects another registration.
package registrycontract

import (
	"encoding/hex"
	"fmt"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// ResolveRegistered validates and copies an exact resource selection, then derives
// distinct tool and pagination names. The caller owns scope authorization and
// logical resource selection; private registration routes never supply that identity.
func ResolveRegistered(registry string, source *genregistry.CatalogSource, registered *genregistry.ResolvedToolset) (*Resolution, error) {
	if registry == "" {
		return nil, fmt.Errorf("registered source requires a registry name")
	}
	encoded, err := genregistry.EncodeRegistrySelection(&genregistry.RegistrySelection{
		Selection: genregistry.NewRegistrySelectionKindRegistered(&genregistry.RegisteredToolsetSelection{
			Source: source, Resolved: registered,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("registered source: %w", err)
	}
	copied, err := genregistry.DecodeRegistrySelection(encoded)
	if err != nil {
		return nil, fmt.Errorf("copy registered source: %w", err)
	}
	selected, _ := copied.Selection.AsRegistered()
	source = selected.Source
	registered = selected.Resolved
	kind := "service"
	if native, ok := source.Target.AsNative(); ok {
		kind = "agent"
		if native.Toolset != registered.Toolset.Name {
			return nil, fmt.Errorf("registered native source does not match its declaration route")
		}
	} else {
		service, _ := source.Target.AsService()
		if service.Toolset != registered.Toolset.Name || service.RegistrationToken != registered.RegistrationToken {
			return nil, fmt.Errorf("registered service source does not match its declaration route and token")
		}
	}
	for _, declaration := range registered.Toolset.Tools {
		if declaration.ConsumerContract.Kind != kind {
			return nil, fmt.Errorf("registered source kind %q does not match operation %q", kind, declaration.Name)
		}
	}
	resolved, err := Resolve(registered)
	if err != nil {
		return nil, err
	}
	resolved.Source = source
	originals := resolved.Specs
	resolved.Specs = make(map[tools.Ident]tools.ToolSpec, len(originals))
	resolved.byName = make(map[tools.Ident]*genregistry.ToolSchema, len(originals))
	for _, declaration := range registered.Toolset.Tools {
		original := tools.Ident(declaration.Name)
		spec := originals[original]
		spec.Name = registeredName(registry, source, original)
		if spec.Bounds != nil && spec.Bounds.Paging != nil {
			paging := spec.Bounds.Paging
			if paging.SourceTool != "" {
				paging.SourceTool = registeredName(registry, source, paging.SourceTool)
			}
			if paging.ContinueTool != "" {
				paging.ContinueTool = registeredName(registry, source, paging.ContinueTool)
			}
		}
		resolved.Specs[spec.Name] = spec
		resolved.byName[spec.Name] = declaration
	}
	return resolved, nil
}

// registeredName encodes each selected identity component separately. Hex fields
// cannot contain the separator, so different owners, kinds, resources or registries
// cannot share a name. The full operation starts the name and its leaf remains at the end;
// provider adapters own provider-specific length and character restrictions.
func registeredName(registry string, source *genregistry.CatalogSource, original tools.Ident) tools.Ident {
	return tools.Ident(string(original) + "." +
		hex.EncodeToString([]byte(registry)) + "_" + string(source.Target.Kind()) + "_" +
		hex.EncodeToString([]byte(source.Scope)) + "_" + hex.EncodeToString([]byte(source.Name)) +
		"." + original.Tool())
}
