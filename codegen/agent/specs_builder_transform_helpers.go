// Package codegen assigns one Go function declaration to matching type conversions
// in a generated tool package. The comparison uses the generated types and
// field rules that determine the function body, so later rendering only writes
// each matching function once.
package codegen

import (
	"fmt"
	"reflect"

	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// plannedTransformPackage identifies where one side of a conversion type is
	// written.
	plannedTransformPackage uint8

	// plannedTransformLayout records the package and field rules used by both
	// sides of a generated conversion.
	plannedTransformLayout struct {
		source plannedTransformTypeLayout
		target plannedTransformTypeLayout
	}

	// plannedTransformTypeLayout records the generated package and the rules
	// that decide whether fields and collection elements use pointers.
	plannedTransformTypeLayout struct {
		pkg    plannedTransformPackage
		policy goacodegen.GoLayoutPolicy
	}

	// plannedTransformHelper stores one declaration that every matching
	// conversion may call.
	plannedTransformHelper struct {
		identity    plannedTransformHelperIdentity
		declaration *goacodegen.NameDeclaration
	}

	// plannedTransformHelperIdentity contains every fact that changes one
	// generated conversion function.
	plannedTransformHelperIdentity struct {
		source plannedTransformTypeIdentity
		target plannedTransformTypeIdentity
	}

	// plannedTransformTypeIdentity records the exact generated declaration, or
	// the original Goa type when another generated package owns the Go type.
	// The attribute and policy describe the fields used inside the function.
	plannedTransformTypeIdentity struct {
		declaration *goacodegen.NameDeclaration
		origin      goaexpr.UserType
		attribute   *goaexpr.AttributeExpr
		policy      goacodegen.GoLayoutPolicy
	}

	// plannedTransformAttributePair stops comparison when two recursive types
	// refer back to an attribute pair already being compared.
	plannedTransformAttributePair struct {
		left  *goaexpr.AttributeExpr
		right *goaexpr.AttributeExpr
	}
)

const (
	plannedTransformPublic plannedTransformPackage = iota + 1
	plannedTransformTransport
	plannedTransformService
)

var (
	publicTransformTypeLayout = plannedTransformTypeLayout{
		pkg: plannedTransformPublic,
		policy: goacodegen.GoLayoutPolicy{
			UseDefault: true,
			SumType:    true,
		},
	}
	transportTransformTypeLayout = plannedTransformTypeLayout{
		pkg: plannedTransformTransport,
		policy: goacodegen.GoLayoutPolicy{
			Pointer:             true,
			UnionPointer:        true,
			ArrayElementPointer: true,
			SumType:             true,
		},
	}
	serviceTransformTypeLayout = plannedTransformTypeLayout{
		pkg: plannedTransformService,
		policy: goacodegen.GoLayoutPolicy{
			UseDefault: true,
			SumType:    true,
		},
	}
	codecDecodeTransformLayout = plannedTransformLayout{
		source: transportTransformTypeLayout,
		target: publicTransformTypeLayout,
	}
	codecEncodeTransformLayout = plannedTransformLayout{
		source: publicTransformTypeLayout,
		target: transportTransformTypeLayout,
	}
	adapterPayloadTransformLayout = plannedTransformLayout{
		source: publicTransformTypeLayout,
		target: serviceTransformTypeLayout,
	}
	adapterResultTransformLayout = plannedTransformLayout{
		source: serviceTransformTypeLayout,
		target: publicTransformTypeLayout,
	}
)

// transformHelperIdentity resolves the exact generated type and field rules
// used by one planned conversion function.
func (p *toolSpecsPackagePlan) transformHelperIdentity(definition goacodegen.TransformHelperDefinition, layout plannedTransformLayout) (plannedTransformHelperIdentity, error) {
	source, err := p.transformTypeIdentity(definition.Source, layout.source)
	if err != nil {
		return plannedTransformHelperIdentity{}, err
	}
	target, err := p.transformTypeIdentity(definition.Target, layout.target)
	if err != nil {
		return plannedTransformHelperIdentity{}, err
	}
	return plannedTransformHelperIdentity{
		source: source,
		target: target,
	}, nil
}

