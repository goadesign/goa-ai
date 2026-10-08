// Package codegen gives bound Goa methods the same typed host-answer conversion
// as MCP endpoints. Shared tool specs own these functions; local executors and
// registry providers call them before exposing only completed output to the model.
package codegen

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/internal/inputexchange"
	"goa.design/goa-ai/expr/agent"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// nativeInputPlan retains one method's conversion, validation and final names.
	nativeInputPlan struct {
		method                              *expr.MethodExpr
		conversion                          *inputexchange.Plan
		fill, outcome, pending              *codegen.NameDeclaration
		payloadValidation, resultValidation *jsoncodec.Value
		payloadRef, resultRef, completedRef string
		payloadValidate, resultValidate     string
	}

	// nativeInputFileData supplies final names and types to one generated converter.
	nativeInputFileData struct {
		Fill, Outcome, Pending              string
		PayloadRef, ResultRef, CompletedRef string
		PayloadValidate, ResultValidate     string
		Input                               *inputexchange.Plan
	}
)

// planInputExchanges reserves one private codec package and one conversion per
// native method. Multiple bound tools reuse the same functions and decoders.
func (p *toolSpecsPackagePlan) planInputExchanges(services *service.Plan, api *expr.APIExpr, tools []*agent.ToolExpr) error {
	for _, tool := range tools {
		if tool.Method == nil {
			continue
		}
		mapping, err := mcpinput.InputExchange(tool.Method)
		if err != nil {
			return err
		}
		if mapping == nil {
			continue
		}
		if p.inputMethods[tool.Method] != nil {
			continue
		}
		if err := p.ensureInputCodecs(); err != nil {
			return err
		}
		input := &nativeInputPlan{method: tool.Method}
		input.conversion, err = inputexchange.New(services, api, tool.Method, mapping.Pending, p.inputCodecs)
		if err != nil {
			return err
		}
		input.conversion.Native = true
		for _, field := range []struct {
			suffix    string
			attribute *expr.AttributeExpr
			target    **jsoncodec.Value
		}{
			{"Input", tool.Method.Payload, &input.payloadValidation},
			{"Outcome", tool.Method.Result, &input.resultValidation},
		} {
			layout, err := services.MethodTypeLayout(tool.Method, field.attribute)
			if err != nil {
				return err
			}
			*field.target, err = p.inputCodecs.Add(tool.Method.Name+":"+field.suffix, codegen.Goify(tool.Method.Name, true)+field.suffix, field.attribute, layout, jsoncodec.ValidateOnly)
			if err != nil {
				return err
			}
		}
		for _, field := range []struct {
			name   string
			target **codegen.NameDeclaration
		}{
			{"Fill" + codegen.Goify(tool.Method.Name, true) + "Input", &input.fill},
			{"Read" + codegen.Goify(tool.Method.Name, true) + "Outcome", &input.outcome},
			{"convert" + codegen.Goify(tool.Method.Name, true) + "Pending", &input.pending},
		} {
			visibility := codegen.ExportedName
			if field.target == &input.pending {
				visibility = codegen.UnexportedName
			}
			*field.target = codegen.NewPreferredName(codegen.NameFunction, field.name, visibility, specNameOrder{packagePath: p.public.ImportPath(), key: tool.Method.Name + ":" + field.name})
			if err := p.public.DeclareName(*field.target); err != nil {
				return err
			}
		}
		p.inputMethods[tool.Method] = input
	}
	if p.inputCodecs != nil {
		paths, err := reserveMethodLayoutImports(p.public, services, p.serviceImportPath, tools)
		if err != nil {
			return err
		}
		p.inputImportPaths = paths
	}
	return nil
}

// linkInputExchanges binds native Go references after Goa has assigned all names.
func (p *toolSpecsPackagePlan) linkInputExchanges(services *service.ServicesData) error {
	for _, input := range p.inputMethods {
		codecAttributor := services.ServiceAttributor(input.method.Service.Name, p.inputCodecPackage.ImportPath())
		for _, value := range []*jsoncodec.Value{input.payloadValidation, input.resultValidation} {
			if err := value.BindService(codecAttributor); err != nil {
				return err
			}
		}
		if err := input.conversion.BindCodecs(codecAttributor, codecAttributor); err != nil {
			return err
		}
		attributor := services.ServiceAttributor(input.method.Service.Name, p.public.ImportPath())
		mapping, err := mcpinput.InputExchange(input.method)
		if err != nil {
			return err
		}
		input.payloadRef = attributor.Ref(input.method.Payload, "")
		input.resultRef = attributor.Ref(input.method.Result, "")
		input.completedRef = attributor.Ref(mapping.Complete, "")
		alias := p.public.ImportName(p.inputCodecPackage.ImportPath())
		input.payloadValidate = alias + "." + input.payloadValidation.ValidationDeclaration().Name()
		input.resultValidate = alias + "." + input.resultValidation.ValidationDeclaration().Name()
		if err := input.conversion.Bind(p.public.ImportPath(), p.public.ImportName, alias, "result."+codegen.GoifyAtt(input.method.Result.Find(mapping.OutcomeName), mapping.OutcomeName, true)); err != nil {
			return err
		}
		input.conversion.MCPPackage = p.public.ImportName("goa.design/goa-ai/runtime/mcp")
	}
	return nil
}

// nativeInputFiles writes shared native conversion functions and their private
// codecs. Sorting method names makes generated output independent of map order.
func nativeInputFiles(plan *toolSpecsPlan) ([]*codegen.File, error) {
	var files []*codegen.File
	directories := make([]string, 0, len(plan.byDir))
	for dir := range plan.byDir {
		directories = append(directories, dir)
	}
	slices.Sort(directories)
	for _, dir := range directories {
		p := plan.byDir[dir]
		if p.inputCodecs == nil {
			continue
		}
		codecs, err := p.inputCodecs.Files("inputcodec")
		if err != nil {
			return nil, err
		}
		files = append(files, codecs...)
		if taskFile := nativeTaskFile(p); taskFile != nil {
			files = append(files, taskFile)
		}
		var methods []*nativeInputPlan
		for _, input := range p.inputMethods {
			methods = append(methods, input)
		}
		if len(methods) == 0 {
			continue
		}
		slices.SortFunc(methods, func(a, b *nativeInputPlan) int { return cmp.Compare(a.method.Name, b.method.Name) })
		paths := []string{"encoding/json", "goa.design/goa-ai/runtime/mcp", "goa.design/goa/v3/pkg", p.inputCodecPackage.ImportPath(), p.serviceImportPath}
		paths = append(paths, p.inputImportPaths...)
		sections := []*codegen.SectionTemplate{codegen.Header("Typed host input for bound Goa methods.", p.render.SpecsPackageName, importsForPaths(p.public, paths))}
		for _, input := range methods {
			sections = append(sections, &codegen.SectionTemplate{Name: fmt.Sprintf("input-exchange-%s", input.method.Name), Source: agentsTemplates.Read(nativeInputExchangeFileT), Data: nativeInputFileData{
				Fill:            input.fill.Name(),
				Outcome:         input.outcome.Name(),
				Pending:         input.pending.Name(),
				PayloadRef:      input.payloadRef,
				ResultRef:       input.resultRef,
				CompletedRef:    input.completedRef,
				PayloadValidate: input.payloadValidate,
				ResultValidate:  input.resultValidate,
				Input:           input.conversion,
			}})
		}
		files = append(files, &codegen.File{Path: filepath.Join(p.render.SpecsDir, "input_exchange.go"), SectionTemplates: sections})
	}
	return files, nil
}
