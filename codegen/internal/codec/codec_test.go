// Package codec checks the JSON contract shared by generated MCP clients and servers.
package codec

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// codecTestTypeWriter adds the service package to named types and keeps
	// Goa's empty type as an unnamed empty struct in these generated test files.
	codecTestTypeWriter struct {
		goacodegen.Attributor
		packageName string
	}

	// primitiveCodecTestWriter follows Goa's service resolver contract: Ref
	// owns qualification, so callers do not ask Package before resolving a type.
	primitiveCodecTestWriter struct {
		goacodegen.Attributor
	}

	// importPriorityCodecTestWriter resolves service references with the import
	// names chosen for the generated codec package.
	importPriorityCodecTestWriter struct {
		goacodegen.Attributor
		pkg            *goacodegen.GeneratedPackage
		genpkg         string
		defaultPackage string
	}
)

// TestPlanRendersOneExactJSONContract verifies that decoding keeps every
// representable null until generated validation has checked it. It also checks
// that field names come from the Goa design instead of a transport convention.
func TestPlanRendersOneExactJSONContract(t *testing.T) {
	serviceAttribute := codecTestAttribute()
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	servicePackage, err := generation.ClaimPackage("example.com/gen/widgets")
	require.NoError(t, err)
	declareCodecTestServiceTypes(t, servicePackage, serviceAttribute)

	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	value, err := planned.Add("widgets.create.payload", "CreatePayload", serviceAttribute, EncodeAndDecode)
	require.NoError(t, err)
	require.NoError(t, value.PlanTransportConstructor())
	require.NoError(t, generation.Freeze())

	service := goacodegen.NewAttributeContext(false, false, true, "widgets", servicePackage.Scope())
	require.NoError(t, value.BindService(newCodecTestServiceAttributor(service.Scope)))
	files, err := planned.Files()
	require.NoError(t, err)
	require.Len(t, files, 1)

	directory := t.TempDir()
	_, err = files[0].Render(directory)
	require.NoError(t, err)
	source := readOnlyGoFile(t, directory)

	require.Contains(t, source, "MimeType *string")
	require.Contains(t, source, "`json:\"mimeType\"`")
	require.Contains(t, source, "Aliases  []*CreatePayloadAliasTransport")
	require.Contains(t, source, "State    *CreatePayloadStateTransport")
	require.Contains(t, source, "if e == nil")
	require.Contains(t, source, "func (u CreatePayloadStateTransport) Validate() error")
	require.Contains(t, source, "Inactive *struct")
	require.Contains(t, source, "if u.Inactive == nil")
	require.NotContains(t, source, "widgets.ValidateCreatePayload(in)")
	require.Contains(t, source, "func EncodeCreatePayload")
	require.Contains(t, source, "func DecodeCreatePayload")
	require.Contains(t, source, "func NewCreatePayload(body *CreatePayloadTransport)")
	require.Contains(t, source, "return NewCreatePayload(body)")
	require.Contains(t, source, "*widgets.CreatePayload")
}

// TestValueReportsExactTransportFields checks that another generated package
// can fill the private JSON type without guessing its field or alias names.
func TestValueReportsExactTransportFields(t *testing.T) {
	serviceAttribute := codecTestAttribute()
	payload := serviceAttribute.Type.(goaexpr.UserType).Attribute()
	fields := *goaexpr.AsObject(payload.Type)
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	value, err := planned.Add("widgets.create.payload", "CreatePayload", serviceAttribute, DecodeOnly)
	require.NoError(t, err)
	require.NoError(t, value.PlanTransportConstructor())
	require.NoError(t, generation.Freeze())

	qualifier := func(importPath string) string {
		require.Equal(t, "example.com/gen/mcp_widgets/internal/codec", importPath)
		return "mcpcodec"
	}
	typeRef, err := value.TransportTypeName("example.com/gen/mcp_widgets", qualifier)
	require.NoError(t, err)
	require.Equal(t, "mcpcodec.CreatePayloadTransport", typeRef)
	mimeType, err := value.TransportField(
		fields[0].Attribute,
		"mimeType",
		"example.com/gen/mcp_widgets",
		qualifier,
	)
	require.NoError(t, err)
	require.Equal(t, "MimeType", mimeType.Selector)
	require.Equal(t, "*string", mimeType.TypeRef)
	require.Equal(t, "string", mimeType.ValueTypeRef)
	require.True(t, mimeType.Pointer)
	require.Empty(t, mimeType.ElementTypeRef)
	aliases, err := value.TransportField(
		fields[1].Attribute,
		"aliases",
		"example.com/gen/mcp_widgets",
		qualifier,
	)
	require.NoError(t, err)
	require.Equal(t, "Aliases", aliases.Selector)
	require.Equal(t, "[]*mcpcodec.CreatePayloadAliasTransport", aliases.TypeRef)
	require.Equal(t, "[]*mcpcodec.CreatePayloadAliasTransport", aliases.ValueTypeRef)
	require.Equal(t, "mcpcodec.CreatePayloadAliasTransport", aliases.ElementTypeRef)
	require.False(t, aliases.Pointer)
	require.True(t, aliases.ElementPointer)
}

