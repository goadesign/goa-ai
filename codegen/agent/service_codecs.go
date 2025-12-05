package codegen

import (
	"path/filepath"
	"sort"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// serviceCodecTypeKey uniquely identifies a user type located in a specific
// service/types locator package (struct:pkg:path).
type serviceCodecTypeKey struct {
	RelImportPath string
	TypeName      string
}

// serviceCodecTypeSpec captures the information needed to emit JSON helper
// types and transform helpers for a service-level user type used as a tool
// payload or result. Helpers are generated into the locator package derived
// from struct:pkg:path so that Goa's union representation (anonymous
// interfaces with unexported methods) is always referenced from its owning
// package.
type serviceCodecTypeSpec struct {
	// Svc is the Goa service that owns (or uses) the user type.
	Svc *service.Data
	// UType is the Goa user type for which helpers are generated.
	UType goaexpr.UserType
	// Loc is the struct:pkg:path location for UType.
	Loc *codegen.Location

	// Name is the public name of the user type (UType.Name()).
	Name string
	// ValRef is the Go type reference for the value form (for example, Doc).
	ValRef string
	// PtrRef is the Go type reference for the pointer form (for example, *Doc).
	PtrRef string
	// VarName is the lower-camel form of the type name (for error messages).
	VarName string
}

// serviceCodecFilePlan groups all helper specs that share the same locator
// directory (for example "types"). All helpers for a given locator are
// emitted into a single Go file under gen/<rel>/<file>.go.
type serviceCodecFilePlan struct {
	Dir         string
	PackageName string
	Scope       *codegen.NameScope
	Types       []*serviceCodecTypeSpec
}

// prepareServiceCodecPlan populates Name and type reference fields on each
// serviceCodecTypeSpec using Goa's NameScope helpers so that generated code
// references types consistently with the rest of the locator package.
func prepareServiceCodecPlan(plan *serviceCodecFilePlan) {
	if plan == nil {
		return
	}
	for _, spec := range plan.Types {
		if spec == nil || spec.UType == nil {
			continue
		}
		spec.Name = spec.UType.Name()
		if spec.Name == "" {
			continue
		}
		// Service-level helpers live in the same package as the user type, so we
		// can reference the concrete type name directly and use a simple pointer
		// for helper signatures (*TypeName).
		spec.ValRef = spec.Name
		spec.PtrRef = "*" + spec.Name
		spec.VarName = lowerCamel(spec.Name)
	}
}

// gatherServiceCodecImports builds the import list for a service codec file,
// including JSON, fmt, goa (when validations are present), unicode/utf8 when
// needed, and any meta-type imports discovered on user types.
func gatherServiceCodecImports(genpkg string, plan *serviceCodecFilePlan) []*codegen.ImportSpec {
	if plan == nil || len(plan.Types) == 0 {
		return nil
	}
	selfPath := ""
	if len(plan.Types) > 0 {
		if loc := plan.Types[0].Loc; loc != nil && loc.RelImportPath != "" {
			selfPath = joinImportPath(genpkg, loc.RelImportPath)
		}
	}

	base := []*codegen.ImportSpec{
		codegen.SimpleImport("encoding/json"),
		codegen.SimpleImport("fmt"),
	}

	extra := make(map[string]*codegen.ImportSpec)

	for _, spec := range plan.Types {
		if spec == nil || spec.UType == nil {
			continue
		}
		att := spec.UType.Attribute()
		for _, im := range gatherAttributeImports(genpkg, att) {
			if im == nil || im.Path == "" {
				continue
			}
			if im.Path == selfPath {
				continue
			}
			extra[im.Path] = im
		}
	}

	if len(extra) > 0 {
		paths := make([]string, 0, len(extra))
		for p := range extra {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			base = append(base, extra[p])
		}
	}
	return base
}

// serviceToolCodecFiles builds service-level helper files for tool payload and
// result user types that specify a struct:pkg:path locator. Helpers live in
// the same package as the user types so that Goa's GoTransform can safely
// materialize union carrier interfaces and wrapper types.
//
// The returned files are appended to the main files slice in Generate. The
// helpers are intentionally conservative and only target user types with
// explicit struct:pkg:path locators; agent-local anonymous shapes continue to
// use specs-local JSON helpers.
func serviceToolCodecFiles(data *GeneratorData) []*codegen.File {
	if data == nil || len(data.Services) == 0 {
		return nil
	}

	// Collect unique located user types referenced by any tool payload or
	// result across all agents, keyed by locator path and type name.
	seen := make(map[serviceCodecTypeKey]struct{})
	plansByDir := make(map[string]*serviceCodecFilePlan)

	for _, svc := range data.Services {
		for _, ag := range svc.Agents {
			for _, t := range ag.Tools {
				// Only consider payload user types with struct:pkg:path locators.
				if t.Args == nil || t.Args.Type == nil || t.Args.Type == goaexpr.Empty {
					continue
				}
				ut, ok := t.Args.Type.(goaexpr.UserType)
				if !ok || ut == nil {
					continue
				}
				loc := codegen.UserTypeLocation(ut)
				if loc == nil || loc.RelImportPath == "" {
					continue
				}
				// Skip user types whose attribute graph contains unions. Union
				// payloads currently rely on specs-local JSON helpers and
				// bespoke transforms; service-level helpers are emitted only
				// for non-union payloads so they can safely rely on direct
				// JSON encoding plus generated validation.
				if attributeHasUnion(ut.Attribute()) {
					continue
				}
				key := serviceCodecTypeKey{RelImportPath: loc.RelImportPath, TypeName: ut.Name()}
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				addServiceCodecType(planKeyForLocation(loc), plansByDir, svc.Service, ut, loc)
			}
		}
	}

	if len(plansByDir) == 0 {
		return nil
	}

	files := make([]*codegen.File, 0, len(plansByDir))
	for _, plan := range plansByDir {
		if plan == nil || len(plan.Types) == 0 {
			continue
		}

		prepareServiceCodecPlan(plan)

		// Gather imports from all helper specs.
		imports := gatherServiceCodecImports(data.Genpkg, plan)
		header := codegen.Header(
			"Agent tool JSON helpers (service-local)",
			plan.PackageName,
			imports,
		)
		sections := []*codegen.SectionTemplate{
			header,
			{
				Name:    "agent-service-codecs",
				Source:  agentsTemplates.Read(serviceCodecsFileT),
				Data:    plan,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(codegen.Gendir, plan.Dir, "agent_tool_codecs.go")
		files = append(files, &codegen.File{
			Path:             path,
			SectionTemplates: sections,
		})
	}
	return files
}

// planKeyForLocation computes the relative directory under gen/ for the given
// user type location.
func planKeyForLocation(loc *codegen.Location) string {
	if loc == nil || loc.FilePath == "" {
		return ""
	}
	return filepath.Dir(loc.FilePath)
}

// addServiceCodecType attaches a located user type to the appropriate file
// plan, creating the plan when needed.
func addServiceCodecType(
	dir string,
	plans map[string]*serviceCodecFilePlan,
	svc *service.Data,
	ut goaexpr.UserType,
	loc *codegen.Location,
) {
	if dir == "" || svc == nil || ut == nil || loc == nil {
		return
	}
	plan, ok := plans[dir]
	if !ok {
		plan = &serviceCodecFilePlan{
			Dir:         dir,
			PackageName: loc.PackageName(),
			Scope:       codegen.NewNameScope(),
		}
		plans[dir] = plan
	}
	plan.Types = append(plan.Types, &serviceCodecTypeSpec{
		Svc:   svc,
		UType: ut,
		Loc:   loc,
	})
}
