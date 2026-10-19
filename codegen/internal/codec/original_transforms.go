// Package codec shares original-value conversion helpers only after Goa has
// compared their generated types, conversion rules, and nested helper calls.
package codec

import (
	"strings"

	"goa.design/goa/v3/codegen"
)

type (
	// originalTransformOrder keeps helper names stable across package planning.
	originalTransformOrder struct {
		key      string
		location codegen.TransformHelperDefinitionLocation
	}
)

// FinalizeOriginals binds conversion helpers after all original values have
// been added and before generation chooses final package names.
func (p *Plan) FinalizeOriginals() error {
	registry := codegen.NewTransformHelperRegistry()
	prefixes := make(map[codegen.TransformHelperDefinitionID]string)
	for _, value := range p.values {
		if value.originalLayout == nil {
			continue
		}
		for _, direction := range []struct {
			prefix         string
			plan           *codegen.TransformPlan
			source, target *codegen.GoTypePlan
		}{
			{"encode", value.encode, value.originalLayout, value.transportLayout},
			{"decode", value.decode, value.transportLayout, value.originalLayout},
		} {
			order := originalTransformOrder{key: value.key + ":" + direction.prefix}
			if err := registry.Collect(direction.plan, direction.source, direction.target, order.at); err != nil {
				return err
			}
			for _, definition := range direction.plan.HelperDefinitions() {
				prefixes[definition.ID] = direction.prefix
			}
		}
	}
	groups, err := registry.Finalize()
	if err != nil {
		return err
	}
	for _, group := range groups {
		definition := group.Definition()
		preferred := prefixes[definition.ID] + codegen.Goify(definition.Source.Type.Name(), true) + "To" + codegen.Goify(definition.Target.Type.Name(), true)
		declaration := codegen.NewPreferredName(codegen.NameFunction, preferred, codegen.UnexportedName, group.Order())
		if err := p.pkg.DeclareName(declaration); err != nil {
			return err
		}
		if err := group.Bind(declaration); err != nil {
			return err
		}
	}
	return nil
}

// ComparePackageName compares the containing conversion before its child path.
func (o originalTransformOrder) ComparePackageName(other codegen.PackageNameOrder) int {
	right := other.(originalTransformOrder)
	if compared := strings.Compare(o.key, right.key); compared != 0 {
		return compared
	}
	return o.location.Compare(right.location)
}

// at adds Goa's exact child location to the conversion's stable name order.
func (o originalTransformOrder) at(location codegen.TransformHelperDefinitionLocation) codegen.PackageNameOrder {
	o.location = location
	return o
}
