// These tests compile the generated private integer types and exercise exact
// numeric decoding through both ordinary and complete-original value codecs.
package codec

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

func TestGeneratedIntegerJSONValues(t *testing.T) {
	count := &expr.UserTypeExpr{TypeName: "Count", UID: "integer-test:count", AttributeExpr: &expr.AttributeExpr{Type: expr.Int64}}
	attribute := &expr.AttributeExpr{Type: &expr.UserTypeExpr{
		TypeName: "Numbers", UID: "integer-test:numbers", AttributeExpr: &expr.AttributeExpr{Type: &expr.Object{
			{Name: "count", Attribute: &expr.AttributeExpr{Type: count}},
			{Name: "signed", Attribute: &expr.AttributeExpr{Type: expr.Int32, Meta: expr.MetaExpr{"struct:tag:json": {"signedValue,omitempty"}}}},
			{Name: "unsigned", Attribute: &expr.AttributeExpr{Type: expr.UInt64}},
			{Name: "items", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.Int64}, NonNullableElems: true}}},
			{Name: "values", Attribute: &expr.AttributeExpr{Type: &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: &expr.AttributeExpr{Type: expr.UInt32}}}},
			{Name: "choice", Attribute: &expr.AttributeExpr{Type: &expr.Union{TypeName: "NumberChoice", Values: []*expr.NamedAttributeExpr{
				{Name: "count", Attribute: &expr.AttributeExpr{Type: expr.Int64}},
			}}}},
		}},
	}}
	generation, err := codegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	owner, err := generation.ClaimPackage("example.com/gen/widgets")
	require.NoError(t, err)
	require.NoError(t, walkAttribute(attribute, make(map[expr.UserType]struct{}), func(current *expr.AttributeExpr) error {
		if named, ok := current.Type.(expr.UserType); ok && named != expr.Empty {
			_, err := owner.DeclareUserType(named)
			return err
		}
		if _, ok := current.Type.(*expr.Union); ok {
			_, err := owner.DeclareUnion(current)
			return err
		}
		return nil
	}))
	layout, err := codegen.PlanGoType(attribute, codegen.GoTypePlanOptions{
		Owner: owner.ImportPath(), Policy: codegen.GoLayoutPolicy{UseDefault: true, SumType: true}, RetainNamedValue: true,
		Bind: func(request codegen.GoTypeBindingRequest) (codegen.GoTypeBinding, error) {
			binding := codegen.GoTypeBinding{Owner: owner.ImportPath(), PreferredImportName: "widgets"}
			var err error
			if request.Kind == codegen.GoUnion {
				binding.Union, err = owner.Union(request.Attribute)
			} else {
				binding.Type, err = owner.Type(request.Attribute.Type.(expr.UserType))
			}
			return binding, err
		},
	})
	require.NoError(t, err)
	plan, err := NewPlan(generation, "example.com/gen/mcp_widgets/internal/codec")
	require.NoError(t, err)
	ordinary, err := plan.Add("numbers", "Numbers", attribute, layout, EncodeAndDecode)
	require.NoError(t, err)
	original, err := plan.AddStandalone("original", "OriginalNumbers", attribute, ordinary.serviceLayout)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	for _, definition := range original.types {
		assert.NotEqual(t, codegen.Goify(definition.declaration.Name(), true), definition.declaration.Name(), "original transport types must remain private: %s", definition.declaration.Name())
	}
	writer := newCodecTestServiceAttributor(codegen.NewAttributeScope(owner.Scope()))
	require.NoError(t, ordinary.BindService(writer))
	require.NoError(t, original.BindService(writer))
	files, err := plan.Files("codec")
	require.NoError(t, err)
	directory := t.TempDir()
	_, err = files[0].Render(directory)
	require.NoError(t, err)
	writeTestFile(t, filepath.Join(directory, "go.mod"), fmt.Sprintf("module example.com\n\ngo 1.24\n\nrequire goa.design/goa/v3 v3.0.0\n\nreplace goa.design/goa/v3 => %s\n", filepath.ToSlash(goaModuleDirectory(t))))
	writeTestFile(t, filepath.Join(directory, "gen/widgets/service.go"), integerServiceSource)
	behavior := strings.ReplaceAll(integerBehaviorSource, "DecodeOriginalNumbers", original.DecodeDeclaration().Name())
	behavior = strings.ReplaceAll(behavior, "EncodeOriginalNumbers", original.EncodeDeclaration().Name())
	writeTestFile(t, filepath.Join(directory, "gen/mcp_widgets/internal/codec/integer_behavior_test.go"), behavior)
	runGeneratedCodecTests(t, directory)
}

