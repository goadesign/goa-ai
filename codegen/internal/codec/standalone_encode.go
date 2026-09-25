// Package codec emits preflight walks over generated Go fields. It checks strings before
// JSON replacement and pointer cycles before recursive generated transforms.
package codec

import (
	"fmt"
	"strings"

	"goa.design/goa-ai/codegen/internal/jsonshape"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// renderPreflight writes direct checks using the canonical bound type resolver.
func (v *Value) renderPreflight() (string, error) {
	var source strings.Builder
	imports := v.plan.importNames()
	writer := &serviceTypeWriter{Attributor: v.serviceAttributor}
	ref := writer.Ref(v.service, "")
	fmt.Fprintf(&source, "\n// %s checks text and cycles before conversion.\nfunc %s(in %s) error {\n",
		v.standalone.preflight.Name(), v.standalone.preflight.Name(), ref)
	if codegen.IsNilable(v.service.Type) || expr.IsObject(v.service.Type) || expr.IsUnion(v.service.Type) {
		fmt.Fprintf(&source, "if in == nil { return %s.Errorf(\"missing root value\") }\n", imports.Fmt)
	}
	source.WriteString("active := make(map[any]bool)\n")
	if err := v.writeValueCheck(&source, writer, v.service, "in", `"$"`, expr.IsObject(v.service.Type) || expr.IsUnion(v.service.Type), 0); err != nil {
		return "", err
	}
	source.WriteString("return nil\n}\n")
	for _, check := range v.standalone.orderedTypedChecks {
		attribute := check.attribute
		named := attribute.Type.(expr.UserType)
		ref := writer.Ref(attribute, "")
		name := check.name.Name()
		fmt.Fprintf(&source, "\n// %s checks one generated value on the active path.\nfunc %s(in %s, field string, active map[any]bool) error {\n", name, name, ref)
		if expr.IsObject(named) || expr.IsUnion(named) {
			fmt.Fprintf(&source, `if in == nil { return nil }
if active[in] { return %s.Errorf("%%s: cyclic Go value", field) }
active[in] = true
defer delete(active, in)
`, imports.Fmt)
		}
		if check.recursiveCollection {
			if expr.IsArray(named) {
				fmt.Fprintf(&source, "identity := struct { pointer any; length int }{%s.ValueOf(in).UnsafePointer(), len(in)}\n", imports.Reflect)
			} else {
				fmt.Fprintf(&source, "identity := %s.ValueOf(in).UnsafePointer()\n", imports.Reflect)
			}
			fmt.Fprintf(&source, `if active[identity] { return %s.Errorf("%%s: cyclic Go value", field) }
active[identity] = true
defer delete(active, identity)
`, imports.Fmt)
		}
		definition := attribute
		owner := codegen.Attributor(writer)
		for {
			owner = owner.Enter(definition)
			base, ok := definition.Type.(expr.UserType)
			if !ok {
				break
			}
			definition = base.Attribute()
		}
		value := "in"
		if expr.IsUnion(named) {
			// A defined Go type has no methods from its underlying union.
			// Use that union's pointer for branch access, keeping in unchanged.
			value = fmt.Sprintf("(%s)(in)", owner.Ref(definition, ""))
		}
		if err := v.writeValueCheck(&source, owner, definition, value, "field", expr.IsObject(named) || expr.IsUnion(named), 0); err != nil {
			return "", err
		}
		source.WriteString("return nil\n}\n")
	}
	return source.String(), nil
}

// writeValueCheck specializes checks to one schema occurrence. Pointer is the
// existing service layout's representation at this call site, not a codec mode.
func (v *Value) writeValueCheck(out *strings.Builder, writer codegen.Attributor, attribute *expr.AttributeExpr, target, field string, pointer bool, depth int) error {
	imports := v.plan.importNames()
	childPath := v.plan.jsonHelpers.child.Name()
	if named, ok := attribute.Type.(expr.UserType); ok && named != expr.Empty {
		name := v.standalone.typedChecks[named.Origin()].name
		arg := target
		if expr.IsUnion(named) && !pointer {
			arg = "&" + target
		}
		if _, ok := jsonshape.PrimitiveType(attribute); ok && pointer {
			fmt.Fprintf(out, "if %s != nil {\n", target)
			arg = "*" + target
		}
		fmt.Fprintf(out, "if err := %s(%s, %s, active); err != nil { return err }\n", name.Name(), arg, field)
		if _, ok := jsonshape.PrimitiveType(attribute); ok && pointer {
			out.WriteString("}\n")
		}
		return nil
	}
	switch actual := attribute.Type.(type) {
	case expr.Primitive:
		if actual != expr.String {
			return nil
		}
		text := target
		if pointer {
			fmt.Fprintf(out, "if %s != nil {\n", target)
			text = "*" + target
		}
		fmt.Fprintf(out, "if !%s.ValidString(string(%s)) { return %s.Errorf(\"%%s: invalid UTF-8\", %s) }\n", imports.UTF8, text, imports.Fmt, field)
		if pointer {
			out.WriteString("}\n")
		}
	case *expr.Object:
		if pointer {
			fmt.Fprintf(out, "if %s != nil {\n", target)
		}
		for _, member := range *actual {
			name := writer.Field(member.Attribute, member.Name, true)
			child := target + "." + name
			childField := fmt.Sprintf("%s(%s, %q, false)", childPath, field, member.Name)
			memberPointer := attribute.IsPrimitivePointer(member.Name, true)
			if expr.IsObject(member.Attribute.Type) {
				memberPointer = true
			}
			if err := v.writeValueCheck(out, writer, member.Attribute, child, childField, memberPointer, depth+1); err != nil {
				return err
			}
		}
		if pointer {
			out.WriteString("}\n")
		}
	case *expr.Array:
		index, item := fmt.Sprintf("index%d", depth), fmt.Sprintf("item%d", depth)
		fmt.Fprintf(out, "for %s, %s := range %s {\n", index, item, target)
		fmt.Fprintf(out, "_ = %s; _ = %s\n", index, item)
		childField := fmt.Sprintf("%s(%s, %s.Itoa(%s), true)", childPath, field, imports.Strconv, index)
		if err := v.writeValueCheck(out, writer, actual.ElemType, item, childField, expr.IsObject(actual.ElemType.Type), depth+1); err != nil {
			return err
		}
		out.WriteString("}\n")
	case *expr.Map:
		key, item := fmt.Sprintf("key%d", depth), fmt.Sprintf("item%d", depth)
		fmt.Fprintf(out, "for %s, %s := range %s {\n", key, item, target)
		fmt.Fprintf(out, "_ = %s\n", item)
		fmt.Fprintf(out, "if !%s.ValidString(string(%s)) { return %s.Errorf(\"%%s: invalid UTF-8 map key\", %s) }\n", imports.UTF8, key, imports.Fmt, field)
		childField := fmt.Sprintf("%s(%s, string(%s), true)", childPath, field, key)
		if err := v.writeValueCheck(out, writer, actual.ElemType, item, childField, expr.IsObject(actual.ElemType.Type), depth+1); err != nil {
			return err
		}
		out.WriteString("}\n")
	case *expr.Union:
		if pointer {
			fmt.Fprintf(out, "if %s != nil {\n", target)
		}
		for _, branch := range actual.Values {
			name := writer.Field(branch.Attribute, branch.Name, true)
			item := fmt.Sprintf("branch%d", depth)
			fmt.Fprintf(out, "if %s, ok := %s.As%s(); ok {\n_ = %s\n", item, target, name, item)
			childField := fmt.Sprintf("%s(%s, %q, false)", childPath, field, actual.GetValueKey())
			if err := v.writeValueCheck(out, writer.Enter(attribute), branch.Attribute, item, childField,
				expr.IsObject(branch.Attribute.Type) || expr.IsUnion(branch.Attribute.Type), depth+1); err != nil {
				return err
			}
			out.WriteString("}\n")
		}
		if pointer {
			out.WriteString("}\n")
		}
	}
	return nil
}
