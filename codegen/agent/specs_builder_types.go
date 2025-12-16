package codegen

import (
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
)

type (
	// toolSpecsData aggregates all type and codec metadata for a set of tools
	// owned by a single Goa service. It collects type definitions, schemas, and
	// codec functions during the generation process and provides them to
	// templates for rendering.
	toolSpecsData struct {
		// svc is the Goa service that owns the tools.
		svc *service.Data
		// genpkg is the Go import path to the generated root (typically `<module>/gen`).
		genpkg string
		// Type metadata indexed by cache key for deduplication.
		types map[string]*typeData
		// Deterministic ordering of types for generation.
		order []*typeData
		// Tool entries with their payload/result type metadata.
		tools []*toolEntry
		// Unions contains the sum-type unions required by any generated tool types
		// in this specs package.
		Unions []*service.UnionTypeData
		// Scope captures the name scope used to materialize nested local types for
		// this toolset. It is reused by other generator passes (e.g., adapter
		// transforms) to compute type references that match the specs package.
		Scope *codegen.NameScope
	}

	// toolEntry pairs a tool declaration with its payload/result type metadata.
	// Each entry represents one tool that will be generated in the tool_specs
	// package with its associated type definitions and codecs.
	toolEntry struct {
		// Qualified tool name used in runtime lookups (e.g., "toolset.tool").
		Name string
		// GoName is the Go-friendly identifier for this tool (e.g., "GetTimeSeries").
		GoName string
		// ConstName is the Go constant identifier for this tool's ID, computed
		// using the name scope to ensure uniqueness within the package.
		ConstName string
		// Title is the human-friendly display title.
		Title string
		// Service name that owns the tool.
		Service string
		// Toolset name that contains the tool.
		Toolset string
		// Tool description for documentation and LLM context.
		Description string
		// Classification tags for policy and filtering.
		Tags []string
		// Whether this tool is exported by an agent (agent-as-tool).
		IsExportedByAgent bool
		// ID of the agent that exports this tool.
		ExportingAgentID string
		// Type metadata for the tool's input arguments.
		Payload *typeData
		// Type metadata for the tool's output result.
		Result *typeData
		// Type metadata for the optional tool sidecar payload.
		Sidecar *typeData
		// BoundedResult indicates that this tool's result is declared as a bounded
		// view over a potentially larger data set (set via the BoundedResult DSL
		// helper). It is propagated into ToolSpec for runtime consumers.
		BoundedResult bool
		// ResultReminder is an optional system reminder injected into the
		// conversation after the tool result is returned.
		ResultReminder string
		// Confirmation configures design-time confirmation requirements for this tool.
		Confirmation *ToolConfirmationData
	}

	// typeData holds all metadata needed to generate a type definition, schema,
	// and codec functions for a tool's payload or result type. It includes the
	// Go type definition, JSON schema, validation code, and import specifications.
	typeData struct {
		// Cache key for type deduplication (either "ref:<fullref>" or "name:<typename>").
		Key string
		// Go type name (e.g., "MyToolPayload").
		TypeName string
		// GoDoc comment describing the type.
		Doc string
		// Type definition line (e.g., "MyType struct { ... }" or "MyType = service.Type").
		Def string
		// SchemaJSON holds the OpenAPI JSON schema bytes for this type. When
		// empty, no schema is available or the type cannot be represented as
		// a JSON schema.
		SchemaJSON []byte
		// ExampleJSON holds a canonical example JSON document for this type when
		// available. For payloads, it is derived from Goa examples and can be used
		// by runtimes to surface concrete examples in retry hints or UI prompts.
		ExampleJSON []byte
		// Typed codec variable name (e.g., "MyToolPayloadCodec").
		ExportedCodec string
		// Untyped codec variable name (e.g., "myToolPayloadCodec").
		GenericCodec string
		// Marshal function name (e.g., "MarshalMyToolPayload").
		MarshalFunc string
		// Unmarshal function name (e.g., "UnmarshalMyToolPayload").
		UnmarshalFunc string
		// Validation function name (e.g., "ValidateMyToolPayload").
		ValidateFunc string
		// Validation code body.
		Validation string
		// Fully-qualified type reference with pointer prefix (e.g., "*MyType" or "MyType").
		FullRef string
		// Whether the type is a pointer.
		Pointer bool
		// Argument expression for marshal calls (e.g., "v" or "*v").
		MarshalArg string
		// Argument expression for unmarshal calls (e.g., "v" or "&v").
		UnmarshalArg string
		// Validation code split into lines for template rendering.
		ValidationSrc []string
		// Whether to generate a type definition.
		NeedType bool
		// IsToolType is true when this entry represents a top-level tool-facing
		// payload/result/sidecar type (not a nested helper type or JSON helper).
		IsToolType bool
		// Import spec for the type's package (when aliasing external types).
		Import *codegen.ImportSpec
		// Import spec for the service package (when referencing service types).
		ServiceImport *codegen.ImportSpec
		// Error message for nil values.
		NilError string
		// Error message for decode failures.
		DecodeError string
		// Error message for validation failures.
		ValidateError string
		// Error message for empty JSON input.
		EmptyError string
		// Whether this is a payload or result type.
		Usage typeUsage
		// Imports needed for this type's definition.
		TypeImports []*codegen.ImportSpec
		// Whether to generate codec functions.
		GenerateCodec bool
		// FieldDescs maps dotted field paths to descriptions (for payload types).
		FieldDescs map[string]string
		// AcceptEmpty indicates that empty JSON input should be accepted and
		// treated as the zero value (only for payloads). This is true for
		// payload types that are empty structs (no fields).
		AcceptEmpty bool
		// JSON decode-body support. When enabled, codecs decode into a JSON-body
		// helper type (HTTP server body style) and then transform into the final
		// payload/result type. Helper types use pointers for primitives so the
		// codec can distinguish "missing" from zero values and return structured
		// validation issues.
		JSONTypeName      string
		JSONDef           string
		JSONRef           string
		JSONValidation    string
		JSONValidationSrc []string
		TransformBody     string
		TransformHelpers  []*codegen.TransformFunctionData
		// ImplementsBounds indicates that this type implements agent.BoundedResult.
		// When true, templates emit a Bounds() method on the result alias type so
		// runtimes can rely on the interface rather than reflection.
		ImplementsBounds bool
	}

	// toolSpecBuilder walks tool types and generates corresponding type metadata,
	// schemas, and validation code. It maintains caches for deduplication and
	// handles cross-service type references for MCP and external toolsets.
	toolSpecBuilder struct {
		// Generation package base path.
		genpkg string
		// Service data for the owning service.
		service *service.Data
		// Name scope for service type references.
		svcScope *codegen.NameScope
		// Import specs for service types.
		svcImports map[string]*codegen.ImportSpec
		// Cache of generated type metadata indexed by cache key.
		types map[string]*typeData
		// helperScope provides a global scope to assign short, unique names
		// to transform helper functions across all generated tool payloads.
		// Using a shared scope ensures there are no collisions while keeping
		// names compact and readable.
		helperScope *codegen.NameScope
		// unions accumulates all union sum types referenced by generated tool
		// payload/result/sidecar types in this specs package, indexed by union hash.
		unions map[string]*service.UnionTypeData
	}

	typeUsage string
)

const (
	usagePayload typeUsage = "payload"
	usageResult  typeUsage = "result"
	usageSidecar typeUsage = "sidecar"
)
