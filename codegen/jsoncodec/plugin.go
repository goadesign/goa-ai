// Package jsoncodec registers strict standalone JSON codecs for named Goa
// values selected with Meta("goa-ai:json:codec"). Import this package for its
// generator registration. Selection never creates a service, tool, or type.
package jsoncodec

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/generator"
	"goa.design/goa/v3/expr"
)

type (
	// plugin owns only the packages added by one generator invocation.
	plugin struct {
		packages []*packagePlan
	}

	// packagePlan keeps the public value API separate from private transport types.
	packagePlan struct {
		public         *codegen.GeneratedPackage
		implementation *codegen.GeneratedPackage
		codecs         *codec.Plan
		values         []*valuePlan
	}

	// valuePlan retains the original type layout and canonical name declarations.
	valuePlan struct {
		attribute *expr.AttributeExpr
		layout    *codegen.GoTypePlan
		codec     *codec.Value
		encode    *codegen.NameDeclaration
		decode    *codegen.NameDeclaration
	}

	// nameOrder gives generated functions a stable order within their package.
	nameOrder string

	// publicValue is the linked data needed to emit the two typed functions.
	publicValue struct {
		Ref, Encode, Decode, Implementation, PrivateEncode, PrivateDecode string
	}
)

func init() {
	generator.RegisterPlugin("goa-ai-json-codec", "gen", newPlugin)
}

// newPlugin gives every invocation its own plan and no global selection state.
func newPlugin() generator.Plugin {
	p := new(plugin)
	return generator.Plugin{
		Plan: func(plan *generator.Plan) error { return p.plan(plan.Generation()) },
		Generate: func(_ *generator.Plan, files []*codegen.File) ([]*codegen.File, error) {
			return p.generate(files)
		},
	}
}

// ComparePackageName keeps public function ordering independent of traversal.
func (n nameOrder) ComparePackageName(other codegen.PackageNameOrder) int {
	return strings.Compare(string(n), string(other.(nameOrder)))
}

// plan selects only original declarations already owned by Goa's service plan.
func (p *plugin) plan(generation *codegen.Generation) error {
	selected, err := selections(generation.Roots())
	if err != nil {
		return err
	}
	sort.Slice(selected, func(i, j int) bool {
		left, right := codegen.UserTypeLocation(selected[i]), codegen.UserTypeLocation(selected[j])
		if left.RelImportPath != right.RelImportPath {
			return left.RelImportPath < right.RelImportPath
		}
		return selected[i].Name() < selected[j].Name()
	})
	byOwner := make(map[string]*packagePlan)
	for _, userType := range selected {
		location := codegen.UserTypeLocation(userType)
		ownerPath := path.Join(generation.GenPkg(), location.RelImportPath)
		// PackageForFile performs a non-mutating lookup of the located directory.
		owner, exists := generation.PackageForFile(path.Join(codegen.Gendir, location.RelImportPath, "codec.go"))
		if !exists || owner.ImportPath() != ownerPath {
			return fmt.Errorf("JSON codec %q: located package %q is not generated", userType.Name(), ownerPath)
		}
		declaration, err := owner.UserType(userType)
		if err != nil {
			return fmt.Errorf("JSON codec %q requires an already generated original type: %w", userType.Name(), err)
		}
		pkg := byOwner[ownerPath]
		if pkg == nil {
			pkg, err = planPackage(generation, owner)
			if err != nil {
				return err
			}
			byOwner[ownerPath] = pkg
			p.packages = append(p.packages, pkg)
		}
		attribute := &expr.AttributeExpr{Type: userType}
		layout, importNames, err := planOriginalValue(generation, ownerPath, attribute)
		if err != nil {
			return fmt.Errorf("JSON codec %q original type layout: %w", userType.Name(), err)
		}
		for _, target := range []*codegen.GeneratedPackage{pkg.public, pkg.implementation} {
			for _, imp := range layout.CompleteImportPreferences() {
				if err := target.ReserveGeneratedImport(codegen.NewImport(importNames[imp.Path], imp.Path)); err != nil {
					return err
				}
			}
		}
		value, err := pkg.codecs.AddStandalone(userType.Name(), userType.Name(), attribute, layout)
		if err != nil {
			return err
		}
		encode, err := pkg.public.DeclareDependentName(codegen.NameFunction, declaration.Declaration(), "Encode", "", nameOrder(userType.Name()+":encode"))
		if err != nil {
			return err
		}
		decode, err := pkg.public.DeclareDependentName(codegen.NameFunction, declaration.Declaration(), "Decode", "", nameOrder(userType.Name()+":decode"))
		if err != nil {
			return err
		}
		pkg.values = append(pkg.values, &valuePlan{attribute: attribute, layout: layout, codec: value, encode: encode, decode: decode})
	}
	return nil
}

