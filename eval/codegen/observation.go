// Package codegen plans strict observation codecs and compiles authored field selectors
// into direct typed Go access. The generated program neither interprets a schema
// nor asks a model to choose assertion identities or recover missing context.
package codegen

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"goa.design/goa-ai/codegen/shared"
	evalexpr "goa.design/goa-ai/eval/expr"
	"goa.design/goa-ai/internal/codegen/codec"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	observationCodecPlan struct {
		Value  *codec.Value
		Layout *goacodegen.GoTypePlan
	}

	checkData struct {
		Name        string
		Method      string
		Description string
	}

	requirementPlan struct {
		ID        string
		Statement string
		Subject   string
		Evidence  []string
		ForEach   string
	}

	requirementData struct {
		ID        string
		SchemaID  string
		Statement string
		Subject   string
		Evidence  []string
		ForEach   string
	}

	selectedField struct {
		Expression string
		Present    []string
		Attribute  *goaexpr.AttributeExpr
		Pointer    bool
	}
)

// planObservation copies a supported schema and plans the existing standalone
// codec against this package's declared types. Schemas describe captured facts;
// desired outcomes belong in checks and requirements, not type validation.
func planObservation(suite *suitePlan, scenario *evalexpr.ScenarioExpr, planned *scenarioPlan, seen map[string]*inputTypePlan) error {
	observation, err := localizeInput(scenario.Observation)
	if err != nil {
		return err
	}
	if !codec.SupportsStandalone(observation) {
		return fmt.Errorf("scenario %q observation requires a closed Goa JSON type without Any, custom Go types, or non-string map keys", scenario.Name)
	}
	if _, named := observation.Type.(goaexpr.UserType); !named {
		localType := &goaexpr.UserTypeExpr{TypeName: planned.Method + "Observation", AttributeExpr: observation}
		observation = &goaexpr.AttributeExpr{Type: localType, Description: observation.Description}
	}
	planned.Observation = observation
	newTypes, err := planInputTypes(suite, observation, seen)
	if err != nil {
		return err
	}
	for _, value := range newTypes {
		if err := suite.SuiteImports.AddTypeExpressions(value.Attribute); err != nil {
			return err
		}
		if err := suite.SuiteImports.Require(goacodegen.GoaImport("")); err != nil {
			return err
		}
		for _, spec := range goacodegen.ValidationRuntimeImports(value.Attribute, goacodegen.GoLayoutPolicy{UseDefault: true, SumType: true}) {
			if err := suite.SuiteImports.Require(goacodegen.NewImport(spec.Name, spec.Path)); err != nil {
				return err
			}
		}
	}
	layout, err := goacodegen.PlanGoType(observation, goacodegen.GoTypePlanOptions{
		Owner: suite.ImportPath, Policy: goacodegen.GoLayoutPolicy{UseDefault: true, SumType: true}, RetainNamedValue: true,
		Bind: func(request goacodegen.GoTypeBindingRequest) (goacodegen.GoTypeBinding, error) {
			userType, ok := request.Attribute.Type.(goaexpr.UserType)
			if !ok {
				return goacodegen.GoTypeBinding{}, fmt.Errorf("observation has unsupported generated type %s", request.Kind)
			}
			declaration, err := suite.PackagePlan.Type(userType)
			return goacodegen.GoTypeBinding{Owner: suite.ImportPath, Type: declaration}, err
		},
	})
	if err != nil {
		return err
	}
	// Two scenarios may share one observation type. Its strict codec is also
	// shared; selectors and predicates remain specific to each scenario.
	for _, existing := range suite.ObservationCodecs {
		if existing.Layout.TypeDeclaration() == layout.TypeDeclaration() {
			planned.Codec = existing
			break
		}
	}
	if planned.Codec == nil {
		value, err := suite.Codecs.AddStandalone(scenario.Name, observation.Type.Name(), observation, layout)
		if err != nil {
			return err
		}
		planned.Codec = &observationCodecPlan{Value: value, Layout: layout}
		suite.ObservationCodecs = append(suite.ObservationCodecs, planned.Codec)
	}
	planned.Schema, err = shared.ToJSONSchema(observation)
	if err != nil {
		return fmt.Errorf("scenario %q observation schema: %w", scenario.Name, err)
	}
	for _, check := range scenario.Checks {
		planned.Checks = append(planned.Checks, checkData{
			Name: strconv.Quote(check.Name), Method: "Check" + planned.Method + goacodegen.Goify(check.Name, true), Description: check.Description,
		})
	}
	for _, requirement := range scenario.Requirements {
		local := observation
		if requirement.ForEach != "" {
			selected, err := evalexpr.Select(observation, observation, requirement.ForEach)
			if err != nil {
				return err
			}
			local = goaexpr.AsArray(selected.Type).ElemType
		}
		selected, err := evalexpr.Select(observation, local, requirement.Subject)
		if err != nil {
			return err
		}
		if len(requirement.Evidence) > 0 || !shared.IsStringType(selected.Type) {
			if err := suite.SuiteImports.Require(goacodegen.NewImport("json", "encoding/json")); err != nil {
				return err
			}
		}
		planned.Requirements = append(planned.Requirements, requirementPlan{
			ID:        suite.Name + "/" + scenario.Name + "/" + requirement.Name,
			Statement: requirement.Statement, Subject: requirement.Subject,
			Evidence: append([]string(nil), requirement.Evidence...), ForEach: requirement.ForEach,
		})
	}
	return nil
}

