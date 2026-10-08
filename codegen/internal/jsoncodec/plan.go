// Package jsoncodec adds complete-value JSON functions to the original Go
// packages already selected by Goa. It neither selects new types nor changes
// service, tool, or completion representations.
package jsoncodec

import (
	"fmt"

	"goa.design/goa-ai/internal/codegen/codec"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
)

type (
	// Plan retains supported original values until Goa finishes choosing names.
	Plan struct {
		generation *codegen.Generation
		packages   []*packagePlan
	}

	// packagePlan shares private JSON declarations within an existing Go package.
	packagePlan struct {
		owner  *codegen.GeneratedPackage
		codecs *codec.Plan
		values []*valuePlan
	}

	// valuePlan binds one codec to the complete layout of its original type.
	valuePlan struct {
		layout *codegen.GoTypePlan
		codec  *codec.Value
	}
)

const sourceHeader = "source-header"

// NewPlan adds codecs only for supported original declarations already chosen
// by Goa. Unsupported complete values do not claim names, imports, or packages.
func NewPlan(generation *codegen.Generation, services *service.Plan) (*Plan, error) {
	plan := &Plan{generation: generation}
	byOwner := make(map[*codegen.GeneratedPackage]*packagePlan)
	for userType, declaration := range generation.UserTypes() {
		attribute, layout, err := services.UserTypeLayout(userType, declaration)
		if err != nil {
			return nil, fmt.Errorf("JSON codec %q original type layout: %w", userType.Name(), err)
		}
		if !codec.SupportsStandalone(attribute) {
			continue
		}
		owner := generation.Package(declaration.PackagePath())
		pkg := byOwner[owner]
		if pkg == nil {
			codecs, err := codec.NewPlan(generation, owner.ImportPath())
			if err != nil {
				return nil, err
			}
			pkg = &packagePlan{owner: owner, codecs: codecs}
			byOwner[owner] = pkg
			plan.packages = append(plan.packages, pkg)
		}
		value, err := pkg.codecs.AddStandalone(userType.Name(), userType.Name(), attribute, layout)
		if err != nil {
			return nil, err
		}
		pkg.values = append(pkg.values, &valuePlan{layout: layout, codec: value})
	}
	return plan, nil
}

// Files adds codec declarations to an existing file in each original package.
// Its header supplies the actual package name and receives the planned imports;
// no codec directory or filename is reserved.
func (p *Plan) Files(files []*codegen.File) ([]*codegen.File, error) {
	owners := make(map[*codegen.GeneratedPackage]*codegen.File)
	headers := make(map[*codegen.GeneratedPackage]*codegen.SectionTemplate)
	for _, file := range files {
		// Earlier plugins may return empty entries; Goa omits them when it
		// assembles the final files.
		if file == nil {
			continue
		}
		owner, ok := p.generation.PackageForFile(file.Path)
		if !ok || owners[owner] != nil {
			continue
		}
		for _, section := range file.SectionTemplates {
			if section.Name != sourceHeader {
				continue
			}
			owners[owner], headers[owner] = file, section
			break
		}
	}
	for _, pkg := range p.packages {
		file, exists := owners[pkg.owner]
		if !exists {
			return nil, fmt.Errorf("JSON codec owner %q has no generated Go file", pkg.owner.ImportPath())
		}
		header := headers[pkg.owner]
		packageName := header.Data.(map[string]any)["Pkg"].(string)
		for _, value := range pkg.values {
			layout := value.layout.Link(pkg.owner.ImportPath(), pkg.owner.ImportName)
			context := &codegen.AttributeContext{
				UseDefault: true,
				Scope:      codegen.NewAttributeScope(pkg.owner.Scope()),
			}
			context, err := context.WithGoTypeLayout(layout)
			if err != nil {
				return nil, err
			}
			if err := value.codec.BindService(context.Scope); err != nil {
				return nil, err
			}
		}
		generated, err := pkg.codecs.Files(packageName)
		if err != nil {
			return nil, err
		}
		for _, contribution := range generated {
			for _, section := range contribution.SectionTemplates {
				if section.Name == sourceHeader {
					imports := section.Data.(map[string]any)["Imports"].([]*codegen.ImportSpec)
					codegen.AddImport(header, imports...)
					continue
				}
				file.SectionTemplates = append(file.SectionTemplates, section)
			}
		}
	}
	return files, nil
}
