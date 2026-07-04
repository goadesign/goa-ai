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
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						Args(func() {
							Attribute("retries", Int, "Retry count.")
							Attribute("query", String, "Search query.")
							Required("retries", "query")
						})
						Inject("retries")
					})
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `Inject field "retries" must be a String on the tool payload`)
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

// TestInjectRejectsMissingFieldOnBoundMethod proves the same generation-time
// contract applies to BindTo tools, validated against the bound method
// payload rather than the tool's own Args.
func TestInjectRejectsMissingFieldOnBoundMethod(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("atlas", func() {
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
