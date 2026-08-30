// Package codegen plans the route constants written into Goa service packages.
// Each service owns the names for the toolset routes that it explicitly exports.
package codegen

import (
	"fmt"
	"strings"

	agentir "goa.design/goa-ai/codegen/ir"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
)

type (
	// serviceExportPackagesPlan stores the generated service packages that receive
	// exported toolset route constants.
	serviceExportPackagesPlan struct {
		packages []*serviceExportPackagePlan
		files    []*serviceExportFileData
	}

	// serviceExportPackagePlan stores one service package and its route constants.
	serviceExportPackagePlan struct {
		service *agentir.Service
		pkg     *goacodegen.GeneratedPackage
		exports []*plannedServiceExport
	}

	// plannedServiceExport stores one explicit export and its generated constant.
	plannedServiceExport struct {
		export      *agentir.ToolsetRef
		declaration *goacodegen.NameDeclaration
	}

	// serviceExportFileData contains the final package and constant names rendered
	// into one service-owned file.
	serviceExportFileData struct {
		PackageName string
		Dir         string
		Exports     []*serviceExportData
	}

	// serviceExportData contains one final constant name and runtime route.
	serviceExportData struct {
		ConstName     string
		Name          string
		QualifiedName string
	}

	// serviceExportNameOrder keeps collision results stable when design order changes.
	serviceExportNameOrder struct {
		packagePath string
		route       string
	}
)

// planServiceExportPackages claims each exporting service package and declares
// one route constant for every explicit export.
func planServiceExportPackages(generation *goacodegen.Generation, design *agentir.Design) (*serviceExportPackagesPlan, error) {
	planned := &serviceExportPackagesPlan{}
	for _, service := range design.Services {
		if len(service.Exports) == 0 {
			continue
		}
		pkg, err := generation.ClaimPackage(service.ImportPath)
		if err != nil {
			return nil, fmt.Errorf("plan service %q toolset exports package: %w", service.Name, err)
		}
		packagePlan := &serviceExportPackagePlan{service: service, pkg: pkg}
		for _, export := range service.Exports {
			declaration := goacodegen.NewPreferredName(
				goacodegen.NameConstant,
				goacodegen.Goify(export.Slug, true)+"ToolsetName",
				goacodegen.ExportedName,
				serviceExportNameOrder{packagePath: pkg.ImportPath(), route: export.QualifiedName},
			)
			if err := pkg.DeclareName(declaration); err != nil {
				return nil, fmt.Errorf("plan service %q toolset export %q name: %w", service.Name, export.Name, err)
			}
			packagePlan.exports = append(packagePlan.exports, &plannedServiceExport{
				export:      export,
				declaration: declaration,
			})
		}
		planned.packages = append(planned.packages, packagePlan)
	}
	return planned, nil
}

// link copies the final service package and constant names into render data.
func (p *serviceExportPackagesPlan) link(services *service.ServicesData) error {
	for _, packagePlan := range p.packages {
		serviceData := services.Get(packagePlan.service.Name)
		if serviceData == nil {
			return fmt.Errorf("service %q toolset exports have no linked service data", packagePlan.service.Name)
		}
		file := &serviceExportFileData{
			PackageName: serviceData.PkgName,
			Dir:         packagePlan.pkg.OutputDirectory(),
		}
		for _, export := range packagePlan.exports {
			file.Exports = append(file.Exports, &serviceExportData{
				ConstName:     export.declaration.Name(),
				Name:          export.export.Name,
				QualifiedName: export.export.QualifiedName,
			})
		}
		p.files = append(p.files, file)
	}
	return nil
}

// ComparePackageName orders service export constants by package and route.
func (o serviceExportNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(serviceExportNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.route, right.route)
}
