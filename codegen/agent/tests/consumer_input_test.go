// Consumer input tests generate two callers of one provider and verify that
// binding changes only the selected caller's accepted model JSON.
package tests

import (
	"context"
	"crypto/sha256"
	"fmt"
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

func TestConsumerFieldBindingAndLookup(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", func() {
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
		provider := Toolset("records", func() {
			Tool("read", "Read records in a required scope.", func() {
				Args(func() {
					Attribute("scope", scope, "The scope containing the records.")
					Attribute("query", String, "The requested records.")
					Attribute("render", Boolean, "Whether to render a card.")
					Required("scope", "query")
				})
				Return(String)
				UIOnly("render")
			})
			Tool("search_optional", "Search records with an optional scope.", func() {
				Args(func() {
					Attribute("scope", scope, "Optional scope filter.")
					Attribute("query", String, "The search query.")
					Required("query")
				})
				Return(String)
			})
			Tool("list_all", "List all scopes.", func() {
				Args(func() {
					Attribute("filter", String, "Optional filter.")
				})
				Return(String)
			})
		})
		Service("reader", func() {
			Agent("bound", "Reads one or more scopes.", func() {
				Use(provider, func() {
					BindField("scope", func() {
						Lookup(String, "Selected scope name.", func() {
							MinLength(1)
							Example("North")
						})
					})
				})
			})
		})
	})

	assert.Contains(t, fileContent(t, files, "gen/reader/agents/bound/registry.go"), `WithInputBinding([]tools.Ident{ "records.read", "records.search_optional", }, cfg.PrepareInputs)`)
	catalog := readSchemaCatalogs(t, files)["reader.bound"]
	var provider catalogTool
	for _, tool := range catalog.Tools {
		if tool.ID == "records.read" {
			provider = tool
		}
	}
	require.NotNil(t, provider.Payload)
	require.NotNil(t, provider.Result)
	require.NotNil(t, provider.TextOnlyPayload)
	require.NotNil(t, provider.InputBinding)
	consumer := provider.InputBinding
	require.NotNil(t, consumer.TextOnlyPayload)
	require.NotNil(t, consumer.LookupPayload)
	require.NotNil(t, consumer.LookupTextOnlyPayload)
	artifactTest := fmt.Sprintf(`
func TestArtifactMatchesInstalledSpecifications(t *testing.T) {
 original := records.SpecRead()
 assert.JSONEq(t,%q,string(original.Payload.Schema)); assert.JSONEq(t,%q,string(original.Result.Schema)); assert.JSONEq(t,%q,string(original.ForTextOnly().Payload.Schema))
 scope := &records.ScopeRef{Name:"north",Details:&records.DetailsRef{Code:"one"}}
 bound,err := prepareReadInput(original,scope); if err != nil { t.Fatal(err) }
 assert.JSONEq(t,%q,string(bound.Payload.Schema)); assert.JSONEq(t,%q,string(bound.ForTextOnly().Payload.Schema))
 lookup,err := prepareReadLookup(original,map[string]*records.ScopeRef{"North":scope}); if err != nil { t.Fatal(err) }
 assert.JSONEq(t,%q,string(lookup.Payload.Schema)); assert.JSONEq(t,%q,string(lookup.ForTextOnly().Payload.Schema))
}
`, string(provider.Payload.Schema), string(provider.Result.Schema), string(provider.TextOnlyPayload.Schema),
		string(consumer.Payload.Schema), string(consumer.TextOnlyPayload.Schema), string(consumer.LookupPayload.Schema), string(consumer.LookupTextOnlyPayload.Schema))
	root := writeGeneratedModuleKeepingGen(t, files)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/reader/agents/bound/records/inputbinding/field_binding_test.go"), []byte(consumerFieldBindingBehaviorTest+artifactTest), 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-count=1", "./gen/reader/agents/bound/records/inputbinding"))
}

