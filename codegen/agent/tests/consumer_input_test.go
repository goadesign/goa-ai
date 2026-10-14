// Consumer input tests generate two callers of one provider and verify that
// binding changes only the selected caller's accepted model JSON.
package tests

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestConsumerInputUsesProviderPayload(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", consumerInputDesign(true))
	baseline := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", consumerInputDesign(false))
	for _, file := range baseline {
		if strings.HasPrefix(file.Path, "gen/reader/toolsets/records/") {
			assert.Equal(t, sha256.Sum256([]byte(fileContent(t, baseline, file.Path))), sha256.Sum256([]byte(fileContent(t, files, file.Path))), "provider changed: %s", file.Path)
		}
	}

	var consumerCodecs, providerCodecs string
	for _, file := range files {
		if file.Path == "gen/reader/agents/bound/records/inputbinding/codecs.go" {
			consumerCodecs = fileContent(t, files, file.Path)
		}
		if file.Path == "gen/reader/toolsets/records/codecs.go" {
			providerCodecs = fileContent(t, files, file.Path)
		}
	}
	require.NotEmpty(t, consumerCodecs)
	require.NotEmpty(t, providerCodecs)
	assert.Contains(t, consumerCodecs, "*records.ReadPayload")
	assert.NotContains(t, consumerCodecs, "type ReadPayload")

	assert.Contains(t, providerCodecs, `case "scope":`)
	assert.Contains(t, fileContent(t, files, "gen/reader/agents/bound/config.go"), "PrepareInputs func(")
	assert.NotContains(t, fileContent(t, files, "gen/reader/agents/direct/config.go"), "PrepareInputs")
	assert.Contains(t, fileContent(t, files, "gen/reader/agents/bound/registry.go"), `WithInputBinding([]tools.Ident{ "records.read", "records.read_bound_values", }, cfg.PrepareInputs)`)
	root := writeGeneratedModuleKeepingGen(t, files)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/reader/agents/bound/records/inputbinding/binding_test.go"), []byte(consumerInputBehaviorTest), 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-count=1", "./gen/reader/agents/bound/records/inputbinding"))
}

func consumerInputDesign(bind bool) func() {
	return func() {
		API("records", func() {})
		details := Type("DetailsRef", func() {
			Attribute("code", String, "The selected detail code.", func() { MinLength(1) })
			Required("code")
		})
		scope := Type("ScopeRef", func() {
			Attribute("name", String, "The selected scope.", func() { MinLength(1) })
			Attribute("details", details, "Additional scope details.")
			Required("name")
		})
		scopeCode := Type("ScopeCode", String, func() {
			Description("A lowercase scope code supplied by the application.")
			Pattern("^[a-z]+$")
			MinLength(3)
			MaxLength(8)
			Example("north")
		})
		provider := Toolset("records", func() {
			Tool("read", "Read records in a scope.", func() {
				Args(func() {
					Attribute("scope", scope, "The scope containing the records.")
					Attribute("query", String, "The requested records.")
					Attribute("render", Boolean, "Whether to render a card.")
					Required("scope", "query")
				})
				Return(String)
				UIOnly("render")
			})

			Tool("read_bound_values", "Read a named value.", func() {
				Args(func() { Attribute("scope", scopeCode, "The selected scope."); Required("scope") })
				Return(String)
			})
		})
		Service("reader", func() {
			Agent("bound", "Reads one accepted scope.", func() {
				Use(provider, func() {
					if bind {
						BindInput("read", "scope")
						BindInput("read_bound_values", "scope")
					}
				})
			})
			Agent("direct", "Chooses a scope.", func() { Use(provider) })
		})
	}
}

