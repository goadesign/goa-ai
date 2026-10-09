// These checks send the union JSON advertised by generated tool schemas through
// the corresponding typed codecs. Ordinary, flat and untagged mappings must keep
// the same branch constructors and field validation without changing wire shape.
package tests

import (
	"fmt"
	"testing"

	"goa.design/goa-ai/codegen/testhelpers"
	agentdsl "goa.design/goa-ai/dsl"
	goardsl "goa.design/goa/v3/dsl"
)

func TestGeneratedToolUnionMappings(t *testing.T) {
	for _, test := range []struct {
		mapping, payload string
	}{
		{"tagged", `{"value":{"type":"structured","value":{"label":"checked"}}}`},
		{"tagged_custom", `{"value":{"kind":"structured","body":{"label":"checked"}}}`},
		{"flat", `{"value":{"type":"structured","label":"checked"}}`},
		{"flat_custom", `{"value":{"kind":"structured","label":"checked"}}`},
		{"untagged", `{"value":{"label":"checked"}}`},
	} {
		t.Run(test.mapping, func(t *testing.T) {
			files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", unionMappingDesign(test.mapping))
			root := writeGeneratedModule(t, files)
			writeGeneratedPackageTest(t, root, "alpha/toolsets/union/http/validate.go", renderedFileContent(t, files, "gen/alpha/toolsets/union/http/validate.go"))
			writeGeneratedPackageTest(t, root, "alpha/toolsets/union/union_mapping_test.go", fmt.Sprintf(`package union

import (
 "encoding/json"
 "testing"
 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 "github.com/santhosh-tekuri/jsonschema/v6"
 "google.golang.org/protobuf/proto"
 genregistry "goa.design/goa-ai/registry/gen/registry"
 genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
 genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
 genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
 "goa.design/goa-ai/runtime/agent/tools"
)

func TestSchemaAndTypedCodecAgree(t *testing.T) {
 input:=[]byte(%q)
 var schemaDocument,inputDocument any
 require.NoError(t,json.Unmarshal(SpecEcho().Payload.Schema,&schemaDocument))
 require.NoError(t,json.Unmarshal(input,&inputDocument))
 compiler:=jsonschema.NewCompiler();require.NoError(t,compiler.AddResource("schema.json",schemaDocument))
 schema,err:=compiler.Compile("schema.json");require.NoError(t,err)
 require.NoError(t,schema.Validate(inputDocument))
 decoded,err:=UnmarshalEchoPayload(input);require.NoError(t,err)
 structured,selected:=decoded.Value.AsStructured();require.True(t,selected)
 assert.Equal(t,"checked",structured.Label)
 encoded,err:=MarshalEchoPayload(decoded);require.NoError(t,err)
 assert.JSONEq(t,string(input),string(encoded))
 var document map[string]any
 require.NoError(t,json.Unmarshal(input,&document))
 value:=document["value"].(map[string]any)
 for _,tag:=range []string{"type","kind"} {
  if _,exists:=value[tag];exists { value[tag]="invented" }
 }
 invalid,err:=json.Marshal(document);require.NoError(t,err)
 if %q!="untagged" { _,err=UnmarshalEchoPayload(invalid);assert.Error(t,err) }
}

func TestSelectedBranchRejectsUndeclaredAndInvalidFields(t *testing.T) {
 var document map[string]any
 require.NoError(t,json.Unmarshal([]byte(%q),&document))
 value:=document["value"].(map[string]any)
 if nested,ok:=value["value"].(map[string]any);ok { value=nested }
 if nested,ok:=value["body"].(map[string]any);ok { value=nested }
 for _,change:=range []struct{field string; value any}{
  {"extra",true},{"label",7},{"label",""},
 } {
  value[change.field]=change.value
  input,err:=json.Marshal(document);require.NoError(t,err)
  _,err=UnmarshalEchoPayload(input);assert.Error(t,err)
  delete(value,change.field)
  value["label"]="checked"
 }
}

func TestUntaggedPrimitiveAndCollectionBranches(t *testing.T) {
 if %q!="untagged" { return }
 for _,input:=range []string{
  "{\"value\":\"selected\"}","{\"value\":9007199254740993}",
  "{\"value\":true}","{\"value\":[\"one\",\"two\"]}",
 } {
  decoded,err:=UnmarshalEchoPayload([]byte(input));require.NoError(t,err)
  encoded,err:=MarshalEchoPayload(decoded);require.NoError(t,err)
  assert.JSONEq(t,input,string(encoded))
 }
 for _,input:=range []string{"{\"value\":null}","{\"value\":1.5}","{\"value\":[7]}"} {
  _,err:=UnmarshalEchoPayload([]byte(input));assert.Error(t,err)
 }
}

func TestRegistryRetainsUnionSelections(t *testing.T) {
 declarations:=ToolSchemas()
 require.Len(t,declarations,1)
 fields:=declarations[0].ConsumerContract.Payload.Fields
 local:=SpecEcho().Payload.Fields
 require.Len(t,fields,len(local))
 for i,field:=range local {
  require.Len(t,fields[i].Branches,len(field.Branches))
  for j,branch:=range field.Branches {
   selection:=fields[i].Branches[j].Selection
   switch branch:=branch.(type) {
   case tools.TaggedUnionBranch:
    actual,ok:=selection.AsTagged();require.True(t,ok)
    assert.Equal(t,branch.Value,actual.Value)
    require.Len(t,actual.Discriminator,len(branch.Discriminator))
   case tools.UntaggedUnionBranch:
    actual,ok:=selection.AsUntagged();require.True(t,ok)
    assert.Equal(t,branch.JSONKind,actual.JSONKind)
    assert.Equal(t,branch.Index,actual.Index)
    require.Len(t,actual.Path,len(branch.Path))
   default:
    t.Fatalf("unexpected union selection %%T",branch)
   }
  }
 }
 resolution:=&genregistry.ResolvedToolset{
  Toolset:&genregistry.Toolset{Name:"union",RegisteredAt:"2026-09-17T00:00:00Z",Tools:declarations},
  RegistrationToken:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
 }
 encoded,err:=proto.Marshal(genregistryserver.NewProtoResolveToolsetResponse(resolution));require.NoError(t,err)
 var received genregistrypb.ResolveToolsetResponse
 require.NoError(t,proto.Unmarshal(encoded,&received))
 require.NoError(t,genregistryclient.ValidateResolveToolsetResponse(&received))
 assert.Equal(t,resolution,genregistryclient.NewResolveToolsetResult(&received))
 encoded,err=json.Marshal(resolution);require.NoError(t,err)
 var restored genregistry.ResolvedToolset
 require.NoError(t,json.Unmarshal(encoded,&restored))
 assert.Equal(t,resolution,&restored)
}
`, test.payload, test.mapping, test.payload, test.mapping))
			runGeneratedUnionGoTest(t, root)
		})
	}
}

