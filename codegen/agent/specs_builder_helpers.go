package codegen

import (
	"strings"

	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// userTypeCloner clones attribute graphs and can inject struct package locators
// into user types to keep generated references consistent with Goa’s
// `struct:pkg:path` behavior.
type userTypeCloner struct {
	pkg  string
	seen map[string]goaexpr.UserType
}

// stripStructPkgMeta returns a shallow copy of att with struct:pkg:* locator
// metadata removed so synthesized local alias user types are treated as local
// to the tool specs package.
func stripStructPkgMeta(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil {
		return nil
	}
	out := *att
	if len(att.Meta) == 0 {
		return &out
	}
	meta := make(map[string][]string, len(att.Meta))
	for k, v := range att.Meta {
		// Drop locator metadata so synthesized local alias user types are treated
		// as local to the tool package (unqualified), even when the underlying
		// attribute graph was placed via struct:pkg:path.
		if strings.HasPrefix(k, "struct:pkg:") {
			continue
		}
		meta[k] = v
	}
	out.Meta = meta
	return &out
}

// propagateStructPkgMetaToNestedUserTypes returns a deep clone of att where any
// nested user types without an explicit package locator are treated as belonging
// to pkg via `struct:pkg:path`.
//
// This is required for correctness when the root tool payload/result aliases a
// located user type (e.g. `types.Event`) that itself references other named user
// types (e.g. `types.Status`). When those nested types have no explicit locator
// metadata, Goa generates them in the same package as the root. Tool specs
// generation must mirror that so transforms and codecs reference `pkg.<Type>`
// rather than materializing conflicting local types.
func propagateStructPkgMetaToNestedUserTypes(att *goaexpr.AttributeExpr, pkg string) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || pkg == "" {
		return att
	}
	c := &userTypeCloner{
		pkg:  pkg,
		seen: make(map[string]goaexpr.UserType),
	}
	return c.cloneAttr(att)
}

func (c *userTypeCloner) cloneAttr(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}

	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		return c.cloneUserType(att, dt)
	case *goaexpr.Object:
		obj := &goaexpr.Object{}
		for _, nat := range *dt {
			if nat == nil {
				continue
			}
			*obj = append(*obj, &goaexpr.NamedAttributeExpr{
				Name:      nat.Name,
				Attribute: c.cloneAttr(nat.Attribute),
			})
		}
		out := *att
		out.Type = obj
		return &out
	case *goaexpr.Array:
		out := *att
		out.Type = &goaexpr.Array{ElemType: c.cloneAttr(dt.ElemType)}
		return &out
	case *goaexpr.Map:
		out := *att
		out.Type = &goaexpr.Map{
			KeyType:  c.cloneAttr(dt.KeyType),
			ElemType: c.cloneAttr(dt.ElemType),
		}
		return &out
	case *goaexpr.Union:
		u := &goaexpr.Union{TypeName: dt.TypeName}
		for _, nat := range dt.Values {
			if nat == nil {
				continue
			}
			u.Values = append(u.Values, &goaexpr.NamedAttributeExpr{
				Name:      nat.Name,
				Attribute: c.cloneAttr(nat.Attribute),
			})
		}
		out := *att
		out.Type = u
		return &out
	default:
		out := *att
		return &out
	}
}

func (c *userTypeCloner) cloneUserType(att *goaexpr.AttributeExpr, ut goaexpr.UserType) *goaexpr.AttributeExpr {
	if ut == nil {
		out := *att
		return &out
	}

	id := ut.ID()
	if id != "" {
		if cached, ok := c.seen[id]; ok {
			out := *att
			out.Type = cached
			return &out
		}
	}

	switch t := ut.(type) {
	case *goaexpr.UserTypeExpr:
		clone := *t
		clone.AttributeExpr = c.cloneAttr(t.AttributeExpr)
		c.ensurePkgLocator(clone.AttributeExpr)
		if id != "" {
			c.seen[id] = &clone
		}
		out := *att
		out.Type = &clone
		return &out
	case *goaexpr.ResultTypeExpr:
		clone := *t
		clone.AttributeExpr = c.cloneAttr(t.AttributeExpr)
		c.ensurePkgLocator(clone.AttributeExpr)
		if id != "" {
			c.seen[id] = &clone
		}
		out := *att
		out.Type = &clone
		return &out
	default:
		// Unknown user type impl: preserve as-is.
		out := *att
		out.Type = ut
		return &out
	}
}

func (c *userTypeCloner) ensurePkgLocator(att *goaexpr.AttributeExpr) {
	if att == nil {
		return
	}
	if att.Meta == nil {
		att.Meta = make(map[string][]string)
	}
	if _, ok := att.Meta["struct:pkg:path"]; ok {
		return
	}
	att.Meta["struct:pkg:path"] = []string{c.pkg}
}

// fixTransformHelperSignatures fixes known Goa GoTransform helper signature
// mismatches for anonymous inline structs.
//
// Goa may generate helper bodies that construct pointers to anonymous structs
// (e.g., `res := &struct {...}{}`) while emitting a non-pointer ResultTypeRef.
// This produces uncompilable code at call sites that expect pointer values.
func fixTransformHelperSignatures(helpers []*codegen.TransformFunctionData) []*codegen.TransformFunctionData {
	for _, h := range helpers {
		if h == nil {
			continue
		}
		if strings.HasPrefix(h.ResultTypeRef, "struct {") && strings.Contains(h.Code, "res := &struct {") {
			h.ResultTypeRef = "*" + h.ResultTypeRef
		}
	}
	return helpers
}

func dedupTransformHelpers(types []*typeData) {
	seen := make(map[string]struct{})
	for _, td := range types {
		if td == nil || len(td.TransformHelpers) == 0 {
			continue
		}
		out := td.TransformHelpers[:0]
		for _, h := range td.TransformHelpers {
			if h == nil || h.Name == "" {
				continue
			}
			if _, ok := seen[h.Name]; ok {
				continue
			}
			seen[h.Name] = struct{}{}
			out = append(out, h)
		}
		td.TransformHelpers = out
	}
}
