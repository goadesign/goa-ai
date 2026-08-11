// Package codegen generates typed evaluation suites and application scaffolds.
// This file owns input materialization and nested-agent contract traversal.
package codegen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	agentir "goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/codegen/shared"
	evalexpr "goa.design/goa-ai/eval/expr"
	agentexpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// suiteData contains fully evaluated values consumed by generated suite and
	// example templates.
	suiteData struct {
		Name           string
		ID             string
		Description    string
		Package        string
		Scenarios      []scenarioData
		Types          []inputTypeData
		Validators     []validatorData
		Imports        []*goacodegen.ImportSpec
		NeedsGoa       bool
		NeedsUTF8      bool
		ExamplePackage string
		ExampleAlias   string
	}

	// scenarioData contains one generated hook method and immutable case data.
	scenarioData struct {
		ID              string
		RawID           string
		Description     string
		Method          string
		Tags            []string
		Timeout         int64
		HasInput        bool
		InputField      string
		InputRef        string
		InputValidator  string
		InputZero       string
		ExampleInputRef string
	}

	// inputTypeData describes one public Go type materialized in the eval
	// package from a scenario input schema.
	inputTypeData struct {
		Name        string
		Description string
		Def         string
		Ref         string
		Validator   string
		Attribute   *goaexpr.AttributeExpr
		Type        goaexpr.UserType
	}

	// validatorData contains one generated validation function body.
	validatorData struct {
		Name    string
		Ref     string
		Pointer bool
		Lines   []string
	}

	// contractData lists aggregate agent specs packages in lookup order.
	contractData struct {
		AgentID string
		Specs   []contractSpecData
	}

	contractSpecData struct {
		Alias string
		Path  string
	}
)

// buildSuiteData localizes scenario input schemas and partially evaluates all
// static suite decisions before rendering runtime code.
func buildSuiteData(genpkg string, suite *evalexpr.SuiteExpr) (*suiteData, error) {
	scope := goacodegen.NewNameScope()
	data := &suiteData{
		Name:           suite.Name,
		ID:             strconv.Quote(suite.Name),
		Description:    strconv.Quote(suite.Description),
		Package:        goacodegen.Goify(strings.ReplaceAll(suite.Name, "_", ""), false),
		Scenarios:      make([]scenarioData, len(suite.Scenarios)),
		ExamplePackage: shared.JoinImportPath(genpkg, "evals/"+suite.Name),
	}
	data.ExampleAlias = "geneval" + data.Package
	seenTypes := make(map[string]struct{})
	seenValidators := make(map[string]struct{})
	imports := make(map[string]*goacodegen.ImportSpec)

	for index, scenario := range suite.Scenarios {
		timeout := scenario.Timeout
		if timeout == 0 {
			timeout = suite.Timeout
		}
		tags := make([]string, len(scenario.Tags))
		for tagIndex, tag := range scenario.Tags {
			tags[tagIndex] = strconv.Quote(tag)
		}
		method := goacodegen.Goify(scenario.Name, true)
		scenarioData := scenarioData{
			ID:          strconv.Quote(scenario.Name),
			RawID:       scenario.Name,
			Description: strconv.Quote(scenario.Description),
			Method:      method,
			Tags:        tags,
			Timeout:     int64(timeout),
		}
		if scenario.Input != nil {
			input, err := localizeInput(scenario.Input)
			if err != nil {
				return nil, fmt.Errorf("localize input for scenario %q: %w", scenario.Name, err)
			}
			if _, ok := input.Type.(*goaexpr.Object); ok {
				localType := &goaexpr.UserTypeExpr{
					AttributeExpr: input,
					TypeName:      method + "Input",
				}
				input = &goaexpr.AttributeExpr{
					Type:        localType,
					Description: scenario.Input.Description,
				}
			}
			scenarioData.HasInput = true
			scenarioData.InputField = method
			scenarioData.InputRef = scope.GoTypeRef(input)
			scenarioData.ExampleInputRef = scope.GoFullTypeRef(input, data.ExampleAlias)
			scenarioData.InputZero = zeroValue(scenarioData.ExampleInputRef)

			types := collectInputTypes(input, scope, seenTypes)
			for _, inputType := range types {
				data.Types = append(data.Types, inputType)
				for _, spec := range shared.GatherAttributeImports(genpkg, inputType.Attribute) {
					imports[spec.Path] = spec
				}
				if _, exists := seenValidators[inputType.Validator]; exists {
					continue
				}
				validator := buildValidator(
					inputType.Attribute,
					inputType.Type,
					inputType.Validator,
					inputType.Ref,
					scope,
				)
				data.Validators = append(data.Validators, validator)
				seenValidators[validator.Name] = struct{}{}
				recordValidationImports(data, validator)
			}

			if _, ok := input.Type.(goaexpr.UserType); ok {
				typeName := scope.GoTypeName(input)
				scenarioData.InputValidator = "Validate" + typeName
			} else {
				validatorName := "validate" + method + "Input"
				validator := buildValidator(input, nil, validatorName, scenarioData.InputRef, scope)
				if len(validator.Lines) > 0 {
					scenarioData.InputValidator = validatorName
					data.Validators = append(data.Validators, validator)
					recordValidationImports(data, validator)
				}
			}
		}
		data.Scenarios[index] = scenarioData
	}
	paths := make([]string, 0, len(imports))
	for path := range imports {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		data.Imports = append(data.Imports, imports[path])
	}
	return data, nil
}