const integerServiceSource = `package widgets

type (
 Count int64
 Numbers struct {
  Count *Count
  Signed *int32
  Unsigned *uint64
  Items []int64
  Values map[string]uint32
  Choice NumberChoice
 }
 NumberChoice struct { value int64 }
)
func NewNumberChoiceCount(value int64) NumberChoice { return NumberChoice{value:value} }
func (value NumberChoice) AsCount() (int64,bool) { return value.value,true }
func (value NumberChoice) Validate() error { return nil }
func (value NumberChoice) Kind() string { return "count" }
func (value *NumberChoice) SetCount(count int64) { value.value=count }
`

const integerBehaviorSource = `package codec

import (
 "testing"
 "strings"
 "example.com/gen/widgets"
)

func TestIntegerValues(t *testing.T) {
 for _, decode := range []struct {
  name string
  call func([]byte)(*widgets.Numbers,error)
  encode func(*widgets.Numbers)([]byte,error)
 }{{"ordinary",DecodeNumbers,EncodeNumbers},{"original",DecodeOriginalNumbers,EncodeOriginalNumbers}} {
  t.Run(decode.name,func(t *testing.T){
   for _, test := range []struct{input string; value int64}{
    {"3",3},{"3.0",3},{"3e0",3},{"300e-2",3},{"0.003e3",3},
    {"-0.0",0},{"0e999999999999999999999999",0},
    {"9223372036854775807.0",9223372036854775807},{"-9223372036854775808e0",-9223372036854775808},
   } {
    result,err:=decode.call([]byte("{\"count\":"+test.input+"}"))
    if err!=nil {t.Fatalf("%s: %v",test.input,err)}
    if result.Count==nil||int64(*result.Count)!=test.value {t.Fatalf("%s: decoded %#v",test.input,result)}
   }
   for _, input:=range []string{
    "3.1","3e-1","9223372036854775808","-9223372036854775809",
    "1e999999999999999999999999","1e-999999999999999999999999","\"3\"",
   } {
    if _,err:=decode.call([]byte("{\"count\":"+input+"}"));err==nil {t.Fatalf("accepted %s",input)}
   }
   for _, input:=range []string{
    "{\"signedValue\":2147483648}","{\"signedValue\":-2147483649}",
    "{\"unsigned\":-1}","{\"unsigned\":18446744073709551616}",
    "{\"values\":{\"a\":4294967296}}","{\"items\":[3.1]}",
    "{\"choice\":{\"type\":\"count\",\"value\":1.5}}",
   } {
    if _,err:=decode.call([]byte(input));err==nil {t.Fatalf("accepted %s",input)}
   }
   result,err:=decode.call([]byte("{\"signedValue\":2147483647.0,\"unsigned\":18446744073709551615e0,\"items\":[3.0,-4e0],\"values\":{\"a\":4294967295.0},\"choice\":{\"type\":\"count\",\"value\":5e0}}"))
   if err!=nil {t.Fatal(err)}
   choice,ok:=result.Choice.AsCount()
   encoded,err:=decode.encode(result)
   if err!=nil {t.Fatal(err)}
   if !strings.Contains(string(encoded),"\"signedValue\":") || strings.Contains(string(encoded),"\"signed\":") {t.Fatalf("wrong JSON name: %s",encoded)}
   if result.Signed==nil||*result.Signed!=2147483647||result.Unsigned==nil||*result.Unsigned!=18446744073709551615||len(result.Items)!=2||result.Items[0]!=3||result.Items[1]!=-4||result.Values["a"]!=4294967295||!ok||choice!=5 {t.Fatalf("wrong values: %#v",result)}
  })
 }
}
`
