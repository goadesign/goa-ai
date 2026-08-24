// Package codegen assigns one Go function declaration to matching type conversions
// in a generated tool package. The comparison uses the generated types and
// field rules that determine the function body, so later rendering only writes
// each matching function once.
package codegen

import (
	"fmt"
	"reflect"
	"slices"

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

	// plannedTransformHelper stores one declaration that matching conversion
	// plans may call. One plan cannot use the declaration for two different
	// helper occurrences.
	plannedTransformHelper struct {
		identity    plannedTransformHelperIdentity
		declaration *goacodegen.NameDeclaration
		plans       map[*goacodegen.TransformPlan]struct{}
	}

	// plannedTransformHelperIdentity contains every fact that changes one
	// generated conversion function.
	plannedTransformHelperIdentity struct {
		source   plannedTransformTypeIdentity
		target   plannedTransformTypeIdentity
		required bool
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
func (p *toolSpecsPackagePlan) transformHelperIdentity(helper goacodegen.TransformHelper, layout plannedTransformLayout) (plannedTransformHelperIdentity, error) {
	source, err := p.transformTypeIdentity(helper.Source, layout.source)
	if err != nil {
		return plannedTransformHelperIdentity{}, err
	}
	target, err := p.transformTypeIdentity(helper.Target, layout.target)
	if err != nil {
		return plannedTransformHelperIdentity{}, err
	}
	return plannedTransformHelperIdentity{
		source:   source,
		target:   target,
		required: helper.Required,
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

// findTransformHelper returns a matching declaration that plan has not already
// used for another conversion function.
func (p *toolSpecsPackagePlan) findTransformHelper(identity plannedTransformHelperIdentity, plan *goacodegen.TransformPlan) *plannedTransformHelper {
	for _, helper := range p.transformHelpers {
		if _, used := helper.plans[plan]; used {
			continue
		}
		if plannedTransformHelperIdentitiesEqual(helper.identity, identity) {
			return helper
		}
	}
	return nil
}

// plannedTransformHelperIdentitiesEqual reports whether two functions have the
// same parameter, result, field layout, and missing-value behavior.
func plannedTransformHelperIdentitiesEqual(left, right plannedTransformHelperIdentity) bool {
	return left.required == right.required &&
		plannedTransformTypeIdentitiesEqual(left.source, right.source) &&
		plannedTransformTypeIdentitiesEqual(left.target, right.target)
}

// plannedTransformTypeIdentitiesEqual compares exact generated declarations
// and layouts. Types owned by another package also compare the fields used by
// the conversion function.
func plannedTransformTypeIdentitiesEqual(left, right plannedTransformTypeIdentity) bool {
	if left.declaration != right.declaration || left.origin != right.origin || left.policy != right.policy {
		return false
	}
	if left.declaration != nil {
		return true
	}
	return plannedTransformAttributesEqual(left.attribute, right.attribute, make(map[plannedTransformAttributePair]struct{}))
}

// plannedTransformAttributesEqual compares defaults, required fields, Go field
// metadata, and nested types without following recursive types forever.
func plannedTransformAttributesEqual(left, right *goaexpr.AttributeExpr, seen map[plannedTransformAttributePair]struct{}) bool {
	if left == right {
		return true
	}
	pair := plannedTransformAttributePair{left: left, right: right}
	if _, ok := seen[pair]; ok {
		return true
	}
	seen[pair] = struct{}{}
	if !reflect.DeepEqual(left.DefaultValue, right.DefaultValue) ||
		!reflect.DeepEqual(left.Validation, right.Validation) ||
		!plannedTransformMetadataEqual(left.Meta, right.Meta) {
		return false
	}
	switch leftType := left.Type.(type) {
	case goaexpr.UserType:
		rightType, ok := right.Type.(goaexpr.UserType)
		if !ok {
			return false
		}
		if leftResult, ok := leftType.(*goaexpr.ResultTypeExpr); ok {
			rightResult, ok := rightType.(*goaexpr.ResultTypeExpr)
			return ok && leftResult.Identifier == rightResult.Identifier &&
				leftResult.Name() == rightResult.Name() &&
				plannedTransformAttributesEqual(leftResult.Attribute(), rightResult.Attribute(), seen)
		}
		return leftType.Origin() == rightType.Origin() &&
			plannedTransformAttributesEqual(leftType.Attribute(), rightType.Attribute(), seen)
	case *goaexpr.Object:
		rightType, ok := right.Type.(*goaexpr.Object)
		if !ok || len(*leftType) != len(*rightType) {
			return false
		}
		for index, field := range *leftType {
			other := (*rightType)[index]
			if field.Name != other.Name || !plannedTransformAttributesEqual(field.Attribute, other.Attribute, seen) {
				return false
			}
		}
		return true
	case *goaexpr.Array:
		rightType, ok := right.Type.(*goaexpr.Array)
		return ok && leftType.NonNullableElems == rightType.NonNullableElems &&
			plannedTransformAttributesEqual(leftType.ElemType, rightType.ElemType, seen)
	case *goaexpr.Map:
		rightType, ok := right.Type.(*goaexpr.Map)
		return ok && plannedTransformAttributesEqual(leftType.KeyType, rightType.KeyType, seen) &&
			plannedTransformAttributesEqual(leftType.ElemType, rightType.ElemType, seen)
	case *goaexpr.Union:
		rightType, ok := right.Type.(*goaexpr.Union)
		if !ok || leftType.GetTypeKey() != rightType.GetTypeKey() ||
			leftType.GetValueKey() != rightType.GetValueKey() || len(leftType.Values) != len(rightType.Values) {
			return false
		}
		for index, branch := range leftType.Values {
			other := rightType.Values[index]
			if branch.Name != other.Name || !plannedTransformAttributesEqual(branch.Attribute, other.Attribute, seen) {
				return false
			}
		}
		return true
	default:
		return right.Type != nil && left.Type.Kind() == right.Type.Kind() && left.Type.Name() == right.Type.Name()
	}
}

// plannedTransformMetadataEqual compares metadata that changes generated field
// names or type references. The assigned type name is excluded because the
// generated declaration or original Goa type already identifies that type.
func plannedTransformMetadataEqual(left, right goaexpr.MetaExpr) bool {
	leftKeys := plannedTransformMetadataKeys(left)
	rightKeys := plannedTransformMetadataKeys(right)
	if !slices.Equal(leftKeys, rightKeys) {
		return false
	}
	for _, key := range leftKeys {
		if !slices.Equal(left[key], right[key]) {
			return false
		}
	}
	return true
}

// plannedTransformMetadataKeys returns metadata keys in stable order without
// the generated type name already represented by the type identity.
func plannedTransformMetadataKeys(metadata goaexpr.MetaExpr) []string {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		if key != "struct:type:name" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}