// localizeInput copies an input schema and removes package placement metadata
// from copied user types because the eval package owns their generated forms.
func localizeInput(input *goaexpr.AttributeExpr) (*goaexpr.AttributeExpr, error) {
	local := goaexpr.DupAtt(input)
	err := goacodegen.Walk(local, func(attribute *goaexpr.AttributeExpr) error {
		userType, ok := attribute.Type.(goaexpr.UserType)
		if !ok {
			return nil
		}
		for name := range userType.Attribute().Meta {
			if strings.HasPrefix(name, "struct:pkg:") {
				delete(userType.Attribute().Meta, name)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return local, nil
}

// collectInputTypes gathers every local user type reachable from input.
func collectInputTypes(
	input *goaexpr.AttributeExpr,
	scope *goacodegen.NameScope,
	seen map[string]struct{},
) []inputTypeData {
	if input == nil || input.Type == goaexpr.Empty {
		return nil
	}
	var result []inputTypeData
	var collect func(*goaexpr.AttributeExpr)
	collect = func(attribute *goaexpr.AttributeExpr) {
		if attribute == nil {
			return
		}
		switch actual := attribute.Type.(type) {
		case goaexpr.UserType:
			if _, exists := seen[actual.ID()]; exists {
				return
			}
			seen[actual.ID()] = struct{}{}
			name := scope.GoTypeName(attribute)
			description := name + " is a generated evaluation input type."
			if authored := strings.TrimSpace(actual.Attribute().Description); authored != "" {
				description += " " + authored
			}
			result = append(result, inputTypeData{
				Name:        name,
				Description: description,
				Def:         scope.GoTypeDef(actual.Attribute(), false, true),
				Ref:         scope.GoTypeRef(attribute),
				Validator:   "Validate" + name,
				Attribute:   actual.Attribute(),
				Type:        actual,
			})
			collect(actual.Attribute())
		case *goaexpr.Object:
			for _, named := range *actual {
				collect(named.Attribute)
			}
		case *goaexpr.Array:
			collect(actual.ElemType)
		case *goaexpr.Map:
			collect(actual.KeyType)
			collect(actual.ElemType)
		}
	}
	collect(input)
	return result
}

// buildValidator emits Goa's canonical recursive validation for one input.
func buildValidator(
	attribute *goaexpr.AttributeExpr,
	parent goaexpr.UserType,
	name string,
	ref string,
	scope *goacodegen.NameScope,
) validatorData {
	context := goacodegen.NewAttributeContext(false, false, true, "", scope)
	source := goacodegen.ValidationCode(
		attribute,
		parent,
		context,
		true,
		parent != nil && goaexpr.IsAlias(parent),
		false,
		"value",
	)
	var lines []string
	if strings.TrimSpace(source) != "" {
		lines = strings.Split(source, "\n")
	}
	return validatorData{
		Name:    name,
		Ref:     ref,
		Pointer: strings.HasPrefix(ref, "*"),
		Lines:   lines,
	}
}

// recordValidationImports records standard packages referenced by generated
// Goa validation source.
func recordValidationImports(data *suiteData, validator validatorData) {
	if len(validator.Lines) == 0 && !validator.Pointer {
		return
	}
	data.NeedsGoa = true
	if strings.Contains(strings.Join(validator.Lines, "\n"), "utf8.") {
		data.NeedsUTF8 = true
	}
}

// zeroValue returns a compiling placeholder used only by create-once examples.
func zeroValue(ref string) string {
	if strings.HasPrefix(ref, "*") {
		return "new(" + strings.TrimPrefix(ref, "*") + ")"
	}
	return "*new(" + ref + ")"
}

// buildContractData computes the exact static toolset specs reachable from the
// agent attached to suite. The root agent is searched before nested agents.
func buildContractData(suite *evalexpr.SuiteExpr, design *agentir.Design) (*contractData, error) {
	if suite.Agent == nil || design == nil {
		return nil, nil
	}
	root := designAgent(design, suite.Agent)
	if root == nil {
		return nil, fmt.Errorf("resolve agent attached to evaluation suite %q", suite.Name)
	}
	scope := goacodegen.NewNameScope()
	result := &contractData{AgentID: root.ID}
	queue := []*agentir.Agent{root}
	seenAgents := make(map[string]struct{})
	seenSpecs := make(map[string]struct{})
	for len(queue) > 0 {
		agent := queue[0]
		queue = queue[1:]
		if _, exists := seenAgents[agent.ID]; exists {
			continue
		}
		seenAgents[agent.ID] = struct{}{}
		for _, reference := range agent.UsedToolsets {
			if reference.Definition == nil {
				return nil, fmt.Errorf(
					"resolve toolset %q used by agent %q",
					reference.Name,
					agent.ID,
				)
			}
			if len(reference.Definition.Expr.Tools) > 0 {
				if reference.SpecsImportPath == "" || reference.SpecsPackageName == "" {
					return nil, fmt.Errorf(
						"resolve specs for toolset %q used by agent %q",
						reference.Name,
						agent.ID,
					)
				}
				if _, exists := seenSpecs[reference.SpecsImportPath]; !exists {
					alias := scope.Unique(
						"gen" +
							strings.ReplaceAll(reference.SpecsPackageName, "_", ""),
					)
					result.Specs = append(result.Specs, contractSpecData{
						Alias: alias,
						Path:  reference.SpecsImportPath,
					})
					seenSpecs[reference.SpecsImportPath] = struct{}{}
				}
			}
			if reference.AgentToolsImportPath == "" {
				continue
			}
			owner := reference.Definition.Owner
			nested := findDesignAgent(design, owner.ServiceName, owner.AgentName)
			if nested == nil {
				return nil, fmt.Errorf(
					"resolve provider agent %q.%q for toolset %q",
					owner.ServiceName,
					owner.AgentName,
					reference.Name,
				)
			}
			queue = append(queue, nested)
		}
	}
	if len(result.Specs) == 0 {
		return nil, nil
	}
	return result, nil
}

func designAgent(design *agentir.Design, expression *agentexpr.AgentExpr) *agentir.Agent {
	for _, agent := range design.Agents {
		if agent.Expr == expression {
			return agent
		}
	}
	return nil
}

func findDesignAgent(design *agentir.Design, serviceName, agentName string) *agentir.Agent {
	for _, agent := range design.Agents {
		if agent.Service.Name == serviceName && agent.Name == agentName {
			return agent
		}
	}
	return nil
}
