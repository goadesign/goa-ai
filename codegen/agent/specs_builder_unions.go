// Package codegen reads the final names for unions and builds the data used to write
// each union type, branch constructor, and JSON function.
package codegen

import (
	"sort"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
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
	collectUnionSumTypes(att, scope, b.publicPackage, b.unions, seen)
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
	collectUnionSumTypes(att, scope, b.transportPackage, b.transportUnions, seen)
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
		return out[i].TypeDeclaration.Name() < out[j].TypeDeclaration.Name()
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
		return out[i].TypeDeclaration.Name() < out[j].TypeDeclaration.Name()
	})
	return out
}

func collectUnionSumTypes(
	att *goaexpr.AttributeExpr,
	scope *codegen.NameScope,
	pkg *codegen.GeneratedPackage,
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
		if codegen.UserTypeLocation(dt) != nil {
			return
		}
		origin := dt.Origin()
		if _, ok := seen[origin]; ok {
			return
		}
		seen[origin] = struct{}{}
		collectUnionSumTypes(dt.Attribute(), scope, pkg, unions, seen)
	case *goaexpr.Object:
		for _, nat := range *dt {
			if nat == nil {
				continue
			}
			collectUnionSumTypes(nat.Attribute, scope, pkg, unions, seen)
		}
	case *goaexpr.Array:
		collectUnionSumTypes(dt.ElemType, scope, pkg, unions, seen)
	case *goaexpr.Map:
		collectUnionSumTypes(dt.KeyType, scope, pkg, unions, seen)
		collectUnionSumTypes(dt.ElemType, scope, pkg, unions, seen)
	case *goaexpr.Union:
		identity := codegen.NewUnionDeclarationID(att)
		if _, ok := unions[identity]; !ok {
			unions[identity] = buildUnionTypeData(att, scope, pkg)
		}
		for _, nat := range dt.Values {
			if nat == nil {
				continue
			}
			collectUnionSumTypes(nat.Attribute, scope, pkg, unions, seen)
		}
	}
}

func buildUnionTypeData(attribute *goaexpr.AttributeExpr, scope *codegen.NameScope, pkg *codegen.GeneratedPackage) *unionTypeData {
	union := attribute.Type.(*goaexpr.Union)
	declaration, err := pkg.Union(attribute)
	if err != nil {
		panic(err)
	}
	context := codegen.NewAttributeContext(false, false, true, "", scope)
	storageNames := codegen.NewNameScope()
	storageNames.Unique("kind")

	fields := make([]*service.UnionFieldData, 0, len(union.Values))
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
		fields = append(fields, &service.UnionFieldData{
			Name:                   nat.Name,
			KindDeclaration:        branch.KindDeclaration(),
			ConstructorDeclaration: branch.ConstructorDeclaration(),
			FieldName:              fieldName,
			StorageName:            storageNames.Unique(codegen.Goify(nat.Name, false)),
			FieldType:              fieldType,
			Nilable:                codegen.IsNilable(nat.Attribute.Type),
			TypeTag:                nat.Name,
			JSONKind:               goaexpr.JSONKind(nat.Attribute.Type),
		})
	}

	return &unionTypeData{
		TypeDeclaration: declaration.Declaration(),
		KindDeclaration: declaration.KindDeclaration(),
		Fields:          fields,
		TypeKey:         union.GetTypeKey(),
		ValueKey:        union.GetValueKey(),
		Flatten:         union.Flatten,
		Untagged:        union.Untagged,
	}
}

// unionTypeSections renders each planned union with Goa's shared template. Tool
// and completion packages therefore use the same constructors and JSON mapping
// as the native service types they convert to and from.
func unionTypeSections(header *codegen.SectionTemplate, name string, unions []*unionTypeData) []*codegen.SectionTemplate {
	sections := make([]*codegen.SectionTemplate, 1, len(unions)+1)
	sections[0] = header
	for _, union := range unions {
		sections = append(sections, &codegen.SectionTemplate{
			Name: name, Source: service.UnionTypeSource, Data: union,
		})
	}
	return sections
}
