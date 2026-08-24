// Package codec plans the private JSON types shared by generated clients and servers.
package codec

import (
	"fmt"
	"path"
	"strings"

	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// Direction selects which conversion functions one generated value needs.
	Direction uint8

	// Plan records every JSON value written to one generated codec package.
	Plan struct {
		generation         *goacodegen.Generation
		pkg                *goacodegen.GeneratedPackage
		packageName        string
		serviceImportPath  string
		values             []*Value
		valuesByKey        map[string]*Value
		importPaths        map[string]struct{}
		locatedImportPaths map[string]struct{}
	}

	// Value records one service value and its private JSON representation.
	Value struct {
		plan              *Plan
		key               string
		preferredName     string
		direction         Direction
		service           *goaexpr.AttributeExpr
		transport         *goaexpr.AttributeExpr
		types             []*plannedType
		unions            []*plannedUnion
		decode            *goacodegen.TransformPlan
		encode            *goacodegen.TransformPlan
		decodeDeclaration *goacodegen.NameDeclaration
		encodeDeclaration *goacodegen.NameDeclaration
		constructor       *goacodegen.NameDeclaration
		serviceAttributor goacodegen.Attributor
	}

	// TransportField describes one top-level field in a private JSON type.
	// Generated adapters use these exact names and types when they already hold
	// parsed values and do not need to decode JSON.
	TransportField struct {
		// Selector is the generated Go field name.
		Selector string
		// TypeRef is the complete generated Go field type.
		TypeRef string
		// ValueTypeRef is the field type without its presence pointer.
		ValueTypeRef string
		// Pointer reports whether the field stores its value through a pointer.
		Pointer bool
		// ElementTypeRef is the generated element type for an array field.
		ElementTypeRef string
		// ElementPointer reports whether an array stores each value through a pointer.
		ElementPointer bool
	}

	// plannedType contains one generated transport declaration and its validator.
	plannedType struct {
		userType             goaexpr.UserType
		declaration          *goacodegen.NameDeclaration
		typeDeclaration      *goacodegen.TypeDeclaration
		validatorDeclaration *goacodegen.NameDeclaration
		layout               *goacodegen.GoTypePlan
		validation           *goacodegen.ValidationPlan
	}

	// plannedUnion contains one generated Goa OneOf declaration and its branches.
	plannedUnion struct {
		union       *goaexpr.Union
		declaration *goacodegen.UnionDeclaration
		branches    []*plannedUnionBranch
	}

	// plannedUnionBranch contains the type and generated names for one branch.
	plannedUnionBranch struct {
		name        string
		fieldName   string
		declaration *goacodegen.UnionBranchDeclaration
		layout      *goacodegen.GoTypePlan
	}

	// nameOrder gives colliding generated names a stable order.
	nameOrder struct {
		packagePath string
		key         string
	}
)

const (
	// EncodeOnly generates only the service-to-JSON conversion.
	EncodeOnly Direction = iota + 1
	// DecodeOnly generates only the JSON-to-service conversion.
	DecodeOnly
	// EncodeAndDecode generates both conversions.
	EncodeAndDecode
	// ConstructOnly generates a typed transport-to-service constructor without a raw JSON decoder.
	ConstructOnly
)

// NewPlan creates the plan for one generated service's private JSON package.
func NewPlan(generation *goacodegen.Generation, importPath, packageName, serviceImportPath string) (*Plan, error) {
	if generation == nil {
		return nil, fmt.Errorf("plan JSON codecs: generation must not be nil")
	}
	if importPath == "" {
		return nil, fmt.Errorf("plan JSON codecs: import path must not be empty")
	}
	if packageName == "" {
		return nil, fmt.Errorf("plan JSON codecs: package name must not be empty")
	}
	if serviceImportPath == "" {
		return nil, fmt.Errorf("plan JSON codecs: service import path must not be empty")
	}
	pkg, err := generation.ClaimPackage(importPath)
	if err != nil {
		return nil, fmt.Errorf("plan JSON codec package: %w", err)
	}
	return &Plan{
		generation:         generation,
		pkg:                pkg,
		packageName:        packageName,
		serviceImportPath:  serviceImportPath,
		valuesByKey:        make(map[string]*Value),
		importPaths:        make(map[string]struct{}),
		locatedImportPaths: make(map[string]struct{}),
	}, nil
}

