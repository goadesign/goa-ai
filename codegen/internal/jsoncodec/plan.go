// Package jsoncodec adds typed validation and JSON functions to the original Go
// packages already selected by Goa. It neither selects new types nor changes
// service, tool, or completion representations.
package jsoncodec

import (
	"fmt"
	"path/filepath"

	"goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/internal/jsonshape"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
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

// NewPlan adds validation and codecs for supported original declarations chosen
// by Goa. Unsupported complete values do not claim names, imports, or packages.
func NewPlan(generation *codegen.Generation, services *service.Plan) (*Plan, error) {
	plan := &Plan{generation: generation}
	byOwner := make(map[*codegen.GeneratedPackage]*packagePlan)
	for userType, declaration := range generation.UserTypes() {
		attribute, layout, err := services.UserTypeLayout(userType, declaration)
		if err != nil {
			return nil, fmt.Errorf("JSON codec %q original type layout: %w", userType.Name(), err)
		}
		// Embedded fields must have one truthful JSON shape even when another
		// field prevents generation of a complete-value strict codec.
		if err := codegen.Walk(attribute, validateEmbeddedDeclaration); err != nil {
			return nil, fmt.Errorf("JSON codec %q: %w", userType.Name(), err)
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
		if err := value.PlanValidation(); err != nil {
			return nil, err
		}
		pkg.values = append(pkg.values, &valuePlan{layout: layout, codec: value})
	}
	for _, pkg := range plan.packages {
		if err := pkg.codecs.FinalizeOriginals(); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// Files adds codec declarations to existing files in each original package.
// Each recipient file's header supplies the actual package name and receives the
// planned imports; no codec directory or filename is reserved.
func (p *Plan) Files(files []*codegen.File) ([]*codegen.File, error) {
	type fileTarget struct {
		file   *codegen.File
		header *codegen.SectionTemplate
	}
	const maxCodecShards = 16
	owners := make(map[*codegen.GeneratedPackage][]fileTarget)
	for _, file := range files {
		// Earlier plugins may return empty entries; Goa omits them when it
		// assembles the final files.
		if file == nil {
			continue
		}
		owner, ok := p.generation.PackageForFile(file.Path)
		if !ok {
			continue
		}
		for _, section := range file.SectionTemplates {
			if section.Name != sourceHeader {
				continue
			}
			owners[owner] = append(owners[owner], fileTarget{file: file, header: section})
			break
		}
	}
	for _, pkg := range p.packages {
		targets := owners[pkg.owner]
		if len(targets) == 0 {
			return nil, fmt.Errorf("JSON codec owner %q has no generated Go file", pkg.owner.ImportPath())
		}
		if filepath.Base(targets[0].file.Path) == "service.go" {
			targets = targets[:1]
		} else if len(targets) > maxCodecShards {
			targets = targets[:maxCodecShards]
		}
		packageName := targets[0].header.Data.(map[string]any)["Pkg"].(string)
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
		var (
			plannedImports []*codegen.ImportSpec
			codecSections  []*codegen.SectionTemplate
		)
		for _, contribution := range generated {
			for _, section := range contribution.SectionTemplates {
				if section.Name == sourceHeader {
					imports := section.Data.(map[string]any)["Imports"].([]*codegen.ImportSpec)
					plannedImports = append(plannedImports, imports...)
					continue
				}
				codecSections = append(codecSections, section)
			}
		}
		if len(codecSections) == 0 {
			continue
		}
		shardCount := min(len(targets), len(codecSections))
		for i := range shardCount {
			codegen.AddImport(targets[i].header, plannedImports...)
		}
		for i, section := range codecSections {
			target := targets[i%shardCount].file
			target.SectionTemplates = append(target.SectionTemplates, section)
		}
	}
	return files, nil
}

// validateEmbeddedDeclaration checks one authored object before codec eligibility
// is decided, so unsupported siblings cannot hide an invalid embedded field.
func validateEmbeddedDeclaration(attribute *expr.AttributeExpr) error {
	_, err := jsonshape.EmbeddedMap(attribute)
	return err
}
