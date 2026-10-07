// Package codec links planned names and writes the generated JSON codec source.
package codec

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"goa.design/goa-ai/codegen/internal/jsonshape"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// fileData contains the complete source model for one generated codec file.
	fileData struct {
		Types              []*typeData
		Unions             []*unionData
		Values             []*valueData
		Helpers            []*goacodegen.TransformFunctionData
		JSONValidators     []*shapeData
		Preflights         string
		OriginalValidators []*originalValidatorData
		Imports            codecImportNames
		JSONNames          jsonshape.Names
		ReadStrictJSON     string
		ReadJSONValue      string
		ValidateJSONText   string
	}

	// typeData contains one transport type and its validation function.
	typeData struct {
		Name       string
		Definition string
		Alias      bool
		Validator  string
		Reference  string
		Pointer    bool
		Validation string
		// IntegerDecode emits numeric decoding only for a required integer transport type.
		IntegerDecode bool
	}

	// unionData contains one Goa OneOf type and its JSON behavior.
	unionData struct {
		Name     string
		KindName string
		TypeKey  string
		ValueKey string
		Flatten  bool
		Encode   bool
		Decode   bool
		Open     bool
		Branches []*unionBranchData
	}

	// unionBranchData contains one generated Goa OneOf branch.
	unionBranchData struct {
		Name        string
		FieldName   string
		FieldType   string
		Kind        string
		Constructor string
		Nilable     bool
	}

	// valueData contains the generated functions for one service value.
	valueData struct {
		Name              string
		ServiceRef        string
		ServicePointer    bool
		TransportRef      string
		Validator         string
		Validate          string
		Constructor       string
		Encode            string
		Decode            string
		EncodeTransform   string
		DecodeTransform   string
		Shape             string
		Preflight         string
		OriginalValidator string
		// Elicitation checks protocol actions before decoding native answer fields.
		Elicitation bool
		// Form selects non-sensitive form content rather than URL consent.
		Form bool
	}

	// serviceTypeWriter lets Goa's service resolver add the package name to each
	// named service type.
	serviceTypeWriter struct {
		goacodegen.Attributor
	}
)

// Files writes the single codec file after Goa has fixed all package names.
func (p *Plan) Files(packageName string) ([]*goacodegen.File, error) {
	if !p.generation.Frozen() {
		return nil, fmt.Errorf("JSON codec files cannot be rendered before generation freeze")
	}
	if packageName == "" {
		return nil, fmt.Errorf("JSON codec files require the owning package name")
	}
	for _, value := range p.values {
		if value.serviceAttributor == nil {
			return nil, fmt.Errorf("JSON value %q has no service type writer", value.key)
		}
	}
	data, imports, err := p.link()
	if err != nil {
		return nil, err
	}
	sections := []*goacodegen.SectionTemplate{
		goacodegen.Header("Private JSON codecs for generated service values.", packageName, imports),
		{Name: "json-codecs", Source: codecSource, Data: data},
	}
	if len(data.JSONValidators) > 0 {
		sections = append(sections, &goacodegen.SectionTemplate{
			Name:   "strict-json-values",
			Source: `{{template "json-value-validators" .}}` + jsonshape.ValidatorsSource + standaloneSource + `{{.Preflights}}`,
			Data:   data,
		})
	}
	return []*goacodegen.File{{
		Path:             filepath.Join(p.pkg.OutputDirectory(), "codec.go"),
		SectionTemplates: sections,
	}}, nil
}

