package ir_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	ir "goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestBuild_Deterministic(t *testing.T) {
	design := func() {
		API("svc", func() {})

		var QPayload = Type("QPayload", func() {
			Attribute("q", String, "Q")
			Required("q")
		})
		var OkResult = Type("OkResult", func() {
			Attribute("ok", Boolean, "OK")
		})
		Service("svc", func() {
			Method("Do", func() {
				Payload(func() {
					Attribute("q", String, "Q")
					Required("q")
				})
				Result(func() {
					Attribute("ok", Boolean, "OK")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Use("lookup", func() {
					Tool("by_id", "Lookup by ID", func() {
						Args(QPayload)
						Return(OkResult)
						BindTo("Do")
					})
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	a, err := ir.Build(genpkg, roots)
	require.NoError(t, err)
	b, err := ir.Build(genpkg, roots)
	require.NoError(t, err)

	aj, err := json.Marshal(a)
	require.NoError(t, err)
	bj, err := json.Marshal(b)
	require.NoError(t, err)
	require.Equal(t, string(aj), string(bj))
}

func TestBuild_ToolsetOwnership_ServiceLexicographic(t *testing.T) {
	design := func() {
		API("multi", func() {})

		var Shared = Toolset("shared", func() {
			Tool("ping", "Ping", func() {
				Args(func() {
					Attribute("msg", String, "Message")
					Required("msg")
				})
				Return(func() {
					Attribute("ok", Boolean, "OK")
					Required("ok")
				})
			})
		})

		Service("bravo", func() {
			Agent("b", "B", func() {
				Use(Shared, func() {
					Tool("ping")
				})
			})
		})
		Service("alpha", func() {
			Agent("a", "A", func() {
				Use(Shared, func() {
					Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := ir.Build(genpkg, roots)
	require.NoError(t, err)

	require.Len(t, got.Toolsets, 1)
	ts := got.Toolsets[0]
	require.Equal(t, "shared", ts.Name)
	require.Equal(t, ir.OwnerKindService, ts.Owner.Kind)
	require.Equal(t, "alpha", ts.Owner.ServiceName)
	require.Equal(t, "alpha", ts.Owner.ServicePathName)
}

func TestBuild_ToolsetOwnership_ExportWins(t *testing.T) {
	design := func() {
		API("multi", func() {})

		var Shared = Toolset("shared", func() {
			Tool("ping", "Ping", func() {
				Args(func() {
					Attribute("msg", String, "Message")
					Required("msg")
				})
				Return(func() {
					Attribute("ok", Boolean, "OK")
					Required("ok")
				})
			})
		})

		Service("bravo", func() {
			Agent("provider", "Provider", func() {
				Export(Shared, func() {
					Tool("ping")
				})
			})
		})
		Service("alpha", func() {
			Agent("consumer", "Consumer", func() {
				Use(Shared, func() {
					Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := ir.Build(genpkg, roots)
	require.NoError(t, err)

	require.Len(t, got.Toolsets, 1)
	ts := got.Toolsets[0]
	require.Equal(t, "shared", ts.Name)
	require.Equal(t, ir.OwnerKindAgentExport, ts.Owner.Kind)
	require.Equal(t, "bravo", ts.Owner.ServiceName)
	require.Equal(t, "provider", ts.Owner.AgentName)
	require.NotEmpty(t, ts.Owner.AgentSlug)
}
