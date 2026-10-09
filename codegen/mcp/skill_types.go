// Package codegen defines the Skills extension's discovery values as Goa types.
// Generated clients retain complete manifests and arbitrary frontmatter fields;
// ordinary configured endpoints still own lookup and resource authorization.
package codegen

import (
	"goa.design/goa-ai/internal/mcpskills/schema"
	"goa.design/goa/v3/expr"
)

// buildSkillsMethods emits only the paired discovery methods declared by the
// service. They return complete values and do not perform resource reads.
func (b *mcpExprBuilder) buildSkillsMethods() []*expr.MethodExpr {
	return []*expr.MethodExpr{
		{
			Name: "skills/list", Description: "List complete skill entries visible to this client without reading their files",
			Payload: b.userTypeAttr("SkillsListPayload", b.buildListPayloadType),
			Result:  b.userTypeAttr("SkillsListResult", b.buildSkillsListResultType),
			Errors:  buildMCPMethodErrors(mcpInvalidParamsError),
		},
		{
			Name: "skills/get", Description: "Look up a complete skill entry by its SKILL.md URI independently of listing",
			Payload: b.userTypeAttr("SkillsGetPayload", func() *expr.AttributeExpr {
				return &expr.AttributeExpr{
					Type:       &expr.Object{{Name: "uri", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Exact SKILL.md resource URI", Validation: &expr.ValidationExpr{Format: expr.FormatURI}}}},
					Validation: &expr.ValidationExpr{Required: []string{"uri"}},
				}
			}),
			Result: b.userTypeAttr("SkillsGetResult", func() *expr.AttributeExpr {
				return &expr.AttributeExpr{
					Type:       &expr.Object{{Name: "skill", Attribute: &expr.AttributeExpr{Type: b.getOrCreateType("SkillEntry", b.buildSkillEntryType), Description: "Complete entry for the requested URI"}}},
					Validation: &expr.ValidationExpr{Required: []string{"skill"}},
				}
			}),
			Errors: buildMCPMethodErrors(mcpInvalidParamsError),
		},
	}
}

// buildSkillsListResultType preserves whole entries within each catalog page.
func (b *mcpExprBuilder) buildSkillsListResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "skills", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("SkillEntry", b.buildSkillEntryType)}, NonNullableElems: true}, Description: "Complete skill entries in this catalog page"}},
			{Name: "nextCursor", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Opaque cursor for the next page"}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"skills"}},
	}
}

// buildSkillEntryType uses the same discovery contract as the runtime decoder.
// Authored application types still use Goa's ordinary conversion planner.
func (b *mcpExprBuilder) buildSkillEntryType() *expr.AttributeExpr {
	return schema.Entry().AttributeExpr
}
