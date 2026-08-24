// This file reads the final names for unions and builds the data used to write
// each union type, branch constructor, and JSON function.
package codegen

import (
	"sort"

	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// collectUnionSumTypes saves every union used by att. Equal unions are written
// once in the public package.
func (b *toolSpecBuilder) collectUnionSumTypes(scope *codegen.NameScope, att *goaexpr.AttributeExpr) {
	if b == nil || scope == nil || att == nil {
		return
	}
	if b.unions == nil {
		b.unions = make(map[codegen.UnionTypeID]*unionTypeData)
	}
	seen := make(map[string]struct{})
	collectUnionSumTypes(att, scope, b.publicPackage, b.publicUnionErrors, b.unions, seen)
}

// collectTransportUnionSumTypes saves every union used by the HTTP decoding
// type. The generated HTTP package can then define those unions locally.
func (b *toolSpecBuilder) collectTransportUnionSumTypes(scope *codegen.NameScope, att *goaexpr.AttributeExpr) {
	if b == nil || scope == nil || att == nil {
		return
	}
	if b.transportUnions == nil {
		b.transportUnions = make(map[codegen.UnionTypeID]*unionTypeData)
	}
	seen := make(map[string]struct{})
	collectUnionSumTypes(att, scope, b.transportPackage, b.transportUnionErrors, b.transportUnions, seen)
}

// unionTypes returns the public unions in name order.
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

// transportUnionTypes returns the HTTP decoding unions in name order.
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
	pkg *codegen.GeneratedPackage,
	helpers map[codegen.UnionTypeID]*codegen.NameDeclaration,
	unions map[codegen.UnionTypeID]*unionTypeData,
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
		collectUnionSumTypes(dt.Attribute(), scope, pkg, helpers, unions, seen)
	case *goaexpr.Object:
		for _, nat := range *dt {
			if nat == nil {
				continue
			}
			collectUnionSumTypes(nat.Attribute, scope, pkg, helpers, unions, seen)
		}
	case *goaexpr.Array:
		collectUnionSumTypes(dt.ElemType, scope, pkg, helpers, unions, seen)
	case *goaexpr.Map:
		collectUnionSumTypes(dt.KeyType, scope, pkg, helpers, unions, seen)
		collectUnionSumTypes(dt.ElemType, scope, pkg, helpers, unions, seen)
	case *goaexpr.Union:
		identity := codegen.NewUnionTypeID(dt)
		if _, ok := unions[identity]; !ok {
			unions[identity] = buildUnionTypeData(dt, scope, pkg, helpers[identity])
		}
		for _, nat := range dt.Values {
			if nat == nil {
				continue
			}
			collectUnionSumTypes(nat.Attribute, scope, pkg, helpers, unions, seen)
		}
	}
}

func buildUnionTypeData(u *goaexpr.Union, scope *codegen.NameScope, pkg *codegen.GeneratedPackage, helper *codegen.NameDeclaration) *unionTypeData {
	declaration, err := pkg.Union(u)
	if err != nil {
		panic(err)
	}
	name := declaration.Declaration().Name()
	kindName := declaration.KindDeclaration().Name()

	fields := make([]*unionFieldData, 0, len(u.Values))
	for _, nat := range u.Values {
		if nat == nil || nat.Attribute == nil {
			continue
		}
		fieldName := codegen.Goify(nat.Name, true)
		var qualifier string
		if tloc := codegen.UserTypeLocation(nat.Attribute.Type); tloc != nil {
			qualifier = tloc.PackageName()
		}
		fieldType := scope.GoFullTypeRef(nat.Attribute, qualifier)
		branch, err := pkg.UnionBranch(u, nat.Name)
		if err != nil {
			panic(err)
		}
		fields = append(fields, &unionFieldData{
			Name:        nat.Name,
			KindConst:   branch.KindConst(),
			Constructor: branch.Constructor(),
			FieldName:   fieldName,
			FieldType:   fieldType,
			Nilable:     generatedTypeNilable(nat.Attribute.Type),
			JSONType:    generatedJSONType(nat.Attribute.Type),
			TypeTag:     nat.Name,
		})
	}

	return &unionTypeData{
		Name:               name,
		KindName:           kindName,
		DiscriminatorError: helper.Name(),
		Fields:             fields,
	}
}

// generatedTypeNilable reports whether the generated Go representation can be
// nil even though a tagged union branch always requires a value.
func generatedTypeNilable(dt goaexpr.DataType) bool {
	return goaexpr.IsObject(dt) ||
		goaexpr.IsArray(dt) ||
		goaexpr.IsMap(dt) ||
		dt.Kind() == goaexpr.BytesKind ||
		dt.Kind() == goaexpr.AnyKind
}