// TestPlanKeepsUnionSourceIdentity checks that copied OneOf fields reuse one
// declaration while separately written fields remain separate.
func TestPlanKeepsUnionSourceIdentity(t *testing.T) {
	t.Run("copy reuses declaration", func(t *testing.T) {
		generation, err := goacodegen.NewGeneration("example.com/gen", nil)
		require.NoError(t, err)
		planned, err := NewPlan(
			generation,
			"example.com/gen/mcp_widgets/internal/codec",
			"codec",
			"example.com/gen/widgets",
		)
		require.NoError(t, err)
		attribute := codecTestAttribute()
		first, err := planned.Add("widgets.first.payload", "CreatePayload", attribute, EncodeAndDecode)
		require.NoError(t, err)
		copy, err := planned.Add("widgets.copy.payload", "CreatePayload", goaexpr.DupAtt(attribute), EncodeAndDecode)
		require.NoError(t, err)
		require.Same(t, first.unions[0].declaration, copy.unions[0].declaration)
	})

	t.Run("independent same name fails", func(t *testing.T) {
		generation, err := goacodegen.NewGeneration("example.com/gen", nil)
		require.NoError(t, err)
		planned, err := NewPlan(
			generation,
			"example.com/gen/mcp_widgets/internal/codec",
			"codec",
			"example.com/gen/widgets",
		)
		require.NoError(t, err)
		_, err = planned.Add("widgets.first.payload", "CreatePayload", codecTestAttribute(), EncodeAndDecode)
		require.NoError(t, err)
		_, err = planned.Add("widgets.second.payload", "CreatePayload", codecTestAttribute(), EncodeAndDecode)
		require.ErrorContains(t, err, "set TypeName")
	})

	t.Run("independent names stay separate", func(t *testing.T) {
		generation, err := goacodegen.NewGeneration("example.com/gen", nil)
		require.NoError(t, err)
		planned, err := NewPlan(
			generation,
			"example.com/gen/mcp_widgets/internal/codec",
			"codec",
			"example.com/gen/widgets",
		)
		require.NoError(t, err)
		first, err := planned.Add("widgets.first.payload", "CreatePayload", codecTestAttribute(), EncodeAndDecode)
		require.NoError(t, err)
		second, err := planned.Add("widgets.second.payload", "OtherPayload", codecTestAttribute(), EncodeAndDecode)
		require.NoError(t, err)
		require.NotSame(t, first.unions[0].declaration, second.unions[0].declaration)
	})
}

// TestGeneratedCodecBehavior compiles the rendered codec and exercises the
// JSON errors that depend on pointers, union selection, and strict decoding.
func TestGeneratedCodecBehavior(t *testing.T) {
	moduleDirectory := t.TempDir()
	serviceAttribute := codecTestAttribute()
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	servicePackage, err := generation.ClaimPackage("example.com/gen/widgets")
	require.NoError(t, err)
	declareCodecTestServiceTypes(t, servicePackage, serviceAttribute)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	value, err := planned.Add("widgets.create.payload", "CreatePayload", serviceAttribute, EncodeAndDecode)
	require.NoError(t, err)
	require.NoError(t, value.PlanTransportConstructor())
	require.NoError(t, generation.Freeze())
	service := goacodegen.NewAttributeContext(false, false, true, "widgets", servicePackage.Scope())
	require.NoError(t, value.BindService(newCodecTestServiceAttributor(service.Scope)))
	files, err := planned.Files()
	require.NoError(t, err)
	_, err = files[0].Render(moduleDirectory)
	require.NoError(t, err)

	goaDirectory := goaModuleDirectory(t)
	writeTestFile(t, filepath.Join(moduleDirectory, "go.mod"), fmt.Sprintf(`module example.com

go 1.24

require goa.design/goa/v3 v3.0.0

replace goa.design/goa/v3 => %s
`, filepath.ToSlash(goaDirectory)))
	writeTestFile(t, filepath.Join(moduleDirectory, "gen/widgets/service.go"), codecTestServiceSource)
	writeTestFile(
		t,
		filepath.Join(moduleDirectory, "gen/mcp_widgets/internal/codec/codec_behavior_test.go"),
		codecBehaviorTestSource,
	)

	runGeneratedCodecTests(t, moduleDirectory)
}

// TestPlanUsesCodecPackageImportScope checks that an import name used by
// another generated package does not rename imports in the codec file.
func TestPlanUsesCodecPackageImportScope(t *testing.T) {
	serviceAttribute := codecTestAttribute()
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	unrelated, err := generation.ClaimPackage("example.com/gen/unrelated")
	require.NoError(t, err)
	require.NoError(t, unrelated.RequireImport(goacodegen.NewImport("json", "example.com/custom/json")))
	servicePackage, err := generation.ClaimPackage("example.com/gen/widgets")
	require.NoError(t, err)
	declareCodecTestServiceTypes(t, servicePackage, serviceAttribute)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	value, err := planned.Add("widgets.create.payload", "CreatePayload", serviceAttribute, EncodeAndDecode)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	require.Equal(t, "json", unrelated.ImportName("example.com/custom/json"))
	require.Equal(t, "json", planned.pkg.ImportName("encoding/json"))

	service := goacodegen.NewAttributeContext(false, false, true, "widgets", servicePackage.Scope())
	require.NoError(t, value.BindService(newCodecTestServiceAttributor(service.Scope)))
	files, err := planned.Files()
	require.NoError(t, err)
	directory := t.TempDir()
	_, err = files[0].Render(directory)
	require.NoError(t, err)
	source := readOnlyGoFile(t, directory)
	require.Contains(t, source, `"encoding/json"`)
	require.Contains(t, source, "json.NewDecoder")
	require.NotContains(t, source, "json2.NewDecoder")
}

