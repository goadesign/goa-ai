// Package codegen plans the imports used by generated example main programs.
//
// The goa example command writes one cmd/<service>/main.go file for each
// service that owns agents. This file reserves every import before generation
// chooses Go names, so an agent named runtime or model still receives a valid
// import name in the example program.
package codegen

import (
	"fmt"
	"path/filepath"

	agentir "goa.design/goa-ai/codegen/ir"
	goacodegen "goa.design/goa/v3/codegen"
)

type (
	// exampleMainPackagesPlan stores one planned main package per service path.
	exampleMainPackagesPlan struct {
		byService map[string]*exampleMainPackagePlan
	}

	// exampleMainPackagePlan stores the imports written into one service's
	// example main program.
	exampleMainPackagePlan struct {
		pkg             *goacodegen.GeneratedPackage
		bootstrapPath   string
		completionsPath string
		agentPaths      map[string]string
		hasCompletions  bool
	}
)

const (
	exampleMainModelImportPath        = "goa.design/goa-ai/runtime/agent/model"
	exampleMainRawJSONImportPath      = "goa.design/goa-ai/runtime/agent/rawjson"
	exampleMainStorageInmemImportPath = "goa.design/goa-ai/runtime/agent/storage/inmem"
)

// planExampleMainPackages records every import used by generated example main
// programs before generation chooses package names.
func planExampleMainPackages(generation *goacodegen.Generation, design *agentir.Design) (*exampleMainPackagesPlan, error) {
	planned := &exampleMainPackagesPlan{byService: make(map[string]*exampleMainPackagePlan)}
	moduleBase := moduleBaseImport(design.Genpkg)
	for _, service := range design.Services {
		if len(service.Agents) == 0 {
			continue
		}
		mainPath := filepath.Join("cmd", service.PathName, "main.go")
		pkg, ok := generation.PackageForFile(mainPath)
		if !ok {
			var err error
			pkg, err = generation.ClaimOutputPackage(
				filepath.ToSlash(filepath.Join(moduleBase, "cmd", service.PathName)),
				filepath.ToSlash(filepath.Dir(mainPath)),
			)
			if err != nil {
				return nil, fmt.Errorf("plan service %q example main package: %w", service.Name, err)
			}
		}
		packagePlan := &exampleMainPackagePlan{
			pkg:           pkg,
			bootstrapPath: filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", service.PathName, "bootstrap")),
			agentPaths:    make(map[string]string, len(service.Agents)),
		}
		if err := packagePlan.declare(service, moduleBase); err != nil {
			return nil, fmt.Errorf("plan service %q example main imports: %w", service.Name, err)
		}
		planned.byService[service.PathName] = packagePlan
	}
	return planned, nil
}

// declare records every import used by one example main. The package chooses
// a different name whenever Goa's server example or an agent already uses the
// requested name.
func (p *exampleMainPackagePlan) declare(service *agentir.Service, moduleBase string) error {
	imports := []*goacodegen.ImportSpec{
		goacodegen.NewImport("context", "context"),
		goacodegen.NewImport("fmt", "fmt"),
		goacodegen.NewImport("log", "log"),
		goacodegen.NewImport("time", "time"),
		goacodegen.NewImport("bootstrap", p.bootstrapPath),
		goacodegen.NewImport("model", exampleMainModelImportPath),
		goacodegen.NewImport("runtime", bootstrapAgentRuntimeImportPath),
		goacodegen.NewImport("storageinmem", exampleMainStorageInmemImportPath),
	}
	for _, spec := range imports {
		if err := p.pkg.ReserveGeneratedImport(spec); err != nil {
			return err
		}
	}

	for _, completion := range service.Completions {
		if authoredExampleForAttribute(completion.Expr.Return) == nil {
			continue
		}
		p.hasCompletions = true
		break
	}
	if p.hasCompletions {
		p.completionsPath = filepath.ToSlash(filepath.Join(moduleBase, "gen", service.PathName, "completions"))
		for _, spec := range []*goacodegen.ImportSpec{
			goacodegen.NewImport("io", "io"),
			goacodegen.NewImport("completions", p.completionsPath),
			goacodegen.NewImport("rawjson", exampleMainRawJSONImportPath),
		} {
			if err := p.pkg.ReserveGeneratedImport(spec); err != nil {
				return err
			}
		}
	}

	for _, agent := range service.Agents {
		if err := p.pkg.ReserveGeneratedImport(goacodegen.NewImport(agent.PackageName, agent.ImportPath)); err != nil {
			return err
		}
		p.agentPaths[agent.ID] = agent.ImportPath
	}
	return nil
}
