// Package codegen validates native job observations and generates the same typed
// host-answer conversions for local executors and registry providers. The service
// owns job state; generated functions only describe the next workflow operation.
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
	// nativeTaskPlan retains one creator's native observation and answer codecs.
	nativeTaskPlan struct {
		binding                                 *mcpinput.TaskBinding
		input                                   *inputexchange.TaskPlan
		created, read, fill, pending            *codegen.NameDeclaration
		observationValidation, answerValidation *jsoncodec.Value
		roles                                   [3]*nativeTaskRolePlan
		metadataLayout                          *codegen.GoTypePlan
		data                                    nativeTaskFileData
	}
	// nativeTaskFileData supplies final types, fields and functions to the converter.
	nativeTaskFileData struct {
		Created, Read, Fill, Pending                           string
		ObservationRef, CompletedRef, AnswerRef                string
		ObservationValidate, AnswerValidate                    string
		PollPointer                                            bool
		MetadataField, TaskIDField, PollField                  string
		OutcomeField, FailureCode, FailureMessage, FailureData string
		API, MCP                                               string
		Roles                                                  []*nativeTaskRoleData
		AnswerTaskIDField, AnswerTaskIDRef                     string
		Input                                                  *inputexchange.TaskPlan
	}
)

// ensureInputCodecs claims one private answer-codec package shared by ordinary
// input rounds and job questions. Each authored method retains its own types.
func (p *toolSpecsPackagePlan) ensureInputCodecs() error {
	if p.inputCodecs != nil {
		return nil
	}
	codecPath := p.public.ImportPath() + "/internal/inputcodec"
	var err error
	p.inputCodecs, err = jsoncodec.NewPlan(p.generation, codecPath)
	if err != nil {
		return err
	}
	p.inputCodecPackage, err = p.generation.ClaimPackage(codecPath)
	if err != nil {
		return err
	}
	if err := p.public.ReserveGeneratedImport(codegen.NewImport("geninputcodec", codecPath)); err != nil {
		return err
	}
	if err := requirePackageImports(p.public, []*codegen.ImportSpec{
		codegen.SimpleImport("encoding/json"),
		codegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
		codegen.NewImport("goa", "goa.design/goa/v3/pkg"),
	}); err != nil {
		return err
	}
	p.inputMethods = make(map[*expr.MethodExpr]*nativeInputPlan)
	p.taskMethods = make(map[*expr.MethodExpr]*nativeTaskPlan)
	return nil
}

// planTaskExchanges reserves one converter per native creator, including when
// several tools bind that same creator. The original read and update attributes
// supply all service types and field-level validators.
func (p *toolSpecsPackagePlan) planTaskExchanges(services *service.Plan, api *expr.APIExpr, tools []*agent.ToolExpr) error {
	for _, tool := range tools {
		if tool.Method == nil {
			continue
		}
		binding, err := mcpinput.TaskExchange(tool.Method)
		if err != nil {
			return err
		}
		if binding == nil || p.taskMethods[tool.Method] != nil {
			continue
		}
		if err := p.ensureInputCodecs(); err != nil {
			return err
		}
		if err := requirePackageImports(p.public, []*codegen.ImportSpec{
			codegen.SimpleImport("context"),
			codegen.SimpleImport("encoding/base64"),
			codegen.SimpleImport("strings"),
			codegen.NewImport("api", "goa.design/goa-ai/runtime/agent/api"),
		}); err != nil {
			return err
		}
		plan := &nativeTaskPlan{binding: binding}
		plan.input, err = inputexchange.NewTask(services, api, binding, binding.Pending, p.inputCodecs)
		if err != nil {
			return err
		}
		plan.metadataLayout, err = services.MethodTypeLayout(binding.Read, binding.Metadata)
		if err != nil {
			return err
		}
		plan.input.Native = true
		prefix := codegen.Goify(tool.Method.Name, true) + "Task"
		for _, field := range []struct {
			name      string
			method    *expr.MethodExpr
			attribute *expr.AttributeExpr
			direction jsoncodec.Direction
			target    **jsoncodec.Value
		}{
			{"Observation", binding.Read, binding.Observation, jsoncodec.ValidateOnly, &plan.observationValidation},
			{"Answers", binding.Answer, binding.Answer.Payload, jsoncodec.ValidateOnly, &plan.answerValidation},
		} {
			layout, err := services.MethodTypeLayout(field.method, field.attribute)
			if err != nil {
				return err
			}
			*field.target, err = p.inputCodecs.Add(binding.Creator.Service.Name+":"+binding.Creator.Name+":task:"+field.name, prefix+field.name, field.attribute, layout, field.direction)
			if err != nil {
				return err
			}
		}
		for _, field := range []struct {
			name       string
			target     **codegen.NameDeclaration
			visibility codegen.PackageNameVisibility
		}{
			{"Created" + prefix, &plan.created, codegen.ExportedName},
			{"Read" + prefix, &plan.read, codegen.ExportedName},
			{"Fill" + prefix + "Answers", &plan.fill, codegen.ExportedName},
			{"convert" + prefix + "Input", &plan.pending, codegen.UnexportedName},
		} {
			*field.target = codegen.NewPreferredName(codegen.NameFunction, field.name, field.visibility, specNameOrder{packagePath: p.public.ImportPath(), key: binding.Creator.Name + ":" + field.name})
			if err := p.public.DeclareName(*field.target); err != nil {
				return err
			}
		}
		if err := p.planTaskRoles(services, plan); err != nil {
			return err
		}
		p.taskMethods[binding.Creator] = plan
		roleTools := []*agent.ToolExpr{{Method: binding.Read}, {Method: binding.Answer}, {Method: binding.Cancel}}
		paths, err := reserveMethodLayoutImports(p.public, services, p.serviceImportPath, roleTools)
		if err != nil {
			return err
		}
		p.inputImportPaths = append(p.inputImportPaths, paths...)
	}
	return nil
}

