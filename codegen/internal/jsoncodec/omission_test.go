// These tests compile complete-value codecs and compare their saved JSON with
// the authored omission rules, including nested values and explicit zero values.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
)

func TestGeneratedStandaloneCodecPreservesAuthoredOmission(t *testing.T) {
	files, err := generate(t, func() {
		flag := dsl.Type("Flag", dsl.Boolean)
		policy := located("Policy", func() {
			dsl.Attribute("Enabled", dsl.Boolean, func() {
				dsl.Default(false)
				dsl.Meta("struct:tag:json", "enabled,omitempty")
			})
			dsl.Attribute("Count", dsl.Int, func() {
				dsl.Default(0)
				dsl.Meta("struct:tag:json", "count", "omitempty")
			})
			dsl.Attribute("Ratio", dsl.Float64, func() {
				dsl.Default(0.0)
				dsl.Meta("struct:tag:json", "ratio,omitempty")
			})
			dsl.Attribute("Title", dsl.String, func() {
				dsl.Default("")
				dsl.Meta("struct:tag:json", "title,omitempty")
			})
			dsl.Attribute("Flag", flag, func() {
				dsl.Default(false)
				dsl.Meta("struct:tag:json", "flag,omitempty")
			})
			dsl.Attribute("Required", dsl.Boolean, func() {
				dsl.Default(false)
				dsl.Meta("struct:tag:json", "required,omitempty")
			})
			dsl.Attribute("Explicit", dsl.Boolean, func() {
				dsl.Meta("struct:tag:json", "explicit,omitempty")
			})
			dsl.Attribute("DefaultTrue", dsl.Boolean, func() {
				dsl.Default(true)
				dsl.Meta("struct:tag:json", "default_true,omitempty")
			})
			dsl.Attribute("DefaultCount", dsl.Int, func() {
				dsl.Default(7)
				dsl.Meta("struct:tag:json", "default_count,omitempty")
			})
			dsl.Attribute("Untagged", dsl.Boolean, func() { dsl.Default(false) })
			dsl.Attribute("Named", dsl.Boolean, func() {
				dsl.Default(false)
				dsl.Meta("struct:tag:json:name", "named")
			})
			dsl.Required("Required")
		})
		located("Envelope", func() {
			dsl.Attribute("Policy", policy)
			dsl.Attribute("Policies", dsl.ArrayOf(policy))
			dsl.Required("Policy", "Policies")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/omission_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/omission_test.go"), source, 0o600)) // #nosec G703 -- root is this test's private temporary directory.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}