// TestPlanLetsServiceWriterResolvePrimitiveReference checks that primitive
// codecs render without asking for an unused service package.
func TestPlanLetsServiceWriterResolvePrimitiveReference(t *testing.T) {
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	value, err := planned.Add(
		"widgets.answer.result",
		"AnswerResult",
		&goaexpr.AttributeExpr{Type: goaexpr.String},
		EncodeAndDecode,
	)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	service := &primitiveCodecTestWriter{Attributor: goacodegen.NewAttributeScope(goacodegen.NewNameScope())}
	require.NoError(t, value.BindService(service))
	files, err := planned.Files()
	require.NoError(t, err)
	directory := t.TempDir()
	_, err = files[0].Render(directory)
	require.NoError(t, err)
	source := readOnlyGoFile(t, directory)
	require.Contains(t, source, "func EncodeAnswerResult(in string)")
	require.Contains(t, source, "func DecodeAnswerResult(data []byte) (out string, err error)")
	require.NotContains(t, source, `"example.com/gen/widgets"`)
}

// TestPlanKeepsGeneratedImportPriority checks both field orders because an
// authored field must not rename a generated package requested by another
// field that happens to be visited later.
func TestPlanKeepsGeneratedImportPriority(t *testing.T) {
	generatedFirst := renderImportPriorityCodec(t, true)
	authoredFirst := renderImportPriorityCodec(t, false)
	require.Equal(t, importPriorityReferences(generatedFirst), importPriorityReferences(authoredFirst))
	for _, source := range []string{generatedFirst, authoredFirst} {
		require.Contains(t, source, `shared "example.com/gen/shared"`)
		require.Contains(t, source, "shared.SharedValue")
		require.Contains(t, source, "shared.Value")
		require.NotContains(t, source, `custom "example.com/gen/shared"`)
	}
}

// renderImportPriorityCodec writes and compiles one codec after visiting the
// generated and authored references in the requested order.
func renderImportPriorityCodec(t *testing.T, generatedFirst bool) string {
	t.Helper()
	shared := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{
			Type: goaexpr.String,
			Meta: goaexpr.MetaExpr{"struct:pkg:path": {"shared"}},
		},
		TypeName: "SharedValue",
		UID:      "codec-test-shared-value",
	}
	fields := []*goaexpr.NamedAttributeExpr{
		{Name: "generated", Attribute: &goaexpr.AttributeExpr{Type: shared}},
		{Name: "authored", Attribute: &goaexpr.AttributeExpr{
			Type: goaexpr.String,
			Meta: goaexpr.MetaExpr{
				"struct:field:type": {"custom.Value", "example.com/gen/shared", "custom"},
			},
		}},
	}
	if !generatedFirst {
		fields[0], fields[1] = fields[1], fields[0]
	}
	object := goaexpr.Object(fields)
	payload := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{
			Type:       &object,
			Validation: &goaexpr.ValidationExpr{Required: []string{"generated", "authored"}},
		},
		TypeName: "CollisionPayload",
		UID:      "codec-test-collision-payload",
	}
	attribute := &goaexpr.AttributeExpr{Type: payload}
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	servicePackage, err := generation.ClaimPackage("example.com/gen/widgets")
	require.NoError(t, err)
	_, err = servicePackage.DeclareUserType(payload)
	require.NoError(t, err)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	value, err := planned.Add("widgets.collision.payload", "CollisionPayload", attribute, EncodeAndDecode)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	require.Equal(t, "shared", planned.pkg.ImportName("example.com/gen/shared"))
	service := goacodegen.NewAttributeContext(false, false, true, "widgets", servicePackage.Scope())
	require.NoError(t, value.BindService(&importPriorityCodecTestWriter{
		Attributor:     service.Scope,
		pkg:            planned.pkg,
		genpkg:         generation.GenPkg(),
		defaultPackage: "widgets",
	}))
	files, err := planned.Files()
	require.NoError(t, err)
	directory := t.TempDir()
	_, err = files[0].Render(directory)
	require.NoError(t, err)
	source := readOnlyGoFile(t, directory)
	compileImportPriorityCodec(t, source)
	return source
}

