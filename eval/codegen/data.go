// Package codegen generates typed evaluation suites and application scaffolds.
// This file copies suite inputs and the agent's tool packages before generated
// files are written.
package codegen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	agentir "goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/codegen/shared"
	evalexpr "goa.design/goa-ai/eval/expr"
	agentexpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// suitePlan stores a suite description, its scenarios and inputs, and the Go
	// names used when its file is written.
	suitePlan struct {
		Name         string
		ID           string
		Description  string
		Package      string
		GenPkg       string
		ImportPath   string
		Output       string
		PackagePlan  *goacodegen.GeneratedPackage
		Scenarios    []scenarioPlan
		Types        []*inputTypePlan
		Validators   []*validatorPlan
		Imports      []*goacodegen.ImportSpec
		Hooks        *goacodegen.NameDeclaration
		Inputs       *goacodegen.NameDeclaration
		New          *goacodegen.NameDeclaration
		ToolContract *goacodegen.NameDeclaration
		Contract     *contractPlan
	}

	// examplePlan keeps one suite and the command names used by its starter file.
	examplePlan struct {
		Suite          *suitePlan
		Output         string
		PackagePlan    *goacodegen.GeneratedPackage
		Hooks          *goacodegen.NameDeclaration
		Values         *goacodegen.NameDeclaration
		Options        *goacodegen.NameDeclaration
		Main           *goacodegen.NameDeclaration
		Run            *goacodegen.NameDeclaration
		ScenarioInputs *goacodegen.NameDeclaration
	}

	// scenarioPlan keeps one copied scenario and its saved input validator.
	scenarioPlan struct {
		ID             string
		RawID          string
		Description    string
		Method         string
		Tags           []string
		Timeout        int64
		Input          *goaexpr.AttributeExpr
		InputValidator *goacodegen.NameDeclaration
	}

	// inputTypePlan keeps one local input type and its package declarations.
	inputTypePlan struct {
		Description string
		Attribute   *goaexpr.AttributeExpr
		Type        goaexpr.UserType
		Declaration *goacodegen.NameDeclaration
		Validator   *validatorPlan
	}

	// validatorPlan keeps one input check and the function that writes it.
	validatorPlan struct {
		Attribute         *goaexpr.AttributeExpr
		Reference         *goaexpr.AttributeExpr
		Parent            goaexpr.UserType
		Declaration       *goacodegen.NameDeclaration
		NestedDeclaration *goacodegen.NameDeclaration
	}

	// contractPlan stores the agent ID and tool packages until the file chooses
	// local import names.
	contractPlan struct {
		AgentID string
		Specs   []contractSpecPlan
	}

	contractSpecPlan struct {
		Package string
		Path    string
	}

	// evalNameOrder sorts names that request the same spelling.
	evalNameOrder struct {
		Suite  string
		Role   string
		Symbol string
	}

	// evalAttributeScope supplies saved validator calls while Goa writes one
	// suite file.
	evalAttributeScope struct {
		goacodegen.Attributor
		validators map[string]*goacodegen.NameDeclaration
	}

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
		Hooks          string
		Inputs         string
		New            string
	}

	// exampleData contains the final suite and command names used by one file.
	exampleData struct {
		*suiteData
		ExampleHooks          string
		ExampleValues         string
		ExampleOptions        string
		ExampleMain           string
		ExampleRun            string
		ExampleScenarioInputs string
	}

	// scenarioData contains one generated hook method and copied case data.
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

	// inputTypeData stores one public Go definition written for a scenario input.
	inputTypeData struct {
		Name        string
		Description string
		Def         string
	}

	// validatorData contains one generated validation function body.
	validatorData struct {
		Name       string
		NestedName string
		Ref        string
		Pointer    bool
		Lines      []string
	}

	// contractData lists the generated tool packages checked in order.
	contractData struct {
		AgentID          string
		MustToolContract string
		Specs            []contractSpecData
	}

	contractSpecData struct {
		Alias string
		Path  string
	}
)

