package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
)

// TestInjectAcceptsArbitraryLabelBackedName proves the generalized DSL
// surface: any name not resolving to a ToolCallMeta field (see
// codegen/agent/inject.go's metaFieldByGoName) is accepted as a label-backed
// injected field as long as it exists, is required, and is a String.
func TestInjectAcceptsArbitraryLabelBackedName(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("household_id", String, "Household scope.")
							Attribute("query", String, "Search query.")
							Required("household_id", "query")
						})
						Inject("household_id")
					})
				})
			})
		})
	})

	tool := agentsexpr.Root.Agents[0].Used.Toolsets[0].Tools[0]
	require.Equal(t, []string{"household_id"}, tool.InjectedFields)
}

// TestInjectAcceptsExistingSessionIDDesignUnchanged proves the non-breaking
// constraint: the reference consumer's Inject("session_id") design regens
// with zero design edits.
func TestInjectAcceptsExistingSessionIDDesignUnchanged(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("session_id", String, "Server-injected session identifier.")
							Attribute("query", String, "Search query.")
							Required("session_id", "query")
						})
						Inject("session_id")
					})
				})
			})
		})
	})

	tool := agentsexpr.Root.Agents[0].Used.Toolsets[0].Tools[0]
	require.Equal(t, []string{"session_id"}, tool.InjectedFields)
}

// TestInjectAcceptsNamedString verifies that a named Goa String remains a
// valid injected field.
func TestInjectAcceptsNamedString(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		runtimeSessionID := Type("RuntimeSessionID", String)
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("session_id", runtimeSessionID, "Server-injected session identifier.")
							Required("session_id")
						})
						Inject("session_id")
					})
				})
			})
		})
	})
}

// TestInjectRejectsCustomGoFieldType verifies that Inject does not emit a
// string conversion for a field whose Goa design replaces its Go type.
func TestInjectRejectsCustomGoFieldType(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("session_id", String, "Server-injected session identifier.", func() {
								Meta("struct:field:type", "time.Time", "time")
							})
							Required("session_id")
						})
						Inject("session_id")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "session_id" on the tool payload uses struct:field:type "time.Time"`)
}

// TestInjectRejectsCustomGoFieldTypeOnBoundMethod verifies that the same
// design error is reported when BindTo supplies the effective tool input.
func TestInjectRejectsCustomGoFieldTypeOnBoundMethod(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Method("lookup", func() {
				Payload(func() {
					Attribute("session_id", String, "Server-injected session identifier.", func() {
						Meta("struct:field:type", "time.Time", "time")
					})
					Required("session_id")
				})
				Result(String)
			})
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						BindTo("lookup")
						Inject("session_id")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "session_id" on the bound method payload uses struct:field:type "time.Time"`)
}

// TestInjectRejectsNamedStringWithCustomGoType verifies that a Go type
// override cannot be hidden inside an otherwise valid named Goa String.
func TestInjectRejectsNamedStringWithCustomGoType(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		runtimeSessionID := Type("RuntimeSessionID", String, func() {
			Meta("struct:field:type", "time.Time", "time")
		})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("session_id", runtimeSessionID, "Server-injected session identifier.")
							Required("session_id")
						})
						Inject("session_id")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "session_id" on the tool payload uses struct:field:type "time.Time"`)
}

func TestInjectRejectsMissingField(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("query", String, "Search query.")
							Required("query")
						})
						Inject("household_id")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "household_id" does not exist on the tool payload`)
}

func TestInjectRejectsOptionalField(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("household_id", String, "Household scope.")
							Attribute("query", String, "Search query.")
							Required("query")
						})
						Inject("household_id")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "household_id" must be required on the tool payload`)
}

func TestInjectRejectsNonStringField(t *testing.T) {
	tests := []struct {
		name      string
		fieldType func() any
	}{
		{name: "array", fieldType: func() any { return ArrayOf(String) }},
		{name: "named number", fieldType: func() any { return Type("RetryCount", Int) }},
		{name: "number", fieldType: func() any { return Int }},
		{name: "object", fieldType: func() any {
			return Type("Scope", func() {
				Attribute("name", String)
			})
		}},
		{name: "result type", fieldType: func() any {
			return ResultType("application/vnd.inject-result", func() {
				Attribute("value", String)
				View("default", func() {
					Attribute("value")
				})
			})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				API("test", func() {})
				fieldType := test.fieldType()
				Service("calc", func() {
					Agent("scribe", "Doc helper", func() {
						Use("helpers", func() {
							Tool("lookup", "Lookup", func() {
								Args(func() {
									Attribute("value", fieldType, "Server-injected value.")
									Required("value")
								})
								Inject("value")
							})
						})
					})
				})
			})

			require.Error(t, err)
			require.ErrorContains(t, err, `Inject field "value" must be a String on the tool payload`)
		})
	}
}

func TestInjectRejectsDuplicateNames(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("session_id", String, "Server-injected session identifier.")
							Attribute("query", String, "Search query.")
							Required("session_id", "query")
						})
						Inject("session_id", "session_id")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "session_id" is declared more than once`)
}

