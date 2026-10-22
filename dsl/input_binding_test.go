// These tests keep consumer input choices off shared provider declarations and
// reject names that cannot identify one generated binding.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
)

func TestBindInputBelongsToConsumer(t *testing.T) {
	runDSL(t, func() {
		shared := Toolset("records", func() {
			Tool("read", "Read a record.", func() {
				Args(func() {
					Attribute("scope", String)
					Attribute("query", String)
					Required("scope", "query")
				})
				Return(String)
			})
		})
		Service("records", func() {
			Agent("bound", "Read in a known scope.", func() {
				Use(shared, func() { BindInput("read", "scope") })
			})
			Agent("direct", "Choose a scope.", func() { Use(shared) })
			Export(shared)
		})
	})
	bound := agentsexpr.Root.Agents[0].Used.Toolsets[0]
	require.Len(t, bound.InputBindings, 1)
	assert.Equal(t, "read", bound.InputBindings[0].Tool)
	assert.Equal(t, []string{"scope"}, bound.InputBindings[0].Fields)
	assert.Empty(t, agentsexpr.Root.Agents[1].Used.Toolsets[0].InputBindings)
	assert.Empty(t, agentsexpr.Root.Toolsets[0].InputBindings)
	assert.Empty(t, agentsexpr.Root.ServiceExports[0].Toolsets[0].InputBindings)
}

func TestBindInputRejectsAmbiguousDeclarations(t *testing.T) {
	for _, test := range []struct {
		name string
		bind func()
		want string
	}{
		{"empty tool", func() { BindInput("", "scope") }, "requires a tool name"},
		{"no fields", func() { BindInput("read") }, "at least one required field"},
		{"empty field", func() { BindInput("read", "") }, "field name cannot be empty"},
		{"duplicate field", func() { BindInput("read", "scope", "scope") }, "field \"scope\" is repeated"},
		{"duplicate tool", func() { BindInput("read", "scope"); BindInput("read", "query") }, "tool \"read\" is repeated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				shared := Toolset("records", func() { Tool("read", "Read a record.") })
				Service("records", func() {
					Agent("reader", "Read records.", func() { Use(shared, test.bind) })
				})
			})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestBindInputRejectsUnknownProviderContract(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "remote"}[remote], func(t *testing.T) {
			err := runDSLWithError(t, func() {
				var source any = "records"
				if remote {
					registry := Registry("company", func() { URL("https://registry.example.invalid") })
					source = Toolset(FromRegistry(registry, "records"))
				}
				Service("records", func() {
					Agent("reader", "Read records.", func() {
						Use(source, func() { BindInput("read", "scope") })
					})
				})
			})
			require.Error(t, err)
		})
	}
}

func TestBindInputRejectsProviderDeclaration(t *testing.T) {
	err := runDSLWithError(t, func() {
		Toolset("records", func() { BindInput("read", "scope") })
	})
	require.ErrorContains(t, err, "must appear inside Use")
}

func TestBindFieldAndLookupBelongToConsumer(t *testing.T) {
	runDSL(t, func() {
		shared := Toolset("records", func() {
			Tool("read", "Read a record.", func() {
				Args(func() {
					Attribute("scope", String)
					Attribute("query", String)
					Required("scope", "query")
				})
				Return(String)
			})
		})
		Service("records", func() {
			Agent("bound", "Read in a known or looked-up scope.", func() {
				Use(shared, func() {
					BindField("scope", func() {
						Lookup(String, "Selected scope name.", func() {
							MinLength(1)
							Example("North")
						})
					})
				})
			})
			Agent("direct", "Choose a scope.", func() { Use(shared) })
		})
	})
	bound := agentsexpr.Root.Agents[0].Used.Toolsets[0]
	require.Len(t, bound.FieldBindings, 1)
	assert.Equal(t, "scope", bound.FieldBindings[0].Field)
	require.NotNil(t, bound.FieldBindings[0].Lookup)
	assert.Equal(t, "Selected scope name.", bound.FieldBindings[0].Lookup.Description)
	assert.Empty(t, agentsexpr.Root.Agents[1].Used.Toolsets[0].FieldBindings)
	assert.Empty(t, agentsexpr.Root.Toolsets[0].FieldBindings)
}

func TestBindFieldRejectsInvalidDeclarations(t *testing.T) {
	for _, test := range []struct {
		name string
		bind func()
		want string
	}{
		{"empty field", func() { BindField("") }, "field name cannot be empty"},
		{"duplicate field", func() { BindField("scope"); BindField("scope") }, "field \"scope\" is repeated"},
		{"lookup outside BindField", func() { Lookup(String) }, "must appear inside BindField"},
		{"duplicate lookup", func() { BindField("scope", func() { Lookup(String); Lookup(String) }) }, "can only be declared once per BindField"},
		{"non-string lookup", func() { BindField("scope", func() { Lookup(Int) }) }, "Lookup key type must be String"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				shared := Toolset("records", func() { Tool("read", "Read a record.") })
				Service("records", func() {
					Agent("reader", "Read records.", func() { Use(shared, test.bind) })
				})
			})
			require.ErrorContains(t, err, test.want)
		})
	}
}