// ComparePackageName orders eval declarations by copied suite and symbol names.
func (o evalNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(evalNameOrder)
	if compared := strings.Compare(o.Suite, right.Suite); compared != 0 {
		return compared
	}
	if compared := strings.Compare(o.Role, right.Role); compared != 0 {
		return compared
	}
	return strings.Compare(o.Symbol, right.Symbol)
}

// Enter keeps saved validator names while checking fields inside an input.
func (s *evalAttributeScope) Enter(attribute *goaexpr.AttributeExpr) goacodegen.Attributor {
	return &evalAttributeScope{
		Attributor: s.Attributor.Enter(attribute),
		validators: s.validators,
	}
}

// ValidatorCall uses the saved private function for a nested input and keeps
// the field path supplied by its parent validator.
func (s *evalAttributeScope) ValidatorCall(
	attribute *goaexpr.AttributeExpr,
	_, target, path string,
) string {
	userType, ok := attribute.Type.(goaexpr.UserType)
	if !ok {
		panic(fmt.Sprintf("eval validator requested for non-user type %T", attribute.Type))
	}
	declaration := s.validators[userType.ID()]
	if declaration == nil {
		panic(fmt.Sprintf("eval validator for input type %q was not saved", userType.Name()))
	}
	return fmt.Sprintf("%s(%s, %s)", declaration.Name(), target, path)
}

// planSuite copies one suite and declares every name written in its package.
func planSuite(
	generation *goacodegen.Generation,
	suite *evalexpr.SuiteExpr,
	contract *contractPlan,
) (*suitePlan, error) {
	importPath := shared.JoinImportPath(generation.GenPkg(), "evals/"+suite.Name)
	pkg, err := generation.ClaimPackage(importPath)
	if err != nil {
		return nil, err
	}
	planned := &suitePlan{
		Name:        suite.Name,
		ID:          strconv.Quote(suite.Name),
		Description: strconv.Quote(suite.Description),
		Package:     goacodegen.Goify(strings.ReplaceAll(suite.Name, "_", ""), false),
		GenPkg:      generation.GenPkg(),
		ImportPath:  importPath,
		Output:      filepath.Join(goacodegen.Gendir, "evals", suite.Name, "suite.go"),
		PackagePlan: pkg,
		Scenarios:   make([]scenarioPlan, len(suite.Scenarios)),
		Contract:    contract,
	}
	planned.Hooks, err = declareEvalName(pkg, goacodegen.NameType, "Hooks", goacodegen.ExportedName, suite.Name, "suite")
	if err != nil {
		return nil, err
	}
	planned.Inputs, err = declareEvalName(pkg, goacodegen.NameType, "Inputs", goacodegen.ExportedName, suite.Name, "suite")
	if err != nil {
		return nil, err
	}
	planned.New, err = declareEvalName(pkg, goacodegen.NameFunction, "New", goacodegen.ExportedName, suite.Name, "suite")
	if err != nil {
		return nil, err
	}
	if contract != nil {
		planned.ToolContract, err = declareEvalName(
			pkg,
			goacodegen.NameFunction,
			"MustToolContract",
			goacodegen.ExportedName,
			suite.Name,
			"contract",
		)
		if err != nil {
			return nil, err
		}
	}

	seenTypes := make(map[string]*inputTypePlan)
	imports := make(map[string]*goacodegen.ImportSpec)
	for index, scenario := range suite.Scenarios {
		item, err := planScenario(planned, scenario, suite.Timeout, seenTypes, imports)
		if err != nil {
			return nil, err
		}
		planned.Scenarios[index] = item
	}
	paths := make([]string, 0, len(imports))
	for importPath := range imports {
		paths = append(paths, importPath)
	}
	sort.Strings(paths)
	for _, importPath := range paths {
		planned.Imports = append(planned.Imports, imports[importPath])
	}
	return planned, nil
}