// linkTaskExchanges reads Goa's assigned names and native selectors. Completed
// output remains separate from job metadata and unfinished workflow state.
func (p *toolSpecsPackagePlan) linkTaskExchanges(services *service.ServicesData) error {
	for _, plan := range p.taskMethods {
		binding := plan.binding
		codecTypes := services.ServiceAttributor(binding.Creator.Service.Name, p.inputCodecPackage.ImportPath())
		for _, value := range []*jsoncodec.Value{plan.observationValidation, plan.answerValidation} {
			if err := value.BindService(codecTypes); err != nil {
				return err
			}
		}
		if err := plan.input.BindCodecs(codecTypes, codecTypes); err != nil {
			return err
		}
		native := services.ServiceAttributor(binding.Creator.Service.Name, p.public.ImportPath())
		codecAlias := p.public.ImportName(p.inputCodecPackage.ImportPath())
		if err := plan.input.Bind(p.generation, native, p.public.ImportPath(), p.public.ImportName, codecAlias); err != nil {
			return err
		}
		plan.input.MCPPackage = p.public.ImportName("goa.design/goa-ai/runtime/mcp")
		data := &plan.data
		data.Created, data.Read, data.Fill, data.Pending = plan.created.Name(), plan.read.Name(), plan.fill.Name(), plan.pending.Name()
		data.ObservationRef, data.CompletedRef, data.AnswerRef = native.Ref(binding.Observation, ""), native.Ref(binding.Complete, ""), native.Ref(binding.Answer.Payload, "")
		data.ObservationValidate = codecAlias + "." + plan.observationValidation.ValidationDeclaration().Name()
		data.AnswerValidate = codecAlias + "." + plan.answerValidation.ValidationDeclaration().Name()
		data.MetadataField = native.Field(binding.Metadata, "task", true)
		data.TaskIDField = native.Field(binding.Metadata.Find("taskId"), "taskId", true)
		if polling := binding.Metadata.Find("pollIntervalMs"); polling != nil {
			data.PollField = native.Field(polling, "pollIntervalMs", true)
			matches := plan.metadataLayout.PlansForOccurrence(polling)
			if len(matches) != 1 {
				return fmt.Errorf("TaskExchange polling guidance has %d native field layouts", len(matches))
			}
			data.PollPointer = matches[0].IsPointer()
		}
		data.OutcomeField = native.Field(binding.Outcome, "outcome", true)
		data.FailureCode, data.FailureMessage = native.Field(binding.Failure.Find("code"), "code", true), native.Field(binding.Failure.Find("message"), "message", true)
		if field := binding.Failure.Find("data"); field != nil {
			data.FailureData = native.Field(field, "data", true)
		}
		data.API, data.MCP, data.Input = p.public.ImportName("goa.design/goa-ai/runtime/agent/api"), plan.input.MCPPackage, plan.input
		if err := p.linkTaskRoles(services, plan); err != nil {
			return err
		}
		for _, role := range plan.roles {
			data.Roles = append(data.Roles, &role.data)
		}
		data.AnswerTaskIDField, data.AnswerTaskIDRef = plan.roles[1].data.TaskIDField, plan.roles[1].data.TaskIDRef
	}
	return nil
}

// nativeTaskFile writes the typed job converters beside the tool specifications.
// Each method appears once, with deterministic ordering across generation runs.
func nativeTaskFile(p *toolSpecsPackagePlan) *codegen.File {
	if len(p.taskMethods) == 0 {
		return nil
	}
	plans := make([]*nativeTaskPlan, 0, len(p.taskMethods))
	for _, plan := range p.taskMethods {
		plans = append(plans, plan)
	}
	slices.SortFunc(plans, func(a, b *nativeTaskPlan) int { return cmp.Compare(a.binding.Creator.Name, b.binding.Creator.Name) })
	paths := make([]string, 0, 9+len(p.inputImportPaths))
	paths = append(paths, "context", "encoding/base64", "encoding/json", "strings", "goa.design/goa-ai/runtime/agent/api", "goa.design/goa-ai/runtime/mcp", "goa.design/goa/v3/pkg", p.inputCodecPackage.ImportPath(), p.serviceImportPath)
	paths = append(paths, p.inputImportPaths...)
	sections := []*codegen.SectionTemplate{codegen.Header("Typed job observations and host answers for bound Goa methods.", p.render.SpecsPackageName, importsForPaths(p.public, paths))}
	for _, plan := range plans {
		sections = append(sections, &codegen.SectionTemplate{Name: fmt.Sprintf("task-exchange-%s", plan.binding.Creator.Name), Source: agentsTemplates.Read(nativeTaskExchangeFileT), Data: plan.data})
	}
	return &codegen.File{Path: filepath.Join(p.render.SpecsDir, "task_exchange.go"), SectionTemplates: sections}
}