func TestInjectRejectsEmptyName(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("query", String, "Search query.")
							Required("query")
						})
						Inject("")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "Inject requires non-empty field names")
}

// TestInjectRejectsPagingCursor prevents continuation handling and provider
// injection from both supplying the same field.
func TestInjectRejectsPagingCursor(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Agent("scribe", "Search helper", func() {
				Use("helpers", func() {
					Tool("search", "Search", func() {
						Args(func() {
							Attribute("query", String)
							Required("query")
						})
						BoundedResult(func() {
							ContinueWith("continue_search", "cursor")
							NextCursor("next_cursor")
						})
					})
					Tool("continue_search", "Continue search", func() {
						Args(func() {
							Attribute("cursor", String)
							Required("cursor")
						})
						Inject("cursor")
						BoundedResult(func() {
							Cursor("cursor")
							NextCursor("next_cursor")
						})
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "cursor" cannot be the paging cursor`)
}

// TestInjectRejectsFieldMissingFromDivergentArgs reproduces the
// generation-soundness gap: a BindTo tool with explicit Args that
// structurally diverge from the bound method payload (the
// MethodComplexEmbedded / PayloadAliasesMethod=false shape) injecting a name
// that exists on the method payload but not on Args. Codegen resolves
// injection against the effective tool Args, so without this eval-time error
// the generated inject.go would assign a field the tool payload struct does
// not have — a generated-code compile failure instead of a design-time
// diagnostic.
func TestInjectRejectsFieldMissingFromDivergentArgs(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		var Profile = Type("Profile", func() {
			Attribute("session_id", String, "Server-injected session identifier.")
			Attribute("id", String, "Identifier")
			Required("session_id", "id")
		})
		var UpsertArgs = Type("UpsertArgs", func() {
			Attribute("profile_id", String, "Profile identifier")
			Required("profile_id")
		})
		Service("alpha", func() {
			Method("UpsertProfile", func() {
				Payload(Profile)
				Result(Profile)
			})
			Agent("scribe", "Profile helper", func() {
				Use("profiles", func() {
					Tool("upsert", "Upsert a profile", func() {
						Args(UpsertArgs)
						Return(Profile)
						BindTo("alpha", "UpsertProfile")
						Inject("session_id")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "session_id" does not exist on the tool Args even though the bound method payload defines it; the two shapes diverge`)
}

// TestInjectAcceptsFieldOnBothDivergentShapes is the positive counterpart:
// a divergent-Args bound tool may inject a name declared (required String)
// on BOTH the tool Args and the bound method payload, since both generated
// surfaces can then populate it.
func TestInjectAcceptsFieldOnBothDivergentShapes(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		var Profile = Type("Profile", func() {
			Attribute("session_id", String, "Server-injected session identifier.")
			Attribute("id", String, "Identifier")
			Required("session_id", "id")
		})
		var UpsertArgs = Type("UpsertArgs", func() {
			Attribute("session_id", String, "Server-injected session identifier.")
			Attribute("profile_id", String, "Profile identifier")
			Required("session_id", "profile_id")
		})
		Service("alpha", func() {
			Method("UpsertProfile", func() {
				Payload(Profile)
				Result(Profile)
			})
			Agent("scribe", "Profile helper", func() {
				Use("profiles", func() {
					Tool("upsert", "Upsert a profile", func() {
						Args(UpsertArgs)
						Return(Profile)
						BindTo("alpha", "UpsertProfile")
						Inject("session_id")
					})
				})
			})
		})
	})

	tool := agentsexpr.Root.Agents[0].Used.Toolsets[0].Tools[0]
	require.Equal(t, []string{"session_id"}, tool.InjectedFields)
}

// TestInjectAcceptsLabelBackedFieldOnBoundTool proves registry-served BindTo
// tools may receive immutable run labels through ToolCallMeta.
func TestInjectAcceptsLabelBackedFieldOnBoundTool(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("catalog", func() {
			Method("get_data", func() {
				Payload(func() {
					Attribute("household_id", String, "Household scope.")
					Attribute("query", String, "Search query.")
					Required("household_id", "query")
				})
				Result(func() {
					Attribute("ok", Boolean, "OK")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("get_data", "Get data", func() {
						BindTo("get_data")
						Inject("household_id")
					})
				})
			})
		})
	})

	require.NoError(t, err)
	tool := agentsexpr.Root.Agents[0].Used.Toolsets[0].Tools[0]
	require.Equal(t, []string{"household_id"}, tool.InjectedFields)
}

// TestInjectRejectsMissingFieldOnBoundMethod proves the same generation-time
// contract applies to BindTo tools, validated against the bound method
// payload rather than the tool's own Args.
func TestInjectRejectsMissingFieldOnBoundMethod(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("catalog", func() {
			Method("get_data", func() {
				Payload(func() {
					Attribute("query", String, "Search query.")
					Required("query")
				})
				Result(func() {
					Attribute("ok", Boolean, "OK")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("get_data", "Get data", func() {
						BindTo("get_data")
						Inject("household_id")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "household_id" does not exist on the bound method payload`)
}
