package tests

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestGolden_ToolSpecs_SchemaAndExamples(t *testing.T) {
	root := filepath.Join("testdata", "golden")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		switch filepath.Base(path) {
		case "specs.go.golden":
			validateSpecsGolden(t, path)
		case "tool_schemas.json.golden":
			validateToolSchemasGolden(t, path)
		}
		return nil
	})
	require.NoError(t, err)
}

func validateSpecsGolden(t *testing.T, path string) {
	t.Helper()

	src, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	require.NoError(t, err)

	specs := extractTypeSpecs(t, file)
	if len(specs) == 0 {
		return
	}

	for _, spec := range specs {
		validateSchemaBytes(t, spec.schemaBytes, fmt.Sprintf("%s (%s)", path, spec.name))
		if len(spec.exampleBytes) > 0 {
			validateExampleAgainstSchema(t, spec.schemaBytes, spec.exampleBytes, fmt.Sprintf("%s (%s)", path, spec.name))
		}
	}
}

type extractedTypeSpec struct {
	name         string
	schemaBytes  []byte
	exampleBytes []byte
}

func extractTypeSpecs(t *testing.T, file *ast.File) []extractedTypeSpec {
	t.Helper()

	var specs []extractedTypeSpec
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}

		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "TypeSpec" {
			return true
		}

		spec := extractedTypeSpec{
			name: "<unknown>",
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "Name":
				if s, ok := evalStringLiteral(kv.Value); ok {
					spec.name = s
				}
			case "Schema":
				if b, ok := evalByteSliceLiteral(kv.Value); ok {
					spec.schemaBytes = b
				}
			case "ExampleJSON":
				if b, ok := evalByteSliceLiteral(kv.Value); ok {
					spec.exampleBytes = b
				}
			}
		}

		// We validate schemas later and keep the extraction lenient so a parse
		// mismatch doesn't hide other failures in the same file.
		if len(spec.schemaBytes) > 0 {
			specs = append(specs, spec)
		}
		return true
	})
	return specs
}

func validateToolSchemasGolden(t *testing.T, path string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err)
	require.True(t, json.Valid(raw), "expected valid JSON in %q", path)

	var doc struct {
		Tools []struct {
			ID      string `json:"id"`
			Payload struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
			} `json:"payload"`
			Result struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
			} `json:"result"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Tools, "expected at least one tool schema in %q", path)

	for _, tool := range doc.Tools {
		require.NotEmpty(t, tool.ID, "tool schema in %q missing id", path)
		validateSchemaBytes(t, tool.Payload.Schema, fmt.Sprintf("%s (%s.payload.schema)", path, tool.ID))
		validateSchemaBytes(t, tool.Result.Schema, fmt.Sprintf("%s (%s.result.schema)", path, tool.ID))
	}
}

func validateSchemaBytes(t *testing.T, schemaBytes []byte, label string) {
	t.Helper()

	require.NotEmpty(t, schemaBytes, "expected schema bytes for %s", label)
	require.True(t, json.Valid(schemaBytes), "schema is not valid JSON for %s", label)

	var schemaDoc any
	require.NoError(t, json.Unmarshal(schemaBytes, &schemaDoc), "unmarshal schema for %s", label)

	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource("schema.json", schemaDoc), "add schema resource for %s", label)
	_, err := c.Compile("schema.json")
	require.NoError(t, err, "compile schema for %s", label)
}

func validateExampleAgainstSchema(t *testing.T, schemaBytes []byte, exampleBytes []byte, label string) {
	t.Helper()

	var schemaDoc any
	require.NoError(t, json.Unmarshal(schemaBytes, &schemaDoc), "unmarshal schema for %s", label)

	var exampleDoc any
	require.NoError(t, json.Unmarshal(exampleBytes, &exampleDoc), "unmarshal example for %s", label)

	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource("schema.json", schemaDoc), "add schema resource for %s", label)
	schema, err := c.Compile("schema.json")
	require.NoError(t, err, "compile schema for %s", label)

	require.NoError(t, schema.Validate(exampleDoc), "example does not validate against schema for %s", label)
}

func evalStringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func evalByteSliceLiteral(expr ast.Expr) ([]byte, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil, false
	}
	arg := call.Args[0]
	s, ok := evalStringLiteral(arg)
	if !ok {
		return nil, false
	}
	return []byte(s), true
}