// transformTypeIdentity finds the declaration used for a tool-package type.
// Service types and located external types keep their original Goa declaration
// because another generated package owns their Go name.
func (p *toolSpecsPackagePlan) transformTypeIdentity(attribute *goaexpr.AttributeExpr, layout plannedTransformTypeLayout) (plannedTransformTypeIdentity, error) {
	userType, ok := attribute.Type.(goaexpr.UserType)
	if !ok {
		return plannedTransformTypeIdentity{}, fmt.Errorf("tool conversion function type %q is not named", attribute.Type.Name())
	}
	identity := plannedTransformTypeIdentity{
		attribute: attribute,
		policy:    layout.policy,
	}
	// Goa's conversion plan copies each type. Origin returns the planned type
	// whose declaration was recorded before that copy was made.
	plannedType := userType.Origin()
	switch layout.pkg {
	case plannedTransformPublic:
		identity.declaration = p.publicTypeUses[plannedType]
	case plannedTransformTransport:
		identity.declaration = p.transportTypeUses[plannedType]
	case plannedTransformService:
		identity.origin = plannedType
	default:
		return plannedTransformTypeIdentity{}, fmt.Errorf("unknown tool conversion package %d", layout.pkg)
	}
	if identity.declaration == nil && layout.pkg != plannedTransformService {
		if goacodegen.UserTypeLocation(userType) == nil {
			packageName := "public"
			if layout.pkg == plannedTransformTransport {
				packageName = "transport"
			}
			return plannedTransformTypeIdentity{}, fmt.Errorf(
				"tool conversion type %q has no declaration in its generated %s package",
				userType.Name(),
				packageName,
			)
		}
		// A located type is written by another generated package. Its original
		// Goa declaration identifies the exact external Go type.
		identity.origin = userType.Origin()
	}
	return identity, nil
}

// findTransformHelper returns the declaration used by the same conversion.
func (p *toolSpecsPackagePlan) findTransformHelper(identity plannedTransformHelperIdentity) *plannedTransformHelper {
	for _, helper := range p.transformHelpers {
		if plannedTransformHelperIdentitiesEqual(helper.identity, identity) {
			return helper
		}
	}
	return nil
}

// plannedTransformHelperIdentitiesEqual reports whether two functions have the
// same parameter, result, and field layout.
func plannedTransformHelperIdentitiesEqual(left, right plannedTransformHelperIdentity) bool {
	return plannedTransformTypeIdentitiesEqual(left.source, right.source) &&
		plannedTransformTypeIdentitiesEqual(left.target, right.target)
}

// plannedTransformTypeIdentitiesEqual compares exact generated declarations
// and layouts. Types owned by another package also compare the fields used by
// the conversion function.
func plannedTransformTypeIdentitiesEqual(left, right plannedTransformTypeIdentity) bool {
	if left.declaration != right.declaration || left.origin != right.origin || left.policy != right.policy {
		return false
	}
	return plannedTransformAttributesEqualR(
		left.attribute,
		right.attribute,
		make(map[plannedTransformAttributePair]struct{}),
		left.declaration != nil,
	)
}

// plannedTransformAttributesEqual compares the facts read while rendering a
// conversion without following recursive types forever.
func plannedTransformAttributesEqual(left, right *goaexpr.AttributeExpr, seen map[plannedTransformAttributePair]struct{}) bool {
	return plannedTransformAttributesEqualR(left, right, seen, false)
}

// plannedTransformAttributesEqualR compares type layout, required fields,
// defaults, and Go metadata. Generated types may come from independent Goa
// copies, so their authored names replace copy-local origin pointers.
func plannedTransformAttributesEqualR(left, right *goaexpr.AttributeExpr, seen map[plannedTransformAttributePair]struct{}, generated bool) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left == right {
		return true
	}
	pair := plannedTransformAttributePair{left: left, right: right}
	if _, ok := seen[pair]; ok {
		return true
	}
	seen[pair] = struct{}{}
	if !plannedTransformMetadataEqual(left.Meta, right.Meta) || !reflect.DeepEqual(left.DefaultValue, right.DefaultValue) ||
		!plannedTransformRequiredEqual(left, right) {
		return false
	}
	return plannedTransformDataTypesEqual(left.Type, right.Type, seen, generated)
}