// Add records one service value, its JSON type, validation, and conversions.
func (p *Plan) Add(
	key, preferredName string,
	attribute *goaexpr.AttributeExpr,
	direction Direction,
) (*Value, error) {
	if key == "" {
		return nil, fmt.Errorf("plan JSON value: key must not be empty")
	}
	if preferredName == "" {
		return nil, fmt.Errorf("plan JSON value %q: preferred name must not be empty", key)
	}
	if attribute == nil || attribute.Type == nil {
		return nil, fmt.Errorf("plan JSON value %q: service attribute must not be nil", key)
	}
	if !direction.valid() {
		return nil, fmt.Errorf(
			"plan JSON value %q: direction must select encoding, decoding, or typed construction",
			key,
		)
	}
	if p.valuesByKey[key] != nil {
		return nil, fmt.Errorf("JSON value key %q is already planned", key)
	}
	transport, localTypes := localTransportAttribute(attribute, key, preferredName)
	if err := p.requireValueImports(direction, attribute); err != nil {
		return nil, fmt.Errorf("plan JSON value %q imports: %w", key, err)
	}
	value := &Value{
		plan:          p,
		key:           key,
		preferredName: preferredName,
		direction:     direction,
		service:       attribute,
		transport:     transport,
	}
	if err := value.declareTypes(localTypes); err != nil {
		return nil, fmt.Errorf("plan JSON value %q types: %w", key, err)
	}
	if err := value.declareUnions(); err != nil {
		return nil, fmt.Errorf("plan JSON value %q unions: %w", key, err)
	}
	if err := value.planTypes(); err != nil {
		return nil, fmt.Errorf("plan JSON value %q layouts: %w", key, err)
	}
	if err := value.requireValidationImports(); err != nil {
		return nil, fmt.Errorf("plan JSON value %q validation imports: %w", key, err)
	}
	if err := value.planTransforms(); err != nil {
		return nil, fmt.Errorf("plan JSON value %q conversions: %w", key, err)
	}
	if err := p.recordLocatedImports(attribute); err != nil {
		return nil, fmt.Errorf("plan JSON value %q imports: %w", key, err)
	}
	p.values = append(p.values, value)
	p.valuesByKey[key] = value
	return value, nil
}

// EncodeDeclaration returns the function that turns a service value into JSON.
func (v *Value) EncodeDeclaration() *goacodegen.NameDeclaration {
	return v.encodeDeclaration
}

// DecodeDeclaration returns the function that turns JSON into a service value.
func (v *Value) DecodeDeclaration() *goacodegen.NameDeclaration {
	return v.decodeDeclaration
}

// PlanTransportConstructor adds a function that validates a private transport
// value and converts it to the service type. Generated adapters call this when
// they have already parsed the incoming fields.
func (v *Value) PlanTransportConstructor() error {
	if v.constructor != nil {
		return fmt.Errorf("plan JSON value %q transport constructor: constructor is already planned", v.key)
	}
	if err := v.planDecodeTransform(); err != nil {
		return fmt.Errorf("plan JSON value %q transport constructor conversion: %w", v.key, err)
	}
	v.constructor = goacodegen.NewPreferredName(
		goacodegen.NameFunction,
		"New"+v.preferredName,
		goacodegen.ExportedName,
		nameOrder{packagePath: v.plan.pkg.ImportPath(), key: v.key + ":constructor"},
	)
	if err := v.plan.pkg.DeclareName(v.constructor); err != nil {
		return fmt.Errorf("plan JSON value %q transport constructor: %w", v.key, err)
	}
	return nil
}

// TransportConstructorDeclaration returns the planned transport constructor.
// It returns nil when the caller did not request one.
func (v *Value) TransportConstructorDeclaration() *goacodegen.NameDeclaration {
	return v.constructor
}

