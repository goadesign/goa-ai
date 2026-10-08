// Package codec plans the private JSON types shared by generated clients and servers.
package codec

import (
	"fmt"
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
		values             []*Value
		valuesByKey        map[string]*Value
		importPaths        map[string]struct{}
		locatedImportPaths map[string]struct{}
		originals          *originalTransportGraph
		jsonHelpers        *jsonHelperPlan
	}

	// Value records one service value and its private JSON representation.
	Value struct {
		plan                *Plan
		key                 string
		preferredName       string
		direction           Direction
		service             *goaexpr.AttributeExpr
		serviceLayout       *goacodegen.GoTypePlan
		transport           *goaexpr.AttributeExpr
		transportLayout     *goacodegen.GoTypePlan
		types               []*plannedType
		unions              []*plannedUnion
		decode              *goacodegen.TransformPlan
		encode              *goacodegen.TransformPlan
		decodeDeclaration   *goacodegen.NameDeclaration
		validateDeclaration *goacodegen.NameDeclaration
		encodeDeclaration   *goacodegen.NameDeclaration
		constructor         *goacodegen.NameDeclaration
		serviceAttributor   goacodegen.Attributor
		standalone          *standalonePlan
		originalLayout      *goacodegen.GoTypePlan
		elicitation         *elicitationCodec
	}

	// TransportField describes one uniquely retained field in a private JSON type.
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
		// Default retains Goa-rendered declarations and the expression for an authored default.
		Default *goacodegen.GoValueCode
		// KeyTypeRef is the generated key type for a map field.
		KeyTypeRef string
		// ElementTypeRef is the generated element type for an array or map field.
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
		alias                bool
		layout               *goacodegen.GoTypePlan
		parameter            *goacodegen.GoTypePlan
		validation           *goacodegen.ValidationPlan
		integerDecode        bool
	}

	// plannedUnion contains one generated Goa OneOf declaration and its branches.
	plannedUnion struct {
		key       string
		attribute *goaexpr.AttributeExpr
		name      *goacodegen.NameDeclaration
		kind      *goacodegen.NameDeclaration
		branches  []*plannedUnionBranch
	}

	// plannedUnionBranch contains the type and generated names for one branch.
	plannedUnionBranch struct {
		name        string
		fieldName   string
		kind        *goacodegen.NameDeclaration
		constructor *goacodegen.NameDeclaration
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
	// ValidateOnly checks a typed service value without encoding it as JSON.
	ValidateOnly
)

// NewPlan creates a codec plan for one output package. Original-value codecs may
// use the original owner's package; Files receives its authoritative Go name.
func NewPlan(generation *goacodegen.Generation, importPath string) (*Plan, error) {
	if generation == nil {
		return nil, fmt.Errorf("plan JSON codecs: generation must not be nil")
	}
	if importPath == "" {
		return nil, fmt.Errorf("plan JSON codecs: import path must not be empty")
	}
	pkg, err := generation.ClaimPackage(importPath)
	if err != nil {
		return nil, fmt.Errorf("plan JSON codec package: %w", err)
	}
	return &Plan{
		generation:         generation,
		pkg:                pkg,
		valuesByKey:        make(map[string]*Value),
		importPaths:        make(map[string]struct{}),
		locatedImportPaths: make(map[string]struct{}),
	}, nil
}

// Add records one service value, its JSON type, validation, and conversions.
func (p *Plan) Add(
	key, preferredName string,
	attribute *goaexpr.AttributeExpr,
	layout *goacodegen.GoTypePlan,
	direction Direction,
) (*Value, error) {
	return p.add(key, preferredName, attribute, layout, direction, nil)
}

