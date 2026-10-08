// These tests generate standalone codecs and check that sharing validators
// preserves each root's rules, nested required fields, and defaulted values.
package codec

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

func TestStandaloneValidationSharing(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse_%t", reverse), func(t *testing.T) {
			status := standaloneTestType("Status", expr.String)
			status.Validation = &expr.ValidationExpr{Values: []any{"online", "offline"}}
			status.DefaultValue = "online"
			minimum := 1
			node := standaloneTestType("Node", &expr.Object{})
			node.Type = &expr.Object{
				{Name: "name", Attribute: &expr.AttributeExpr{
					Type: expr.String, Validation: &expr.ValidationExpr{MinLength: &minimum},
				}},
				{Name: "data", Attribute: &expr.AttributeExpr{Type: expr.Bytes}},
				{Name: "status", Attribute: &expr.AttributeExpr{Type: status}},
				{Name: "hint", Attribute: &expr.AttributeExpr{Type: expr.String}},
				{Name: "next", Attribute: &expr.AttributeExpr{Type: node}},
			}
			node.Validation = &expr.ValidationExpr{Required: []string{"name", "data"}}
			bundle := standaloneTestType("Bundle", &expr.Object{
				{Name: "first", Attribute: &expr.AttributeExpr{Type: node}},
				{Name: "second", Attribute: &expr.AttributeExpr{
					Type: node, Validation: &expr.ValidationExpr{Required: []string{"name", "data"}},
				}},
			})
			bundle.Validation = &expr.ValidationExpr{Required: []string{"first", "second"}}
			generation, owner, plan := standaloneTestPlan(t, bundle, node, status)
			roots := []struct {
				name       string
				typeExpr   expr.UserType
				validation *expr.ValidationExpr
			}{
				{"Bundle", bundle, nil},
				{"BundleTwin", bundle, &expr.ValidationExpr{}},
				{"Node", node, nil},
				{"NodeRedundant", node, &expr.ValidationExpr{Required: []string{"name", "data"}}},
				{"NodeRequired", node, &expr.ValidationExpr{Required: []string{"hint"}}},
				{"Status", status, nil},
				{"StatusSame", status, &expr.ValidationExpr{Values: []any{"online", "offline"}}},
				{"StatusOverride", status, &expr.ValidationExpr{Values: []any{"online", "unexpected"}}},
				{"StatusOverrideTwin", status, &expr.ValidationExpr{Values: []any{"online", "unexpected"}}},
			}
			values := make(map[string]*Value, len(roots))
			for index := range roots {
				if reverse {
					index = len(roots) - 1 - index
				}
				root := roots[index]
				attribute := &expr.AttributeExpr{Type: root.typeExpr, Validation: root.validation}
				layout, err := codegen.PlanGoType(attribute, codegen.GoTypePlanOptions{
					Owner: owner.ImportPath(), RetainNamedValue: true,
					Policy: codegen.GoLayoutPolicy{UseDefault: true, SumType: true},
					Bind: func(request codegen.GoTypeBindingRequest) (codegen.GoTypeBinding, error) {
						declaration, err := owner.Type(request.Attribute.Type.(expr.UserType))
						return codegen.GoTypeBinding{Owner: owner.ImportPath(), Type: declaration}, err
					},
				})
				require.NoError(t, err)
				value, err := plan.AddStandalone(root.name, root.name, attribute, layout)
				require.NoError(t, err)
				values[root.name] = value
			}
			source := renderStandaloneTestPlan(t, generation, owner, plan)
			originalCount := len(regexp.MustCompile(`(?m)^func validate\w+Original\w*\(`).FindAllString(source, -1))
			rootCount := len(regexp.MustCompile(`(?m)^func validate\w+JSON\(`).FindAllString(source, -1))
			t.Logf("generated original validators=%d root transport validators=%d", originalCount, rootCount)

			module := t.TempDir()
			writeTestFile(t, filepath.Join(module, "go.mod"), fmt.Sprintf(`module example.com

go 1.26.0

require goa.design/goa/v3 v3.0.0

replace goa.design/goa/v3 => %s
`, filepath.ToSlash(goaModuleDirectory(t))))
			writeTestFile(t, filepath.Join(module, "gen/types/codec.go"), source)
			sections := make([]*codegen.SectionTemplate, 0, 4)
			sections = append(sections, codegen.Header("Synthetic named values.", "types", nil))
			for _, name := range []string{"Bundle", "Node", "Status"} {
				value := values[name]
				definition, err := originalDefinitionLayout(value.originalLayout, value.service.Type.(expr.UserType).Attribute())
				require.NoError(t, err)
				sections = append(sections, &codegen.SectionTemplate{
					Name: name, Source: "\ntype {{.Name}} {{.Definition}}\n",
					Data: map[string]string{
						"Name": name, "Definition": definition.Link(owner.ImportPath(), owner.ImportName).Def(),
					},
				})
			}
			declarations := &codegen.File{Path: "gen/types/types.go", SectionTemplates: sections}
			_, err := declarations.Render(module)
			require.NoError(t, err)
			// Multiple roots use one named declaration, so bind each consumer
			// call to the function name Goa chose for that occurrence.
			var replacements []string
			for index := len(roots) - 1; index >= 0; index-- {
				name := roots[index].name
				replacements = append(replacements,
					"Encode"+name, values[name].EncodeDeclaration().Name(),
					"Decode"+name, values[name].DecodeDeclaration().Name())
			}
			consumer := strings.NewReplacer(replacements...).Replace(standaloneValidationConsumer)
			writeTestFile(t, filepath.Join(module, "gen/types/contract_test.go"), consumer)
			runGeneratedCodecTests(t, module)
			assert.Equal(t, 5, originalCount, "identical effective rules share the original helper")
			assert.Equal(t, 2, rootCount, "only the two distinct overrides need transport helpers")
			assert.Equal(t, 1, strings.Count(source, "func validatejsonNodeTransport("))
		})
	}
}

