// Package codegen reads the final names for unions and builds the data used to write
// each union type, branch constructor, and JSON function.
package codegen

import (
	"sort"

	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// collectUnionSumTypes saves every union used by att. Copies of the same OneOf
// field are written once in the public package.
func (b *toolSpecBuilder) collectUnionSumTypes(scope *codegen.NameScope, att *goaexpr.AttributeExpr) {
	if b == nil || scope == nil || att == nil {
		return
	}
	if b.unions == nil {
		b.unions = make(map[codegen.UnionDeclarationID]*unionTypeData)
	}
	seen := make(map[goaexpr.UserType]struct{})
	collectUnionSumTypes(att, scope, b.publicPackage, b.publicUnionErrors, b.unions, seen)
}

// collectTransportUnionSumTypes saves every union used by the HTTP decoding
// type. The generated HTTP package can then define those unions locally.
func (b *toolSpecBuilder) collectTransportUnionSumTypes(scope *codegen.NameScope, att *goaexpr.AttributeExpr) {
	if b == nil || scope == nil || att == nil {
		return
	}
	if b.transportUnions == nil {
		b.transportUnions = make(map[codegen.UnionDeclarationID]*unionTypeData)
	}
	seen := make(map[goaexpr.UserType]struct{})
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
	helpers map[codegen.UnionDeclarationID]*codegen.NameDeclaration,
	unions map[codegen.UnionDeclarationID]*unionTypeData,
	seen map[goaexpr.UserType]struct{},
) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		if dt == nil {
			return
		}
		origin := dt.Origin()
		if _, ok := seen[origin]; ok {
			return
		}
		seen[origin] = struct{}{}
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
		identity := codegen.NewUnionDeclarationID(att)
		if _, ok := unions[identity]; !ok {
			unions[identity] = buildUnionTypeData(att, scope, pkg, helpers[identity])
		}
		for _, nat := range dt.Values {
			if nat == nil {
				continue
			}
			collectUnionSumTypes(nat.Attribute, scope, pkg, helpers, unions, seen)
		}
	}
}

func buildUnionTypeData(attribute *goaexpr.AttributeExpr, scope *codegen.NameScope, pkg *codegen.GeneratedPackage, helper *codegen.NameDeclaration) *unionTypeData {
	union := attribute.Type.(*goaexpr.Union)
	declaration, err := pkg.Union(attribute)
	if err != nil {
		panic(err)
	}
	name := declaration.Declaration().Name()
	kindName := declaration.KindDeclaration().Name()
	context := codegen.NewAttributeContext(false, false, true, "", scope)

	fields := make([]*unionFieldData, 0, len(union.Values))
	for _, nat := range union.Values {
		if nat == nil || nat.Attribute == nil {
			continue
		}
		fieldName := codegen.Goify(nat.Name, true)
		fieldType := context.Scope.Ref(nat.Attribute, context.Pkg(nat.Attribute))
		branch, err := pkg.UnionBranch(attribute, nat.Name)
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
