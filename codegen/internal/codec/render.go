// This file links planned names and writes the generated JSON codec source.
package codec

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// fileData contains the complete source model for one generated codec file.
	fileData struct {
		Types   []*typeData
		Unions  []*unionData
		Values  []*valueData
		Helpers []*goacodegen.TransformFunctionData
	}

	// typeData contains one transport type and its validation function.
	typeData struct {
		Name       string
		Definition string
		Validator  string
		Reference  string
		Pointer    bool
		Validation string
	}

	// unionData contains one Goa OneOf type and its JSON behavior.
	unionData struct {
		Name     string
		KindName string
		TypeKey  string
		ValueKey string
		Encode   bool
		Decode   bool
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
		Name            string
		ServiceRef      string
		TransportRef    string
		Validator       string
		Constructor     string
		Encode          string
		Decode          string
		EncodeTransform string
		DecodeTransform string
	}

	// transportTypeWriter applies transport pointer rules while keeping Goa's
	// built-in empty type as an unnamed empty struct.
	transportTypeWriter struct {
		goacodegen.Attributor
		types  map[goaexpr.UserType]*goacodegen.TypeDeclaration
		unions map[goacodegen.UnionTypeID]*goacodegen.UnionDeclaration
	}

	// serviceTypeWriter lets Goa's service resolver add the package name to each
	// named service type.
	serviceTypeWriter struct {
		goacodegen.Attributor
	}
)

