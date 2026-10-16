// These tests check that consumer selections keep method bindings and generated
// routes owned by the service that declares the provider methods.
package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestBoundSelectionKeepsProviderService(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func()
	}{
		{name: "all"},
		{name: "deferred", configure: func() { Deferred() }},
		{name: "bound", configure: func() { BindInput("read", "scope") }},
		{name: "bare", configure: func() { Tool("read") }},
		{name: "description", configure: func() { Tool("read", "Read a record.") }},
		{name: "callback", configure: func() { Tool("read", func() {}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := buildCompleteGeneratedFiles(t, func() {
				API("selection", func() {})
				shared := Toolset("records", func() {
					Tool("read", "Read a record.", func() { BindTo("provider", "read") })
					Tool("write", "Write a record.", func() { BindTo("provider", "write") })
				})
				Service("provider", func() {
					Method("read", func() {
						Payload(func() {
							Attribute("scope", String, "Record scope supplied by the application.")
							Required("scope")
						})
						Result(String)
					})
					Method("write", func() { Result(String) })
				})
				Service("consumer", func() {
					Agent("worker", "Use the provider records.", func() {
						if test.configure == nil {
							Use(shared)
						} else {
							Use(shared, test.configure)
						}
					})
				})
			})
			provider := fileContent(t, files, "gen/consumer/toolsets/records/specs.go")
			require.Contains(t, provider, "newSpecRead()")
			require.Contains(t, provider, "newSpecWrite()")
			registry := fileContent(t, files, "gen/consumer/agents/worker/registry.go")
			require.Contains(t, registry, "provider.records")
		})
	}
}