const standaloneValidationConsumer = `package types

import "testing"

func TestRootAndNestedValidation(t *testing.T) {
	hint := "present"
	good := func() *Node {
		return &Node{Name: "valid", Data: []byte("x"), Status: Status("online")}
	}
	for _, test := range []struct {
		name string
		encode func(*Node) ([]byte, error)
		decode func([]byte) (*Node, error)
		requiredHint bool
	}{
		{"definition", EncodeNode, DecodeNode, false},
		{"same_rules", EncodeNodeRedundant, DecodeNodeRedundant, false},
		{"root_required", EncodeNodeRequired, DecodeNodeRequired, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := good()
			if test.requiredHint { node.Hint = &hint }
			node.Next = good()
			if _, err := test.encode(node); err != nil { t.Fatal(err) }
			text := ` + "`" + `{"name":"valid","data":"eA==","next":{"name":"child","data":"eA=="}}` + "`" + `
			if test.requiredHint {
				text = ` + "`" + `{"name":"valid","data":"eA==","hint":"present","next":{"name":"child","data":"eA=="}}` + "`" + `
			}
			decoded, err := test.decode([]byte(text))
			if err != nil { t.Fatal(err) }
			if decoded.Status != "online" || decoded.Next.Status != "online" {
				t.Fatal("missing optional status did not receive the named default")
			}
			if _, err := test.encode(nil); err == nil { t.Fatal("nil root accepted") }
			for _, invalid := range []string{
				"null", ` + "`" + `{"data":"eA=="}` + "`" + `,
				` + "`" + `{"name":"valid"}` + "`" + `, ` + "`" + `{"name":"valid","data":null}` + "`" + `,
				` + "`" + `{"name":"valid","data":"eA==","hint":"present","next":{"name":"child"}}` + "`" + `,
				` + "`" + `{"name":"valid","data":"eA==","hint":"present","status":"unexpected"}` + "`" + `,
			} {
				if _, err := test.decode([]byte(invalid)); err == nil { t.Fatalf("accepted %s", invalid) }
			}
			node.Data = nil
			if _, err := test.encode(node); err == nil { t.Fatal("nil required data accepted") }
			node = good()
			node.Hint = &hint
			node.Next = good()
			node.Next.Data = nil
			if _, err := test.encode(node); err == nil { t.Fatal("nil nested required data accepted") }
			node = good()
			node.Hint = &hint
			node.Status = "unexpected"
			if _, err := test.encode(node); err == nil { t.Fatal("invalid defaulted alias accepted") }
			node = good()
			_, encodeErr := test.encode(node)
			_, decodeErr := test.decode([]byte(` + "`" + `{"name":"valid","data":"eA=="}` + "`" + `))
			if (encodeErr != nil) != test.requiredHint || (decodeErr != nil) != test.requiredHint {
				t.Fatalf("root Required override lost: encode=%v decode=%v", encodeErr, decodeErr)
			}
		})
	}
	for _, test := range []struct {
		name string
		encode func(*Bundle) ([]byte, error)
		decode func([]byte) (*Bundle, error)
	}{
		{"definition", EncodeBundle, DecodeBundle},
		{"same_rules", EncodeBundleTwin, DecodeBundleTwin},
	} {
		t.Run("bundle_"+test.name, func(t *testing.T) {
			if _, err := test.encode(&Bundle{First: good(), Second: good()}); err != nil { t.Fatal(err) }
			if _, err := test.encode(&Bundle{First: good()}); err == nil { t.Fatal("missing required sibling accepted") }
			if _, err := test.decode([]byte(` + "`" + `{"first":{"name":"valid","data":"eA=="}}` + "`" + `)); err == nil {
				t.Fatal("missing decoded sibling accepted")
			}
		})
	}
}

func TestEnumReplacement(t *testing.T) {
	for _, test := range []struct {
		name string
		encode func(Status) ([]byte, error)
		decode func([]byte) (Status, error)
		override bool
	}{
		{"definition", EncodeStatus, DecodeStatus, false},
		{"same_rules", EncodeStatusSame, DecodeStatusSame, false},
		{"override", EncodeStatusOverride, DecodeStatusOverride, true},
		{"override_twin", EncodeStatusOverrideTwin, DecodeStatusOverrideTwin, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, value := range []Status{"online", "offline", "unexpected", "invalid"} {
				want := value == "online" || !test.override && value == "offline" || test.override && value == "unexpected"
				_, encodeErr := test.encode(value)
				decoded, decodeErr := test.decode([]byte(` + "`" + `"` + "`" + ` + string(value) + ` + "`" + `"` + "`" + `))
				if (encodeErr == nil) != want || (decodeErr == nil) != want {
					t.Fatalf("%s: want accepted=%t encode=%v decode=%v", value, want, encodeErr, decodeErr)
				}
				if want && decoded != value { t.Fatalf("decoded %q as %q", value, decoded) }
			}
		})
	}
}
`