// TransportTypeName returns the final private transport declaration name as
// referenced from outputPath.
func (v *Value) TransportTypeName(outputPath string, qualifier goacodegen.GoTypeQualifier) (string, error) {
	if !v.plan.generation.Frozen() {
		return "", fmt.Errorf("link JSON value %q transport type before generation freeze", v.key)
	}
	top := v.types[0]
	name := top.typeDeclaration.Name()
	if outputPath != v.plan.pkg.ImportPath() {
		name = qualifier(v.plan.pkg.ImportPath()) + "." + name
	}
	return name, nil
}

// TransportField returns the final private field built from attribute and
// designName. Both values are checked so a reused attribute cannot select the
// wrong field.
func (v *Value) TransportField(
	attribute *goaexpr.AttributeExpr,
	designName string,
	outputPath string,
	qualifier goacodegen.GoTypeQualifier,
) (*TransportField, error) {
	if !v.plan.generation.Frozen() {
		return nil, fmt.Errorf("link JSON value %q transport field before generation freeze", v.key)
	}
	if attribute == nil {
		return nil, fmt.Errorf("link JSON value %q transport field %q: attribute must not be nil", v.key, designName)
	}
	top := v.types[0]
	object := goaexpr.AsObject(top.userType.Attribute().Type)
	if object == nil || top.layout.Kind() != goacodegen.GoStruct {
		return nil, fmt.Errorf("link JSON value %q transport field %q: transport value is not an object", v.key, designName)
	}
	fields := top.layout.Fields()
	var selected *goacodegen.GoTypePlan
	for index, named := range *object {
		if named.Name != designName || named.Attribute.AuthoredAttribute() != attribute.AuthoredAttribute() {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("link JSON value %q transport field %q: field occurs more than once", v.key, designName)
		}
		selected = fields[index]
	}
	if selected == nil {
		return nil, fmt.Errorf("link JSON value %q transport field %q: field was not planned", v.key, designName)
	}
	linked := top.layout.Link(outputPath, qualifier).Enter(selected)
	typeRef := linked.Def()
	if selected.IsPointer() {
		typeRef = "*" + typeRef
	}
	field := &TransportField{
		Selector:     selected.FieldName(true),
		TypeRef:      typeRef,
		ValueTypeRef: linked.Def(),
		Pointer:      selected.IsPointer(),
	}
	if selected.Kind() == goacodegen.GoArray {
		array := goaexpr.AsArray((*object).Attribute(designName).Type)
		field.ElementTypeRef = linked.Enter(selected.Elem()).Def()
		field.ElementPointer = goaexpr.IsObject(array.ElemType.Type) ||
			transportArrayElementIsPointer(array)
	}
	return field, nil
}

// BindService supplies the exact Goa service type writer after names are final.
func (v *Value) BindService(attributor goacodegen.Attributor) error {
	if attributor == nil {
		return fmt.Errorf("bind JSON value %q: service type writer must not be nil", v.key)
	}
	if v.serviceAttributor != nil {
		return fmt.Errorf("bind JSON value %q: service type writer is already bound", v.key)
	}
	v.serviceAttributor = attributor
	return nil
}

// ComparePackageName orders names by their generated package and stable value key.
func (o nameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(nameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.key, right.key)
}

// valid reports whether the caller selected one supported generated direction.
func (d Direction) valid() bool {
	return d == EncodeOnly || d == DecodeOnly || d == EncodeAndDecode || d == ConstructOnly
}

// encodes reports whether this value needs a service-to-JSON conversion.
func (d Direction) encodes() bool {
	return d == EncodeOnly || d == EncodeAndDecode
}

// decodes reports whether this value needs a JSON-to-service conversion.
func (d Direction) decodes() bool {
	return d == DecodeOnly || d == EncodeAndDecode
}