// TestPlanRejectsIncompleteLifecycle checks the errors returned when a caller
// skips name planning, service linking, or supplies the same value twice.
func TestPlanRejectsIncompleteLifecycle(t *testing.T) {
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	attribute := codecTestAttribute()
	_, err = planned.Add("widgets.create.payload", "CreatePayload", attribute, EncodeAndDecode)
	require.NoError(t, err)
	_, err = planned.Add("widgets.create.payload", "OtherPayload", attribute, EncodeAndDecode)
	require.EqualError(t, err, `JSON value key "widgets.create.payload" is already planned`)
	_, err = planned.Add("widgets.invalid", "Invalid", attribute, Direction(0))
	require.EqualError(t, err, `plan JSON value "widgets.invalid": direction must select encoding, decoding, or typed construction`)
	_, err = planned.Files()
	require.EqualError(t, err, "JSON codec files cannot be rendered before generation freeze")
}

// TestPlanKeepsNestedTypesLocalToEachValue verifies that payload and result
// codecs may use the same nested design type without declaring it twice.
func TestPlanKeepsNestedTypesLocalToEachValue(t *testing.T) {
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	attribute := codecTestAttribute()
	_, err = planned.Add("widgets.create.payload", "CreatePayload", attribute, EncodeAndDecode)
	require.NoError(t, err)
	_, err = planned.Add("widgets.create.result", "CreateResult", attribute, EncodeAndDecode)
	require.NoError(t, err)
}

// TestPlanHandlesRecursiveAndRepeatedTypes checks that one design type is
// copied once even when fields refer to it more than once or back to itself.
func TestPlanHandlesRecursiveAndRepeatedTypes(t *testing.T) {
	node := recursiveCodecTestType()
	attribute := &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		{Name: "root", Attribute: &goaexpr.AttributeExpr{Type: node}},
		{Name: "alternate", Attribute: &goaexpr.AttributeExpr{Type: node}},
	}}
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	servicePackage, err := generation.ClaimPackage("example.com/gen/widgets")
	require.NoError(t, err)
	_, err = servicePackage.DeclareUserType(node)
	require.NoError(t, err)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	value, err := planned.Add("widgets.walk.payload", "WalkPayload", attribute, EncodeAndDecode)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	service := goacodegen.NewAttributeContext(false, false, true, "widgets", servicePackage.Scope())
	require.NoError(t, value.BindService(newCodecTestServiceAttributor(service.Scope)))
	files, err := planned.Files()
	require.NoError(t, err)

	directory := t.TempDir()
	_, err = files[0].Render(directory)
	require.NoError(t, err)
	source := readOnlyGoFile(t, directory)
	require.Equal(t, 1, strings.Count(source, "type WalkPayloadNodeTransport "))
	require.Contains(t, source, "Root      *WalkPayloadNodeTransport")
	require.Contains(t, source, "Alternate *WalkPayloadNodeTransport")
}

// TestPlanUsesStableNamesForCollidingGoSpellings checks that two distinct
// design names which become the same Go name still produce valid stable code.
func TestPlanUsesStableNamesForCollidingGoSpellings(t *testing.T) {
	forward := renderCollidingCodec(t, false)
	reverse := renderCollidingCodec(t, true)
	require.Equal(t, forward, reverse)
	require.Contains(t, forward, "type FooBarTransport string")
	require.Contains(t, forward, "type FooBarTransport2 string")

	moduleDirectory := t.TempDir()
	writeTestFile(t, filepath.Join(moduleDirectory, "go.mod"), fmt.Sprintf(`module example.com

go 1.24

require goa.design/goa/v3 v3.0.0

replace goa.design/goa/v3 => %s
`, filepath.ToSlash(goaModuleDirectory(t))))
	writeTestFile(t, filepath.Join(moduleDirectory, "codec/codec.go"), forward)
	runGeneratedCodecTests(t, moduleDirectory)
}

// TestPlanEmitsOnlyRequestedDirection checks that one-way callers do not leave
// the opposite conversion function or its recursive helpers in generated code.
func TestPlanEmitsOnlyRequestedDirection(t *testing.T) {
	encodeSource, encodeValue := renderDirectionalCodec(t, EncodeOnly)
	require.NotNil(t, encodeValue.EncodeDeclaration())
	require.Nil(t, encodeValue.DecodeDeclaration())
	require.Contains(t, encodeSource, "func EncodeCreatePayload")
	require.Contains(t, encodeSource, "func encodeEmptyToEmpty")
	require.NotContains(t, encodeSource, "func DecodeCreatePayload")
	require.NotContains(t, encodeSource, "func decodeEmptyToEmpty")
	require.NotContains(t, encodeSource, `bytes "bytes"`)
	require.NotContains(t, encodeSource, `io "io"`)

	decodeSource, decodeValue := renderDirectionalCodec(t, DecodeOnly)
	require.Nil(t, decodeValue.EncodeDeclaration())
	require.NotNil(t, decodeValue.DecodeDeclaration())
	require.Contains(t, decodeSource, "func DecodeCreatePayload")
	require.Contains(t, decodeSource, "func decodeEmptyToEmpty")
	require.NotContains(t, decodeSource, "func EncodeCreatePayload")
	require.NotContains(t, decodeSource, "func encodeEmptyToEmpty")
	require.Contains(t, decodeSource, `bytes "bytes"`)
	require.Contains(t, decodeSource, `io "io"`)

	constructorSource, constructorValue := renderDirectionalCodec(t, ConstructOnly)
	require.Nil(t, constructorValue.EncodeDeclaration())
	require.Nil(t, constructorValue.DecodeDeclaration())
	require.NotNil(t, constructorValue.TransportConstructorDeclaration())
	require.Contains(t, constructorSource, "func NewCreatePayload")
	require.Contains(t, constructorSource, "func decodeEmptyToEmpty")
	require.NotContains(t, constructorSource, "func EncodeCreatePayload")
	require.NotContains(t, constructorSource, "func DecodeCreatePayload")
	require.NotContains(t, constructorSource, `bytes "bytes"`)
	require.NotContains(t, constructorSource, `io "io"`)
}

