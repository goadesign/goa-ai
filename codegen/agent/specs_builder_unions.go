package codegen

import (
	"sort"

	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// collectUnionSumTypes walks att and records all union sum types referenced by
// the attribute graph. Union types are keyed by hash so they are emitted once
// per specs package.
func (b *toolSpecBuilder) collectUnionSumTypes(scope *codegen.NameScope, att *goaexpr.AttributeExpr) {
	if b == nil || scope == nil || att == nil {
		return
	}
	if b.unions == nil {
		b.unions = make(map[string]*unionTypeData)
	}
	seen := make(map[string]struct{})
	collectUnionSumTypes(att, scope, b.unions, seen)
}

// collectTransportUnionSumTypes walks a transport-localized attribute graph and
// records all union sum types referenced by it. This is used to emit
// toolset-local http/unions.go without leaking service gen/types references.
func (b *toolSpecBuilder) collectTransportUnionSumTypes(scope *codegen.NameScope, att *goaexpr.AttributeExpr) {
	if b == nil || scope == nil || att == nil {
		return
	}
	if b.transportUnions == nil {
		b.transportUnions = make(map[string]*unionTypeData)
	}
	seen := make(map[string]struct{})
	collectUnionSumTypes(att, scope, b.transportUnions, seen)
}

// unionTypes returns the collected union sum types in deterministic order.
func (b *toolSpecBuilder) unionTypes() []*unionTypeData {
	if b == nil || len(b.unions) == 0 {
		return nil
	}
	out := make([]*unionTypeData, 0, len(b.unions))
	for _, u := range b.unions {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// transportUnionTypes returns the collected transport union sum types in
// deterministic order.
func (b *toolSpecBuilder) transportUnionTypes() []*unionTypeData {
	if b == nil || len(b.transportUnions) == 0 {
		return nil
	}
	out := make([]*unionTypeData, 0, len(b.transportUnions))
	for _, u := range b.transportUnions {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func collectUnionSumTypes(
	att *goaexpr.AttributeExpr,
	scope *codegen.NameScope,
	unions map[string]*unionTypeData,
	seen map[string]struct{},
) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		if dt == nil {
			return
		}
		if _, ok := seen[dt.ID()]; ok {
			return
		}
		seen[dt.ID()] = struct{}{}
		collectUnionSumTypes(dt.Attribute(), scope, unions, seen)
	case *goaexpr.Object:
		for _, nat := range *dt {
			if nat == nil {
				continue
			}
			collectUnionSumTypes(nat.Attribute, scope, unions, seen)
		}
	case *goaexpr.Array:
		collectUnionSumTypes(dt.ElemType, scope, unions, seen)
	case *goaexpr.Map:
		collectUnionSumTypes(dt.KeyType, scope, unions, seen)
		collectUnionSumTypes(dt.ElemType, scope, unions, seen)
	case *goaexpr.Union:
		hash := dt.Hash()
		if _, ok := unions[hash]; !ok {
			unions[hash] = buildUnionTypeData(dt, scope)
		}
		for _, nat := range dt.Values {
			if nat == nil {
				continue
			}
			collectUnionSumTypes(nat.Attribute, scope, unions, seen)
		}
	}
}

func buildUnionTypeData(u *goaexpr.Union, scope *codegen.NameScope) *unionTypeData {
	att := &goaexpr.AttributeExpr{Type: u}
	name := scope.GoTypeName(att)
	kindName := scope.Unique(name + "Kind")

	fields := make([]*unionFieldData, 0, len(u.Values))
	for _, nat := range u.Values {
		if nat == nil || nat.Attribute == nil {
			continue
		}
		fieldName := codegen.Goify(nat.Name, true)
		var pkg string
		if tloc := codegen.UserTypeLocation(nat.Attribute.Type); tloc != nil {
			pkg = tloc.PackageName()
		}
		fieldType := scope.GoFullTypeRef(nat.Attribute, pkg)
		kindConst := kindName + codegen.Goify(nat.Name, true)
		fields = append(fields, &unionFieldData{
			Name:      nat.Name,
			KindConst: kindConst,
			FieldName: fieldName,
			FieldType: fieldType,
			JSONType:  generatedJSONType(nat.Attribute.Type),
			TypeTag:   nat.Name,
		})
	}

	return &unionTypeData{
		Name:     name,
		KindName: kindName,
		Fields:   fields,
	}
}