// observationFiles binds the planned original layouts after names freeze, then
// delegates all presence, value, and JSON validation to the shared codec generator.
func observationFiles(suite *suitePlan) ([]*goacodegen.File, error) {
	for _, planned := range suite.ObservationCodecs {
		context := &goacodegen.AttributeContext{UseDefault: true, Scope: goacodegen.NewAttributeScope(suite.PackagePlan.Scope())}
		context, err := context.WithGoTypeLayout(planned.Layout.Link(suite.ImportPath, suite.PackagePlan.ImportName))
		if err != nil {
			return nil, err
		}
		if err := planned.Value.BindService(context.Scope); err != nil {
			return nil, err
		}
	}
	return suite.Codecs.Files(suite.Package)
}

// linkRequirements emits selector access and a separate result slot for every
// declared requirement. A missing reference field returns an assessment error;
// an absent subject becomes empty content and cannot borrow from the reference.
func linkRequirements(scenario scenarioPlan, scope *goacodegen.NameScope) ([]requirementData, string) {
	var out strings.Builder
	definitions := make([]requirementData, len(scenario.Requirements))
	for i, requirement := range scenario.Requirements {
		definition := requirementData{
			ID: strconv.Quote(requirement.ID), Statement: strconv.Quote(requirement.Statement),
			SchemaID: strconv.Quote(fmt.Sprintf("%x", sha256.Sum256([]byte(scenario.Schema)))),
			Subject:  strconv.Quote(requirement.Subject), ForEach: strconv.Quote(requirement.ForEach),
		}
		for _, field := range requirement.Evidence {
			definition.Evidence = append(definition.Evidence, strconv.Quote(field))
		}
		definitions[i] = definition
		fmt.Fprintf(&out, "{\n// Bind %s from the captured observation.\n", requirement.ID)
		local, target := scenario.Observation, "observed"
		if requirement.ForEach != "" {
			selected := selectField(scenario.Observation, local, target, requirement.ForEach, scope)
			fmt.Fprintf(&out, "binding.Subjects[%q] = []eval.Subject{}\n", requirement.ID)
			if len(selected.Present) > 0 {
				fmt.Fprintf(&out, "if !(%s) { return eval.Binding{}, fmt.Errorf(%q) }\n",
					strings.Join(selected.Present, " && "), "requirement "+requirement.ID+": missing captured collection "+requirement.ForEach)
			}
			fmt.Fprintf(&out, "for _, item := range %s {\n", selected.Expression)
			local, target = goaexpr.AsArray(selected.Attribute.Type).ElemType, "item"
		}
		out.WriteString("subject := eval.Subject{}\n")
		selected := selectField(scenario.Observation, local, target, requirement.Subject, scope)
		writeSelectedContent(&out, selected)
		if len(requirement.Evidence) > 0 {
			out.WriteString("reference := make(map[string]json.RawMessage)\n")
			for _, field := range requirement.Evidence {
				selected := selectField(scenario.Observation, local, target, field, scope)
				if len(selected.Present) > 0 {
					fmt.Fprintf(&out, "if !(%s) { return eval.Binding{}, fmt.Errorf(%q) }\n",
						strings.Join(selected.Present, " && "), "requirement "+requirement.ID+": missing captured evidence "+field)
				}
				out.WriteString("{\n")
				writeSelectedJSON(&out, "encoded", selected)
				fmt.Fprintf(&out, "reference[%q] = encoded\n}\n", field)
			}
			out.WriteString("encodedReference, err := json.Marshal(reference)\nif err != nil { return eval.Binding{}, err }\nsubject.Reference = string(encodedReference)\n")
		}
		fmt.Fprintf(&out, "binding.Subjects[%q] = append(binding.Subjects[%q], subject)\n", requirement.ID, requirement.ID)
		if requirement.ForEach != "" {
			out.WriteString("}\n")
		}
		out.WriteString("}\n")
	}
	return definitions, out.String()
}

