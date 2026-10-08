// Package codegen plans strict observation codecs and compiles authored field selectors
// into direct typed Go access. The generated program neither interprets a schema
// nor asks a model to choose assertion identities or recover missing context.
package codegen

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"goa.design/goa-ai/codegen/shared"
	evalexpr "goa.design/goa-ai/eval/expr"
	"goa.design/goa-ai/internal/codegen/codec"
	"goa.design/goa-ai/internal/codegen/jsonshape"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	observationCodecPlan struct {
		Value  *codec.Value
		Layout *goacodegen.GoTypePlan
		Schema string
	}

	// observationOccurrence retains decoder facts omitted by JSON Schema.
	// Named definitions and their uses occupy separate paths, so a scenario's
	// validation cannot disappear when another scenario uses the same Go type.
	observationOccurrence struct {
		Primitive        string                  `json:"primitive,omitempty"`
		Validation       *goaexpr.ValidationExpr `json:"validation,omitempty"`
		Default          any                     `json:"default,omitempty"`
		NonNullableElems bool                    `json:"non_nullable_elems,omitempty"`
	}

	checkData struct {
		Name        string
		Method      string
		Description string
		Selector    string
		Attribute   *goaexpr.AttributeExpr
		Ref         string
		ExampleRef  string
		Binding     string
	}

	requirementPlan struct {
		ID        string
		Statement string
		Subject   string
		Evidence  []string
		ForEach   string
		Scope     string
		Reasoning bool
	}

	requirementData struct {
		ID        string
		SchemaID  string
		Statement string
		Subject   string
		Evidence  []string
		ForEach   string
		Scope     string
		Reasoning bool
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
	planned.Schema, err = observationSchema(observation, layout.Policy())
	if err != nil {
		return fmt.Errorf("scenario %q observation schema: %w", scenario.Name, err)
	}
	// Scenarios share a codec only when both their Go representation and every
	// observation rule agree. Different rules still reuse the named Go type.
	for _, existing := range suite.ObservationCodecs {
		if existing.Layout.TypeDeclaration() == layout.TypeDeclaration() && existing.Schema == planned.Schema {
			planned.Codec = existing
			break
		}
	}
	if planned.Codec == nil {
		value, err := suite.Codecs.AddStandalone(scenario.Name, observation.Type.Name(), observation, layout)
		if err != nil {
			return err
		}
		planned.Codec = &observationCodecPlan{Value: value, Layout: layout, Schema: planned.Schema}
		suite.ObservationCodecs = append(suite.ObservationCodecs, planned.Codec)
	}
	for _, check := range scenario.Checks {
		planned.Checks = append(planned.Checks, checkData{
			Name: strconv.Quote(check.Name), Method: "Check" + planned.Method + goacodegen.Goify(check.Name, true), Description: check.Description,
			Selector: "$", Attribute: observation,
		})
	}
	if err := planRequirements(suite, planned, observation, scenario.Requirements, suite.Name+"/"+scenario.Name, ""); err != nil {
		return err
	}
	for _, assessment := range scenario.Assessments {
		selected, err := evalexpr.Select(observation, observation, assessment.Selector)
		if err != nil {
			return err
		}
		for _, check := range assessment.Component.Checks {
			planned.Checks = append(planned.Checks, checkData{
				Name:        strconv.Quote(assessment.Name + "/" + check.Name),
				Method:      "CheckComponent" + goacodegen.Goify(assessment.Component.Name, true) + goacodegen.Goify(check.Name, true),
				Description: check.Description, Selector: assessment.Selector, Attribute: selected,
			})
		}
		if err := planRequirements(suite, planned, selected, assessment.Component.Requirements, suite.Name+"/"+scenario.Name+"/"+assessment.Name, assessment.Selector); err != nil {
			return err
		}
	}
	return nil
}

// observationSchema preserves the public JSON shape and adds a private digest
// of the decoder facts that JSON Schema cannot distinguish. The existing replay
// and qualification hashes therefore change when captured values would acquire
// a different meaning, without requiring a runtime schema interpreter.
func observationSchema(attribute *goaexpr.AttributeExpr, policy goacodegen.GoLayoutPolicy) (string, error) {
	schema, err := shared.ToJSONSchema(attribute)
	if err != nil {
		return "", err
	}
	contract := struct {
		Policy      goacodegen.GoLayoutPolicy
		Occurrences map[string]observationOccurrence
	}{Policy: policy, Occurrences: make(map[string]observationOccurrence)}
	// ToJSONSchema rejects recursive observations before this traversal starts.
	recordObservationOccurrences(attribute, "$", contract.Occurrences)
	encoded, err := json.Marshal(contract)
	if err != nil {
		return "", fmt.Errorf("marshal observation decoder contract: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(schema), &document); err != nil {
		return "", fmt.Errorf("read generated observation schema: %w", err)
	}
	document["x-goa-observation-contract"] = json.RawMessage(strconv.Quote(fmt.Sprintf("%x", sha256.Sum256(encoded))))
	encoded, err = json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("marshal observation schema identity: %w", err)
	}
	return string(encoded), nil
}