// declareTypes records the package-level type and validation names.
func (v *Value) declareTypes(localTypes []goaexpr.UserType) error {
	for index, userType := range localTypes {
		typeDeclaration, err := v.plan.pkg.DeclareGeneratedType(
			userType.Name(),
			nameOrder{
				packagePath: v.plan.pkg.ImportPath(),
				key:         fmt.Sprintf("%s:type:%s:%06d", v.key, userType.Name(), index),
			},
		)
		if err != nil {
			return err
		}
		declaration := typeDeclaration.Declaration()
		validator, err := v.plan.pkg.DeclareDependentName(
			goacodegen.NameFunction,
			declaration,
			"Validate",
			"",
			nameOrder{packagePath: v.plan.pkg.ImportPath(), key: fmt.Sprintf("%s:validator:%06d", v.key, index)},
		)
		if err != nil {
			return err
		}
		v.types = append(v.types, &plannedType{
			userType:             userType,
			declaration:          declaration,
			typeDeclaration:      typeDeclaration,
			validatorDeclaration: validator,
		})
	}
	return nil
}

// declareUnions records every union found in the copied transport types.
func (v *Value) declareUnions() error {
	seen := make(map[goacodegen.UnionTypeID]struct{})
	return walkAttribute(v.transport, make(map[goaexpr.UserType]struct{}), func(attribute *goaexpr.AttributeExpr) error {
		union, ok := attribute.Type.(*goaexpr.Union)
		if !ok {
			return nil
		}
		identity := goacodegen.NewUnionTypeID(union)
		if _, exists := seen[identity]; exists {
			return nil
		}
		seen[identity] = struct{}{}
		declaration, err := v.plan.pkg.DeclareUnion(union)
		if err != nil {
			return err
		}
		v.unions = append(v.unions, &plannedUnion{union: union, declaration: declaration})
		return nil
	})
}

// planTypes copies all pointer, field, validation, and union branch choices.
func (v *Value) planTypes() error {
	typesByOrigin := make(map[goaexpr.UserType]*plannedType, len(v.types))
	for _, planned := range v.types {
		typesByOrigin[planned.userType.Origin()] = planned
	}
	binder := func(request goacodegen.GoTypeBindingRequest) (goacodegen.GoTypeBinding, error) {
		switch request.Kind {
		case goacodegen.GoNamed:
			userType := request.Attribute.Type.(goaexpr.UserType)
			planned := typesByOrigin[userType.Origin()]
			if planned == nil {
				return goacodegen.GoTypeBinding{}, fmt.Errorf("transport type %q has no declaration", userType.Name())
			}
			return goacodegen.GoTypeBinding{Owner: v.plan.pkg.ImportPath(), Type: planned.typeDeclaration}, nil
		case goacodegen.GoUnion:
			declaration, err := v.plan.pkg.Union(request.Attribute.Type.(*goaexpr.Union))
			if err != nil {
				return goacodegen.GoTypeBinding{}, err
			}
			return goacodegen.GoTypeBinding{Owner: v.plan.pkg.ImportPath(), Union: declaration}, nil
		default:
			return goacodegen.GoTypeBinding{}, fmt.Errorf("unsupported transport type kind %s", request.Kind)
		}
	}
	policy := transportPolicy()
	for _, planned := range v.types {
		layout, err := goacodegen.PlanGoType(planned.userType.Attribute(), goacodegen.GoTypePlanOptions{
			Owner:  v.plan.pkg.ImportPath(),
			Policy: policy,
			Bind:   binder,
		})
		if err != nil {
			return err
		}
		validation, err := goacodegen.NewValidationPlan(
			planned.userType.Attribute(),
			layout,
			goacodegen.ValidationPlanOptions{
				Required: true,
				Alias:    goaexpr.IsAlias(planned.userType),
				Bind: func(request goacodegen.ValidatorBindingRequest) (*goacodegen.NameDeclaration, error) {
					userType := request.Attribute.Type.(goaexpr.UserType)
					nested := typesByOrigin[userType.Origin()]
					if nested == nil {
						return nil, fmt.Errorf("transport validator for %q has no declaration", userType.Name())
					}
					return nested.validatorDeclaration, nil
				},
			},
		)
		if err != nil {
			return err
		}
		planned.layout = layout
		planned.validation = validation
	}
	for _, union := range v.unions {
		for _, branch := range union.union.Values {
			declaration, err := v.plan.pkg.UnionBranch(union.union, branch.Name)
			if err != nil {
				return err
			}
			layout, err := goacodegen.PlanGoType(branch.Attribute, goacodegen.GoTypePlanOptions{
				Owner:  v.plan.pkg.ImportPath(),
				Policy: policy,
				Bind:   binder,
			})
			if err != nil {
				return err
			}
			union.branches = append(union.branches, &plannedUnionBranch{
				name:        branch.Name,
				fieldName:   goacodegen.Goify(branch.Name, true),
				declaration: declaration,
				layout:      layout,
			})
		}
	}
	return nil
}

