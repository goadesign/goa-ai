// Package codec shares complete-original transport definitions within one output
// package. Occurrence attributes remain separate; named definitions are keyed by
// the actual Goa declaration, including its original owner.
package codec

import (
	"fmt"
	"maps"
	"path"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type originalTransportGraph struct {
	types         map[*codegen.TypeDeclaration]*plannedType
	typesByLocal  map[expr.UserType]*plannedType
	unions        map[*codegen.UnionDeclaration]*plannedUnion
	unionsByLocal map[*expr.Union]*plannedUnion
}

// addOriginal shares only complete-original definitions. Ordinary protocol Add
// continues to localize each payload/result independently.
func (p *Plan) addOriginal(key, preferredName string, attribute *expr.AttributeExpr, layout *codegen.GoTypePlan) (*Value, error) {
	if key == "" || preferredName == "" {
		return nil, fmt.Errorf("standalone JSON value requires a key and preferred name")
	}
	if p.valuesByKey[key] != nil {
		return nil, fmt.Errorf("JSON value key %q is already planned", key)
	}
	if layout == nil || layout.TypeDeclaration() == nil {
		return nil, fmt.Errorf("standalone JSON value %q requires an original type declaration", key)
	}
	if p.originals == nil {
		p.originals = &originalTransportGraph{
			types:         make(map[*codegen.TypeDeclaration]*plannedType),
			typesByLocal:  make(map[expr.UserType]*plannedType),
			unions:        make(map[*codegen.UnionDeclaration]*plannedUnion),
			unionsByLocal: make(map[*expr.Union]*plannedUnion),
		}
	}
	transport, err := p.copyOriginalTransport(attribute, layout.Owner())
	if err != nil {
		return nil, err
	}
	value := &Value{
		plan: p, key: key, preferredName: preferredName, direction: EncodeAndDecode,
		service: attribute, transport: transport, originalLayout: layout,
	}
	seenTypes := make(map[*plannedType]bool)
	seenUnions := make(map[*plannedUnion]bool)
	if err := walkAttribute(transport, make(map[expr.UserType]struct{}), func(current *expr.AttributeExpr) error {
		switch actual := current.Type.(type) {
		case expr.UserType:
			if actual == expr.Empty {
				return nil
			}
			planned := p.originals.typesByLocal[actual.Origin()]
			if !seenTypes[planned] {
				seenTypes[planned] = true
				value.types = append(value.types, planned)
			}
		case *expr.Union:
			planned := p.originals.unionsByLocal[actual]
			if !seenUnions[planned] {
				seenUnions[planned] = true
				value.unions = append(value.unions, planned)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := p.requireValueImports(EncodeAndDecode, attribute); err != nil {
		return nil, err
	}
	if err := value.planTypes(); err != nil {
		return nil, err
	}
	if err := value.requireValidationImports(); err != nil {
		return nil, err
	}
	if err := value.planTransforms(); err != nil {
		return nil, err
	}
	for _, imp := range layout.CompleteImportPreferences() {
		if imp.Path == p.pkg.ImportPath() {
			continue
		}
		if err := p.pkg.ReserveGeneratedImport(codegen.NewImport(imp.Name, imp.Path)); err != nil {
			return nil, err
		}
		p.locatedImportPaths[imp.Path] = struct{}{}
	}
	p.values = append(p.values, value)
	p.valuesByKey[key] = value
	return value, nil
}

// copyOriginalTransport copies each occurrence without deep-copying named
// subgraphs. Registering the named shell before descent closes recursive graphs.
func (p *Plan) copyOriginalTransport(attribute *expr.AttributeExpr, owner string) (*expr.AttributeExpr, error) {
	copied := *attribute
	copied.Meta = maps.Clone(attribute.Meta)
	stripPackageMetadata(&copied)
	switch actual := attribute.Type.(type) {
	case expr.UserType:
		if actual == expr.Empty {
			return &copied, nil
		}
		if location := codegen.UserTypeLocation(actual); location != nil {
			owner = path.Join(p.generation.GenPkg(), location.RelImportPath)
		}
		source, err := p.generation.Package(owner).Type(actual)
		if err != nil {
			return nil, err
		}
		if planned := p.originals.types[source]; planned != nil {
			copied.Type = planned.userType
			return &copied, nil
		}
		local := &expr.UserTypeExpr{
			TypeName: actual.Name() + "Transport",
			UID:      "goa-ai-json:original:" + source.PackagePath() + ":" + actual.Origin().ID(),
		}
		name, err := p.pkg.DeclareDependentName(codegen.NameType, source.Declaration(),
			"json", "Transport", nameOrder{packagePath: p.pkg.ImportPath(), key: local.UID + ":type"})
		if err != nil {
			return nil, err
		}
		validator, err := p.pkg.DeclareDependentName(codegen.NameFunction, name,
			"validate", "", nameOrder{packagePath: p.pkg.ImportPath(), key: local.UID + ":validator"})
		if err != nil {
			return nil, err
		}
		planned := &plannedType{
			userType: local, declaration: name, validatorDeclaration: validator,
			alias: expr.IsUnion(actual),
		}
		p.originals.types[source] = planned
		p.originals.typesByLocal[local] = planned
		local.AttributeExpr, err = p.copyOriginalTransport(actual.Attribute(), owner)
		if err != nil {
			return nil, err
		}
		copied.Type = local
	case *expr.Object:
		fields := make(expr.Object, 0, len(*actual))
		for _, field := range *actual {
			child, err := p.copyOriginalTransport(field.Attribute, owner)
			if err != nil {
				return nil, err
			}
			if child.Meta == nil {
				child.Meta = make(expr.MetaExpr)
			}
			delete(child.Meta, "struct:tag:json")
			child.Meta["struct:tag:json:name"] = []string{field.Name}
			fields = append(fields, &expr.NamedAttributeExpr{Name: field.Name, Attribute: child})
		}
		copied.Type = &fields
	case *expr.Array:
		array := *actual
		var err error
		array.ElemType, err = p.copyOriginalTransport(actual.ElemType, owner)
		if err != nil {
			return nil, err
		}
		copied.Type = &array
	case *expr.Map:
		mapping := *actual
		var err error
		mapping.KeyType, err = p.copyOriginalTransport(actual.KeyType, owner)
		if err != nil {
			return nil, err
		}
		mapping.ElemType, err = p.copyOriginalTransport(actual.ElemType, owner)
		if err != nil {
			return nil, err
		}
		copied.Type = &mapping
	case *expr.Union:
		source, err := p.generation.Package(owner).Union(attribute)
		if err != nil {
			return nil, err
		}
		if planned := p.originals.unions[source]; planned != nil {
			copied.Type = planned.attribute.Type
			return &copied, nil
		}
		union := *actual
		union.Values = nil
		copied.Type = &union
		key := "original-union:" + owner + ":" + actual.Name()
		order := nameOrder{packagePath: p.pkg.ImportPath(), key: key}
		name, err := p.pkg.DeclareDependentName(codegen.NameType, source.Declaration(), "json", "Transport", order)
		if err != nil {
			return nil, err
		}
		order.key += ":kind"
		kind, err := p.pkg.DeclareDependentName(codegen.NameType, name, "", "Kind", order)
		if err != nil {
			return nil, err
		}
		planned := &plannedUnion{key: key, attribute: &copied, name: name, kind: kind}
		p.originals.unions[source] = planned
		p.originals.unionsByLocal[&union] = planned
		for _, branch := range actual.Values {
			child, err := p.copyOriginalTransport(branch.Attribute, owner)
			if err != nil {
				return nil, err
			}
			union.Values = append(union.Values, &expr.NamedAttributeExpr{Name: branch.Name, Attribute: child})
		}
	}
	return &copied, nil
}

// declarePrivateUnionBranch plans package-level symbols without adding public
// constructors or rebinding the original union's catalog entry.
func (p *Plan) declarePrivateUnionBranch(union *plannedUnion, branch string) (*codegen.NameDeclaration, *codegen.NameDeclaration, error) {
	order := nameOrder{packagePath: p.pkg.ImportPath(), key: union.key + ":branch:" + branch}
	kind, err := p.pkg.DeclareDependentName(codegen.NameConstant, union.kind, "", codegen.Goify(branch, true), order)
	if err != nil {
		return nil, nil, err
	}
	order.key += ":constructor"
	constructor, err := p.pkg.DeclareDependentName(codegen.NameFunction, union.name, "new", codegen.Goify(branch, true), order)
	return kind, constructor, err
}