const consumerInputBehaviorTest = `package inputbinding
import (
 "bytes"
 "context"
 "strings"
 "goa.design/goa-ai/runtime/agent/planner"
 genbound "generated.local/gen/reader/agents/bound"
 "sync"
 "testing"
 "generated.local/gen/reader/toolsets/records"
)
type testPlanner struct{}
func (*testPlanner) PlanStart(context.Context,*planner.PlanInput)(*planner.PlanResult,error){return nil,nil}
func (*testPlanner) PlanResume(context.Context,*planner.PlanResumeInput)(*planner.PlanResult,error){return nil,nil}
func TestRequiredPreparation(t *testing.T){
 cfg:=genbound.BoundAgentConfig{Planner:&testPlanner{}}
 if err:=cfg.Validate(); err==nil || !strings.Contains(err.Error(),"input preparation is required") { t.Fatalf("missing preparation accepted: %v",err) }
}
func TestModelInputCodec(t *testing.T) {
 codec := ReadModelInputCodec()
 full := &records.ReadPayload{Scope:&records.ScopeRef{Name:"north"}, Query:"active"}
 modelJSON,err := codec.ToJSON(full); if err != nil { t.Fatal(err) }
 if bytes.Contains(modelJSON,[]byte("scope")) { t.Fatalf("bound field exposed: %s",modelJSON) }
 decoded,err := codec.FromJSON(modelJSON); if err != nil { t.Fatal(err) }
 if decoded.Scope!=nil || decoded.Query!="active" { t.Fatalf("semantic input changed: %#v",decoded) }
 if _,err:=codec.FromJSON([]byte("{\"scope\":{\"name\":\"forged\"},\"query\":\"active\"}")); err==nil { t.Fatal("model supplied bound field accepted") }
 bound,err:=prepareReadInput(records.SpecRead(),full.Scope); if err!=nil { t.Fatal(err) }
 execution,err:=bound.PrepareInput(decoded); if err!=nil { t.Fatal(err) }
 value,err:=records.ReadPayloadCodec().FromJSON(execution); if err!=nil { t.Fatal(err) }
 if value.Scope.Name!="north" || value.Query!="active" { t.Fatalf("complete input changed: %#v",value) }
}
func TestNamedStringBoundInput(t *testing.T) {
 for _, test := range []struct { name string; value records.ScopeCode; valid bool }{
  {"valid", "north", true},
  {"minimum length", "abc", true},
  {"maximum length", "abcdefgh", true},
  {"invalid pattern", "NORTH", false},
  {"below minimum", "ab", false},
  {"above maximum", "abcdefghi", false},
 } {
  t.Run(test.name, func(t *testing.T) {
   bound, err := prepareReadBoundValuesInput(records.SpecReadBoundValues(), test.value)
   if (err == nil) != test.valid { t.Fatalf("validation error = %v, valid = %v", err, test.valid) }
   if !test.valid { return }
   decoded, err := bound.Payload.Codec.FromJSON([]byte("{}")); if err != nil { t.Fatal(err) }
   complete, err := bound.PrepareInput(decoded); if err != nil { t.Fatal(err) }
   value, err := records.ReadBoundValuesPayloadCodec().FromJSON(complete); if err != nil { t.Fatal(err) }
   if value.Scope != test.value { t.Fatalf("bound value changed: %q", value.Scope) }
  })
 }
}
func TestBoundInput(t *testing.T) {
 original := records.SpecRead()
 schema := append([]byte(nil), original.ExecutionPayloadSchema...)
 scope := &records.ScopeRef{Name: "north",Details:&records.DetailsRef{Code:"one"}}
 bound, err := prepareReadInput(original, scope)
 if err != nil { t.Fatal(err) }
 scope.Name = "changed"
 scope.Details.Code="changed"
 if !bytes.Equal(schema, records.SpecRead().ExecutionPayloadSchema) { t.Fatal("provider schema changed") }
 if _, err := original.Payload.Codec.FromJSON([]byte("{\"query\":\"active\"}")); err == nil { t.Fatal("provider accepted missing scope") }
 if _, err := bound.Payload.Codec.FromJSON([]byte("{\"scope\":{\"name\":\"forged\"},\"query\":\"active\"}")); err == nil { t.Fatal("consumer accepted model scope") }
 if _, err := prepareReadInput(original, &records.ScopeRef{}); err == nil { t.Fatal("invalid owner scope accepted") }
 if _, err := prepareReadInput(original, nil); err == nil { t.Fatal("nil owner scope accepted") }
 if _,err:=prepareReadInput(original,&records.ScopeRef{Name:"north",Details:&records.DetailsRef{}}); err==nil { t.Fatal("invalid nested value accepted") }
 if _,err:=prepareReadBoundValuesInput(records.SpecReadBoundValues(),"north"); err!=nil { t.Fatal(err) }
 if _,err:=prepareReadBoundValuesInput(records.SpecReadBoundValues(),""); err==nil { t.Fatal("invalid primitive binding accepted") }
 for _, textOnly := range []bool{false,true} {
  selected := bound
  modelJSON := []byte("{\"query\":\"active\",\"render\":true}")
  if textOnly { selected=bound.ForTextOnly(); modelJSON=[]byte("{\"query\":\"active\"}") }
  saved := append([]byte(nil),modelJSON...)
  decoded,err := selected.Payload.Codec.FromJSON(modelJSON); if err != nil { t.Fatal(err) }
  complete,err := selected.PrepareInput(decoded); if err != nil { t.Fatal(err) }
  value,err := selected.ExecutionPayloadCodec.FromJSON(complete); if err != nil { t.Fatal(err) }
  full := value.(*records.ReadPayload)
  if full.Scope.Details==nil || full.Scope.Details.Code!="one" || full.Scope.Name!="north" || full.Query!="active" || full.Render==nil || *full.Render==textOnly { t.Fatalf("bad complete payload: %#v",full) }
  if decoded.(*records.ReadPayload).Scope != nil { t.Fatal("decoded model payload mutated") }
  if !bytes.Equal(modelJSON,saved) { t.Fatal("original model JSON mutated") }
 }
 if _,err:=bound.PrepareInput("bad type"); err==nil { t.Fatal("invalid decoded type accepted") }
 var work sync.WaitGroup
 for _, name := range []string{"east","west"} {
  work.Go(func(){
   view,err:=prepareReadInput(records.SpecRead(),&records.ScopeRef{Name:name}); if err!=nil { t.Error(err); return }
   decoded,err:=view.Payload.Codec.FromJSON([]byte("{\"query\":\"active\"}")); if err!=nil { t.Error(err); return }
   payload,err:=view.PrepareInput(decoded); if err!=nil { t.Error(err); return }
   result,err:=records.ReadPayloadCodec().FromJSON(payload); if err!=nil { t.Error(err); return }
   if result.Scope.Name!=name { t.Errorf("scope changed: %s",result.Scope.Name) }
  })
 }
 work.Wait()
}
`