// add records a value after the caller has selected its external JSON contract.
// Ordinary tool values stay closed; elicitation answers use MCP's open result
// fields and validate the complete response before typed decoding.
func (p *Plan) add(key, preferredName string, attribute *goaexpr.AttributeExpr, layout *goacodegen.GoTypePlan, direction Direction, elicitation *elicitationCodec) (*Value, error) {
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
			"plan JSON value %q: direction must select encoding, decoding, typed construction, or typed validation",
			key,
		)
	}
	if p.valuesByKey[key] != nil {
		return nil, fmt.Errorf("JSON value key %q is already planned", key)
	}
	if layout == nil {
		return nil, fmt.Errorf("plan JSON value %q: service layout must not be nil", key)
	}
	transport, localTypes := localTransportAttribute(attribute, key, preferredName)
	if direction.decodes() {
		localTypes = append(localTypes, integerTransportFields(transport, key, preferredName)...)
	}
	if elicitation != nil {
		choice := goaexpr.AsUnion(transport.Type)
		choice.TypeKey = "action"
		choice.Flatten = true
		if elicitation.form {
			// MCP fixes the accepted-answer envelope's content name. The fields
			// inside that content retain their authored JSON names and constraints.
			for _, branch := range choice.Values {
				if branch.Name != "accept" {
					continue
				}
				content := goaexpr.AsObject(branch.Attribute.Type).Attribute("content")
				delete(content.Meta, "struct:tag:json")
				content.Meta["struct:tag:json:name"] = []string{"content"}
			}
		}
	}
	if err := p.requireValueImports(direction, layout); err != nil {
		return nil, fmt.Errorf("plan JSON value %q imports: %w", key, err)
	}
	value := &Value{
		plan:          p,
		key:           key,
		preferredName: preferredName,
		direction:     direction,
		service:       attribute,
		serviceLayout: layout,
		transport:     transport,
		elicitation:   elicitation,
	}
	if err := value.declareTypes(localTypes); err != nil {
		return nil, fmt.Errorf("plan JSON value %q types: %w", key, err)
	}
	if err := value.planIntegerJSON(); err != nil {
		return nil, err
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
	p.values = append(p.values, value)
	p.valuesByKey[key] = value
	return value, nil
}

// EncodeDeclaration returns the function that turns a service value into JSON.
func (v *Value) EncodeDeclaration() *goacodegen.NameDeclaration {
	return v.encodeDeclaration
}

// ValidationDeclaration returns the function that checks a service value.
// It returns nil when no typed service-value validator was planned.
func (v *Value) ValidationDeclaration() *goacodegen.NameDeclaration {
	return v.validateDeclaration
}

// PlanValidation adds a typed result check to a value that already converts to
// its private transport type. Callers use it when they do not need JSON bytes.
func (v *Value) PlanValidation() error {
	if v.validateDeclaration != nil {
		return fmt.Errorf("validation for %q is already planned", v.key)
	}
	if v.encode == nil {
		return fmt.Errorf("validation for %q requires a service-to-transport conversion", v.key)
	}
	v.validateDeclaration = goacodegen.NewPreferredName(
		goacodegen.NameFunction, "Validate"+v.preferredName+"Value", goacodegen.ExportedName,
		nameOrder{packagePath: v.plan.pkg.ImportPath(), key: v.key + ":validate"},
	)
	return v.plan.pkg.DeclareName(v.validateDeclaration)
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
	name := top.declaration.Name()
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
	var owner *plannedType
	var selected *goacodegen.GoTypePlan
	var transportAttribute *goaexpr.AttributeExpr
	for _, planned := range v.types {
		object := goaexpr.AsObject(planned.userType.Attribute().Type)
		if object == nil || planned.layout.Kind() != goacodegen.GoStruct {
			continue
		}
		fields := planned.layout.Fields()
		for index, named := range *object {
			if named.Name != designName || named.Attribute.AuthoredAttribute() != attribute.AuthoredAttribute() {
				continue
			}
			if selected != nil {
				return nil, fmt.Errorf("link JSON value %q transport field %q: field occurs more than once", v.key, designName)
			}
			owner, selected, transportAttribute = planned, fields[index], named.Attribute
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("link JSON value %q transport field %q: field was not planned", v.key, designName)
	}
	linked := owner.layout.Link(outputPath, qualifier).Enter(selected)
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
	if value := goaexpr.NewMappedAttributeExpr(owner.userType.Attribute()).GetDefault(designName); value != nil {
		rendered, err := goacodegen.RenderGoValue(transportAttribute, value, linked, selected.IsPointer(), func(attribute *goaexpr.AttributeExpr, branch string) (string, error) {
			declaration, err := v.plan.pkg.UnionBranch(attribute, branch)
			if err != nil {
				return "", err
			}
			name := declaration.Constructor()
			if outputPath != v.plan.pkg.ImportPath() {
				name = qualifier(v.plan.pkg.ImportPath()) + "." + name
			}
			return name, nil
		}, designName+"Default")
		if err != nil {
			return nil, fmt.Errorf("render transport default for %q: %w", designName, err)
		}
		field.Default = &rendered
	}
	if selected.Kind() == goacodegen.GoArray {
		array := goaexpr.AsArray(attribute.Type)
		field.ElementTypeRef = linked.Enter(selected.Elem()).Def()
		field.ElementPointer = goaexpr.IsObject(array.ElemType.Type) ||
			transportArrayElementIsPointer(array)
	}
	if selected.Kind() == goacodegen.GoMap {
		field.KeyTypeRef = linked.Enter(selected.Key()).Def()
		field.ElementTypeRef = linked.Enter(selected.Elem()).Def()
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
	return d == EncodeOnly || d == DecodeOnly || d == EncodeAndDecode || d == ConstructOnly || d == ValidateOnly
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
		order := nameOrder{packagePath: v.plan.pkg.ImportPath(), key: fmt.Sprintf("%s:type:%s:%06d", v.key, userType.Name(), index)}
		var typeDeclaration *goacodegen.TypeDeclaration
		var declaration *goacodegen.NameDeclaration
		if v.originalLayout != nil {
			declaration = goacodegen.NewPreferredName(goacodegen.NameType, userType.Name(), goacodegen.UnexportedName, order)
			if err := v.plan.pkg.DeclareName(declaration); err != nil {
				return err
			}
		} else {
			var err error
			typeDeclaration, err = v.plan.pkg.DeclareGeneratedType(userType.Name(), order)
			if err != nil {
				return err
			}
			declaration = typeDeclaration.Declaration()
		}
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
			// A named union keeps the underlying transport's JSON methods.
			alias: goaexpr.IsUnion(userType),
		})
	}
	return nil
}

