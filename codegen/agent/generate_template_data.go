package codegen

import (
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
)

type (
	toolSpecFileData struct {
		PackageName string
		Tools       []*toolEntry
		Types       []*typeData
	}

	toolTypesFileData struct {
		Types []*typeData
	}

	toolUnionTypesFileData struct {
		Unions []*service.UnionTypeData
	}

	toolTransportTypesFileData struct {
		Types []*typeData
	}

	toolCodecsFileData struct {
		Types []*typeData
		Tools []*toolEntry
		// Helpers contains a file-level, de-duplicated list of helper transform
		// functions referenced by codec-local conversions (transport <-> public).
		Helpers []*codegen.TransformFunctionData
	}

	toolSpecsAggregateData struct {
		Toolsets []*ToolsetData
	}

	agentToolsetFileData struct {
		PackageName string
		Toolset     *ToolsetData
		Tools       []*toolEntry
	}

	agentToolsetConsumerFileData struct {
		Agent         *AgentData
		Toolset       *ToolsetData
		ProviderAlias string
	}

	serviceToolsetFileData struct {
		PackageName     string
		Agent           *AgentData
		Toolset         *ToolsetData
		ServicePkgAlias string
	}

	// transforms metadata used by tool_transforms.go.tpl
	transformFuncData struct {
		Name          string
		ParamTypeRef  string
		ResultTypeRef string
		// NilInputReturnsNil indicates whether the generated transform must treat
		// nil input as a valid empty value and return nil without attempting field
		// conversion.
		NilInputReturnsNil bool
		Body               string
		Helpers            []*codegen.TransformFunctionData
	}

	transformsFileData struct {
		HeaderComment string
		PackageName   string
		Imports       []*codegen.ImportSpec
		Functions     []transformFuncData
		// Helpers contains a file-level, de-duplicated list of helper transform
		// functions referenced by any of the Functions bodies. Rendering helpers at
		// the file scope avoids duplicate helper definitions when multiple
		// transforms share the same nested conversions.
		Helpers []*codegen.TransformFunctionData
	}
)
