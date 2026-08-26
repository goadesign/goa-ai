// Package codegen gives Goa the retained layouts for every conversion helper
// written to one tool package. Goa compares the complete conversion plans and
// assigns one declaration to helpers that produce the same function.
package codegen

import (
	"cmp"
	"fmt"
	"slices"

	"goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
)

type (
	// plannedPackageTransform stores one conversion until all generated type
	// layouts are available and Goa can compare every helper in the package.
	plannedPackageTransform struct {
		key          string
		prefix       string
		plan         *goacodegen.TransformPlan
		sourceLayout *goacodegen.GoTypePlan
		targetLayout *goacodegen.GoTypePlan
	}
)

// setTransformLayouts attaches the complete generated source and target forms
// to the conversion recorded by declareTransform.
func (p *toolSpecsPackagePlan) setTransformLayouts(plan *goacodegen.TransformPlan, source, target *goacodegen.GoTypePlan) {
	transform := p.transformFor(plan)
	transform.sourceLayout = source
	transform.targetLayout = target
}

// transformFor returns the saved type layouts for one conversion plan.
func (p *toolSpecsPackagePlan) transformFor(plan *goacodegen.TransformPlan) *plannedPackageTransform {
	for _, transform := range p.transformPlans {
		if transform.plan == plan {
			return transform
		}
	}
	panic("tool conversion plan was not recorded")
}

// finalizeTransformHelpers gives every package conversion to Goa in stable
// order, then declares and binds one function for each equivalent group.
func (p *toolSpecsPackagePlan) finalizeTransformHelpers() error {
	transforms := slices.Clone(p.transformPlans)
	slices.SortFunc(transforms, func(left, right *plannedPackageTransform) int {
		return cmp.Compare(left.key, right.key)
	})
	registry := goacodegen.NewTransformHelperRegistry()
	keys := make(map[goacodegen.TransformHelperDefinitionID]string)
	prefixes := make(map[string]string, len(transforms))
	for _, transform := range transforms {
		if transform.sourceLayout == nil || transform.targetLayout == nil {
			return fmt.Errorf("tool conversion %q has no generated type layouts", transform.key)
		}
		if err := registry.Collect(transform.plan, transform.sourceLayout, transform.targetLayout); err != nil {
			return fmt.Errorf("collect tool conversion %q helpers: %w", transform.key, err)
		}
		for _, definition := range transform.plan.HelperDefinitions() {
			keys[definition.ID] = transform.key
		}
		prefixes[transform.key] = transform.prefix
	}
	groups, err := registry.Finalize()
	if err != nil {
		return err
	}
	for _, group := range groups {
		definition := group.Definition()
		key := keys[definition.ID]
		namePrefix := "transform"
		if prefixes[key] != "" {
			namePrefix = prefixes[key]
		}
		preferred := lowerCamel(namePrefix + goacodegen.Goify(definition.Source.Type.Name(), true) + "To" + goacodegen.Goify(definition.Target.Type.Name(), true))
		declaration := goacodegen.NewPreferredName(
			goacodegen.NameFunction,
			preferred,
			goacodegen.UnexportedName,
			transformHelperNameOrder{
				packagePath: p.public.ImportPath(),
				key:         key,
				location:    definition.Location,
			},
		)
		if err := p.public.DeclareName(declaration); err != nil {
			return err
		}
		if err := group.Bind(declaration); err != nil {
			return err
		}
	}
	return nil
}

// setMethodTransformLayouts supplies the tool and service layouts selected
// earlier in the same generation plan.
func (p *toolSpecsPackagePlan) setMethodTransformLayouts(servicePlan *service.Plan, tools []*agent.ToolExpr) error {
	for _, tool := range tools {
		if tool.Method == nil {
			continue
		}
		names := p.tools[tool.Name]
		if names.methodPayloadTransformPlan != nil {
			serviceLayout, err := servicePlan.MethodPayloadLayout(tool.Method)
			if err != nil {
				return err
			}
			p.setTransformLayouts(names.methodPayloadTransformPlan, names.payloadType.publicLayout, serviceLayout)
		}
		var resultLayout *goacodegen.GoTypePlan
		if names.toolResultTransformPlan != nil || len(names.serverDataTransformPlans) > 0 {
			var err error
			resultLayout, err = servicePlan.MethodResultLayout(tool.Method)
			if err != nil {
				return err
			}
		}
		if names.toolResultTransformPlan != nil {
			p.setTransformLayouts(names.toolResultTransformPlan, resultLayout, names.resultType.publicLayout)
		}
		for _, serverData := range tool.ServerData {
			plan := names.serverDataTransformPlans[serverData.Kind]
			if plan == nil {
				continue
			}
			source := tool.Method.Result.Find(serverData.Source.MethodResultField)
			matches := resultLayout.PlansForOccurrence(source)
			if len(matches) != 1 {
				return fmt.Errorf(
					"server data %q result field %q has %d generated layouts",
					serverData.Kind,
					serverData.Source.MethodResultField,
					len(matches),
				)
			}
			p.setTransformLayouts(plan, matches[0], names.serverDataTypes[serverData.Kind].publicLayout)
		}
	}
	return nil
}

// planAdapterTransformImports reserves every package named by a conversion in
// transforms.go and records the packages that file must import.
func (p *toolSpecsPackagePlan) planAdapterTransformImports() error {
	seen := map[string]struct{}{p.public.ImportPath(): {}}
	var paths []string
	for _, transform := range p.adapterTransformPlans {
		for _, layout := range []*goacodegen.GoTypePlan{transform.sourceLayout, transform.targetLayout} {
			if layoutUsesOwner(layout) {
				if err := p.addAdapterTransformImport(&paths, seen, goacodegen.GoTypeImport{Path: layout.Owner()}); err != nil {
					return err
				}
			}
			for _, preference := range layout.CompleteImportPreferences() {
				if err := p.addAdapterTransformImport(&paths, seen, preference); err != nil {
					return err
				}
			}
		}
	}
	p.transformImportPaths = paths
	return nil
}

// addAdapterTransformImport reserves one package the adapter conversion file
// names and records its path once.
func (p *toolSpecsPackagePlan) addAdapterTransformImport(paths *[]string, seen map[string]struct{}, preference goacodegen.GoTypeImport) error {
	if preference.Path == "" {
		return nil
	}
	if _, ok := seen[preference.Path]; ok {
		return nil
	}
	seen[preference.Path] = struct{}{}
	if err := p.public.ReserveGeneratedImport(goacodegen.NewImport(preference.Name, preference.Path)); err != nil {
		return err
	}
	*paths = append(*paths, preference.Path)
	return nil
}

// finalizeTransformHelpers finalizes every public tool and completion package
// once after all of their conversion layouts are known.
func (p *toolSpecsPlan) finalizeTransformHelpers() error {
	packages := make(map[string]*toolSpecsPackagePlan, len(p.byDir)+len(p.completions))
	for _, planned := range p.byDir {
		packages[planned.public.ImportPath()] = planned
	}
	for _, planned := range p.completions {
		packages[planned.public.ImportPath()] = planned
	}
	paths := make([]string, 0, len(packages))
	for importPath := range packages {
		paths = append(paths, importPath)
	}
	slices.Sort(paths)
	for _, importPath := range paths {
		if err := packages[importPath].finalizeTransformHelpers(); err != nil {
			return fmt.Errorf("finalize transform helpers in %q: %w", importPath, err)
		}
	}
	return nil
}