// planTransforms records every recursive helper before Goa fixes package names.
func (v *Value) planTransforms() error {
	if v.direction.decodes() {
		if err := v.planDecodeTransform(); err != nil {
			return err
		}
		v.decodeDeclaration = goacodegen.NewPreferredName(
			goacodegen.NameFunction,
			"Decode"+v.preferredName,
			goacodegen.ExportedName,
			nameOrder{packagePath: v.plan.pkg.ImportPath(), key: v.key + ":decode"},
		)
		if err := v.plan.pkg.DeclareName(v.decodeDeclaration); err != nil {
			return err
		}
	}
	if v.direction.encodes() {
		encode, err := goacodegen.NewTransformPlan(v.service, v.transport, "encode", nil)
		if err != nil {
			return err
		}
		if err := v.bindTransformHelpers("encode", encode); err != nil {
			return err
		}
		v.encode = encode
		v.encodeDeclaration = goacodegen.NewPreferredName(
			goacodegen.NameFunction,
			"Encode"+v.preferredName,
			goacodegen.ExportedName,
			nameOrder{packagePath: v.plan.pkg.ImportPath(), key: v.key + ":encode"},
		)
		if err := v.plan.pkg.DeclareName(v.encodeDeclaration); err != nil {
			return err
		}
	}
	return nil
}

// planDecodeTransform records the transport-to-service conversion once for a
// raw decoder, a typed constructor, or both.
func (v *Value) planDecodeTransform() error {
	if v.decode != nil {
		return nil
	}
	decode, err := goacodegen.NewTransformPlan(v.transport, v.service, "decode", nil)
	if err != nil {
		return err
	}
	if err := v.bindTransformHelpers("decode", decode); err != nil {
		return err
	}
	v.decode = decode
	return nil
}

// bindTransformHelpers gives every recursive conversion one exact function name.
func (v *Value) bindTransformHelpers(prefix string, transform *goacodegen.TransformPlan) error {
	for _, helper := range transform.Helpers() {
		declaration := goacodegen.NewPreferredName(
			goacodegen.NameFunction,
			prefix+goacodegen.Goify(helper.Source.Type.Name(), true)+"To"+goacodegen.Goify(helper.Target.Type.Name(), true),
			goacodegen.UnexportedName,
			nameOrder{
				packagePath: v.plan.pkg.ImportPath(),
				key:         fmt.Sprintf("%s:%s-helper:%06d", v.key, prefix, helper.Occurrence),
			},
		)
		if err := v.plan.pkg.DeclareName(declaration); err != nil {
			return err
		}
		if err := transform.BindHelperDeclaration(helper.ID, declaration); err != nil {
			return err
		}
	}
	return nil
}

// requireValueImports reserves only the package names used by this value's
// generated conversion and validation code.
func (p *Plan) requireValueImports(
	direction Direction,
	service *goaexpr.AttributeExpr,
) error {
	if err := p.requireImport(goacodegen.NewImport("fmt", "fmt")); err != nil {
		return err
	}
	if direction.encodes() || direction.decodes() {
		if err := p.requireImport(goacodegen.NewImport("json", "encoding/json")); err != nil {
			return err
		}
	}
	if direction.decodes() {
		for _, spec := range []*goacodegen.ImportSpec{
			goacodegen.NewImport("bytes", "bytes"),
			goacodegen.NewImport("io", "io"),
		} {
			if err := p.requireImport(spec); err != nil {
				return err
			}
		}
	}
	usesService, err := serviceUsesPackage(service, p.serviceImportPath, p.generation.GenPkg())
	if err != nil {
		return err
	}
	if usesService {
		spec := goacodegen.NewImport(
			strings.ToLower(goacodegen.Goify(path.Base(p.serviceImportPath), false)),
			p.serviceImportPath,
		)
		if err := p.pkg.ReserveGeneratedImport(spec); err != nil {
			return fmt.Errorf("plan JSON service import: %w", err)
		}
		p.importPaths[spec.Path] = struct{}{}
	}
	return nil
}

