package codegen

import (
	"goa.design/goa/v3/codegen"
)

type (
	toolSpecFileData struct {
		PackageName string
		Tools       []*toolEntry
		Types       []*typeData
		// RequiredLabels lists, sorted and deduplicated, the run label keys
		// this toolset's label-backed Inject() fields require.
		RequiredLabels []string
	}

	toolTypesFileData struct {
		Types []*typeData
	}

	toolUnionTypesFileData struct {
		Unions []*unionTypeData
	}

	toolTransportTypesFileData struct {
		Types []*typeData
	}

	toolCodecsFileData struct {
		Types          []*typeData
		Tools          []*toolEntry
		JSONValidators []*jsonValidatorData
		// EmitToolLookups controls whether the tool-specific codec lookup helpers
		// are rendered in this package.
		EmitToolLookups bool
		// Helpers contains a file-level, de-duplicated list of helper transform
		// functions referenced by codec-local conversions (transport <-> public).
		Helpers []*codegen.TransformFunctionData
	}

	completionSpecFileData struct {
		PackageName string
		Completions []*completionEntry
		Types       []*typeData
	}

	toolSpecsAggregateData struct {
		SpecsFunc          string
		NamesFunc          string
		RequiredLabelsFunc string
		SpecFunc           string
		MetadataFunc       string
		MetadataByNameFunc string
		PolicyPackageName  string
		ToolsPackageName   string
		Toolsets           []*aggregateToolsetRenderData
		// RequiredLabels lists, sorted and deduplicated, the union of every
		// aggregated toolset's RequiredLabels. Runtime.Start/OneShotRun
		// validates a run's supplied labels against this set before scheduling
		// any workflow or activity for the agent.
		RequiredLabels []string
	}

	// aggregateSpecsFileData contains the final names and imports used to write
	// one agent's aggregate specifications file.
	aggregateSpecsFileData struct {
		Path        string
		Description string
		PackageName string
		Imports     []*codegen.ImportSpec
		Template    toolSpecsAggregateData
	}

	aggregateToolsetRenderData struct {
		SpecsPackageName string
		AgentID          string
		Tools            []*toolRenderData
	}

	agentToolsetFileData struct {
		PackageName             string
		Imports                 []*codegen.ImportSpec
		Toolset                 *ToolsetData
		RuntimeAlias            string
		AgentAlias              string
		ToolsAlias              string
		PlannerAlias            string
		SpecsAlias              string
		HintsAlias              string
		ToolsetName             string
		ServiceName             string
		AgentIDName             string
		SpecsFunc               string
		HintsInstaller          string
		ProviderConstructor     string
		RegistrationConstructor string
		Tools                   []*agentToolRenderData
	}

	// agentToolRenderData contains the names written for one exported agent tool.
	agentToolRenderData struct {
		*toolEntry
		ConstName    string
		PayloadAlias string
		ResultAlias  string
		CallFunc     string
	}

	agentToolsetConsumerFileData struct {
		Toolset                         *ToolsetData
		Imports                         []*codegen.ImportSpec
		RuntimeAlias                    string
		ProviderAlias                   string
		ProviderRegistrationConstructor string
	}

	// usedToolsetFileData contains the saved tool names written into a local
	// method-backed helper package.
	usedToolsetFileData struct {
		PackageName string
		SpecsAlias  string
		Toolset     *ToolsetData
		Tools       []*usedToolRenderData
		Imports     []*codegen.ImportSpec
	}

	// usedToolRenderData contains the names written for one local method-backed
	// tool helper.
	usedToolRenderData struct {
		*toolEntry
		ConstName    string
		PayloadAlias string
		ResultAlias  string
		CallFunc     string
	}

	serviceToolsetFileData struct {
		PackageName      string
		Toolset          *ToolsetData
		Tools            []*serviceExecutorToolData
		ServiceClientRef string
		Constructor      string
		Names            serviceExecutorNames
	}

	// serviceExecutorData stores the final imports, aliases, and tool type
	// references used by one generated service executor package.
	serviceExecutorData struct {
		Imports           []*codegen.ImportSpec
		ServiceClientRef  string
		SpecsPackageAlias string
		Constructor       string
		Names             serviceExecutorNames
		Tools             []*serviceExecutorToolData
	}

	// serviceExecutorToolData contains one tool and the private function field
	// that calls its Goa service method.
	serviceExecutorToolData struct {
		*ToolData
		CallerField string
	}

	// serviceExecutorNames contains the fixed public API and private helpers
	// written into one service executor package.
	serviceExecutorNames struct {
		ConfigType          string
		OptionType          string
		InterceptorType     string
		InterceptorFuncType string
		OptionFuncType      string
		WithPayloadMapper   string
		WithResultMapper    string
		WithInterceptors    string
		WithClient          string
		FailedToolResult    string
		FailedCallResult    string
		InvalidToolCall     string
	}

	exampleExecutorFileData struct {
		Agent      *AgentData
		Toolset    *ToolsetData
		SpecsAlias string
		Tools      []*exampleExecutorToolData
	}

	// exampleExecutorToolData describes one generated branch in a starter
	// executor. All names come from the saved tool package plan.
	exampleExecutorToolData struct {
		ID               string
		ConstName        string
		TypedTool        string
		InjectDecodeFunc string
		ResultExample    string
		HasResult        bool
		HasResultExample bool
	}

	mcpExecutorFileData struct {
		PackageName string
		Toolset     *ToolsetData
		*mcpExecutorData
		Tools []mcpExecutorToolData
	}

	// mcpExecutorData contains the final package names and imports used by one
	// MCP executor file.
	mcpExecutorData struct {
		Imports     []*codegen.ImportSpec
		Constructor string
		Failure     string
		SpecsAlias  string
	}

	mcpExecutorToolData struct {
		LocalName        string
		ConstName        string
		SpecVar          string
		HasResult        bool
		StructuredResult bool
		TextResult       bool
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
