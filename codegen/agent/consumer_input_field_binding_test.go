package codegen_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
)

// TestConsumerFieldBindingUniformAndHeterogeneous verifies that toolset-level
// BindField declarations emit toolset-wide Bind<Field>Input / Bind<Field>Lookup
// helpers only when all matched tools share an equivalent Go type for the bound
// field. When tools declare distinct Go types (such as ScopeRef vs
// ScopeSelector), the generator omits toolset-wide wrappers and emits per-tool
// binding functions with each tool's exact Go type.
func TestConsumerFieldBindingUniformAndHeterogeneous(t *testing.T) {
	for _, tc := range []struct {
		name        string
		distinct    bool
		wantPlanned bool
	}{
		{
			name:        "uniform field layout",
			distinct:    false,
			wantPlanned: true,
		},
		{
			name:        "heterogeneous field layouts",
			distinct:    true,
			wantPlanned: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roots := testhelpers.SetupEvalRoots(t)
			design := func() {
				goadsl.API("records_api", func() {})
				scopeRef := goadsl.Type("ScopeRef", func() {
					goadsl.Attribute("id", goadsl.String, "Scope ID.")
					goadsl.Required("id")
				})
				scopeSelector := goadsl.Type("ScopeSelector", func() {
					goadsl.Attribute("region", goadsl.String, "Region code.")
					goadsl.Attribute("ids", goadsl.ArrayOf(goadsl.String), "Scope IDs.")
					goadsl.Required("region", "ids")
				})

				provider := Toolset("records", func() {
					Tool("read", "Read single scope.", func() {
						Args(func() {
							goadsl.Attribute("scope", scopeRef, "Target scope.")
							goadsl.Attribute("query", goadsl.String, "Query string.")
							goadsl.Required("scope", "query")
						})
						Return(goadsl.String)
					})
					Tool("batch_read", "Read multiple scopes.", func() {
						Args(func() {
							if tc.distinct {
								goadsl.Attribute("scope", scopeSelector, "Target scopes.")
							} else {
								goadsl.Attribute("scope", scopeRef, "Target scope.")
							}
							goadsl.Attribute("query", goadsl.String, "Query string.")
							goadsl.Required("scope", "query")
						})
						Return(goadsl.String)
					})
				})

				goadsl.Service("consumer", func() {
					Agent("bound", "Consumes records.", func() {
						Use(provider, func() {
							BindField("scope", func() {
								Lookup(goadsl.String, "Scope name lookup.")
							})
						})
					})
				})
			}

			require.True(t, eval.Execute(design, nil), eval.Context.Error())
			require.NoError(t, eval.RunDSL())

			plan, err := codegen.BuildAgentPlanForTest("example.com/records", roots)
			require.NoError(t, err)

			bindings := codegen.ConsumerFieldBindingsForTest(plan)
			require.Len(t, bindings, 1)
			assert.Equal(t, "scope", bindings[0].Field)

			files, err := plan.Files(nil)
			require.NoError(t, err)

			bindingFile := filepath.ToSlash("gen/consumer/agents/bound/records/inputbinding/binding.go")
			content := testhelpers.FileContent(t, files, bindingFile)
			require.NotEmpty(t, content, "expected generated binding.go at %s", bindingFile)

			if tc.wantPlanned {
				assert.True(t, bindings[0].HasLocal, "uniform field binding should plan group.local")
				assert.True(t, bindings[0].HasLookup, "uniform field binding should plan group.lookupLocal")
				assert.Equal(t, "BindScopeInput", bindings[0].LocalName)
				assert.Equal(t, "BindScopeLookup", bindings[0].LookupName)

				// Toolset-wide wrappers emitted
				assert.Contains(t, content, "func BindScopeInput(")
				assert.Contains(t, content, "func BindScopeSourceInput(")
				assert.Contains(t, content, "func BindScopeLookup(")
				assert.Contains(t, content, "func BindScopeSourceLookup(")

				// Per-tool binding functions emitted
				assert.Contains(t, content, "func BindReadInput(")
				assert.Contains(t, content, "func BindBatchReadInput(")
			} else {
				assert.False(t, bindings[0].HasLocal, "heterogeneous field binding should not plan group.local")
				assert.False(t, bindings[0].HasLookup, "heterogeneous field binding should not plan group.lookupLocal")
				assert.Empty(t, bindings[0].LocalName)
				assert.Empty(t, bindings[0].LookupName)

				// Toolset-wide wrappers omitted
				assert.NotContains(t, content, "func BindScopeInput(")
				assert.NotContains(t, content, "func BindScopeSourceInput(")
				assert.NotContains(t, content, "func BindScopeLookup(")
				assert.NotContains(t, content, "func BindScopeSourceLookup(")

				// Per-tool binding functions emitted with respective Go types
				assert.Contains(t, content, "func BindReadInput(catalog *runtime.RegistryCatalog, boundScope *records.ScopeRef)")
				assert.Contains(t, content, "func BindReadLookup(catalog *runtime.RegistryCatalog, table map[string]*records.ScopeRef)")
				assert.Contains(t, content, "func BindBatchReadInput(catalog *runtime.RegistryCatalog, boundScope *records.ScopeSelector)")
				assert.Contains(t, content, "func BindBatchReadLookup(catalog *runtime.RegistryCatalog, table map[string]*records.ScopeSelector)")
			}
		})
	}
}