// codecTestAttribute builds a service payload with the presence cases that a
// JSON decoder must distinguish from ordinary zero values.
func codecTestAttribute() *goaexpr.AttributeExpr {
	alias := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{Type: goaexpr.String},
		TypeName:      "Alias",
		UID:           "codec-test-alias",
	}
	details := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{
			Type: &goaexpr.Object{
				{Name: "label", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
			},
			Validation: &goaexpr.ValidationExpr{Required: []string{"label"}},
		},
		TypeName: "Details",
		UID:      "codec-test-details",
	}
	state := &goaexpr.Union{
		TypeName: "State",
		Values: []*goaexpr.NamedAttributeExpr{
			{Name: "active", Attribute: &goaexpr.AttributeExpr{Type: alias}},
			{Name: "details", Attribute: &goaexpr.AttributeExpr{Type: details}},
			{Name: "inactive", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.Empty}},
		},
	}
	object := &goaexpr.Object{
		{Name: "mimeType", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		{Name: "aliases", Attribute: &goaexpr.AttributeExpr{Type: &goaexpr.Array{
			ElemType:         &goaexpr.AttributeExpr{Type: alias},
			NonNullableElems: true,
		}}},
		{Name: "state", Attribute: &goaexpr.AttributeExpr{Type: state}},
	}
	payload := &goaexpr.UserTypeExpr{
		AttributeExpr: &goaexpr.AttributeExpr{
			Type:       object,
			Validation: &goaexpr.ValidationExpr{Required: []string{"mimeType", "aliases", "state"}},
		},
		TypeName: "CreatePayload",
		UID:      "codec-test-payload",
	}
	return &goaexpr.AttributeExpr{Type: payload}
}

