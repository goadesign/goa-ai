// Package schema defines the complete MCP Skill entry for protocol generation
// and the private runtime decoder. Each call returns a fresh graph so one
// service cannot change another service's fields or validations.
package schema

import "goa.design/goa/v3/expr"

// Entry returns the exact discovery record with open frontmatter and a native
// array-or-string union. The runtime checks directory membership and file bytes.
func Entry() *expr.UserTypeExpr {
	minimum := float64(0)
	file := &expr.UserTypeExpr{TypeName: "SkillFile", AttributeExpr: &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "uri", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Full resource URI of the file", Validation: &expr.ValidationExpr{Format: expr.FormatURI}}},
			{Name: "digest", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "SHA-256 of the raw file bytes", Validation: &expr.ValidationExpr{Pattern: `^sha256:[0-9a-f]{64}$`}}},
			{Name: "size", Attribute: &expr.AttributeExpr{Type: expr.Int64, Description: "Length of the raw file content in bytes", Validation: &expr.ValidationExpr{Minimum: &minimum}}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"uri", "digest", "size"}},
	}}
	manifest := &expr.UserTypeExpr{TypeName: "SkillResourcesManifest", AttributeExpr: &expr.AttributeExpr{
		Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: file}, NonNullableElems: true}, Description: "Every file belonging to this skill",
	}}
	dynamic := &expr.UserTypeExpr{TypeName: "SkillResourcesDynamic", AttributeExpr: &expr.AttributeExpr{
		Type: expr.String, Description: "Generated content without stable digests", Validation: &expr.ValidationExpr{Values: []any{"dynamic"}},
	}}
	return &expr.UserTypeExpr{TypeName: "SkillEntry", AttributeExpr: &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "uri", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Full resource URI of this skill's SKILL.md", Validation: &expr.ValidationExpr{Format: expr.FormatURI}}},
			{Name: "frontmatter", Attribute: &expr.AttributeExpr{Type: expr.Any, Description: "Every authored YAML frontmatter field preserved as an open JSON object", Meta: expr.MetaExpr{"struct:field:type": {"json.RawMessage", "encoding/json"}}}},
			{Name: "resources", Attribute: &expr.AttributeExpr{Type: &expr.Union{
				TypeName: "SkillResources", Untagged: true,
				Values: []*expr.NamedAttributeExpr{
					{Name: "manifest", Attribute: &expr.AttributeExpr{Type: manifest, Description: "Every file belonging to this skill"}},
					{Name: "dynamic", Attribute: &expr.AttributeExpr{Type: dynamic, Description: "Generated content without stable digests"}},
				},
			}, Description: "Complete file manifest or the literal dynamic"}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"uri", "frontmatter", "resources"}},
	}}
}