// link formats every retained plan with the final Goa names and import aliases.
func (p *Plan) link() (*fileData, []*goacodegen.ImportSpec, error) {
	data := &fileData{Imports: p.importNames()}
	if p.jsonHelpers != nil {
		data.JSONNames = p.jsonNames(data.Imports)
		data.ReadStrictJSON = p.jsonHelpers.read.Name()
		data.ReadJSONValue = p.jsonHelpers.value.Name()
		data.ValidateJSONText = p.jsonHelpers.text.Name()
	}
	typeKeys := make(map[*goacodegen.NameDeclaration]struct{})
	unionKeys := make(map[*goacodegen.NameDeclaration]*unionData)
	helperKeys := make(map[*goacodegen.NameDeclaration]struct{})
	for _, value := range p.values {
		if value.standalone != nil {
			data.JSONValidators = append(data.JSONValidators, value.shapeData()...)
			preflight, err := value.renderPreflight()
			if err != nil {
				return nil, nil, err
			}
			data.Preflights += preflight
			validators, err := value.originalValidationData()
			if err != nil {
				return nil, nil, err
			}
			data.OriginalValidators = append(data.OriginalValidators, validators...)
		}
		for _, planned := range value.types {
			if _, exists := typeKeys[planned.declaration]; exists {
				continue
			}
			typeKeys[planned.declaration] = struct{}{}
			linkedType := planned.layout.Link(p.pkg.ImportPath(), p.pkg.ImportName)
			parameter := planned.parameter.Link(p.pkg.ImportPath(), p.pkg.ImportName)
			linkedValidation, err := planned.validation.Link(parameter)
			if err != nil {
				return nil, nil, err
			}
			data.Types = append(data.Types, &typeData{
				IntegerDecode: planned.integerDecode,
				Name:          planned.declaration.Name(),
				Definition:    linkedType.Def(),
				Alias:         planned.alias,
				Validator:     planned.validatorDeclaration.Name(),
				Reference:     parameter.Ref(),
				Pointer:       parameter.ReferenceIsPointer(),
				Validation:    linkedValidation.Render("value", "body"),
			})
		}
		for _, planned := range value.unions {
			if linked := unionKeys[planned.name]; linked != nil {
				linked.Encode = linked.Encode || value.direction.encodes()
				linked.Decode = linked.Decode || value.direction.decodes()
				continue
			}
			expression := planned.attribute.Type.(*goaexpr.Union)
			union := &unionData{
				Name:     planned.name.Name(),
				KindName: planned.kind.Name(),
				TypeKey:  expression.GetTypeKey(),
				ValueKey: expression.GetValueKey(),
				Flatten:  expression.Flatten,
				Encode:   value.direction.encodes(),
				Decode:   value.direction.decodes(),
				Open:     value.elicitation != nil,
			}
			unionKeys[planned.name] = union
			for _, branch := range planned.branches {
				fieldType := branch.layout.Link(p.pkg.ImportPath(), p.pkg.ImportName).Ref()
				union.Branches = append(union.Branches, &unionBranchData{
					Name:        branch.name,
					FieldName:   branch.fieldName,
					FieldType:   fieldType,
					Kind:        branch.kind.Name(),
					Constructor: branch.constructor.Name(),
					Nilable: strings.HasPrefix(fieldType, "*") ||
						strings.HasPrefix(fieldType, "[]") || strings.HasPrefix(fieldType, "map["),
				})
			}
			data.Unions = append(data.Unions, union)
		}
		linked, helpers, err := value.link()
		if err != nil {
			return nil, nil, err
		}
		data.Values = append(data.Values, linked)
		for _, helper := range helpers {
			if _, exists := helperKeys[helper.Declaration]; exists {
				continue
			}
			helperKeys[helper.Declaration] = struct{}{}
			data.Helpers = append(data.Helpers, helper)
		}
	}
	sort.Slice(data.Types, func(i, j int) bool { return data.Types[i].Name < data.Types[j].Name })
	sort.Slice(data.Unions, func(i, j int) bool { return data.Unions[i].Name < data.Unions[j].Name })
	sort.Slice(data.Values, func(i, j int) bool { return data.Values[i].Name < data.Values[j].Name })
	sort.Slice(data.Helpers, func(i, j int) bool {
		return data.Helpers[i].Declaration.Name() < data.Helpers[j].Declaration.Name()
	})
	return data, p.imports(), nil
}

