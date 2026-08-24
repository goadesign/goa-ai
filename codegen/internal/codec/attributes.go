// Package codec copies service attributes into transport-only types without
// changing the design.
package codec

import (
	"strings"

	goaexpr "goa.design/goa/v3/expr"
)

// localTransportAttribute builds one named JSON value and every nested named
// type it needs. Object fields keep the names written in the Goa design.
func localTransportAttribute(attribute *goaexpr.AttributeExpr, key, preferredName string) (*goaexpr.AttributeExpr, []goaexpr.UserType) {
	shape := attribute
	if userType, ok := attribute.Type.(goaexpr.UserType); ok && userType != goaexpr.Empty {
		shape = userType.Attribute()
	}
	transportShape := goaexpr.DupAtt(shape)
	normalizeTransportAttribute(
		transportShape,
		preferredName,
		make(map[goaexpr.UserType]struct{}),
		make(map[*goaexpr.Union]struct{}),
	)
	transportShape, nested := localizeNestedTypes(transportShape, key, preferredName)
	top := &goaexpr.UserTypeExpr{
		AttributeExpr: transportShape,
		TypeName:      preferredName + "Transport",
		UID:           "goa-ai-json:" + key,
	}
	return &goaexpr.AttributeExpr{Type: top}, append([]goaexpr.UserType{top}, nested...)
}

// normalizeTransportAttribute removes service package locations and writes
// each design field name into its JSON tag. Repeated named types and unions are
// visited once so recursive designs finish and union names change only once.
func normalizeTransportAttribute(
	attribute *goaexpr.AttributeExpr,
	prefix string,
	seenTypes map[goaexpr.UserType]struct{},
	seenUnions map[*goaexpr.Union]struct{},
) {
	if attribute == nil || attribute.Type == nil || attribute.Type == goaexpr.Empty {
		return
	}
	stripPackageMetadata(attribute)
	switch actual := attribute.Type.(type) {
	case goaexpr.UserType:
		origin := actual.Origin()
		if _, exists := seenTypes[origin]; exists {
			return
		}
		seenTypes[origin] = struct{}{}
		normalizeTransportAttribute(actual.Attribute(), prefix, seenTypes, seenUnions)
	case *goaexpr.Object:
		for _, field := range *actual {
			if field == nil || field.Attribute == nil {
				continue
			}
			if field.Attribute.Meta == nil {
				field.Attribute.Meta = make(goaexpr.MetaExpr)
			}
			delete(field.Attribute.Meta, "struct:tag:json")
			field.Attribute.Meta["struct:tag:json:name"] = []string{field.Name}
			normalizeTransportAttribute(field.Attribute, prefix, seenTypes, seenUnions)
		}
	case *goaexpr.Array:
		normalizeTransportAttribute(actual.ElemType, prefix, seenTypes, seenUnions)
	case *goaexpr.Map:
		normalizeTransportAttribute(actual.KeyType, prefix, seenTypes, seenUnions)
		normalizeTransportAttribute(actual.ElemType, prefix, seenTypes, seenUnions)
	case *goaexpr.Union:
		if _, exists := seenUnions[actual]; exists {
			return
		}
		seenUnions[actual] = struct{}{}
		actual.TypeName = prefix + actual.TypeName + "Transport"
		for _, branch := range actual.Values {
			normalizeTransportAttribute(branch.Attribute, prefix, seenTypes, seenUnions)
		}
	}
}

// localizeNestedTypes replaces service user types with copies declared in the
// private codec package. Repeated and recursive references share one copy.
func localizeNestedTypes(attribute *goaexpr.AttributeExpr, key, prefix string) (*goaexpr.AttributeExpr, []goaexpr.UserType) {
	localByOrigin := make(map[goaexpr.UserType]goaexpr.UserType)
	var locals []goaexpr.UserType
	localizeNestedAttribute(attribute, key, prefix, localByOrigin, &locals)
	return attribute, locals
}

// localizeNestedAttribute replaces each copied service type with one local
// type. It records the local type before following its fields so a recursive
// field can point back to that same declaration.
func localizeNestedAttribute(
	attribute *goaexpr.AttributeExpr,
	key string,
	prefix string,
	localByOrigin map[goaexpr.UserType]goaexpr.UserType,
	locals *[]goaexpr.UserType,
) {
	if attribute == nil || attribute.Type == nil || attribute.Type == goaexpr.Empty {
		return
	}
	switch actual := attribute.Type.(type) {
	case goaexpr.UserType:
		origin := actual.Origin()
		if local := localByOrigin[origin]; local != nil {
			attribute.Type = local
			return
		}
		local := &goaexpr.UserTypeExpr{
			AttributeExpr: actual.Attribute(),
			TypeName:      prefix + actual.Name() + "Transport",
			UID:           "goa-ai-json:" + key + ":" + origin.ID(),
		}
		localByOrigin[origin] = local
		*locals = append(*locals, local)
		attribute.Type = local
		localizeNestedAttribute(local.Attribute(), key, prefix, localByOrigin, locals)
	case *goaexpr.Object:
		for _, field := range *actual {
			localizeNestedAttribute(field.Attribute, key, prefix, localByOrigin, locals)
		}
	case *goaexpr.Array:
		localizeNestedAttribute(actual.ElemType, key, prefix, localByOrigin, locals)
	case *goaexpr.Map:
		localizeNestedAttribute(actual.KeyType, key, prefix, localByOrigin, locals)
		localizeNestedAttribute(actual.ElemType, key, prefix, localByOrigin, locals)
	case *goaexpr.Union:
		for _, branch := range actual.Values {
			localizeNestedAttribute(branch.Attribute, key, prefix, localByOrigin, locals)
		}
	}
}

// stripPackageMetadata keeps copied types inside the generated codec package.
func stripPackageMetadata(attribute *goaexpr.AttributeExpr) {
	for key := range attribute.Meta {
		if strings.HasPrefix(key, "struct:pkg:") {
			delete(attribute.Meta, key)
		}
	}
	if len(attribute.Meta) == 0 {
		attribute.Meta = nil
	}
}

// walkAttribute visits each object, collection, named type, and union once.
func walkAttribute(
	attribute *goaexpr.AttributeExpr,
	seen map[goaexpr.UserType]struct{},
	visit func(*goaexpr.AttributeExpr) error,
) error {
	if attribute == nil || attribute.Type == nil || attribute.Type == goaexpr.Empty {
		return nil
	}
	if err := visit(attribute); err != nil {
		return err
	}
	switch actual := attribute.Type.(type) {
	case goaexpr.UserType:
		origin := actual.Origin()
		if _, exists := seen[origin]; exists {
			return nil
		}
		seen[origin] = struct{}{}
		return walkAttribute(actual.Attribute(), seen, visit)
	case *goaexpr.Object:
		for _, field := range *actual {
			if err := walkAttribute(field.Attribute, seen, visit); err != nil {
				return err
			}
		}
	case *goaexpr.Array:
		return walkAttribute(actual.ElemType, seen, visit)
	case *goaexpr.Map:
		if err := walkAttribute(actual.KeyType, seen, visit); err != nil {
			return err
		}
		return walkAttribute(actual.ElemType, seen, visit)
	case *goaexpr.Union:
		for _, branch := range actual.Values {
			if err := walkAttribute(branch.Attribute, seen, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