// Files writes the single codec file after Goa has fixed all package names.
func (p *Plan) Files() ([]*goacodegen.File, error) {
	if !p.generation.Frozen() {
		return nil, fmt.Errorf("JSON codec files cannot be rendered before generation freeze")
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
	return []*goacodegen.File{{
		Path: filepath.Join(p.pkg.OutputDirectory(), "codec.go"),
		SectionTemplates: []*goacodegen.SectionTemplate{
			goacodegen.Header("Private JSON codecs for generated service values.", p.packageName, imports),
			{Name: "json-codecs", Source: codecSource, Data: data},
		},
	}}, nil
}

// link formats every retained plan with the final Goa names and import aliases.
func (p *Plan) link() (*fileData, []*goacodegen.ImportSpec, error) {
	data := &fileData{}
	typeKeys := make(map[*goacodegen.NameDeclaration]struct{})
	unionKeys := make(map[*goacodegen.UnionDeclaration]*unionData)
	helperKeys := make(map[*goacodegen.NameDeclaration]struct{})
	for _, value := range p.values {
		for _, planned := range value.types {
			if _, exists := typeKeys[planned.declaration]; exists {
				continue
			}
			typeKeys[planned.declaration] = struct{}{}
			linkedType := planned.layout.Link(p.pkg.ImportPath(), p.pkg.ImportName)
			linkedValidation, err := planned.validation.Link(linkedType)
			if err != nil {
				return nil, nil, err
			}
			data.Types = append(data.Types, &typeData{
				Name:       planned.typeDeclaration.Name(),
				Definition: linkedType.Def(),
				Validator:  planned.validatorDeclaration.Name(),
				Reference:  planned.typeDeclaration.Ref(planned.userType),
				Pointer:    strings.HasPrefix(planned.typeDeclaration.Ref(planned.userType), "*"),
				Validation: linkedValidation.Render("value", "body"),
			})
		}
		for _, planned := range value.unions {
			if linked := unionKeys[planned.declaration]; linked != nil {
				linked.Encode = linked.Encode || value.direction.encodes()
				linked.Decode = linked.Decode || value.direction.decodes()
				continue
			}
			union := &unionData{
				Name:     planned.declaration.Name(),
				KindName: planned.declaration.KindName(),
				TypeKey:  planned.union.GetTypeKey(),
				ValueKey: planned.union.GetValueKey(),
				Encode:   value.direction.encodes(),
				Decode:   value.direction.decodes(),
			}
			unionKeys[planned.declaration] = union
			for _, branch := range planned.branches {
				fieldType := branch.layout.Link(p.pkg.ImportPath(), p.pkg.ImportName).Ref()
				union.Branches = append(union.Branches, &unionBranchData{
					Name:        branch.name,
					FieldName:   branch.fieldName,
					FieldType:   fieldType,
					Kind:        branch.declaration.KindConst(),
					Constructor: branch.declaration.Constructor(),
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

// link binds the transport and service type writers and renders both conversions.
func (v *Value) link() (*valueData, []*goacodegen.TransformFunctionData, error) {
	transportAttributor := &transportTypeWriter{
		Attributor: goacodegen.NewAttributeScope(v.plan.pkg.Scope()),
		types:      make(map[goaexpr.UserType]*goacodegen.TypeDeclaration, len(v.types)),
		unions:     make(map[goacodegen.UnionTypeID]*goacodegen.UnionDeclaration, len(v.unions)),
	}
	for _, planned := range v.types {
		transportAttributor.types[planned.userType.Origin()] = planned.typeDeclaration
	}
	for _, planned := range v.unions {
		transportAttributor.unions[goacodegen.NewUnionTypeID(planned.union)] = planned.declaration
	}
	transportContext := &goacodegen.AttributeContext{
		Pointer:             true,
		Scope:               transportAttributor,
		UnionPointer:        true,
		ArrayElementPointer: true,
	}
	serviceWriter := &serviceTypeWriter{Attributor: v.serviceAttributor}
	serviceContext := &goacodegen.AttributeContext{
		UseDefault: true,
		Scope:      serviceWriter,
	}
	top := v.types[0]
	data := &valueData{
		Name:         v.preferredName,
		ServiceRef:   serviceWriter.Ref(v.service, ""),
		TransportRef: top.typeDeclaration.Ref(top.userType),
		Validator:    top.validatorDeclaration.Name(),
	}
	if v.constructor != nil {
		data.Constructor = v.constructor.Name()
	}
	var helpers []*goacodegen.TransformFunctionData
	if v.direction.encodes() {
		if err := v.encode.BindContexts(serviceContext, transportContext); err != nil {
			return nil, nil, fmt.Errorf("link JSON value %q encoder: %w", v.key, err)
		}
		transform, encodeHelpers, err := v.encode.Render("in", "body", false)
		if err != nil {
			return nil, nil, fmt.Errorf("render JSON value %q encoder: %w", v.key, err)
		}
		data.Encode = v.encodeDeclaration.Name()
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

// Name returns the generated transport type name.
func (w *transportTypeWriter) Name(attribute *goaexpr.AttributeExpr, pkg string, pointer, useDefault bool) string {
	if attribute.Type == goaexpr.Empty {
		return "struct {}"
	}
	if name, ok := w.plannedName(attribute.Type); ok {
		return qualifyTypeName(pkg, name)
	}
	return w.Attributor.Name(attribute, pkg, pointer, useDefault)
}

// Ref returns the generated transport type reference.
func (w *transportTypeWriter) Ref(attribute *goaexpr.AttributeExpr, pkg string) string {
	if attribute.Type == goaexpr.Empty {
		return "*struct {}"
	}
	if name, ok := w.plannedName(attribute.Type); ok {
		name = qualifyTypeName(pkg, name)
		if goaexpr.IsObject(attribute.Type) || goaexpr.IsUnion(attribute.Type) {
			return "*" + name
		}
		return name
	}
	return w.Attributor.Ref(attribute, pkg)
}

// Enter keeps the empty-type rule while following nested type ownership.
func (w *transportTypeWriter) Enter(attribute *goaexpr.AttributeExpr) goacodegen.Attributor {
	return &transportTypeWriter{
		Attributor: w.Attributor.Enter(attribute),
		types:      w.types,
		unions:     w.unions,
	}
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

// plannedName returns the final Goa name for a private named type or union.
func (w *transportTypeWriter) plannedName(dataType goaexpr.DataType) (string, bool) {
	switch actual := dataType.(type) {
	case goaexpr.UserType:
		declaration := w.types[actual.Origin()]
		if declaration == nil {
			panic(fmt.Sprintf("transport type %q has no planned declaration", actual.Name()))
		}
		return declaration.Name(), true
	case *goaexpr.Union:
		declaration := w.unions[goacodegen.NewUnionTypeID(actual)]
		if declaration == nil {
			panic(fmt.Sprintf("transport union %q has no planned declaration", actual.Name()))
		}
		return declaration.Name(), true
	default:
		return "", false
	}
}

// qualifyTypeName adds a package qualifier when the generated type is used
// outside its private codec package.
func qualifyTypeName(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "." + name
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

const codecSource = `
{{ range .Types }}
// {{ .Name }} stores JSON fields until they have been validated.
type {{ .Name }} {{ .Definition }}

// {{ .Validator }} checks decoded JSON before it becomes a service value.
func {{ .Validator }}(value {{ .Reference }}) (err error) {
	{{- if .Pointer }}
	if value == nil {
		return goa.MissingFieldError("body", "JSON value")
	}
	{{- end }}
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
			return goa.MissingFieldError({{ printf "%q" $union.ValueKey }}, {{ printf "%q" $union.Name }})
		}
		{{- end }}
		return nil
	{{- end }}
	case "":
		return goa.MissingFieldError({{ printf "%q" .TypeKey }}, {{ printf "%q" .Name }})
	default:
		return goa.InvalidEnumValueError({{ printf "%q" .TypeKey }}, u.kind, []any{
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
		return nil, fmt.Errorf("unexpected {{ .Name }} branch %q", u.kind)
	}
	return json.Marshal(struct {
		Type string ` + "`json:\"{{ .TypeKey }}\"`" + `
		Value any ` + "`json:\"{{ .ValueKey }}\"`" + `
	}{Type: string(u.kind), Value: value})
}
{{ end }}

{{ if .Decode }}
// UnmarshalJSON reads one complete branch name and value.
func (u *{{ .Name }}) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type string ` + "`json:\"{{ .TypeKey }}\"`" + `
		Value json.RawMessage ` + "`json:\"{{ .ValueKey }}\"`" + `
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode {{ .Name }} JSON: multiple JSON values")
		}
		return err
	}
	if raw.Type == "" {
		return goa.MissingFieldError({{ printf "%q" .TypeKey }}, {{ printf "%q" .Name }})
	}
	if len(raw.Value) == 0 {
		return goa.MissingFieldError({{ printf "%q" .ValueKey }}, {{ printf "%q" .Name }})
	}
	if bytes.Equal(bytes.TrimSpace(raw.Value), []byte("null")) {
		return goa.InvalidFieldTypeError({{ printf "%q" .ValueKey }}, nil, "non-null JSON value")
	}
	switch raw.Type {
	{{- range .Branches }}
	case string({{ .Kind }}):
		var value {{ .FieldType }}
		decoder := json.NewDecoder(bytes.NewReader(raw.Value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		u.kind = {{ .Kind }}
		u.{{ .FieldName }} = value
	{{- end }}
	default:
		return goa.InvalidEnumValueError({{ printf "%q" .TypeKey }}, raw.Type, []any{
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
	var body {{ .TransportRef }}
	{{ .EncodeTransform }}
	if err := {{ .Validator }}(body); err != nil {
		return nil, fmt.Errorf("validate {{ .Name }} JSON: %w", err)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode {{ .Name }} JSON: %w", err)
	}
	return data, nil
}
{{ end }}

{{ if .Constructor }}
// {{ .Constructor }} validates a decoded transport value and returns the service value.
func {{ .Constructor }}(body {{ .TransportRef }}) (out {{ .ServiceRef }}, err error) {
	if err := {{ .Validator }}(body); err != nil {
		return out, fmt.Errorf("validate {{ .Name }} JSON: %w", err)
	}
	{{ .DecodeTransform }}
	return out, nil
}

{{ end }}
{{ if .Decode }}
// {{ .Decode }} checks JSON field names from the Goa design and returns a service value.
func {{ .Decode }}(data []byte) (out {{ .ServiceRef }}, err error) {
	var body {{ .TransportRef }}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return out, fmt.Errorf("decode {{ .Name }} JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return out, fmt.Errorf("decode {{ .Name }} JSON: multiple JSON values")
		}
		return out, fmt.Errorf("decode {{ .Name }} JSON after first value: %w", err)
	}
	{{- if .Constructor }}
	return {{ .Constructor }}(body)
	{{- else }}
	if err := {{ .Validator }}(body); err != nil {
		return out, fmt.Errorf("validate {{ .Name }} JSON: %w", err)
	}
	{{ .DecodeTransform }}
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
