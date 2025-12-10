package codegen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
)

type (
	// A2AConsumerConfigData holds template-ready data for generating a consumer-side
	// ProviderConfig and caller for a remote A2A provider.
	A2AConsumerConfigData struct {
		// PackageName is the Go package name for the remote_a2a package.
		PackageName string
		// Dir is the filesystem directory for the remote_a2a package.
		Dir string
		// ImportPath is the full Go import path for the remote_a2a package.
		ImportPath string
		// Suite is the canonical A2A suite identifier (for example,
		// "service.agent.toolset").
		Suite string
		// GoName is an exported Go identifier derived from Suite.
		GoName string
		// Skills contains the skill metadata derived from the consumer's toolsets.
		Skills []*A2ASkillData
	}
)

// a2aConsumerFiles generates consumer-side A2A helper packages for all
// FromA2A providers referenced by agents under a single Goa service. Each
// provider suite produces one package under:
//
//	gen/<service>/remote_a2a/<suite_slug>/
//
// The package contains:
//   - config.go: static a2a.ProviderConfig with SkillConfig entries
//   - caller.go: NewCaller / Register / RegisterWithURL helpers
func a2aConsumerFiles(genpkg string, svc *ServiceAgentsData) []*codegen.File {
	if svc == nil || svc.Service == nil {
		return nil
	}

	pkgs := buildA2AConsumerConfigData(genpkg, svc)
	if len(pkgs) == 0 {
		return nil
	}

	files := make([]*codegen.File, 0, len(pkgs)*2) // 2 files per package: config.go and caller.go
	for _, data := range pkgs {
		// config.go (ProviderConfig)
		configSections := []*codegen.SectionTemplate{
			{
				Name:   "a2a-consumer-config",
				Source: agentsTemplates.Read(a2aConsumerConfigFileT),
				Data:   data,
			},
		}
		files = append(files, &codegen.File{
			Path:             filepath.Join(data.Dir, "config.go"),
			SectionTemplates: configSections,
		})

		// caller.go (NewCaller / Register helpers)
		callerSections := []*codegen.SectionTemplate{
			{
				Name:   "a2a-consumer-caller",
				Source: agentsTemplates.Read(a2aConsumerCallerFileT),
				Data:   data,
			},
		}
		files = append(files, &codegen.File{
			Path:             filepath.Join(data.Dir, "caller.go"),
			SectionTemplates: callerSections,
		})
	}

	return files
}

// buildA2AConsumerConfigData groups all FromA2A toolsets under a service into
// per-suite consumer packages. Each suite aggregates skills from all agents and
// toolsets that reference it.
func buildA2AConsumerConfigData(genpkg string, svc *ServiceAgentsData) []*A2AConsumerConfigData {
	bySuite := make(map[string]*A2AConsumerConfigData)

	for _, agent := range svc.Agents {
		for _, ts := range agent.UsedToolsets {
			if ts == nil || ts.Expr == nil || ts.Expr.Provider == nil {
				continue
			}
			if ts.Expr.Provider.Kind != agentsExpr.ProviderA2A {
				continue
			}
			suite := ts.Expr.Provider.A2ASuite
			if suite == "" {
				panic(fmt.Errorf("goa-ai: FromA2A provider for toolset %q in service %q must specify non-empty suite", ts.Name, svc.Service.Name))
			}
			data, ok := bySuite[suite]
			if !ok {
				pkgName := a2aConsumerPackageName(suite)
				dir := filepath.Join("gen", svc.Service.PathName, "remote_a2a", pkgName)
				importPath := joinImportPath(genpkg, filepath.Join(svc.Service.PathName, "remote_a2a", pkgName))
				data = &A2AConsumerConfigData{
					PackageName: pkgName,
					Dir:         dir,
					ImportPath:  importPath,
					Suite:       suite,
					GoName:      codegen.Goify(suite, true),
					Skills:      make([]*A2ASkillData, 0),
				}
				bySuite[suite] = data
			}

			// Reuse the A2A adapter generator's skill builder for consistent
			// schema and example generation. We construct a shallow copy of the
			// agent that treats this UsedToolset as an exported toolset.
			tmpAgent := *agent
			tmpAgent.ExportedToolsets = []*ToolsetData{ts}
			tmpAgent.UsedToolsets = nil

			gen := newA2AAdapterGenerator(genpkg, &tmpAgent)
			skills := gen.buildSkillDataFromToolsets(tmpAgent.ExportedToolsets)

			for _, sk := range skills {
				if !hasA2ASkill(data.Skills, sk.ID) {
					data.Skills = append(data.Skills, sk)
				}
			}
		}
	}

	if len(bySuite) == 0 {
		return nil
	}

	// Deterministic ordering by suite and skill ID.
	suites := make([]string, 0, len(bySuite))
	for s := range bySuite {
		suites = append(suites, s)
	}
	sort.Strings(suites)

	out := make([]*A2AConsumerConfigData, 0, len(suites))
	for _, suite := range suites {
		data := bySuite[suite]
		sort.Slice(data.Skills, func(i, j int) bool {
			return data.Skills[i].ID < data.Skills[j].ID
		})
		out = append(out, data)
	}
	return out
}

// a2aConsumerPackageName derives a filesystem-safe package name from an A2A
// suite identifier. Dots are replaced with underscores and the result is
// lower-cased.
func a2aConsumerPackageName(suite string) string {
	if suite == "" {
		return "remote_a2a"
	}
	slug := strings.ReplaceAll(suite, ".", "_")
	return strings.ToLower(slug)
}

// hasA2ASkill reports whether skills already contains a skill with the given ID.
func hasA2ASkill(skills []*A2ASkillData, id string) bool {
	for _, sk := range skills {
		if sk != nil && sk.ID == id {
			return true
		}
	}
	return false
}