// link binds the retained transport layout and service writer, then renders conversions.
func (v *Value) link() (*valueData, []*goacodegen.TransformFunctionData, error) {
	transportLayout := v.transportLayout.Link(v.plan.pkg.ImportPath(), v.plan.pkg.ImportName)
	transportContext := &goacodegen.AttributeContext{
		Pointer:             true,
		Scope:               goacodegen.NewAttributeScope(v.plan.pkg.Scope()),
		UnionPointer:        true,
		ArrayElementPointer: true,
	}
	transportContext, err := transportContext.WithGoTypeLayout(transportLayout)
	if err != nil {
		return nil, nil, fmt.Errorf("link JSON value %q transport layout: %w", v.key, err)
	}
	serviceWriter := &serviceTypeWriter{Attributor: v.serviceAttributor}
	serviceContext := &goacodegen.AttributeContext{
		UseDefault: true,
		Scope:      serviceWriter,
	}
	// Service-value codecs keep the exact Go fields supplied by Goa. Codecs
	// for declarations created in this package use their original type writer.
	if v.originalLayout == nil {
		policy := v.serviceLayout.Policy()
		serviceContext.Pointer = policy.Pointer
		serviceContext.UseDefault = policy.UseDefault
		serviceContext.IgnoreRequired = policy.IgnoreRequired
		serviceContext.IgnoreRequiredCollections = policy.IgnoreRequiredCollections
		serviceContext.UnionPointer = policy.UnionPointer
		serviceContext.ArrayElementPointer = policy.ArrayElementPointer
		serviceContext, err = serviceContext.WithGoTypeLayout(v.serviceLayout.Link(v.plan.pkg.ImportPath(), v.plan.pkg.ImportName))
		if err != nil {
			return nil, nil, fmt.Errorf("link JSON value %q service layout: %w", v.key, err)
		}
	}
	top := v.types[0]
	data := &valueData{
		Name:         v.preferredName,
		ServiceRef:   serviceWriter.Ref(v.service, ""),
		TransportRef: transportLayout.Ref(),
		Validator:    top.validatorDeclaration.Name(),
	}
	if v.elicitation != nil {
		data.Elicitation = true
		data.Form = v.elicitation.form
	}
	if v.standalone != nil {
		data.Shape = v.standalone.names[v.standalone.root].Name()
		data.Preflight = v.standalone.preflight.Name()
		if v.standalone.originalValidator != nil {
			data.OriginalValidator = v.standalone.originalValidator.Name()
		}
	}
	if v.constructor != nil {
		data.Constructor = v.constructor.Name()
	}
	var helpers []*goacodegen.TransformFunctionData
	if v.encode != nil {
		if err := v.encode.BindContexts(serviceContext, transportContext); err != nil {
			return nil, nil, fmt.Errorf("link JSON value %q encoder: %w", v.key, err)
		}
		transform, encodeHelpers, err := v.encode.Render("in", "body", false)
		if err != nil {
			return nil, nil, fmt.Errorf("render JSON value %q encoder: %w", v.key, err)
		}
		if v.encodeDeclaration != nil {
			data.Encode = v.encodeDeclaration.Name()
		}
		if v.validateDeclaration != nil {
			data.Validate = v.validateDeclaration.Name()
			data.ServicePointer = v.serviceLayout.Link(v.plan.pkg.ImportPath(), v.plan.pkg.ImportName).ReferenceIsPointer()
		}
		data.EncodeTransform = transform
		helpers = append(helpers, encodeHelpers...)
	}
	if v.decode != nil {
		if err := v.decode.BindContexts(transportContext, serviceContext); err != nil {
			return nil, nil, fmt.Errorf("link JSON value %q decoder: %w", v.key, err)
		}
		transform, decodeHelpers, err := v.decode.Render("body", "out", false)
		if err != nil {
			return nil, nil, fmt.Errorf("render JSON value %q decoder: %w", v.key, err)
		}
		if v.decodeDeclaration != nil {
			data.Decode = v.decodeDeclaration.Name()
		}
		data.DecodeTransform = transform
		helpers = append(helpers, decodeHelpers...)
	}
	return data, helpers, nil
}

// Package returns no separate package name because the service resolver adds
// it when it writes a named type.
func (*serviceTypeWriter) Package(*goaexpr.AttributeExpr) string {
	return ""
}

// Enter keeps that rule while the service resolver follows nested types.
func (w *serviceTypeWriter) Enter(attribute *goaexpr.AttributeExpr) goacodegen.Attributor {
	return &serviceTypeWriter{Attributor: w.Attributor.Enter(attribute)}
}

