package codegen

import (
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// BuildDataForTest chooses service and agent names exactly as generation does,
// then returns the agent data used to write files.
func BuildDataForTest(genpkg string, roots []eval.Root) (*GeneratorData, error) {
	plan, err := buildAgentPlanForTest(genpkg, roots, false)
	if err != nil {
		return nil, err
	}
	return plan.data, nil
}

// BuildFilesForTest runs the same name selection and file-building steps as the
// agent plugin without writing files to disk.
func BuildFilesForTest(genpkg string, roots []eval.Root, example bool) ([]*codegen.File, error) {
	plan, err := buildAgentPlanForTest(genpkg, roots, example)
	if err != nil {
		return nil, err
	}
	return plan.Files(nil)
}

// buildAgentPlanForTest chooses every Goa and agent name and builds the data
// used by a focused generator test.
func buildAgentPlanForTest(genpkg string, roots []eval.Root, example bool) (*Plan, error) {
	if err := Prepare(genpkg, roots); err != nil {
		return nil, err
	}
	generation, err := codegen.NewGeneration(genpkg, roots)
	if err != nil {
		return nil, err
	}
	goaRoot, _ := agentDesignRoots(generation.Roots())
	servicePlan, err := service.NewPlan(
		goaRoot,
		generation,
		goaexpr.NewExampleGenerator(goaRoot.API.RandomizerFactory),
	)
	if err != nil {
		return nil, err
	}
	var plan *Plan
	if example {
		plan, err = NewExamplePlan(generation, servicePlan)
	} else {
		plan, err = NewPlan(generation, servicePlan)
	}
	if err != nil {
		return nil, err
	}
	if err := generation.Freeze(); err != nil {
		return nil, err
	}
	if err := servicePlan.Link(); err != nil {
		return nil, err
	}
	if err := plan.Link(); err != nil {
		return nil, err
	}
	return plan, nil
}

// ToolSpecsDataForTest returns the names, types, and schemas already built for
// the agent's tool packages.
func ToolSpecsDataForTest(agent *AgentData) *toolSpecsData {
	return agentToolSpecsData(agent)
}

// CollectToolNamesForTest returns each generated tool constant and injected
// decoder name keyed by the qualified runtime tool name.
func CollectToolNamesForTest(specs *toolSpecsData) (map[string]string, map[string]string) {
	constNames := make(map[string]string)
	injectDecoders := make(map[string]string)
	if specs == nil {
		return constNames, injectDecoders
	}
	for _, tool := range specs.tools {
		constNames[tool.Name] = tool.ConstName
		if tool.Payload != nil && tool.Payload.InjectDecodeFunc != "" {
			injectDecoders[tool.Name] = tool.Payload.InjectDecodeFunc
		}
	}
	return constNames, injectDecoders
}

// CollectTypeInfoForTest returns a map of type name to definition for all
// types captured in the tool specs data (in declaration order).
func CollectTypeInfoForTest(specs *toolSpecsData) map[string]string {
	out := make(map[string]string)
	if specs == nil {
		return out
	}
	for _, td := range specs.typesList() {
		out[td.TypeName] = td.Def
	}
	return out
}

// CollectTypeSchemasForTest returns a map of type name to JSON schema bytes for
// all types captured in tool specs data.
func CollectTypeSchemasForTest(specs *toolSpecsData) map[string][]byte {
	out := make(map[string][]byte)
	if specs == nil {
		return out
	}
	for _, td := range specs.typesList() {
		if len(td.SchemaJSON) == 0 {
			continue
		}
		out[td.TypeName] = td.SchemaJSON
	}
	return out
}

// CollectTypeJSONTypesForTest returns generated field JSON type metadata by type.
func CollectTypeJSONTypesForTest(specs *toolSpecsData) map[string]map[string]string {
	out := make(map[string]map[string]string)
	if specs == nil {
		return out
	}
	for _, td := range specs.typesList() {
		if len(td.FieldJSONTypes) == 0 {
			continue
		}
		copied := make(map[string]string, len(td.FieldJSONTypes))
		for field, jsonType := range td.FieldJSONTypes {
			copied[field] = jsonType
		}
		out[td.TypeName] = copied
	}
	return out
}

// CollectTypeImportAliasesForTest returns the distinct import aliases used by
// the given type (matched by substring on type name). It includes both the
// direct Import (if any) and TypeImports collected during analysis.
func CollectTypeImportAliasesForTest(specs *toolSpecsData, nameContains string) []string {
	seen := make(map[string]struct{})
	// Preallocate a small capacity; typical import sets are small.
	out := make([]string, 0, 8)
	if specs == nil {
		return out
	}
	for _, td := range specs.typesList() {
		if !strings.Contains(td.TypeName, nameContains) {
			continue
		}
		if td.Import != nil && td.Import.Name != "" {
			if _, ok := seen[td.Import.Name]; !ok {
				seen[td.Import.Name] = struct{}{}
				out = append(out, td.Import.Name)
			}
		}
		for _, im := range td.TypeImports {
			if im == nil || im.Name == "" {
				continue
			}
			if _, ok := seen[im.Name]; !ok {
				seen[im.Name] = struct{}{}
				out = append(out, im.Name)
			}
		}
	}
	return out
}

// CollectAllImportAliasesForTest returns every import name used by generated
// tool type, description, and JSON files.
func CollectAllImportAliasesForTest(specs *toolSpecsData) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)
	if specs == nil {
		return out
	}
	for _, im := range specs.typeImports() {
		if im == nil || im.Name == "" {
			continue
		}
		if _, ok := seen[im.Name]; ok {
			continue
		}
		seen[im.Name] = struct{}{}
		out = append(out, im.Name)
	}
	return out
}
