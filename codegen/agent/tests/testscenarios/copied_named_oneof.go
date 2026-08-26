// Package testscenarios defines a generated-code scenario where two nested
// result types reuse one externally located union.
package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
	goaexpr "goa.design/goa/v3/expr"
)

// CopiedNamedOneOf returns one method result containing two local types that
// each contain a separate copy of the same externally located union type.
func CopiedNamedOneOf() func() {
	return func() {
		API("copied named oneof", func() {})

		sharedFilter := copiedNamedOneOfFilter()
		firstFilter := goaexpr.DupAtt(&goaexpr.AttributeExpr{Type: sharedFilter}).Type
		secondFilter := goaexpr.DupAtt(&goaexpr.AttributeExpr{Type: sharedFilter}).Type
		firstResult := copiedNamedOneOfResult(
			"FirstResult",
			"first-result",
			"Filter used for the first result.",
			firstFilter,
		)
		secondResult := copiedNamedOneOfResult(
			"SecondResult",
			"second-result",
			"Filter used for the second result.",
			secondFilter,
		)
		result := &goaexpr.UserTypeExpr{
			TypeName: "CopiedResults",
			UID:      "copied-results",
			AttributeExpr: &goaexpr.AttributeExpr{
				Type: &goaexpr.Object{
					{
						Name: "first",
						Attribute: &goaexpr.AttributeExpr{
							Description: "First result and its selected filter.",
							Type:        firstResult,
						},
					},
					{
						Name: "second",
						Attribute: &goaexpr.AttributeExpr{
							Description: "Second result and its selected filter.",
							Type:        secondResult,
						},
					},
				},
				Validation: &goaexpr.ValidationExpr{Required: []string{"first", "second"}},
			},
		}

		Service("records", func() {
			Method("Evaluate", func() {
				Result(result)
			})
			aidsl.Agent("reader", "Reads records with a selected filter.", func() {
				aidsl.Use("records", func() {
					aidsl.Tool("evaluate", "Returns both filtered results.", func() {
						aidsl.BindTo("Evaluate")
					})
				})
			})
		})
	}
}

// copiedNamedOneOfFilter builds the authored external type before making two
// independent expression copies for the result fields.
func copiedNamedOneOfFilter() goaexpr.UserType {
	filterName := &goaexpr.UserTypeExpr{
		TypeName: "FilterName",
		UID:      "filter-name",
		AttributeExpr: &goaexpr.AttributeExpr{
			Description: "Name that returned records must match.",
			Type:        goaexpr.String,
			Meta:        goaexpr.MetaExpr{"struct:pkg:path": {"types"}},
		},
	}
	matchAll := &goaexpr.UserTypeExpr{
		TypeName: "MatchAll",
		UID:      "match-all",
		AttributeExpr: &goaexpr.AttributeExpr{
			Description: "A selected filter branch with no additional value.",
			Type:        &goaexpr.Object{},
			Meta:        goaexpr.MetaExpr{"struct:pkg:path": {"types"}},
		},
	}
	return &goaexpr.UserTypeExpr{
		TypeName: "SharedFilter",
		UID:      "shared-filter",
		AttributeExpr: &goaexpr.AttributeExpr{
			Description: "A filter shared by both result types.",
			Type: &goaexpr.Object{
				{
					Name: "filter",
					Attribute: &goaexpr.AttributeExpr{
						Description: "Selected filter value.",
						Type: &goaexpr.Union{
							TypeName: "Filter",
							Values: []*goaexpr.NamedAttributeExpr{
								{
									Name: "name",
									Attribute: &goaexpr.AttributeExpr{
										Description: "Name that returned records must match.",
										Type:        filterName,
									},
								},
								{
									Name: "all",
									Attribute: &goaexpr.AttributeExpr{
										Description: "Match every record.",
										Type:        matchAll,
									},
								},
							},
						},
					},
				},
			},
			Validation: &goaexpr.ValidationExpr{Required: []string{"filter"}},
			Meta:       goaexpr.MetaExpr{"struct:pkg:path": {"types"}},
		},
	}
}

// copiedNamedOneOfResult builds one local result around one copied external
// filter expression.
func copiedNamedOneOfResult(name, id, fieldDescription string, filter goaexpr.DataType) goaexpr.UserType {
	return &goaexpr.UserTypeExpr{
		TypeName: name,
		UID:      id,
		AttributeExpr: &goaexpr.AttributeExpr{
			Type: &goaexpr.Object{
				{
					Name: "filter",
					Attribute: &goaexpr.AttributeExpr{
						Description: fieldDescription,
						Type:        filter,
					},
				},
			},
			Validation: &goaexpr.ValidationExpr{Required: []string{"filter"}},
		},
	}
}