const consumerFieldBindingBehaviorTest = `package inputbinding
import (
 "bytes"
 "errors"
 "testing"
 "github.com/stretchr/testify/assert"
 "generated.local/gen/reader/toolsets/records"
 "goa.design/goa-ai/runtime/agent/tools"
)

func TestLookupAndOptionalFieldBinding(t *testing.T) {
 // 1. Static binding on optional field hides scope and injects the bound ScopeRef.
 optBound, err := prepareSearchOptionalInput(records.SpecSearchOptional(), &records.ScopeRef{Name: "north", Details: &records.DetailsRef{Code: "n1"}})
 if err != nil { t.Fatal(err) }
 if bytes.Contains(optBound.Payload.Schema, []byte("\"scope\"")) {
  t.Fatalf("static bound optional field exposed in schema: %s", optBound.Payload.Schema)
 }
 optDecoded, err := optBound.Payload.Codec.FromJSON([]byte("{\"query\":\"chillers\"}"))
 if err != nil { t.Fatal(err) }
 optComplete, err := optBound.PrepareInput(optDecoded)
 if err != nil { t.Fatal(err) }
 optValue, err := records.SearchOptionalPayloadCodec().FromJSON(optComplete)
 if err != nil { t.Fatal(err) }
 if optValue.Scope == nil || optValue.Scope.Name != "north" || optValue.Query != "chillers" {
  t.Fatalf("unexpected static optional payload: %#v", optValue)
 }

 // 2. Lookup binding replaces ScopeRef with string key in schema and resolves via table.
 table := map[string]*records.ScopeRef{
  "South": {Name: "south-id", Details: &records.DetailsRef{Code: "s1"}},
  "North": {Name: "north-id", Details: &records.DetailsRef{Code: "n1"}},
 }
 readLookup, err := prepareReadLookup(records.SpecRead(), table)
 if err != nil { t.Fatal(err) }
 table["North"].Name = "mutated"
 if !bytes.Contains(readLookup.Payload.Schema, []byte("\"scope\":{\"description\":\"Selected scope name.\",\"example\":\"North\",\"minLength\":1,\"type\":\"string\"}")) {
  t.Fatalf("lookup schema did not replace scope with string: %s", readLookup.Payload.Schema)
 }
 readDecoded, err := readLookup.Payload.Codec.FromJSON([]byte("{\"scope\":\"North\",\"query\":\"active\",\"render\":true}"))
 if err != nil { t.Fatal(err) }
 readComplete, err := readLookup.PrepareInput(readDecoded)
 if err != nil { t.Fatal(err) }
 readValue, err := records.ReadPayloadCodec().FromJSON(readComplete)
 if err != nil { t.Fatal(err) }
 if readValue.Scope == nil || readValue.Scope.Name != "north-id" || readValue.Scope.Details.Code != "n1" || readValue.Query != "active" {
  t.Fatalf("unexpected lookup payload: %#v", readValue)
 }

 // 3. Unknown lookup key returns ValidationError with Constraint "enum" and sorted Allowed keys.
 _, err = readLookup.Payload.Codec.FromJSON([]byte("{\"scope\":\"West\",\"query\":\"active\"}"))
 if err == nil { t.Fatal("expected unknown lookup key error") }
 var verr *tools.ValidationError
 if !errors.As(err, &verr) { t.Fatalf("expected *tools.ValidationError, got %T: %v", err, err) }
 issues := verr.Issues()
 if len(issues) != 1 || issues[0].Field != "scope" || issues[0].Constraint != "invalid_enum_value" {
  t.Fatalf("unexpected validation issues: %#v", issues)
 }
 if len(issues[0].Allowed) != 2 || issues[0].Allowed[0] != "North" || issues[0].Allowed[1] != "South" {
  t.Fatalf("unexpected allowed keys: %#v", issues[0].Allowed)
 }

 // 4. Optional lookup field allows omitting scope or supplying a valid lookup key.
 optLookup, err := prepareSearchOptionalLookup(records.SpecSearchOptional(), table)
 if err != nil { t.Fatal(err) }
 omittedDecoded, err := optLookup.Payload.Codec.FromJSON([]byte("{\"query\":\"chillers\"}"))
 if err != nil { t.Fatal(err) }
 omittedComplete, err := optLookup.PrepareInput(omittedDecoded)
 if err != nil { t.Fatal(err) }
 omittedValue, err := records.SearchOptionalPayloadCodec().FromJSON(omittedComplete)
 if err != nil { t.Fatal(err) }
 if omittedValue.Scope != nil || omittedValue.Query != "chillers" {
  t.Fatalf("expected nil scope when omitted on optional lookup: %#v", omittedValue)
 }
 suppliedDecoded, err := optLookup.Payload.Codec.FromJSON([]byte("{\"scope\":\"South\",\"query\":\"chillers\"}"))
 if err != nil { t.Fatal(err) }
 suppliedComplete, err := optLookup.PrepareInput(suppliedDecoded)
 if err != nil { t.Fatal(err) }
 suppliedValue, err := records.SearchOptionalPayloadCodec().FromJSON(suppliedComplete)
 if err != nil { t.Fatal(err) }
 if suppliedValue.Scope == nil || suppliedValue.Scope.Name != "south-id" {
  t.Fatalf("expected resolved scope when supplied on optional lookup: %#v", suppliedValue)
 }
}
`