// declareUnions records every union found in the copied transport types.
func (v *Value) declareUnions() error {
	seen := make(map[goacodegen.UnionDeclarationID]struct{})
	return walkAttribute(v.transport, make(map[goaexpr.UserType]struct{}), func(attribute *goaexpr.AttributeExpr) error {
		if _, ok := attribute.Type.(*goaexpr.Union); !ok {
			return nil
		}
		identity := goacodegen.NewUnionDeclarationID(attribute)
		if _, exists := seen[identity]; exists {
			return nil
		}
		seen[identity] = struct{}{}
		declaration, err := v.plan.pkg.DeclareUnion(attribute)
		if err != nil {
			return err
		}
		v.unions = append(v.unions, &plannedUnion{
			attribute: attribute,
			name:      declaration.Declaration(), kind: declaration.KindDeclaration(),
		})
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
			if v.originalLayout != nil {
				return goacodegen.GoTypeBinding{Owner: v.plan.pkg.ImportPath(), Declaration: planned.declaration}, nil
			}
			return goacodegen.GoTypeBinding{Owner: v.plan.pkg.ImportPath(), Type: planned.typeDeclaration}, nil
		case goacodegen.GoUnion:
			if v.originalLayout != nil {
				union := v.plan.originals.unionsByLocal[request.Attribute.Type.(*goaexpr.Union)]
				return goacodegen.GoTypeBinding{Owner: v.plan.pkg.ImportPath(), Declaration: union.name}, nil
			}
			declaration, err := v.plan.pkg.Union(request.Attribute)
			if err != nil {
				return goacodegen.GoTypeBinding{}, err
			}
			return goacodegen.GoTypeBinding{Owner: v.plan.pkg.ImportPath(), Union: declaration}, nil
		case goacodegen.GoPrimitive,
			goacodegen.GoArray,
			goacodegen.GoMap,
			goacodegen.GoStruct,
			goacodegen.GoEmpty,
			goacodegen.GoServiceError:
			return goacodegen.GoTypeBinding{}, fmt.Errorf("unsupported transport type kind %s", request.Kind)
		}
		return goacodegen.GoTypeBinding{}, fmt.Errorf("unsupported transport type kind %s", request.Kind)
	}
	policy := transportPolicy()
	// Retain the named root and its contents so conversions can resolve every
	// transport value after package names freeze.
	transportLayout, err := goacodegen.PlanGoType(v.transport, goacodegen.GoTypePlanOptions{
		Owner:            v.plan.pkg.ImportPath(),
		Policy:           policy,
		Bind:             binder,
		RetainNamedValue: true,
	})
	if err != nil {
		return err
	}
	v.transportLayout = transportLayout
	for _, planned := range v.types {
		if planned.validation != nil {
			continue
		}
		definitionPolicy := policy
		if goaexpr.IsPrimitive(planned.userType) {
			// Scalar definition validators receive values. Pointer presence
			// belongs to their enclosing JSON fields, not these parameters.
			definitionPolicy.Pointer = false
		}
		layout, err := goacodegen.PlanGoType(planned.userType.Attribute(), goacodegen.GoTypePlanOptions{
			Owner:            v.plan.pkg.ImportPath(),
			Policy:           definitionPolicy,
			Bind:             binder,
			RetainNamedValue: true,
		})
		if err != nil {
			return err
		}
		// The function accepts the named transport type, whose union methods
		// may belong to an underlying declaration in its retained definition.
		parameter := &goaexpr.AttributeExpr{Type: planned.userType}
		parameterLayout, err := goacodegen.PlanGoType(parameter, goacodegen.GoTypePlanOptions{
			Owner:            v.plan.pkg.ImportPath(),
			Policy:           definitionPolicy,
			Bind:             binder,
			RetainNamedValue: true,
		})
		if err != nil {
			return err
		}
		validation, err := goacodegen.NewValidationPlan(
			parameter,
			parameterLayout,
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
		planned.parameter = parameterLayout
		planned.validation = validation
	}
	for _, union := range v.unions {
		if len(union.branches) > 0 {
			continue
		}
		for _, branch := range union.attribute.Type.(*goaexpr.Union).Values {
			var kind, constructor *goacodegen.NameDeclaration
			if v.originalLayout == nil {
				declaration, err := v.plan.pkg.UnionBranch(union.attribute, branch.Name)
				if err != nil {
					return err
				}
				kind, constructor = declaration.KindDeclaration(), declaration.ConstructorDeclaration()
			} else {
				var err error
				kind, constructor, err = v.plan.declarePrivateUnionBranch(union, branch.Name)
				if err != nil {
					return err
				}
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
				kind:        kind,
				constructor: constructor,
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
		if v.originalLayout != nil {
			var err error
			v.decodeDeclaration, err = v.plan.pkg.DeclareDependentName(
				goacodegen.NameFunction, v.originalLayout.TypeDeclaration().Declaration(),
				"Decode", "", nameOrder{packagePath: v.plan.pkg.ImportPath(), key: v.key + ":decode"})
			if err != nil {
				return err
			}
		} else {
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
	}
	if v.direction.encodes() || v.direction == ValidateOnly {
		encode, err := goacodegen.NewTransformPlan(v.service, v.transport, "encode", nil)
		if err != nil {
			return err
		}
		if err := v.bindTransformHelpers("encode", encode); err != nil {
			return err
		}
		v.encode = encode
		if v.direction == ValidateOnly {
			return v.PlanValidation()
		}
		if v.originalLayout != nil {
			var err error
			v.encodeDeclaration, err = v.plan.pkg.DeclareDependentName(
				goacodegen.NameFunction, v.originalLayout.TypeDeclaration().Declaration(),
				"Encode", "", nameOrder{packagePath: v.plan.pkg.ImportPath(), key: v.key + ":encode"})
			if err != nil {
				return err
			}
		} else {
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
	layout *goacodegen.GoTypePlan,
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
	imports := goacodegen.NewGeneratedImportPlan(p.pkg)
	if err := imports.AddCompleteType(layout); err != nil {
		return fmt.Errorf("plan JSON service imports: %w", err)
	}
	for _, importPath := range imports.Paths() {
		p.locatedImportPaths[importPath] = struct{}{}
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
	var err error
	if p.originals != nil {
		err = p.pkg.DeclareImport(spec)
	} else {
		err = p.pkg.RequireImport(spec)
	}
	if err != nil {
		return fmt.Errorf("plan JSON codec import %q: %w", spec.Path, err)
	}
	p.importPaths[spec.Path] = struct{}{}
	return nil
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