// planPackage claims two new directories; it never appends to another producer's package.
func planPackage(generation *codegen.Generation, owner *codegen.GeneratedPackage) (*packagePlan, error) {
	publicPath := path.Join(owner.ImportPath(), "jsoncodec")
	privatePath := path.Join(publicPath, "internal", "codec")
	for _, directory := range []string{path.Join(owner.OutputDirectory(), "jsoncodec"), path.Join(owner.OutputDirectory(), "jsoncodec", "internal", "codec")} {
		if existing, ok := generation.PackageForFile(path.Join(directory, "codec.go")); ok {
			return nil, fmt.Errorf("JSON codec output directory %q is already owned by %q", directory, existing.ImportPath())
		}
	}
	public, err := generation.ClaimPackage(publicPath)
	if err != nil {
		return nil, err
	}
	codecs, err := codec.NewPlan(generation, privatePath, "codec", owner.ImportPath())
	if err != nil {
		return nil, err
	}
	implementation := generation.Package(privatePath)
	if err := public.ReserveGeneratedImport(codegen.NewImport("codec", privatePath)); err != nil {
		return nil, err
	}
	return &packagePlan{public: public, implementation: implementation, codecs: codecs}, nil
}

// planOriginalValue resolves original declarations through Goa's generated
// package catalog and retains their complete definitions for validation.
func planOriginalValue(generation *codegen.Generation, owner string, attribute *expr.AttributeExpr) (*codegen.GoTypePlan, map[string]string, error) {
	importNames := make(map[string]string)
	binder := func(request codegen.GoTypeBindingRequest) (codegen.GoTypeBinding, error) {
		owner := request.InheritedOwner
		if location := codegen.UserTypeLocation(request.Attribute.Type); location != nil {
			owner = path.Join(generation.GenPkg(), location.RelImportPath)
			importNames[owner] = location.PackageName()
		}
		pkg := generation.Package(owner)
		binding := codegen.GoTypeBinding{Owner: owner}
		var err error
		switch request.Kind {
		case codegen.GoNamed:
			binding.Type, err = pkg.Type(request.Attribute.Type.(expr.UserType))
		case codegen.GoUnion:
			binding.Union, err = pkg.Union(request.Attribute)
		case codegen.GoPrimitive, codegen.GoArray, codegen.GoMap, codegen.GoStruct, codegen.GoEmpty, codegen.GoServiceError:
			return binding, fmt.Errorf("unexpected original type binding %s", request.Kind)
		}
		return binding, err
	}
	layout, err := codegen.PlanGoType(attribute, codegen.GoTypePlanOptions{
		Owner: owner, Policy: codegen.GoLayoutPolicy{UseDefault: true, SumType: true}, RetainNamedValue: true, Bind: binder,
	})
	return layout, importNames, err
}

// generate links original references after freeze and adds only selected files.
func (p *plugin) generate(files []*codegen.File) ([]*codegen.File, error) {
	for _, pkg := range p.packages {
		var data []*publicValue
		imports := []*codegen.ImportSpec{pkg.public.Import(pkg.implementation.ImportPath())}
		seenImports := map[string]bool{pkg.implementation.ImportPath(): true}
		for _, value := range pkg.values {
			for _, imp := range value.layout.ImportPreferences() {
				if !seenImports[imp.Path] {
					imports = append(imports, pkg.public.Import(imp.Path))
					seenImports[imp.Path] = true
				}
			}
			privateLayout := value.layout.Link(pkg.implementation.ImportPath(), pkg.implementation.ImportName)
			context := &codegen.AttributeContext{UseDefault: true, Scope: codegen.NewAttributeScope(pkg.implementation.Scope())}
			context, err := context.WithGoTypeLayout(privateLayout)
			if err != nil {
				return nil, err
			}
			if err := value.codec.BindService(context.Scope); err != nil {
				return nil, err
			}
			data = append(data, &publicValue{
				Ref:    value.layout.Link(pkg.public.ImportPath(), pkg.public.ImportName).Ref(),
				Encode: value.encode.Name(), Decode: value.decode.Name(),
				Implementation: pkg.public.ImportName(pkg.implementation.ImportPath()),
				PrivateEncode:  value.codec.EncodeDeclaration().Name(), PrivateDecode: value.codec.DecodeDeclaration().Name(),
			})
		}
		implementation, err := pkg.codecs.Files()
		if err != nil {
			return nil, err
		}
		files = append(files, &codegen.File{
			Path: filepath.Join(pkg.public.OutputDirectory(), "codec.go"),
			SectionTemplates: []*codegen.SectionTemplate{
				codegen.Header("Strict JSON codecs for selected Goa values.", "jsoncodec", imports),
				{Name: "value-codecs", Source: publicSource, Data: data},
			},
		})
		files = append(files, implementation...)
	}
	return files, nil
}

const publicSource = `
{{ range . }}
// {{ .Encode }} validates and encodes a value using its Goa JSON contract.
// Errors return no encoded value.
func {{ .Encode }}(value {{ .Ref }}) ([]byte, error) {
	return {{ .Implementation }}.{{ .PrivateEncode }}(value)
}

// {{ .Decode }} strictly decodes one complete JSON value. Errors return no
// partially decoded value.
func {{ .Decode }}(data []byte) ({{ .Ref }}, error) {
	return {{ .Implementation }}.{{ .PrivateDecode }}(data)
}
{{ end }}
`