// plannedTransformRequiredEqual compares the field presence decisions used for
// pointer layout, nil checks, and default assignments.
func plannedTransformRequiredEqual(left, right *goaexpr.AttributeExpr) bool {
	leftObject := goaexpr.AsObject(left.Type)
	rightObject := goaexpr.AsObject(right.Type)
	if leftObject == nil || rightObject == nil {
		return leftObject == rightObject
	}
	if len(*leftObject) != len(*rightObject) {
		return false
	}
	for index, field := range *leftObject {
		other := (*rightObject)[index]
		if field.Name != other.Name || left.IsRequired(field.Name) != right.IsRequired(other.Name) {
			return false
		}
	}
	return true
}

// plannedTransformDataTypesEqual compares the complete type graph used by a
// conversion. It ignores only origin pointers for generated copies that use
// the same declared Go type.
func plannedTransformDataTypesEqual(left, right goaexpr.DataType, seen map[plannedTransformAttributePair]struct{}, generated bool) bool {
	if left == nil || right == nil {
		return left == right
	}
	if reflect.TypeOf(left) != reflect.TypeOf(right) {
		return false
	}
	switch leftType := left.(type) {
	case goaexpr.Primitive:
		return leftType == right.(goaexpr.Primitive)
	case goaexpr.UserType:
		rightType := right.(goaexpr.UserType)
		if leftResult, ok := leftType.(*goaexpr.ResultTypeExpr); ok {
			rightResult := rightType.(*goaexpr.ResultTypeExpr)
			if leftResult.Identifier != rightResult.Identifier {
				return false
			}
		}
		if leftType.Name() != rightType.Name() || (!generated && leftType.Origin() != rightType.Origin()) {
			return false
		}
		return plannedTransformAttributesEqualR(leftType.Attribute(), rightType.Attribute(), seen, generated)
	case *goaexpr.Object:
		rightType := right.(*goaexpr.Object)
		if len(*leftType) != len(*rightType) {
			return false
		}
		for index, field := range *leftType {
			other := (*rightType)[index]
			if field.Name != other.Name || !plannedTransformAttributesEqualR(field.Attribute, other.Attribute, seen, generated) {
				return false
			}
		}
		return true
	case *goaexpr.Array:
		rightType := right.(*goaexpr.Array)
		return leftType.NonNullableElems == rightType.NonNullableElems &&
			plannedTransformAttributesEqualR(leftType.ElemType, rightType.ElemType, seen, generated)
	case *goaexpr.Map:
		rightType := right.(*goaexpr.Map)
		return plannedTransformAttributesEqualR(leftType.KeyType, rightType.KeyType, seen, generated) &&
			plannedTransformAttributesEqualR(leftType.ElemType, rightType.ElemType, seen, generated)
	case *goaexpr.Union:
		rightType := right.(*goaexpr.Union)
		if leftType.Name() != rightType.Name() || leftType.GetTypeKey() != rightType.GetTypeKey() ||
			leftType.GetValueKey() != rightType.GetValueKey() || len(leftType.Values) != len(rightType.Values) {
			return false
		}
		for index, branch := range leftType.Values {
			other := rightType.Values[index]
			if branch.Name != other.Name || !plannedTransformAttributesEqualR(branch.Attribute, other.Attribute, seen, generated) {
				return false
			}
		}
		return true
	default:
		panic(fmt.Sprintf("cannot compare planned transform type %T", left))
	}
}

// plannedTransformMetadataEqual compares the metadata that selects Go field,
// type, and package names. Serialization tags do not change conversion code.
func plannedTransformMetadataEqual(left, right goaexpr.MetaExpr) bool {
	for _, key := range [...]string{
		"struct:field:name",
		"struct:field:type",
		"struct:type:name",
		"struct:pkg:path",
	} {
		if !reflect.DeepEqual(left[key], right[key]) {
			return false
		}
	}
	return true
}
