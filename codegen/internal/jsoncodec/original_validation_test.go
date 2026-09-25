// These tests validate original values through complete generated codecs.
// The same named child appears in object, array and union occurrences.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

func TestGeneratedOriginalRequiredValues(t *testing.T) {
	files, err := generate(t, func() {
		entry := located("Entry", func() {
			dsl.Attribute("Label", dsl.String)
			dsl.Required("Label")
		})
		located("Container", func() {
			dsl.Attribute("Single", entry)
			dsl.Attribute("Loose", dsl.ArrayOf(entry))
			dsl.Attribute("Tight", dsl.ArrayOfRequired(entry))
			dsl.OneOf("Choice", func() {
				dsl.Attribute("entry", entry)
				dsl.Attribute("text", dsl.String)
			})
			dsl.Required("Single", "Loose", "Tight", "Choice")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/original_required_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/original_required_test.go"), source, 0o600)) // #nosec G703 -- root belongs to this generated fixture.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}

func TestGeneratedNamedBase(t *testing.T) {
	files, err := generate(t, func() {
		base := located("Base", func() {
			dsl.Attribute("Label", dsl.String, func() { dsl.MinLength(1) })
			dsl.Attribute("Note", dsl.String)
			dsl.Required("Label")
		})
		dsl.Type("Derived", base, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Required("Note")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source := `package types_test
import (
 "testing"
 gentypes "codec.local/gen/types"
)
var _ func(*gentypes.Derived) ([]byte, error) = gentypes.EncodeDerived
func TestNamedBase(t *testing.T) {
 for _, note := range []string{"ready", ""} {
  value := &gentypes.Derived{Label:"valid", Note:note}
  data, err := gentypes.EncodeDerived(value)
  if err != nil { t.Fatal(err) }
  result, err := gentypes.DecodeDerived(data)
  if err != nil || result.Label != value.Label || result.Note != note { t.Fatalf("%#v %v", result, err) }
 }
 for _, invalid := range []*gentypes.Derived{
  {Note:"ready"},
  {Label:string([]byte{0xff}), Note:"ready"},
 } {
  if data, err := gentypes.EncodeDerived(invalid); err == nil || data != nil {
   t.Fatalf("invalid value produced bytes: %s %v", data, err)
  }
 }
 for _, invalid := range []string{
  "{\"Label\":\"valid\"}",
  "{\"Label\":\"valid\",\"Note\":null}",
 } {
  if value, err := gentypes.DecodeDerived([]byte(invalid)); err == nil || value != nil {
   t.Fatalf("invalid JSON produced value: %#v %v", value, err)
  }
 }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/named_base_test.go"), []byte(source), 0o600)) // #nosec G703 -- root belongs to this generated fixture.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}

func TestGeneratedInheritedRequiredValues(t *testing.T) {
	files, err := generate(t, func() {
		base := located("Base", func() {
			dsl.Attribute("Label", dsl.String, func() { dsl.MinLength(1) })
			dsl.Attribute("Note", dsl.String)
			dsl.Required("Label")
		})
		middle := dsl.Type("Middle", base, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
		})
		dsl.Type("Derived", middle, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Required("Note")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/inherited_required_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/inherited_required_test.go"), source, 0o600)) // #nosec G703 -- root belongs to this generated fixture.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}

func TestGeneratedOriginalValidationRules(t *testing.T) {
	for _, aliases := range []bool{false, true} {
		name := "primitive"
		if aliases {
			name = "validated aliases"
		}
		t.Run(name, func(t *testing.T) { generateOriginalRules(t, aliases) })
	}
}

func TestGeneratedDefaultedAliases(t *testing.T) {
	files, err := generate(t, func() {
		label := dsl.Type("Label", dsl.String, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Enum("brief", "detailed")
			dsl.Default("brief")
		})
		optional := dsl.Type("OptionalLabel", dsl.String, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Enum("brief", "detailed")
		})
		located("Record", func() {
			dsl.Attribute("Label", label)
			dsl.Attribute("Optional", optional)
			dsl.Attribute("Required", label)
			dsl.Attribute("Items", dsl.ArrayOf(label))
			dsl.Attribute("Index", dsl.MapOf(dsl.String, label))
			dsl.Required("Required")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/defaulted_alias_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/defaulted_alias_test.go"), source, 0o600)) // #nosec G703 -- root belongs to this generated fixture.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}

func TestGeneratedCodecLocalNames(t *testing.T) {
	files, err := generate(t, func() {
		token := dsl.Type("Token", dsl.String, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.MinLength(1)
		})
		located("Record", func() {
			for _, name := range []string{"Data", "Err", "Root", "Decoder", "In", "Body", "Out"} {
				dsl.Attribute(name, token)
				dsl.Required(name)
			}
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/local_names_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/local_names_test.go"), source, 0o600)) // #nosec G703 -- root belongs to this generated fixture.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}

func generateOriginalRules(t *testing.T, aliases bool) {
	t.Helper()
	files, err := generate(t, func() {
		counter := dsl.Type("Counter", dsl.Int, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Minimum(0)
		})
		token := dsl.Type("Token", dsl.String, func() {
			dsl.Meta("struct:pkg:path", "goa")
			dsl.Format(dsl.FormatUUID)
		})
		entry := dsl.Type("Entry", func() {
			dsl.Meta("struct:pkg:path", "json")
			dsl.Meta("struct:type:name", "RenamedEntry")
			dsl.Attribute("Label", dsl.String, func() {
				dsl.Meta("struct:field:name", "Display")
				dsl.MinLength(1)
				dsl.MaxLength(3)
			})
			dsl.Required("Label")
		})
		other := dsl.Type("OtherEntry", func() {
			dsl.Meta("struct:pkg:path", "utf8")
			dsl.Attribute("Value", dsl.String, func() { dsl.Pattern("^[A-Z]+$") })
			dsl.Required("Value")
		})
		var counterType, tokenType expr.DataType = dsl.Int, dsl.String
		if aliases {
			counterType, tokenType = counter, token
		}
		located("Record", func() {
			dsl.Attribute("Enabled", dsl.Boolean, func() { dsl.Default(true) })
			dsl.Attribute("Count", counterType, func() {
				if !aliases {
					dsl.Minimum(0)
				}
			})
			dsl.Attribute("Text", dsl.String, func() { dsl.Default("default") })
			dsl.Attribute("Token", tokenType, func() {
				if !aliases {
					dsl.Format(dsl.FormatUUID)
				}
			})
			dsl.Attribute("Entry", entry)
			dsl.Attribute("Other", other)
			dsl.Attribute("Mode", dsl.String, func() { dsl.Enum("fast", "slow") })
			dsl.Attribute("Level", dsl.Float64, func() {
				dsl.ExclusiveMinimum(0)
				dsl.ExclusiveMaximum(10)
			})
			dsl.Attribute("Inclusive", dsl.Int, func() { dsl.Minimum(0); dsl.Maximum(2) })
			dsl.Attribute("Items", dsl.ArrayOf(entry), func() { dsl.MaxLength(2) })
			dsl.Attribute("RequiredItems", dsl.ArrayOfRequired(entry))
			dsl.Attribute("Labels", dsl.MapOf(dsl.String, dsl.String), func() { dsl.MaxLength(2) })
			dsl.Required("Enabled", "Count", "Text", "Token", "Entry", "Other", "Mode", "Level", "Inclusive", "Items", "RequiredItems", "Labels")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/original_rules_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/original_rules_test.go"), source, 0o600)) // #nosec G703 -- root belongs to this generated fixture.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}

func TestGeneratedOriginalRecursiveValues(t *testing.T) {
	files, err := generate(t, func() {
		located("Root", func() {
			dsl.Attribute("Node", "Node")
			dsl.Required("Node")
		})
		located("Node", func() {
			dsl.Attribute("Label", dsl.String, func() { dsl.MinLength(1) })
			dsl.Attribute("Children", "NodeList")
			dsl.Attribute("Index", "NodeMap")
			dsl.Attribute("Branch", "Branch")
			dsl.OneOf("Choice", func() {
				dsl.Attribute("node", "Node")
				dsl.Attribute("text", dsl.String)
			})
			dsl.Required("Label")
		})
		located("Branch", func() {
			dsl.Attribute("Nodes", "NodeList")
			dsl.Attribute("Label", dsl.String, func() { dsl.MinLength(1) })
			dsl.Required("Label")
		})
		for _, collection := range []struct {
			name string
			kind any
		}{
			{"NodeList", dsl.ArrayOf("Node")},
			{"NodeMap", dsl.MapOf(dsl.String, "Node")},
		} {
			dsl.Type(collection.name, collection.kind, func() {
				dsl.Meta("struct:pkg:path", "types")
				dsl.Meta("type:generate:force")
			})
		}
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/original_recursive_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/original_recursive_test.go"), source, 0o600)) // #nosec G703 -- root belongs to this generated fixture.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}
