// These checks reject unsupported embedded declarations before codec eligibility.
package jsoncodec

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
)

func TestInvalidEmbeddedDeclarationsRejectGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields func()
		want   string
	}{
		{"unsafe case matching with any", func() {
			dsl.Attribute("dynamic", dsl.Any)
			dsl.Attribute("extras", dsl.MapOf(dsl.String, dsl.String), func() { dsl.Meta("struct:tag:json", ",embed") })
		}, "explicit JSON name"},
		{"empty strict name", func() {
			dsl.Attribute("message", dsl.String, func() { dsl.Meta("struct:tag:json", ",case:strict") })
			dsl.Attribute("extras", dsl.MapOf(dsl.String, dsl.String), func() { dsl.Meta("struct:tag:json", ",embed") })
		}, "explicit JSON name"},
		{"integer keys", func() {
			dsl.Attribute("extras", dsl.MapOf(dsl.Int, dsl.String), func() { dsl.Meta("struct:tag:json", ",embed") })
		}, "string keys"},
		{"required with any", func() {
			dsl.Attribute("dynamic", dsl.Any)
			dsl.Attribute("extras", dsl.MapOf(dsl.String, dsl.String), func() { dsl.Meta("struct:tag:json", ",embed") })
			dsl.Required("extras")
		}, "cannot be required"},
		{"bounded with any", func() {
			dsl.Attribute("dynamic", dsl.Any)
			dsl.Attribute("extras", dsl.MapOf(dsl.String, dsl.String), func() { dsl.Meta("struct:tag:json", ",embed"); dsl.MaxLength(2) })
		}, "length bounds"},
		{"multiple with any", func() {
			dsl.Attribute("dynamic", dsl.Any)
			for _, name := range []string{"first", "second"} {
				dsl.Attribute(name, dsl.MapOf(dsl.String, dsl.String), func() { dsl.Meta("struct:tag:json", ",embed") })
			}
		}, "multiple embedded maps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := generate(t, func() { located("Value", tc.fields) })
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestEmbeddedSiblingNames(t *testing.T) {
	for _, name := range []string{"-", "'message'", `mes\\sage`} {
		t.Run(name, func(t *testing.T) {
			_, err := generate(t, func() {
				located("Value", func() {
					dsl.Attribute("message", dsl.String, func() { dsl.Meta("struct:tag:json", name+",case:strict") })
					dsl.Attribute("extras", dsl.MapOf(dsl.String, dsl.String), func() { dsl.Meta("struct:tag:json", ",embed") })
				})
			})
			require.ErrorContains(t, err, "literal JSON name")
		})
	}
	for _, name := range []string{"message", "説明"} {
		t.Run(name, func(t *testing.T) {
			files, err := generate(t, func() {
				located("Value", func() {
					dsl.Attribute("message", dsl.String, func() { dsl.Meta("struct:tag:json", name+",case:strict") })
					dsl.Attribute("extras", dsl.MapOf(dsl.String, dsl.String), func() { dsl.Meta("struct:tag:json", ",embed") })
					dsl.Required("message")
				})
			})
			require.NoError(t, err)
			root := compileModule(t, files)
			source := fmt.Sprintf(`package types
import("testing";"encoding/json";"bytes")
func TestNames(t *testing.T){
 value:=&Value{Message:"known",Extras:map[string]string{"MESSAGE":"extra"}}
 encoded,err:=EncodeValue(value);if err!=nil{t.Fatal(err)}
 if !bytes.Contains(encoded,[]byte(%q)){t.Fatalf("missing known name: %%s",encoded)}
 decoded,err:=DecodeValue(encoded);if err!=nil{t.Fatal(err)}
 if decoded.Message!="known"||decoded.Extras["MESSAGE"]!="extra"{t.Fatalf("changed values: %%#v",decoded)}
 var native Value;if err:=json.Unmarshal(encoded,&native);err!=nil{t.Fatal(err)}
 if native.Message!="known"||native.Extras["MESSAGE"]!="extra"{t.Fatalf("changed native values: %%#v",native)}
}`, `"`+name+`":"known"`)
			require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/embedded_names_test.go"), []byte(source), 0600))
			runGo(t, root, "test", "-count=1", "./gen/types")
		})
	}
}