// selectField computes Go access and all required nil checks at generation time.
// Runtime code checks only actual optional values, never selector syntax.
func selectField(root, local *goaexpr.AttributeExpr, target, selector string, scope *goacodegen.NameScope) selectedField {
	if selector == "@" {
		selector = ""
	}
	if selector == "$" || strings.HasPrefix(selector, "$.") {
		local, target = root, "observed"
		selector = strings.TrimPrefix(selector, "$")
		selector = strings.TrimPrefix(selector, ".")
	}
	selected := selectedField{Expression: target, Attribute: local}
	if target == "item" && goaexpr.IsObject(local.Type) {
		selected.Present = append(selected.Present, target+" != nil")
	}
	if selector == "" {
		return selected
	}
	context := goacodegen.NewAttributeContext(false, false, true, "", scope)
	for _, name := range strings.Split(selector, ".") {
		field := goaexpr.AsObject(local.Type).Attribute(name)
		selected.Pointer = context.IsFieldPointer(name, local)
		selected.Expression += "." + goacodegen.GoifyAtt(field, name, true)
		if selected.Pointer || goaexpr.IsObject(field.Type) {
			selected.Present = append(selected.Present, selected.Expression+" != nil")
		}
		local = field
	}
	selected.Attribute = local
	return selected
}

func writeSelectedContent(out *strings.Builder, selected selectedField) {
	if len(selected.Present) > 0 {
		fmt.Fprintf(out, "if %s {\n", strings.Join(selected.Present, " && "))
	}
	if shared.IsStringType(selected.Attribute.Type) {
		value := selected.Expression
		if selected.Pointer {
			value = "*" + value
		}
		fmt.Fprintf(out, "subject.Content = string(%s)\n", value)
	} else {
		writeSelectedJSON(out, "encodedSubject", selected)
		out.WriteString("subject.Content = string(encodedSubject)\n")
	}
	if len(selected.Present) > 0 {
		out.WriteString("}\n")
	}
}

// writeSelectedJSON gives nil and empty Go collections the same JSON meaning.
// It serializes a selected value without changing the saved observation.
func writeSelectedJSON(out *strings.Builder, name string, selected selectedField) {
	empty := ""
	switch {
	case goaexpr.IsArray(selected.Attribute.Type):
		empty = "[]"
	case goaexpr.IsMap(selected.Attribute.Type):
		empty = "{}"
	case goacodegen.IsNilable(selected.Attribute.Type) && goaexpr.IsPrimitive(selected.Attribute.Type):
		empty = `""`
	}
	if empty == "" {
		fmt.Fprintf(out, "%s, err := json.Marshal(%s)\nif err != nil { return eval.Binding{}, err }\n", name, selected.Expression)
		return
	}
	fmt.Fprintf(out, "var %s []byte\nif %s == nil { %s = []byte(%q) } else {\nvar err error\n%s, err = json.Marshal(%s)\nif err != nil { return eval.Binding{}, err }\n}\n",
		name, selected.Expression, name, empty, name, selected.Expression)
}
