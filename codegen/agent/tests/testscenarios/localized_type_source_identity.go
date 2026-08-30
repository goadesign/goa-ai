// Package testscenarios defines Goa designs used by generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
	goaexpr "goa.design/goa/v3/expr"
)

// LocalizedTypeSourceIdentity defines two distinct nested types whose names
// become the same Go name and repeats the first type in a second field.
func LocalizedTypeSourceIdentity() func() {
	return func() {
		API("alpha", func() {})

		firstFilter := localizedFilter("FirstFilter", "first-filter")
		secondFilter := localizedFilter("SecondFilter", "second-filter")
		firstInput := &goaexpr.UserTypeExpr{
			TypeName: "FirstInput",
			UID:      "first-input",
			AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
				{Name: "primary", Attribute: &goaexpr.AttributeExpr{Type: firstFilter}},
				{Name: "copy", Attribute: &goaexpr.AttributeExpr{Type: firstFilter}},
			}, Validation: &goaexpr.ValidationExpr{Required: []string{"primary", "copy"}}},
		}
		secondInput := &goaexpr.UserTypeExpr{
			TypeName: "SecondInput",
			UID:      "second-input",
			AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
				{Name: "filter", Attribute: &goaexpr.AttributeExpr{Type: secondFilter}},
			}, Validation: &goaexpr.ValidationExpr{Required: []string{"filter"}}},
		}

		Service("alpha", func() {
			Agent("worker", "Checks nested generated type names.", func() {
				Use("filters", func() {
					Tool("first", "Uses one nested type twice.", func() {
						Args(firstInput)
					})
					Tool("second", "Uses a distinct same-named nested type.", func() {
						Args(secondInput)
					})
				})
			})
		})
	}
}

// localizedFilter returns one independent source type for the generated-name
// collision test.
func localizedFilter(serviceName, id string) *goaexpr.UserTypeExpr {
	return &goaexpr.UserTypeExpr{
		TypeName: "Filter",
		UID:      id,
		AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{
			{Name: "value", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		}, Validation: &goaexpr.ValidationExpr{Required: []string{"value"}}, Meta: goaexpr.MetaExpr{"struct:type:name": {serviceName}}},
	}
}