// recursiveCodecTestType returns a named object whose next field refers back
// to the same design type.
func recursiveCodecTestType() goaexpr.UserType {
	node := &goaexpr.UserTypeExpr{
		TypeName: "Node",
		UID:      "codec-test-node",
	}
	node.AttributeExpr = &goaexpr.AttributeExpr{Type: &goaexpr.Object{
		{Name: "label", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		{Name: "next", Attribute: &goaexpr.AttributeExpr{Type: node}},
	}}
	return node
}

// renderCollidingCodec renders the same two primitive values in either input
// order so the test can compare Goa's final package names.
func renderCollidingCodec(t *testing.T, reverse bool) string {
	t.Helper()
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	entries := []struct {
		key  string
		name string
	}{
		{key: "widgets.first", name: "foo-bar"},
		{key: "widgets.second", name: "foo_bar"},
	}
	if reverse {
		entries[0], entries[1] = entries[1], entries[0]
	}
	values := make([]*Value, 0, len(entries))
	for _, entry := range entries {
		value, addErr := planned.Add(
			entry.key,
			entry.name,
			&goaexpr.AttributeExpr{Type: goaexpr.String},
			EncodeAndDecode,
		)
		require.NoError(t, addErr)
		values = append(values, value)
	}
	require.NoError(t, generation.Freeze())
	require.Panics(t, func() {
		planned.pkg.Import("example.com/gen/widgets")
	})
	service := goacodegen.NewAttributeContext(false, false, true, "", goacodegen.NewNameScope())
	for _, value := range values {
		require.NoError(t, value.BindService(service.Scope))
	}
	files, err := planned.Files()
	require.NoError(t, err)
	directory := t.TempDir()
	_, err = files[0].Render(directory)
	require.NoError(t, err)
	return readOnlyGoFile(t, directory)
}

// renderDirectionalCodec renders the same recursive payload for one requested
// direction and returns its planned declaration accessors for inspection.
func renderDirectionalCodec(t *testing.T, direction Direction) (string, *Value) {
	t.Helper()
	serviceAttribute := codecTestAttribute()
	generation, err := goacodegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	servicePackage, err := generation.ClaimPackage("example.com/gen/widgets")
	require.NoError(t, err)
	declareCodecTestServiceTypes(t, servicePackage, serviceAttribute)
	planned, err := NewPlan(
		generation,
		"example.com/gen/mcp_widgets/internal/codec",
		"codec",
		"example.com/gen/widgets",
	)
	require.NoError(t, err)
	value, err := planned.Add("widgets.create.payload", "CreatePayload", serviceAttribute, direction)
	require.NoError(t, err)
	if direction == ConstructOnly {
		require.NoError(t, value.PlanTransportConstructor())
	}
	require.NoError(t, generation.Freeze())
	if direction == EncodeOnly || direction == ConstructOnly {
		require.Panics(t, func() {
			planned.pkg.Import("bytes")
		})
		require.Panics(t, func() {
			planned.pkg.Import("io")
		})
	} else {
		require.NotPanics(t, func() {
			planned.pkg.Import("bytes")
		})
		require.NotPanics(t, func() {
			planned.pkg.Import("io")
		})
	}
	service := goacodegen.NewAttributeContext(false, false, true, "widgets", servicePackage.Scope())
	require.NoError(t, value.BindService(newCodecTestServiceAttributor(service.Scope)))
	files, err := planned.Files()
	require.NoError(t, err)
	directory := t.TempDir()
	_, err = files[0].Render(directory)
	require.NoError(t, err)
	source := readOnlyGoFile(t, directory)
	compileCodecSource(t, source, codecTestServiceSource)
	return source, value
}

// compileCodecSource builds one rendered codec with the service declarations
// its generated functions call.
func compileCodecSource(t *testing.T, codecSource, serviceSource string) {
	t.Helper()
	moduleDirectory := t.TempDir()
	writeTestFile(t, filepath.Join(moduleDirectory, "go.mod"), fmt.Sprintf(`module example.com

go 1.24

require goa.design/goa/v3 v3.0.0

replace goa.design/goa/v3 => %s
`, filepath.ToSlash(goaModuleDirectory(t))))
	writeTestFile(t, filepath.Join(moduleDirectory, "gen/widgets/service.go"), serviceSource)
	writeTestFile(t, filepath.Join(moduleDirectory, "gen/mcp_widgets/internal/codec/codec.go"), codecSource)
	runGeneratedCodecTests(t, moduleDirectory)
}

// compileImportPriorityCodec builds the rendered codec with both types from
// the one generated package whose import name is under test.
func compileImportPriorityCodec(t *testing.T, codecSource string) {
	t.Helper()
	moduleDirectory := t.TempDir()
	writeTestFile(t, filepath.Join(moduleDirectory, "go.mod"), fmt.Sprintf(`module example.com

go 1.24

require goa.design/goa/v3 v3.0.0

replace goa.design/goa/v3 => %s
`, filepath.ToSlash(goaModuleDirectory(t))))
	writeTestFile(t, filepath.Join(moduleDirectory, "gen/mcp_widgets/internal/codec/codec.go"), codecSource)
	writeTestFile(t, filepath.Join(moduleDirectory, "gen/shared/types.go"), `package shared

type (
	SharedValue string
	Value string
)
`)
	writeTestFile(t, filepath.Join(moduleDirectory, "gen/widgets/service.go"), `package widgets

import shared "example.com/gen/shared"

type CollisionPayload struct {
	Generated shared.SharedValue
	Authored shared.Value
}
`)
	runGeneratedCodecTests(t, moduleDirectory)
}

// declareCodecTestServiceTypes reserves the names used by the service writer
// passed to BindService.
func declareCodecTestServiceTypes(t *testing.T, pkg *goacodegen.GeneratedPackage, attribute *goaexpr.AttributeExpr) {
	t.Helper()
	payload := attribute.Type.(goaexpr.UserType)
	_, err := pkg.DeclareUserType(payload)
	require.NoError(t, err)
	alias := exprAlias(attribute)
	_, err = pkg.DeclareUserType(alias)
	require.NoError(t, err)
	details := exprDetails(attribute)
	_, err = pkg.DeclareUserType(details)
	require.NoError(t, err)
	union := exprStateAttribute(attribute)
	_, err = pkg.DeclareUnion(union)
	require.NoError(t, err)
}

// exprDetails returns the object used by the payload's details state branch.
func exprDetails(attribute *goaexpr.AttributeExpr) goaexpr.UserType {
	for _, branch := range exprState(attribute).Values {
		if branch.Name == "details" {
			return branch.Attribute.Type.(goaexpr.UserType)
		}
	}
	panic("codec test state has no details branch")
}

// exprAlias returns the alias used by the payload's aliases field.
func exprAlias(attribute *goaexpr.AttributeExpr) goaexpr.UserType {
	object := goaexpr.AsObject(attribute.Type)
	array := goaexpr.AsArray(object.Attribute("aliases").Type)
	return array.ElemType.Type.(goaexpr.UserType)
}

// exprState returns the union used by the payload's state field.
func exprState(attribute *goaexpr.AttributeExpr) *goaexpr.Union {
	return exprStateAttribute(attribute).Type.(*goaexpr.Union)
}

// exprStateAttribute returns the field that owns the payload's state union.
func exprStateAttribute(attribute *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	object := goaexpr.AsObject(attribute.Type)
	return object.Attribute("state")
}

// readOnlyGoFile reads the single Go file rendered below directory.
func readOnlyGoFile(t *testing.T, directory string) string {
	t.Helper()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, root.Close())
	})
	var source []byte
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if source != nil {
			t.Fatalf("more than one Go file was rendered below %s", directory)
		}
		relativePath, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		source, err = root.ReadFile(relativePath)
		return err
	})
	require.NoError(t, err)
	require.NotNil(t, source)
	return string(source)
}

// goaModuleDirectory returns the local Goa module selected by the active workspace.
func goaModuleDirectory(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", "goa.design/goa/v3")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return strings.TrimSpace(string(output))
}