// requireValidationImports reserves the packages used by the validation plans
// after Goa has determined the exact checks that it will write.
func (v *Value) requireValidationImports() error {
	// Every private transport declaration is a pointer. Its validator reports
	// a missing JSON value through Goa's error package.
	if err := v.plan.requireImport(goacodegen.GoaImport("")); err != nil {
		return err
	}
	for _, planned := range v.types {
		for _, goImport := range planned.validation.ImportPreferences() {
			if goImport.Path == v.plan.pkg.ImportPath() {
				continue
			}
			if err := v.plan.requireImport(goacodegen.NewImport(goImport.Name, goImport.Path)); err != nil {
				return err
			}
		}
	}
	return nil
}

// requireImport reserves one package once so every generated fragment uses the
// same name for it.
func (p *Plan) requireImport(spec *goacodegen.ImportSpec) error {
	if _, exists := p.importPaths[spec.Path]; exists {
		return nil
	}
	if err := p.pkg.RequireImport(spec); err != nil {
		return fmt.Errorf("plan JSON codec import %q: %w", spec.Path, err)
	}
	p.importPaths[spec.Path] = struct{}{}
	return nil
}

// serviceUsesPackage decides at generation time whether the codec file needs
// the generated service package. The bound Goa service resolver still writes
// every service type reference.
func serviceUsesPackage(attribute *goaexpr.AttributeExpr, serviceImportPath, genpkg string) (bool, error) {
	uses := false
	err := walkAttribute(attribute, make(map[goaexpr.UserType]struct{}), func(current *goaexpr.AttributeExpr) error {
		if uses {
			return nil
		}
		userType, named := current.Type.(goaexpr.UserType)
		if !named || userType == goaexpr.Empty {
			return nil
		}
		location := goacodegen.UserTypeLocation(userType)
		uses = location == nil || path.Join(genpkg, location.RelImportPath) == serviceImportPath
		return nil
	})
	return uses, err
}

// recordLocatedImports reserves generated locations and custom Go field types
// referenced by the service value.
func (p *Plan) recordLocatedImports(attribute *goaexpr.AttributeExpr) error {
	return walkAttribute(attribute, make(map[goaexpr.UserType]struct{}), func(current *goaexpr.AttributeExpr) error {
		location := goacodegen.UserTypeLocation(current.Type)
		if location != nil {
			importPath := path.Join(p.generation.GenPkg(), location.RelImportPath)
			spec := goacodegen.NewImport(
				strings.ToLower(goacodegen.Goify(path.Base(importPath), false)), importPath,
			)
			if err := p.pkg.ReserveGeneratedImport(spec); err != nil {
				return err
			}
			p.locatedImportPaths[importPath] = struct{}{}
		}
		if _, spec := goacodegen.GetMetaType(current); spec != nil {
			if err := p.pkg.DeclareImport(goacodegen.NewImport(spec.Name, spec.Path)); err != nil {
				return err
			}
			p.locatedImportPaths[spec.Path] = struct{}{}
		}
		return nil
	})
}

// transportPolicy preserves JSON presence until validation has completed.
func transportPolicy() goacodegen.GoLayoutPolicy {
	return goacodegen.GoLayoutPolicy{
		Pointer:             true,
		UnionPointer:        true,
		ArrayElementPointer: true,
		SumType:             true,
	}
}

// transportArrayElementIsPointer applies the same null-presence rule used by
// the private transport type planner for primitive array elements.
func transportArrayElementIsPointer(array *goaexpr.Array) bool {
	return array.NonNullableElems && goaexpr.IsPrimitive(array.ElemType.Type) &&
		!goacodegen.IsNilable(array.ElemType.Type)
}
