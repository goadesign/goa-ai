package codegen

import (
	"strings"

	"goa.design/goa/v3/eval"
)

// BuildDataForTest exposes buildGeneratorData to external tests.
func BuildDataForTest(genpkg string, roots []eval.Root) (*GeneratorData, error) {
	return buildGeneratorData(genpkg, roots)
}

// BuildToolSpecsDataForTest exposes buildToolSpecsData to external tests.
func BuildToolSpecsDataForTest(agent *AgentData) (*toolSpecsData, error) {
	return buildToolSpecsData(agent)
}

// CollectTypeInfoForTest returns a map of type name to definition for all
// types captured in the tool specs data (in declaration order).
func CollectTypeInfoForTest(specs *toolSpecsData) map[string]string {
	out := make(map[string]string)
	if specs == nil {
		return out
	}
	for _, td := range specs.typesList() {
		out[td.TypeName] = td.Def
	}
	return out
}

// CollectTypeImportAliasesForTest returns the distinct import aliases used by
// the given type (matched by substring on type name). It includes both the
// direct Import (if any) and TypeImports collected during analysis.
func CollectTypeImportAliasesForTest(specs *toolSpecsData, nameContains string) []string {
	seen := make(map[string]struct{})
	// Preallocate a small capacity; typical import sets are small.
	out := make([]string, 0, 8)
	if specs == nil {
		return out
	}
	for _, td := range specs.typesList() {
		if !strings.Contains(td.TypeName, nameContains) {
			continue
		}
		if td.Import != nil && td.Import.Name != "" {
			if _, ok := seen[td.Import.Name]; !ok {
				seen[td.Import.Name] = struct{}{}
				out = append(out, td.Import.Name)
			}
		}
		for _, im := range td.TypeImports {
			if im == nil || im.Name == "" {
				continue
			}
			if _, ok := seen[im.Name]; !ok {
				seen[im.Name] = struct{}{}
				out = append(out, im.Name)
			}
		}
	}
	return out
}

// CollectAllImportAliasesForTest returns all import aliases used across the
// tool specs files (types/specs/codecs). This leverages the aggregate
// computation used by templates to render imports.
func CollectAllImportAliasesForTest(specs *toolSpecsData) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)
	if specs == nil {
		return out
	}
	for _, im := range specs.typeImports() {
		if im == nil || im.Name == "" {
			continue
		}
		if _, ok := seen[im.Name]; ok {
			continue
		}
		seen[im.Name] = struct{}{}
		out = append(out, im.Name)
	}
	return out
}