// runGeneratedCodecTests compiles every package in one temporary module and
// fails the calling test when compilation does not finish within two minutes.
func runGeneratedCodecTests(t *testing.T, moduleDirectory string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./...")
	command.Dir = moduleDirectory
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

// writeTestFile writes one source file in the disposable generated module.
func writeTestFile(t *testing.T, path, source string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
}

// newCodecTestServiceAttributor returns the service type writer used by the
// generated-module tests.
func newCodecTestServiceAttributor(attributor goacodegen.Attributor) goacodegen.Attributor {
	return &codecTestTypeWriter{Attributor: attributor, packageName: "widgets"}
}

// Name writes Goa's empty service type as an unnamed empty struct.
func (w *codecTestTypeWriter) Name(
	attribute *goaexpr.AttributeExpr,
	_ string,
	pointer bool,
	useDefault bool,
) string {
	if attribute.Type == goaexpr.Empty {
		return "struct {}"
	}
	return w.Attributor.Name(attribute, w.packageName, pointer, useDefault)
}

// Ref writes Goa's empty service type as a pointer to an unnamed empty struct.
func (w *codecTestTypeWriter) Ref(attribute *goaexpr.AttributeExpr, _ string) string {
	if attribute.Type == goaexpr.Empty {
		return "*struct {}"
	}
	return w.Attributor.Ref(attribute, w.packageName)
}

// Enter keeps the empty-type rule while following nested service types.
func (w *codecTestTypeWriter) Enter(attribute *goaexpr.AttributeExpr) goacodegen.Attributor {
	return &codecTestTypeWriter{
		Attributor:  w.Attributor.Enter(attribute),
		packageName: w.packageName,
	}
}

// Name writes a service type with the final package name selected by the
// codec file.
func (w *importPriorityCodecTestWriter) Name(
	attribute *goaexpr.AttributeExpr,
	_ string,
	pointer bool,
	useDefault bool,
) string {
	if custom, spec := goacodegen.GetMetaType(attribute); spec != nil {
		return importPriorityCustomType(custom, w.pkg.ImportName(spec.Path))
	}
	return w.Attributor.Name(attribute, w.packageName(attribute), pointer, useDefault)
}

// Ref writes a service reference with the final package name selected by the
// codec file.
func (w *importPriorityCodecTestWriter) Ref(attribute *goaexpr.AttributeExpr, _ string) string {
	if custom, spec := goacodegen.GetMetaType(attribute); spec != nil {
		return importPriorityCustomType(custom, w.pkg.ImportName(spec.Path))
	}
	return w.Attributor.Ref(attribute, w.packageName(attribute))
}

// Enter keeps final package names while following nested service types.
func (w *importPriorityCodecTestWriter) Enter(attribute *goaexpr.AttributeExpr) goacodegen.Attributor {
	return &importPriorityCodecTestWriter{
		Attributor:     w.Attributor.Enter(attribute),
		pkg:            w.pkg,
		genpkg:         w.genpkg,
		defaultPackage: w.defaultPackage,
	}
}

// packageName returns the codec file's final name for the package that owns
// attribute.
func (w *importPriorityCodecTestWriter) packageName(attribute *goaexpr.AttributeExpr) string {
	location := goacodegen.UserTypeLocation(attribute.Type)
	if location == nil {
		return w.defaultPackage
	}
	return w.pkg.ImportName(path.Join(w.genpkg, location.RelImportPath))
}

// importPriorityCustomType replaces authored metadata's requested package name
// with the final name selected for that import path.
func importPriorityCustomType(custom, packageName string) string {
	_, typeName, qualified := strings.Cut(custom, ".")
	if !qualified {
		panic(fmt.Sprintf("test metadata type %q has no package name", custom))
	}
	return packageName + "." + typeName
}

// importPriorityReferences returns the sorted generated lines that use the
// package whose name is under test.
func importPriorityReferences(source string) []string {
	var references []string
	for _, line := range strings.Split(source, "\n") {
		if strings.Contains(line, "example.com/gen/shared") || strings.Contains(line, "shared.") {
			references = append(references, strings.TrimSpace(line))
		}
	}
	sort.Strings(references)
	return references
}

// Package rejects eager package lookup because primitive references do not
// belong to an imported service package.
func (*primitiveCodecTestWriter) Package(*goaexpr.AttributeExpr) string {
	panic("primitive service reference has no package")
}

const codecTestServiceSource = `package widgets

import goa "goa.design/goa/v3/pkg"

type (
	Alias string
	Details struct {
		Label string
	}

	CreatePayload struct {
		MimeType string
		Aliases []Alias
		State State
	}

	State struct {
		kind StateKind
		Active Alias
		Details *Details
		Inactive *struct{}
	}

	StateKind string
)

const (
	StateKindActive StateKind = "active"
	StateKindDetails StateKind = "details"
	StateKindInactive StateKind = "inactive"
)

func NewStateActive(value Alias) State {
	return State{kind: StateKindActive, Active: value}
}

func NewStateDetails(value *Details) State {
	return State{kind: StateKindDetails, Details: value}
}

func NewStateInactive(value *struct{}) State {
	return State{kind: StateKindInactive, Inactive: value}
}

func (u State) Kind() StateKind {
	return u.kind
}

func (u State) AsActive() (_ Alias, ok bool) {
	if u.kind != StateKindActive {
		return
	}
	return u.Active, true
}

func (u *State) SetActive(value Alias) {
	u.kind = StateKindActive
	u.Active = value
}

func (u State) AsDetails() (_ *Details, ok bool) {
	if u.kind != StateKindDetails {
		return
	}
	return u.Details, true
}

func (u *State) SetDetails(value *Details) {
	u.kind = StateKindDetails
	u.Details = value
}

func (u State) AsInactive() (_ *struct{}, ok bool) {
	if u.kind != StateKindInactive {
		return
	}
	return u.Inactive, true
}

func (u *State) SetInactive(value *struct{}) {
	u.kind = StateKindInactive
	u.Inactive = value
}

func (u State) Validate() error {
	switch u.kind {
	case StateKindActive:
		return nil
	case StateKindDetails:
		if u.Details == nil {
			return goa.MissingFieldError("value", "State")
		}
		return nil
	case StateKindInactive:
		if u.Inactive == nil {
			return goa.MissingFieldError("value", "State")
		}
		return nil
	case "":
		return goa.MissingFieldError("type", "State")
	default:
		return goa.InvalidEnumValueError("type", u.kind, []any{
			string(StateKindActive),
			string(StateKindDetails),
			string(StateKindInactive),
		})
	}
}

`

const codecBehaviorTestSource = `package codec

import (
	"strings"
	"testing"

	"example.com/gen/widgets"
)

func TestDecodeContract(t *testing.T) {
	tests := []struct {
		name string
		json string
		contains string
	}{
		{"missing required field", ` + "`" + `{"aliases":["a"],"state":{"type":"active","value":"on"}}` + "`" + `, "mimeType"},
		{"unknown field", ` + "`" + `{"mimeType":"text/plain","aliases":["a"],"state":{"type":"active","value":"on"},"extra":true}` + "`" + `, "unknown field"},
		{"unknown union field", ` + "`" + `{"mimeType":"text/plain","aliases":["a"],"state":{"type":"active","value":"on","extra":true}}` + "`" + `, "unknown field"},
		{"multiple documents", ` + "`" + `{"mimeType":"text/plain","aliases":["a"],"state":{"type":"active","value":"on"}} {}` + "`" + `, "multiple JSON values"},
		{"no union branch", ` + "`" + `{"mimeType":"text/plain","aliases":["a"],"state":{}}` + "`" + `, "type"},
		{"null union branch", ` + "`" + `{"mimeType":"text/plain","aliases":["a"],"state":{"type":"active","value":null}}` + "`" + `, "non-null JSON value"},
		{"null alias array element", ` + "`" + `{"mimeType":"text/plain","aliases":[null],"state":{"type":"active","value":"on"}}` + "`" + `, "aliases"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeCreatePayload([]byte(test.json))
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("DecodeCreatePayload() error = %v, want text %q", err, test.contains)
			}
		})
	}
}

func TestValidBranchesRoundTrip(t *testing.T) {
	empty, err := DecodeCreatePayload([]byte(` + "`" + `{"mimeType":"text/plain","aliases":["a"],"state":{"type":"inactive","value":{}}}` + "`" + `))
	if err != nil {
		t.Fatalf("decode selected empty branch: %v", err)
	}
	if empty.State.Kind() != widgets.StateKindInactive {
		t.Fatalf("empty branch kind = %q", empty.State.Kind())
	}

	input := &widgets.CreatePayload{
		MimeType: "text/plain",
		Aliases: []widgets.Alias{"a"},
		State: widgets.NewStateActive("on"),
	}
	data, err := EncodeCreatePayload(input)
	if err != nil {
		t.Fatalf("encode active branch: %v", err)
	}
	decoded, err := DecodeCreatePayload(data)
	if err != nil {
		t.Fatalf("decode active branch: %v", err)
	}
	active, ok := decoded.State.AsActive()
	if !ok || active != "on" {
		t.Fatalf("active branch = %q, %t", active, ok)
	}
}

func TestEncodeRejectsNilSelectedBranchValues(t *testing.T) {
	tests := []struct {
		name string
		state widgets.State
	}{
		{"nil object branch", widgets.NewStateDetails(nil)},
		{"nil empty-message branch", widgets.NewStateInactive(nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := EncodeCreatePayload(&widgets.CreatePayload{
				MimeType: "text/plain",
				Aliases: []widgets.Alias{"a"},
				State: test.state,
			})
			if err == nil || !strings.Contains(err.Error(), "value") {
				t.Fatalf("EncodeCreatePayload() data = %s, error = %v, want missing branch value", data, err)
			}
		})
	}

	_, err := EncodeCreatePayload(&widgets.CreatePayload{
		MimeType: "text/plain",
		Aliases: []widgets.Alias{"a"},
		State: widgets.NewStateInactive(&struct{}{}),
	})
	if err != nil {
		t.Fatalf("encode selected empty-message branch: %v", err)
	}
}
`
