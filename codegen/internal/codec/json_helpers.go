// Package codec declares shared JSON checks once per output package. Templates
// receive finalized private names and import aliases, never reserved spellings.
package codec

import (
	"goa.design/goa-ai/codegen/internal/jsonshape"
	"goa.design/goa/v3/codegen"
)

type (
	jsonHelperPlan struct {
		read, value, text, invalid, unknown, kind, child *codegen.NameDeclaration
	}

	codecImportNames struct {
		Fmt, JSON, Bytes, IO, Goa, Sort, Strconv, UTF8, Reflect, MCP string
		// JSONV2 names Go's exact-field decoder for protocol answers with extensions.
		JSONV2 string
		// RawJSON names the shared decoder for exact integer JSON values.
		RawJSON string
	}
)

func (p *Plan) planJSONHelpers() error {
	if p.jsonHelpers != nil {
		return nil
	}
	helpers := &jsonHelperPlan{}
	for _, request := range []struct {
		name   string
		target **codegen.NameDeclaration
	}{
		{"readStrictJSON", &helpers.read},
		{"readJSONValue", &helpers.value},
		{"validateJSONText", &helpers.text},
		{"invalidGeneratedFieldTypeError", &helpers.invalid},
		{"unknownJSONFieldError", &helpers.unknown},
		{"decodedJSONType", &helpers.kind},
		{"generatedJSONChildPath", &helpers.child},
	} {
		declaration := codegen.NewPreferredName(codegen.NameFunction, request.name, codegen.UnexportedName,
			nameOrder{packagePath: p.pkg.ImportPath(), key: "shared-json:" + request.name})
		if err := p.pkg.DeclareName(declaration); err != nil {
			return err
		}
		*request.target = declaration
	}
	p.jsonHelpers = helpers
	return nil
}

func (p *Plan) jsonNames(imports codecImportNames) jsonshape.Names {
	helpers := p.jsonHelpers
	return jsonshape.Names{
		InvalidFieldType: helpers.invalid.Name(), UnknownField: helpers.unknown.Name(),
		DecodedType: helpers.kind.Name(), ChildPath: helpers.child.Name(),
		JSON: imports.JSON, Fmt: imports.Fmt, Sort: imports.Sort, Strconv: imports.Strconv,
	}
}

// importNames only resolves imports actually required by this package plan.
func (p *Plan) importNames() codecImportNames {
	var names codecImportNames
	for _, request := range []struct {
		path   string
		target *string
	}{
		{"fmt", &names.Fmt}, {"encoding/json", &names.JSON}, {"encoding/json/v2", &names.JSONV2},
		{"bytes", &names.Bytes}, {"io", &names.IO}, {"goa.design/goa/v3/pkg", &names.Goa},
		{"sort", &names.Sort}, {"strconv", &names.Strconv}, {"unicode/utf8", &names.UTF8},
		{"goa.design/goa-ai/runtime/agent/rawjson", &names.RawJSON}, {"reflect", &names.Reflect}, {"goa.design/goa-ai/runtime/mcp", &names.MCP},
	} {
		if _, ok := p.importPaths[request.path]; ok {
			*request.target = p.pkg.ImportName(request.path)
		}
	}
	return names
}