// imports returns the packages required by the conversions selected in the
// plan and by the service types they use.
func (p *Plan) imports() []*goacodegen.ImportSpec {
	paths := make([]string, 0, len(p.importPaths)+len(p.locatedImportPaths))
	for importPath := range p.importPaths {
		paths = append(paths, importPath)
	}
	for importPath := range p.locatedImportPaths {
		paths = append(paths, importPath)
	}
	sort.Strings(paths)
	imports := make([]*goacodegen.ImportSpec, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, importPath := range paths {
		if _, exists := seen[importPath]; exists {
			continue
		}
		seen[importPath] = struct{}{}
		imports = append(imports, p.pkg.Import(importPath))
	}
	return imports
}

// Conversion blocks keep field-derived temporary variables separate from the
// encoder and decoder variables while assigning their outer result values.
const codecSource = `
{{ range .Types }}
// {{ .Name }} stores JSON fields until they have been validated.
type {{ .Name }} {{ if .Alias }}= {{ end }}{{ .Definition }}

{{ if .IntegerDecode }}
// UnmarshalJSON reads an exact whole JSON number and stores it within this type's
// declared range. Decimal and exponent spellings do not change its value.
func (value *{{ .Name }}) UnmarshalJSON(data []byte) error {
 number, err := {{ $.Imports.RawJSON }}.DecodeInteger[{{ .Definition }}](data)
 if err != nil { return {{ $.Imports.Fmt }}.Errorf("decode {{ .Name }} integer: %w", err) }
 *value = {{ .Name }}(number)
 return nil
}
{{ end }}
// {{ .Validator }} checks decoded JSON before it becomes a service value.
func {{ .Validator }}(value {{ .Reference }}) (err error) {
	{{- if .Pointer }}
	if value == nil {
		return {{ $.Imports.Goa }}.MissingFieldError("body", "JSON value")
	}
	{{- end }}
	{{ .Validation }}
	return err
}
{{ end }}
{{ range .OriginalValidators }}
// {{ .Name }} checks the original typed value before JSON conversion.
func {{ .Name }}(value {{ .Reference }}) (err error) {
	{{ .Validation }}
	return err
}
{{ end }}
{{ range .Unions }}
{{- $union := . }}
// {{ .Name }} stores exactly one selected Goa OneOf branch.
type {{ .Name }} struct {
	kind {{ .KindName }}
	{{- range .Branches }}
	{{ .FieldName }} {{ .FieldType }}
	{{- end }}
}

// {{ .KindName }} identifies the selected branch of {{ .Name }}.
type {{ .KindName }} string

const (
	{{- range .Branches }}
	{{ .Kind }} {{ $union.KindName }} = {{ printf "%q" .Name }}
	{{- end }}
)

// Kind returns the selected branch.
func (u {{ .Name }}) Kind() {{ .KindName }} {
	return u.kind
}
{{ range .Branches }}
// {{ .Constructor }} creates {{ $union.Name }} with its {{ .Name }} branch selected.
func {{ .Constructor }}(value {{ .FieldType }}) {{ $union.Name }} {
	return {{ $union.Name }}{kind: {{ .Kind }}, {{ .FieldName }}: value}
}

// As{{ .FieldName }} returns the {{ .Name }} branch when it is selected.
func (u {{ $union.Name }}) As{{ .FieldName }}() (_ {{ .FieldType }}, ok bool) {
	if u.kind != {{ .Kind }} {
		return
	}
	return u.{{ .FieldName }}, true
}

// Set{{ .FieldName }} selects the {{ .Name }} branch.
func (u *{{ $union.Name }}) Set{{ .FieldName }}(value {{ .FieldType }}) {
	u.kind = {{ .Kind }}
	u.{{ .FieldName }} = value
}
{{ end }}
// Validate checks that one complete branch is selected.
func (u {{ .Name }}) Validate() error {
	switch u.kind {
	{{- range .Branches }}
	case {{ .Kind }}:
		{{- if .Nilable }}
		if u.{{ .FieldName }} == nil {
			return {{ $.Imports.Goa }}.MissingFieldError({{ printf "%q" $union.ValueKey }}, {{ printf "%q" $union.Name }})
		}
		{{- end }}
		return nil
	{{- end }}
	case "":
		return {{ $.Imports.Goa }}.MissingFieldError({{ printf "%q" .TypeKey }}, {{ printf "%q" .Name }})
	default:
		return {{ $.Imports.Goa }}.InvalidEnumValueError({{ printf "%q" .TypeKey }}, u.kind, []any{
			{{- range .Branches }}string({{ .Kind }}),{{- end }}
		})
	}
}

{{ if .Encode }}
// MarshalJSON writes the selected branch name and value.
func (u {{ .Name }}) MarshalJSON() ([]byte, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	var value any
	switch u.kind {
	{{- range .Branches }}
	case {{ .Kind }}:
		value = u.{{ .FieldName }}
	{{- end }}
	default:
		return nil, {{ $.Imports.Fmt }}.Errorf("unexpected {{ .Name }} branch %q", u.kind)
	}
	{{- if .Flatten }}
	data, err := {{ $.Imports.JSON }}.Marshal(value)
	if err != nil { return nil, err }
	var fields map[string]{{ $.Imports.JSON }}.RawMessage
	if err := {{ $.Imports.JSON }}.Unmarshal(data, &fields); err != nil { return nil, err }
	if fields == nil { return nil, {{ $.Imports.Goa }}.InvalidFieldTypeError({{ printf "%q" .TypeKey }}, nil, "non-null JSON object") }
	if _, exists := fields[{{ printf "%q" .TypeKey }}]; exists {
		return nil, {{ $.Imports.Fmt }}.Errorf("{{ .Name }} branch already contains discriminator %q", {{ printf "%q" .TypeKey }})
	}
	tag, err := {{ $.Imports.JSON }}.Marshal(string(u.kind))
	if err != nil { return nil, err }
	fields[{{ printf "%q" .TypeKey }}] = tag
	return {{ $.Imports.JSON }}.Marshal(fields)
	{{- else }}
	return {{ $.Imports.JSON }}.Marshal(struct {
		Type string ` + "`json:\"{{ .TypeKey }}\"`" + `
		Value any ` + "`json:\"{{ .ValueKey }}\"`" + `
	}{Type: string(u.kind), Value: value})
	{{- end }}
}
{{ end }}

{{ if .Decode }}
// UnmarshalJSON reads one complete branch name and value.
func (u *{{ .Name }}) UnmarshalJSON(data []byte) error {
	{{- $union := . }}
	{{- if .Flatten }}
	var fields map[string]{{ $.Imports.JSON }}.RawMessage
	if err := {{ $.Imports.JSON }}.Unmarshal(data, &fields); err != nil { return err }
	tag, exists := fields[{{ printf "%q" .TypeKey }}]
	if !exists { return {{ $.Imports.Goa }}.MissingFieldError({{ printf "%q" .TypeKey }}, {{ printf "%q" .Name }}) }
	if {{ $.Imports.Bytes }}.Equal({{ $.Imports.Bytes }}.TrimSpace(tag), []byte("null")) {
		return {{ $.Imports.Goa }}.InvalidFieldTypeError({{ printf "%q" .TypeKey }}, nil, "JSON string")
	}
	var raw struct {
		Type string
		Value {{ $.Imports.JSON }}.RawMessage
	}
	if err := {{ $.Imports.JSON }}.Unmarshal(tag, &raw.Type); err != nil { return err }
	delete(fields, {{ printf "%q" .TypeKey }})
	value, err := {{ $.Imports.JSON }}.Marshal(fields)
	if err != nil { return err }
	raw.Value = value
	{{- else }}
	var raw struct {
		Type string ` + "`json:\"{{ .TypeKey }}\"`" + `
		Value {{ $.Imports.JSON }}.RawMessage ` + "`json:\"{{ .ValueKey }}\"`" + `
	}
	decoder := {{ $.Imports.JSON }}.NewDecoder({{ $.Imports.Bytes }}.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != {{ $.Imports.IO }}.EOF {
		if err == nil {
			return {{ $.Imports.Fmt }}.Errorf("decode {{ .Name }} JSON: multiple JSON values")
		}
		return err
	}
	if raw.Type == "" {
		return {{ $.Imports.Goa }}.MissingFieldError({{ printf "%q" .TypeKey }}, {{ printf "%q" .Name }})
	}
	if len(raw.Value) == 0 {
		return {{ $.Imports.Goa }}.MissingFieldError({{ printf "%q" .ValueKey }}, {{ printf "%q" .Name }})
	}
	if {{ $.Imports.Bytes }}.Equal({{ $.Imports.Bytes }}.TrimSpace(raw.Value), []byte("null")) {
		return {{ $.Imports.Goa }}.InvalidFieldTypeError({{ printf "%q" .ValueKey }}, nil, "non-null JSON value")
	}
	{{- end }}
	switch raw.Type {
	{{- range .Branches }}
	case string({{ .Kind }}):
		var value {{ .FieldType }}
		{{- if $union.Open }}
		if err := {{ $.Imports.JSONV2 }}.Unmarshal(raw.Value, &value); err != nil {
			return err
		}
		{{- else }}
		decoder := {{ $.Imports.JSON }}.NewDecoder({{ $.Imports.Bytes }}.NewReader(raw.Value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		{{- end }}
		u.kind = {{ .Kind }}
		u.{{ .FieldName }} = value
	{{- end }}
	default:
		return {{ $.Imports.Goa }}.InvalidEnumValueError({{ printf "%q" .TypeKey }}, raw.Type, []any{
			{{- range .Branches }}string({{ .Kind }}),{{- end }}
		})
	}
	return u.Validate()
}
{{ end }}
{{ end }}
{{ range .Values }}
{{ if .Encode }}
// {{ .Encode }} turns a service value into JSON using the field names in the Goa design.
func {{ .Encode }}(in {{ .ServiceRef }}) ([]byte, error) {
	{{- if .Preflight }}
	if err := {{ .Preflight }}(in); err != nil {
		return nil, {{ $.Imports.Fmt }}.Errorf("encode {{ .Name }} JSON: %w", err)
	}
	{{- end }}
	{{- if .OriginalValidator }}
	if err := {{ .OriginalValidator }}(in); err != nil {
		return nil, {{ $.Imports.Fmt }}.Errorf("validate {{ .Name }} value: %w", err)
	}
	{{- end }}
	var body {{ .TransportRef }}
	{
		{{ .EncodeTransform }}
	}
	if err := {{ .Validator }}(body); err != nil {
		return nil, {{ $.Imports.Fmt }}.Errorf("validate {{ .Name }} JSON: %w", err)
	}
	data, err := {{ $.Imports.JSON }}.Marshal(body)
	if err != nil {
		return nil, {{ $.Imports.Fmt }}.Errorf("encode {{ .Name }} JSON: %w", err)
	}
	return data, nil
}
{{ end }}

{{ if .Validate }}
// {{ .Validate }} checks a service value against its declared field validation.
func {{ .Validate }}(in {{ .ServiceRef }}) error {
 {{ if .ServicePointer }}
 if in == nil {
  return {{ $.Imports.Goa }}.MissingFieldError("result", {{ printf "%q" .Name }})
 }
 {{ end }}
 var body {{ .TransportRef }}
 {
  {{ .EncodeTransform }}
 }
 if err := {{ .Validator }}(body); err != nil {
  return {{ $.Imports.Fmt }}.Errorf("validate {{ .Name }} value: %w", err)
 }
 return nil
}
{{ end }}

{{ if .Constructor }}
// {{ .Constructor }} validates a decoded transport value and returns the service value.
func {{ .Constructor }}(body {{ .TransportRef }}) (out {{ .ServiceRef }}, err error) {
	if err := {{ .Validator }}(body); err != nil {
		return out, {{ $.Imports.Fmt }}.Errorf("validate {{ .Name }} JSON: %w", err)
	}
	{
		{{ .DecodeTransform }}
	}
	return out, nil
}

{{ end }}
{{ if .Decode }}
// {{ .Decode }} checks JSON field names from the Goa design and returns a service value.
func {{ .Decode }}(data []byte) (out {{ .ServiceRef }}, err error) {
	{{- if .Elicitation }}
	{{- if .Form }}
	if err := {{ $.Imports.MCP }}.ValidateFormResponse(data); err != nil {
	{{- else }}
	if err := {{ $.Imports.MCP }}.ValidateURLResponse(data); err != nil {
	{{- end }}
		return out, err
	}
	{{- end }}
	{{- if .Shape }}
	root, err := {{ $.ReadStrictJSON }}(data)
	if err != nil {
		return out, {{ $.Imports.Fmt }}.Errorf("decode {{ .Name }} JSON: %w", err)
	}
	if err := {{ .Shape }}("", root, ""); err != nil {
		return out, {{ $.Imports.Fmt }}.Errorf("decode {{ .Name }} JSON: %w", err)
	}
	{{- end }}
	var body {{ .TransportRef }}
	{{- if .Elicitation }}
	if err := {{ $.Imports.JSONV2 }}.Unmarshal(data, &body); err != nil {
		return out, {{ $.Imports.Fmt }}.Errorf("decode {{ .Name }} JSON: %w", err)
	}
	{{- else }}
	decoder := {{ $.Imports.JSON }}.NewDecoder({{ $.Imports.Bytes }}.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return out, {{ $.Imports.Fmt }}.Errorf("decode {{ .Name }} JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != {{ $.Imports.IO }}.EOF {
		if err == nil {
			return out, {{ $.Imports.Fmt }}.Errorf("decode {{ .Name }} JSON: multiple JSON values")
		}
		return out, {{ $.Imports.Fmt }}.Errorf("decode {{ .Name }} JSON after first value: %w", err)
	}
	{{- end }}
	{{- if .Constructor }}
	return {{ .Constructor }}(body)
	{{- else }}
	if err := {{ .Validator }}(body); err != nil {
		return out, {{ $.Imports.Fmt }}.Errorf("validate {{ .Name }} JSON: %w", err)
	}
	{
		{{ .DecodeTransform }}
	}
	return out, nil
	{{- end }}
}
{{ end }}
{{ end }}
{{ range .Helpers }}
func {{ .Declaration.Name }}(v {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
	{{ .Code }}
	return res
}
{{ end }}
`