// unionMappingDesign authors the same tool branch through each native Goa JSON
// mapping. Flat unions use object branches; untagged branches have distinct kinds.
func unionMappingDesign(mapping string) func() {
	return func() {
		goardsl.API("alpha", func() {})
		structured := goardsl.Type("StructuredValue", func() {
			goardsl.Attribute("label", goardsl.String, "Selected record label", func() { goardsl.MinLength(1) })
			goardsl.Required("label")
		})
		payload := goardsl.Type("UnionPayload", func() {
			goardsl.OneOf("value", "One selected value", func() {
				switch mapping {
				case "tagged_custom":
					goardsl.Meta("oneof:type:field", "kind")
					goardsl.Meta("oneof:value:field", "body")
				case "flat", "flat_custom":
					goardsl.Meta("oneof:json:flatten")
					if mapping == "flat_custom" {
						goardsl.Meta("oneof:type:field", "kind")
					}
				case "untagged":
					goardsl.Meta("oneof:json:untagged")
				}
				goardsl.Attribute("structured", structured, "Selected record")
				if mapping != "flat" && mapping != "flat_custom" {
					goardsl.Attribute("text", goardsl.String, "Selected text")
				}
				if mapping == "untagged" {
					goardsl.Attribute("number", goardsl.Int64, "Exact whole number")
					goardsl.Attribute("kind", goardsl.Boolean, "Selected flag whose branch name also names the union's private selector")
					goardsl.Attribute("items", goardsl.ArrayOf(goardsl.String), "Selected strings")
				}
			})
			goardsl.Required("value")
		})
		goardsl.Service("alpha", func() {
			agentdsl.Agent("scribe", "Review supplied values", func() {
				agentdsl.Use("union", func() {
					agentdsl.Tool("echo", "Return the selected value", func() {
						agentdsl.Args(payload)
						agentdsl.Return(payload)
					})
				})
			})
		})
	}
}
