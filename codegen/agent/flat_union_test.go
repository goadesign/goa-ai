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

func TestUntaggedUnionMetadataAndExamples(t *testing.T) {
	union := &goaexpr.Union{TypeName: "Choice", Untagged: true,
		Values: []*goaexpr.NamedAttributeExpr{
			{Name: "record", Attribute: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
				{Name: "recordKey", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
			}}},
			{Name: "text", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
			{Name: "count", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Int64}},
		},
	}
	attribute := &goaexpr.AttributeExpr{Type: union}
	fields := buildFieldMetadata(attribute)
	for _, field := range fields {
		assert.Empty(t, field.DiscriminatorValues)
		for _, branch := range field.Branches {
			assert.Empty(t, branch.Discriminator)
			assert.Empty(t, branch.Value)
			assert.Empty(t, branch.Path)
			assert.Contains(t, []string{"object", "string", "number"}, branch.JSONKind)
		}
	}
	for _, test := range []struct{ input, expected any }{
		{map[string]any{"recordKey": "selected"}, map[string]any{"record_key": "selected"}},
		{"selected", "selected"},
		{int64(7), int64(7)},
	} {
		canonical := specJSONModel.canonicalizeUnionExamples(attribute, test.input)
		assert.Equal(t, test.expected, specJSONModel.projectExampleFieldNames(attribute, canonical))
	}
}
