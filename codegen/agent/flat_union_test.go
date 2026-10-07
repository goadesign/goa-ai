// These checks keep model field metadata and authored examples aligned with
// Goa's selected union JSON mapping. A discriminator determines the branch;
// generation never guesses a flat branch from whichever fields happen to exist.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goaexpr "goa.design/goa/v3/expr"
)

func TestFlatUnionMetadataAndExamples(t *testing.T) {
	for _, flat := range []bool{false, true} {
		union := &goaexpr.Union{
			TypeName: "Outcome", TypeKey: "resultType", Flatten: flat,
			Values: []*goaexpr.NamedAttributeExpr{{Name: "complete", Attribute: &goaexpr.AttributeExpr{
				Type: &goaexpr.Object{{Name: "referenceId", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}}},
			}}},
		}
		attribute := &goaexpr.AttributeExpr{Type: union}
		fields := buildFieldMetadata(attribute)
		path := []fieldPathSegmentData{{Name: "referenceId"}}
		if !flat {
			path = append([]fieldPathSegmentData{{Name: "value"}}, path...)
		}
		var reference *fieldMetadataData
		for _, field := range fields {
			if generatedFieldMetadataKey(field.Path, field.Branches) == generatedFieldMetadataKey(path, []unionBranchData{{Discriminator: []fieldPathSegmentData{{Name: "resultType"}}, Value: "complete"}}) {
				reference = field
			}
		}
		require.NotNil(t, reference)
		assert.Equal(t, "string", reference.JSONType)
		var example any = map[string]any{"resultType": "complete", "referenceId": "done"}
		var expected any = map[string]any{"resultType": "complete", "reference_id": "done"}
		if !flat {
			example = map[string]any{"resultType": "complete", "value": map[string]any{"referenceId": "done"}}
			expected = map[string]any{"resultType": "complete", "value": map[string]any{"reference_id": "done"}}
		}
		canonical := specJSONModel.canonicalizeUnionExamples(attribute, example)
		projected := specJSONModel.projectExampleFieldNames(attribute, canonical)
		assert.Equal(t, expected, projected)
	}
}

func TestFlatUnionExampleRequiresDiscriminator(t *testing.T) {
	union := &goaexpr.Union{TypeName: "Outcome", Flatten: true,
		Values: []*goaexpr.NamedAttributeExpr{{Name: "complete", Attribute: &goaexpr.AttributeExpr{
			Type: &goaexpr.Object{{Name: "reference", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}}},
		}}},
	}
	assert.PanicsWithValue(t, `agent/specs_builder: flat union example for "Outcome" must name its discriminator`, func() {
		specJSONModel.canonicalizeUnionExamples(&goaexpr.AttributeExpr{Type: union}, map[string]any{"reference": "done"})
	})
}
