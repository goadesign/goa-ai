package codegen

import (
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// FieldPathSegmentForTest exposes one typed generated path segment to
	// external generator tests.
	FieldPathSegmentForTest struct {
		Name    string
		Dynamic bool
	}

	// UnionBranchForTest exposes one generated union branch requirement to
	// external generator tests.
	UnionBranchForTest struct {
		Discriminator []FieldPathSegmentForTest
		Value         string
	}

	// FieldMetadataForTest exposes the static field facts consumed by the
	// generated source template.
	FieldMetadataForTest struct {
		Path                []FieldPathSegmentForTest
		JSONType            string
		Description         string
		Branches            []UnionBranchForTest
		DiscriminatorValues []string
	}
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

// CollectFieldMetadataForTest returns generated field metadata by type.
func CollectFieldMetadataForTest(specs *toolSpecsData) map[string][]FieldMetadataForTest {
	out := make(map[string][]FieldMetadataForTest)
	if specs == nil {
		return out
	}
	for _, td := range specs.typesList() {
		if len(td.Fields) == 0 {
			continue
		}
		copied := make([]FieldMetadataForTest, len(td.Fields))
		for index, field := range td.Fields {
			copied[index] = FieldMetadataForTest{
				Path:                fieldPathForTest(field.Path),
				JSONType:            field.JSONType,
				Description:         field.Description,
				DiscriminatorValues: append([]string(nil), field.DiscriminatorValues...),
			}
			if len(field.Branches) > 0 {
				copied[index].Branches = make([]UnionBranchForTest, len(field.Branches))
				for branchIndex, branch := range field.Branches {
					copied[index].Branches[branchIndex] = UnionBranchForTest{
						Discriminator: fieldPathForTest(branch.Discriminator),
						Value:         branch.Value,
					}
				}
			}
		}
		out[td.TypeName] = copied
	}
	return out
}

func fieldPathForTest(path []fieldPathSegmentData) []FieldPathSegmentForTest {
	if len(path) == 0 {
		return nil
	}
	segments := make([]FieldPathSegmentForTest, len(path))
	for index, segment := range path {
		segments[index] = FieldPathSegmentForTest(segment)
	}
	return segments
}
