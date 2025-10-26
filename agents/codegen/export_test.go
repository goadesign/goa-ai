package codegen

import "goa.design/goa/v3/eval"

// BuildDataForTest exposes buildGeneratorData to external tests.
func BuildDataForTest(genpkg string, roots []eval.Root) (*GeneratorData, error) {
	return buildGeneratorData(genpkg, roots)
}