// planScenario copies one scenario and declares names used by its input.
func planScenario(
	suite *suitePlan,
	scenario *evalexpr.ScenarioExpr,
	suiteTimeout time.Duration,
	seenTypes map[string]*inputTypePlan,
	imports map[string]*goacodegen.ImportSpec,
) (scenarioPlan, error) {
	timeout := scenario.Timeout
	if timeout == 0 {
		timeout = suiteTimeout
	}
	tags := make([]string, len(scenario.Tags))
	for index, tag := range scenario.Tags {
		tags[index] = strconv.Quote(tag)
	}
	method := goacodegen.Goify(scenario.Name, true)
	planned := scenarioPlan{
		ID:          strconv.Quote(scenario.Name),
		RawID:       scenario.Name,
		Description: strconv.Quote(scenario.Description),
		Method:      method,
		Tags:        tags,
		Timeout:     int64(timeout),
	}
	if scenario.Input == nil {
		return planned, nil
	}
	input, err := localizeInput(scenario.Input)
	if err != nil {
		return scenarioPlan{}, fmt.Errorf("localize input for scenario %q: %w", scenario.Name, err)
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
	planned.Input = input
	newTypes, err := planInputTypes(suite, input, seenTypes)
	if err != nil {
		return scenarioPlan{}, err
	}
	for _, inputType := range newTypes {
		for _, spec := range shared.GatherAttributeImports(suite.GenPkg, inputType.Attribute) {
			imports[spec.Path] = spec
		}
	}
	if userType, ok := input.Type.(goaexpr.UserType); ok {
		planned.InputValidator = seenTypes[userType.ID()].Validator.Declaration
		return planned, nil
	}
	if !goacodegen.NeedsValidation(input, goacodegen.GoLayoutPolicy{
		UseDefault: true,
		SumType:    true,
	}) {
		return planned, nil
	}
	validatorName := "validate" + method + "Input"
	declaration, err := declareEvalName(
		suite.PackagePlan,
		goacodegen.NameFunction,
		validatorName,
		goacodegen.UnexportedName,
		suite.Name,
		"validator "+method,
	)
	if err != nil {
		return scenarioPlan{}, err
	}
	validator := &validatorPlan{
		Attribute:   input,
		Reference:   input,
		Declaration: declaration,
	}
	validator.NestedDeclaration, err = suite.PackagePlan.DeclareDependentName(
		goacodegen.NameFunction,
		declaration,
		"",
		"At",
		evalNameOrder{Suite: suite.Name, Role: "nested validator", Symbol: method},
	)
	if err != nil {
		return scenarioPlan{}, err
	}
	suite.Validators = append(suite.Validators, validator)
	planned.InputValidator = declaration
	return planned, nil
}

// linkSuiteData builds template values from the saved suite description,
// scenarios, inputs, and chosen Go names.
func linkSuiteData(planned *suitePlan, exampleAlias string) *suiteData {
	scope := planned.PackagePlan.Scope().Fork()
	data := &suiteData{
		Name:           planned.Name,
		ID:             planned.ID,
		Description:    planned.Description,
		Package:        planned.Package,
		Scenarios:      make([]scenarioData, len(planned.Scenarios)),
		Imports:        append([]*goacodegen.ImportSpec(nil), planned.Imports...),
		ExamplePackage: planned.ImportPath,
		ExampleAlias:   exampleAlias,
		Hooks:          planned.Hooks.Name(),
		Inputs:         planned.Inputs.Name(),
		New:            planned.New.Name(),
	}
	validatorNames := make(map[string]*goacodegen.NameDeclaration, len(planned.Types))
	for _, inputType := range planned.Types {
		description := inputType.Declaration.Name() + " is a generated evaluation input type."
		if inputType.Description != "" {
			description += " " + inputType.Description
		}
		data.Types = append(data.Types, inputTypeData{
			Name:        inputType.Declaration.Name(),
			Description: description,
			Def:         scope.GoTypeDef(inputType.Attribute, false, true),
		})
		validatorNames[inputType.Type.ID()] = inputType.Validator.NestedDeclaration
	}
	for index, scenario := range planned.Scenarios {
		linked := scenarioData{
			ID:          scenario.ID,
			RawID:       scenario.RawID,
			Description: scenario.Description,
			Method:      scenario.Method,
			Tags:        append([]string(nil), scenario.Tags...),
			Timeout:     scenario.Timeout,
		}
		if scenario.Input != nil {
			linked.HasInput = true
			linked.InputField = scenario.Method
			linked.InputRef = scope.GoTypeRef(scenario.Input)
			if exampleAlias != "" {
				linked.ExampleInputRef = scope.GoFullTypeRef(scenario.Input, exampleAlias)
				linked.InputZero = zeroValue(linked.ExampleInputRef)
			}
			if scenario.InputValidator != nil {
				linked.InputValidator = scenario.InputValidator.Name()
			}
		}
		data.Scenarios[index] = linked
	}
	for _, validator := range planned.Validators {
		linked := buildValidator(
			validator,
			scope,
			validatorNames,
		)
		data.Validators = append(data.Validators, linked)
		recordValidationImports(data, linked)
	}
	return data
}

// linkContractData chooses import names for one contract file.
func linkContractData(planned *suitePlan) *contractData {
	scope := planned.PackagePlan.Scope().Fork()
	data := &contractData{
		AgentID:          planned.Contract.AgentID,
		MustToolContract: planned.ToolContract.Name(),
	}
	for _, spec := range planned.Contract.Specs {
		data.Specs = append(data.Specs, contractSpecData{
			Alias: scope.Unique(spec.Package),
			Path:  spec.Path,
		})
	}
	return data
}

// linkExampleData chooses command names and its eval package import name.
func linkExampleData(planned *examplePlan) *exampleData {
	fileScope := planned.PackagePlan.Scope().Fork()
	alias := fileScope.Unique("geneval" + planned.Suite.Package)
	return &exampleData{
		suiteData:             linkSuiteData(planned.Suite, alias),
		ExampleHooks:          planned.Hooks.Name(),
		ExampleValues:         planned.Values.Name(),
		ExampleOptions:        planned.Options.Name(),
		ExampleMain:           planned.Main.Name(),
		ExampleRun:            planned.Run.Name(),
		ExampleScenarioInputs: planned.ScenarioInputs.Name(),
	}
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

// planInputTypes declares every local user type reachable from input.
func planInputTypes(
	suite *suitePlan,
	input *goaexpr.AttributeExpr,
	seen map[string]*inputTypePlan,
) ([]*inputTypePlan, error) {
	if input == nil || input.Type == goaexpr.Empty {
		return nil, nil
	}
	var result []*inputTypePlan
	var collect func(*goaexpr.AttributeExpr) error
	collect = func(attribute *goaexpr.AttributeExpr) error {
		if attribute == nil {
			return nil
		}
		switch actual := attribute.Type.(type) {
		case goaexpr.UserType:
			if _, exists := seen[actual.ID()]; exists {
				return nil
			}
			name := goacodegen.Goify(actual.Name(), true)
			declaration, err := declareEvalName(
				suite.PackagePlan,
				goacodegen.NameType,
				name,
				goacodegen.ExportedName,
				suite.Name,
				"input type "+actual.ID(),
				actual,
			)
			if err != nil {
				return err
			}
			validatorDeclaration, err := suite.PackagePlan.DeclareDependentName(
				goacodegen.NameFunction,
				declaration,
				"Validate",
				"",
				evalNameOrder{Suite: suite.Name, Role: "validator", Symbol: actual.ID()},
			)
			if err != nil {
				return err
			}
			nestedValidatorDeclaration, err := suite.PackagePlan.DeclareDependentName(
				goacodegen.NameFunction,
				declaration,
				"validate",
				"",
				evalNameOrder{Suite: suite.Name, Role: "nested validator", Symbol: actual.ID()},
			)
			if err != nil {
				return err
			}
			validator := &validatorPlan{
				Attribute:         actual.Attribute(),
				Reference:         attribute,
				Parent:            actual,
				Declaration:       validatorDeclaration,
				NestedDeclaration: nestedValidatorDeclaration,
			}
			planned := &inputTypePlan{
				Description: strings.TrimSpace(actual.Attribute().Description),
				Attribute:   actual.Attribute(),
				Type:        actual,
				Declaration: declaration,
				Validator:   validator,
			}
			seen[actual.ID()] = planned
			result = append(result, planned)
			suite.Types = append(suite.Types, planned)
			suite.Validators = append(suite.Validators, validator)
			return collect(actual.Attribute())
		case *goaexpr.Object:
			for _, named := range *actual {
				if err := collect(named.Attribute); err != nil {
					return err
				}
			}
		case *goaexpr.Array:
			return collect(actual.ElemType)
		case *goaexpr.Map:
			if err := collect(actual.KeyType); err != nil {
				return err
			}
			return collect(actual.ElemType)
		}
		return nil
	}
	if err := collect(input); err != nil {
		return nil, err
	}
	return result, nil
}

// declareEvalName records one name and how Goa sorts competing requests.
func declareEvalName(
	pkg *goacodegen.GeneratedPackage,
	kind goacodegen.PackageNameKind,
	preferred string,
	visibility goacodegen.PackageNameVisibility,
	suite, role string,
	keys ...goacodegen.Hasher,
) (*goacodegen.NameDeclaration, error) {
	declaration := goacodegen.NewPreferredName(
		kind,
		preferred,
		visibility,
		evalNameOrder{Suite: suite, Role: role, Symbol: preferred},
	)
	if err := pkg.DeclareName(declaration, keys...); err != nil {
		return nil, err
	}
	return declaration, nil
}

// buildValidator writes Goa's checks with names chosen for this file.
func buildValidator(
	plan *validatorPlan,
	scope *goacodegen.NameScope,
	validators map[string]*goacodegen.NameDeclaration,
) validatorData {
	context := goacodegen.NewAttributeContext(false, false, true, "", scope)
	context.Scope = &evalAttributeScope{
		Attributor: context.Scope,
		validators: validators,
	}
	source := goacodegen.ValidationCodeWithPathParameter(
		plan.Attribute,
		plan.Parent,
		context,
		true,
		plan.Parent != nil && goaexpr.IsAlias(plan.Parent),
		false,
		"value",
		"path",
	)
	var lines []string
	if strings.TrimSpace(source) != "" {
		lines = strings.Split(source, "\n")
	}
	ref := scope.GoTypeRef(plan.Reference)
	return validatorData{
		Name:       plan.Declaration.Name(),
		NestedName: plan.NestedDeclaration.Name(),
		Ref:        ref,
		Pointer:    strings.HasPrefix(ref, "*"),
		Lines:      lines,
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

// planContract copies the tool packages reachable from the suite's agent.
func planContract(suite *evalexpr.SuiteExpr, design *agentir.Design) (*contractPlan, error) {
	if suite.Agent == nil || design == nil {
		return nil, nil
	}
	root := designAgent(design, suite.Agent)
	if root == nil {
		return nil, fmt.Errorf("resolve agent attached to evaluation suite %q", suite.Name)
	}
	result := &contractPlan{AgentID: root.ID}
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
					result.Specs = append(result.Specs, contractSpecPlan{
						Package: "gen" + strings.ReplaceAll(reference.SpecsPackageName, "_", ""),
						Path:    reference.SpecsImportPath,
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