// recordObservationOccurrences retains original occurrence rules because the
// shared JSON shape graph intentionally shares named definitions. Quoted field
// names keep paths distinct even when an authored name contains a separator.
func recordObservationOccurrences(attribute *goaexpr.AttributeExpr, path string, occurrences map[string]observationOccurrence) {
	occurrence := observationOccurrence{Validation: attribute.Validation, Default: attribute.DefaultValue}
	if primitive, ok := jsonshape.PrimitiveType(attribute); ok {
		occurrence.Primitive = primitive.Name()
	}
	switch actual := attribute.Type.(type) {
	case goaexpr.UserType:
		recordObservationOccurrences(actual.Attribute(), path+"/definition", occurrences)
	case *goaexpr.Array:
		occurrence.NonNullableElems = actual.NonNullableElems
		recordObservationOccurrences(actual.ElemType, path+"/element", occurrences)
	case *goaexpr.Map:
		recordObservationOccurrences(actual.KeyType, path+"/key", occurrences)
		recordObservationOccurrences(actual.ElemType, path+"/element", occurrences)
	case *goaexpr.Object:
		for _, field := range *actual {
			recordObservationOccurrences(field.Attribute, path+"/field/"+strconv.Quote(field.Name), occurrences)
		}
	case *goaexpr.Union:
		for _, branch := range actual.Values {
			recordObservationOccurrences(branch.Attribute, path+"/branch/"+strconv.Quote(branch.Name), occurrences)
		}
	}
	occurrences[path] = occurrence
}

// planRequirements copies fixed assertions with their selected observation root.
// Reusing a component never changes its original selectors or declaration order.
func planRequirements(suite *suitePlan, planned *scenarioPlan, observation *goaexpr.AttributeExpr, requirements []*evalexpr.RequirementExpr, prefix, selector string) error {
	for _, requirement := range requirements {
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
			ID:        prefix + "/" + requirement.Name,
			Statement: requirement.Statement, Subject: requirement.Subject,
			Evidence: append([]string(nil), requirement.Evidence...), ForEach: requirement.ForEach,
			Scope: selector, Reasoning: requirement.Reasoning,
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
			Scope: strconv.Quote(requirement.Scope), Reasoning: requirement.Reasoning,
		}
		for _, field := range requirement.Evidence {
			definition.Evidence = append(definition.Evidence, strconv.Quote(field))
		}
		definitions[i] = definition
		fmt.Fprintf(&out, "{\n// Bind %s from the captured observation.\n", requirement.ID)
		root, rootTarget := scenario.Observation, "observed"
		if requirement.Scope != "" {
			selected := selectField(root, rootTarget, root, rootTarget, requirement.Scope, scope)
			writeRequiredSelection(&out, selected, "requirement "+requirement.ID+": missing captured component "+requirement.Scope)
			root, rootTarget = selected.Attribute, selected.Expression
			if selected.Pointer {
				rootTarget = "(*" + rootTarget + ")"
			}
		}
		local, target := root, rootTarget
		if requirement.ForEach != "" {
			selected := selectField(root, rootTarget, local, target, requirement.ForEach, scope)
			fmt.Fprintf(&out, "binding.Subjects[%q] = []eval.Subject{}\n", requirement.ID)
			if len(selected.Present) > 0 {
				fmt.Fprintf(&out, "if !(%s) { return eval.Binding{}, fmt.Errorf(%q) }\n",
					strings.Join(selected.Present, " && "), "requirement "+requirement.ID+": missing captured collection "+requirement.ForEach)
			}
			fmt.Fprintf(&out, "for _, item := range %s {\n", selected.Expression)
			local, target = goaexpr.AsArray(selected.Attribute.Type).ElemType, "item"
		}
		out.WriteString("subject := eval.Subject{}\n")
		selected := selectField(root, rootTarget, local, target, requirement.Subject, scope)
		writeSelectedContent(&out, selected)
		if len(requirement.Evidence) > 0 {
			out.WriteString("reference := make(map[string]json.RawMessage)\n")
			for _, field := range requirement.Evidence {
				selected := selectField(root, rootTarget, local, target, field, scope)
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
func selectField(root *goaexpr.AttributeExpr, rootTarget string, local *goaexpr.AttributeExpr, target, selector string, scope *goacodegen.NameScope) selectedField {
	if selector == "@" {
		selector = ""
	}
	if selector == "$" || strings.HasPrefix(selector, "$.") {
		local, target = root, rootTarget
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

// linkChecks writes direct calls to typed predicates and requires the selected
// component value to exist. One predicate method may serve multiple assessments.
func linkChecks(scenario scenarioPlan, scope *goacodegen.NameScope, exampleAlias string) []checkData {
	checks := make([]checkData, len(scenario.Checks))
	for i, check := range scenario.Checks {
		selected := selectField(scenario.Observation, "observed", scenario.Observation, "observed", check.Selector, scope)
		check.Ref = scope.GoTypeRef(check.Attribute)
		if exampleAlias != "" {
			check.ExampleRef = scope.GoFullTypeRef(check.Attribute, exampleAlias)
		}
		var out strings.Builder
		writeRequiredSelection(&out, selected, "check "+scenario.RawID+"/"+strings.Trim(check.Name, `"`)+": missing captured component "+check.Selector)
		target := selected.Expression
		if selected.Pointer && !goaexpr.IsObject(selected.Attribute.Type) {
			target = "*" + target
		}
		fmt.Fprintf(&out, "diagnostic := checks.%s(%s)\nbinding.Checks = append(binding.Checks, eval.Check{Name: %s, Passed: diagnostic == \"\", Diagnostic: diagnostic})\n", check.Method, target, check.Name)
		check.Binding = out.String()
		checks[i] = check
	}
	return checks
}

// writeRequiredSelection returns a precise error when an optional parent or
// selected value is missing, before generated code dereferences that value.
func writeRequiredSelection(out *strings.Builder, selected selectedField, diagnostic string) {
	if len(selected.Present) > 0 {
		fmt.Fprintf(out, "if !(%s) { return eval.Binding{}, fmt.Errorf(%q) }\n", strings.Join(selected.Present, " && "), diagnostic)
	}
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
