// Package codegen turns Goa and Goa-AI designs into generated agent code.
//
// This file reserves every declaration name written by generated tool and
// completion packages before the templates render those declarations.
package codegen

import (
	"goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// declareToolPackageNames records names shared by all files in one tool package.
func (p *toolSpecsPackagePlan) declareToolPackageNames() error {
	return declareExactNames(p.public, p.publicFixed, map[goacodegen.PackageNameKind][]string{
		goacodegen.NameVariable: {"metadata", "names"},
		goacodegen.NameFunction: {
			"Specs", "Names", "Spec", "PayloadSchema", "ResultSchema", "Metadata", "MetadataByName",
			"RequiredLabels", "PayloadCodec", "ResultCodec", "cloneStringMap", "newValidationError",
			"generatedJSONChildPath", "dottedJSONPathPointer", "escapeJSONPointerToken", "decodedJSONType",
			"generatedUnmarshalJSONType", "SchemaFingerprint", "RegistrationToken",
			"invalidGeneratedFieldTypeError", "unknownJSONFieldError",
		},
	})
}

// declareCompletionPackageNames records names shared by all files in one
// completion package.
func (p *toolSpecsPackagePlan) declareCompletionPackageNames() error {
	return declareExactNames(p.public, p.publicFixed, map[goacodegen.PackageNameKind][]string{
		goacodegen.NameFunction: {
			"newValidationError", "generatedJSONChildPath", "dottedJSONPathPointer",
			"escapeJSONPointerToken", "decodedJSONType", "generatedUnmarshalJSONType",
			"invalidGeneratedFieldTypeError",
			"unknownJSONFieldError",
		},
	})
}

// declareExactNames records names that are written the same way in every file.
func declareExactNames(pkg *goacodegen.GeneratedPackage, records map[string]*goacodegen.NameDeclaration, names map[goacodegen.PackageNameKind][]string) error {
	for kind, values := range names {
		for _, name := range values {
			if records[name] != nil {
				continue
			}
			declaration := goacodegen.NewExactName(kind, name)
			if err := pkg.DeclareName(declaration); err != nil {
				return err
			}
			records[name] = declaration
		}
	}
	return nil
}

// declareToolNames records every constant, variable, and function written for
// one tool.
func (p *toolSpecsPackagePlan) declareToolNames(toolset string, tool *agent.ToolExpr) error {
	qualified := toolset + "." + tool.Name
	constant := goacodegen.NewPreferredName(
		goacodegen.NameConstant,
		tool.Name,
		goacodegen.ExportedName,
		specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":constant"},
	)
	if err := p.public.DeclareName(constant); err != nil {
		return err
	}
	names := &plannedToolNames{
		constant:                 constant,
		serverDataTransforms:     make(map[string]*goacodegen.NameDeclaration),
		serverDataTypes:          make(map[string]*plannedSpecType),
		serverDataTransformPlans: make(map[string]*goacodegen.TransformPlan),
		injectedFieldLayouts:     make(map[string]*goacodegen.GoTypePlan),
	}
	var err error
	names.constructor, err = p.public.DeclareDependentName(
		goacodegen.NameFunction,
		constant,
		"newSpec",
		"",
		specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":constructor"},
	)
	if err != nil {
		return err
	}
	names.spec, err = p.public.DeclareDependentName(
		goacodegen.NameFunction,
		constant,
		"Spec",
		"",
		specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":spec"},
	)
	if err != nil {
		return err
	}
	result := tool.Return
	if (result == nil || result.Type == nil || result.Type == goaexpr.Empty) && tool.Method != nil {
		result = tool.Method.Result
	}
	names.typed, err = p.public.DeclareDependentName(
		goacodegen.NameVariable,
		constant,
		"",
		"Tool",
		specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":typed"},
	)
	if err != nil {
		return err
	}
	if len(tool.InjectedFields) > 0 {
		names.inject, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"Inject",
			"",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":inject"},
		)
		if err != nil {
			return err
		}
		names.decode, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"Decode",
			"",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":decode"},
		)
		if err != nil {
			return err
		}
	}
	if len(tool.ServerData) > 0 {
		names.canonicalizeServerData, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"canonicalize",
			"ServerData",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":server-data"},
		)
		if err != nil {
			return err
		}
		names.canonicalizeServerDataItem, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"canonicalize",
			"ServerDataItem",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":server-data-item"},
		)
		if err != nil {
			return err
		}
	}
	if tool.Method != nil {
		names.methodPayloadTransform, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"Init",
			"MethodPayload",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":method-payload"},
		)
		if err != nil {
			return err
		}
		if result != nil && result.Type != nil && result.Type != goaexpr.Empty {
			names.toolResultTransform, err = p.public.DeclareDependentName(
				goacodegen.NameFunction,
				constant,
				"Init",
				"ToolResult",
				specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":tool-result"},
			)
			if err != nil {
				return err
			}
		}
		for _, serverData := range tool.ServerData {
			if serverData.Source == nil || serverData.Source.MethodResultField == "" {
				continue
			}
			declaration, err := p.public.DeclareDependentName(
				goacodegen.NameFunction,
				constant,
				"Init",
				goacodegen.Goify(serverData.Kind, true)+"ServerData",
				specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":server-data-transform:" + serverData.Kind},
			)
			if err != nil {
				return err
			}
			names.serverDataTransforms[serverData.Kind] = declaration
		}
	}
	if tool.Method != nil && tool.Bounds != nil {
		names.bounds, err = p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			"init",
			"Bounds",
			specNameOrder{packagePath: p.public.ImportPath(), key: qualified + ":bounds"},
		)
		if err != nil {
			return err
		}
	}
	p.tools[tool.Name] = names
	return nil
}

// declareCompletionNames records every name written for one completion.
func (p *toolSpecsPackagePlan) declareCompletionNames(name string) error {
	constant := goacodegen.NewPreferredName(
		goacodegen.NameConstant,
		name,
		goacodegen.ExportedName,
		specNameOrder{packagePath: p.public.ImportPath(), key: "completion:" + name + ":constant"},
	)
	if err := p.public.DeclareName(constant); err != nil {
		return err
	}
	names := &plannedCompletionNames{constant: constant}
	declare := func(prefix, suffix, role string) (*goacodegen.NameDeclaration, error) {
		return p.public.DeclareDependentName(
			goacodegen.NameFunction,
			constant,
			prefix,
			suffix,
			specNameOrder{packagePath: p.public.ImportPath(), key: "completion:" + name + ":" + role},
		)
	}
	var err error
	if names.spec, err = declare("spec", "", "spec"); err != nil {
		return err
	}
	if names.example, err = declare("", "Example", "example"); err != nil {
		return err
	}
	if names.complete, err = declare("Complete", "", "complete"); err != nil {
		return err
	}
	if names.streamComplete, err = declare("StreamComplete", "", "stream-complete"); err != nil {
		return err
	}
	p.completionNames[name] = names
	return nil
}
