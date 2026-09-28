// These checks render transport fields and their validators from the same
// authored attributes. JSON renaming must not change the Go fields they access.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

func TestSchemaValidationUsesTransportFieldNames(t *testing.T) {
	for _, test := range []struct {
		name                                    string
		contract                                specJSONContract
		checksum, overridden, nested, unchanged string
	}{
		{"model", specJSONModel, "content_sha256", "authored_digest", "nested_entry", "unchanged_name"},
		{"native", specJSONNativeImage, "contentSHA256", "authoredDigest", "nestedEntry", "unchangedName"},
	} {
		t.Run(test.name, func(t *testing.T) {
			authoredMetadata := goaexpr.MetaExpr{
				"struct:field:name": {"PublishedSHA256"},
				"fixture:authored":  {"keep"},
			}
			rule := func(metadata goaexpr.MetaExpr) *goaexpr.AttributeExpr {
				return &goaexpr.AttributeExpr{
					Type:       goaexpr.String,
					Meta:       metadata,
					Validation: &goaexpr.ValidationExpr{Pattern: "^[a-f0-9]{64}$"},
				}
			}
			nested := &goaexpr.AttributeExpr{
				Type: &goaexpr.Object{
					{Name: "contentSHA256", Attribute: rule(nil)},
				},
				Validation: &goaexpr.ValidationExpr{Required: []string{"contentSHA256"}},
			}
			source := &goaexpr.AttributeExpr{
				Type: &goaexpr.Object{
					{Name: "contentSHA256", Attribute: rule(nil)},
					{Name: "authoredDigest", Attribute: rule(authoredMetadata.Dup())},
					{Name: "nestedEntry", Attribute: nested},
					{Name: "unchangedName", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
				},
				Validation: &goaexpr.ValidationExpr{
					Required: []string{"contentSHA256", "authoredDigest", "nestedEntry"},
				},
			}
			transport := test.contract.transportAttribute(source)
			schema := test.contract.schemaAttribute(transport)
			scope := goacodegen.NewNameScope()
			context := modelJSONTransportContext(scope, true, "")
			definition := transportTypeDef(scope, transport, context)
			validation := goacodegen.ValidationCode(schema, nil, context, true, false, false, "body")

			require.Contains(t, definition, "ContentSHA256 *string")
			require.Contains(t, definition, "PublishedSHA256 *string")
			require.Contains(t, definition, `json:"`+test.checksum+`"`)
			require.Contains(t, validation, "body.ContentSHA256")
			require.Contains(t, validation, "body.PublishedSHA256")
			require.Contains(t, validation, "body.NestedEntry.ContentSHA256")
			require.NotContains(t, validation, "ContentSha256")
			require.Contains(t, validation, `"body.`+test.checksum+`"`)
			require.Contains(t, validation, `"body.`+test.nested+`.`+test.checksum+`"`)
			require.Contains(t, validation, "goa.MissingFieldError")
			require.Contains(t, validation, "goa.ValidatePattern")

			schemaFields := goaexpr.AsObject(schema.Type)
			checksum := schemaFields.Attribute(test.checksum)
			override := schemaFields.Attribute(test.overridden)
			nestedChecksum := goaexpr.AsObject(schemaFields.Attribute(test.nested).Type).Attribute(test.checksum)
			require.Equal(t, "ContentSHA256", goacodegen.GoifyAtt(checksum, test.checksum, true))
			require.Equal(t, "ContentSHA256", goacodegen.GoifyAtt(nestedChecksum, test.checksum, true))
			for key, value := range authoredMetadata {
				require.Equal(t, value, override.Meta[key])
			}
			require.Empty(t, schemaFields.Attribute(test.unchanged).Meta["struct:field:name"])
			if test.contract == specJSONModel {
				require.Equal(t, []string{"contentSHA256"}, checksum.Meta["struct:field:name"])
				require.Equal(t, []string{"contentSHA256"}, nestedChecksum.Meta["struct:field:name"])
			} else {
				require.Empty(t, checksum.Meta["struct:field:name"])
				require.Empty(t, nestedChecksum.Meta["struct:field:name"])
			}
			require.Equal(t, []string{test.checksum, test.overridden, test.nested}, schema.Validation.Required)
			repeated := test.contract.schemaAttribute(schema)
			require.Equal(t, checksum.Meta, goaexpr.AsObject(repeated.Type).Attribute(test.checksum).Meta)
			sourceFields := goaexpr.AsObject(source.Type)
			require.Nil(t, sourceFields.Attribute("contentSHA256").Meta)
			require.Equal(t, authoredMetadata, sourceFields.Attribute("authoredDigest").Meta)
			require.Empty(t, goaexpr.AsObject(transport.Type).Attribute("contentSHA256").Meta["struct:field:name"])
		})
	}
}
