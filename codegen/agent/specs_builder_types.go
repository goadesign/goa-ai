package codegen

import (
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
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
		// TransportUnions contains the sum-type unions required by any generated
		// transport types in the toolset-local http package. These must be derived
		// from the transport attribute graph (after localization) so they do not
		// leak service `gen/types` references into tool JSON.
		TransportUnions []*service.UnionTypeData
		// CodecTransformHelpers contains helper functions produced by Goa's
		// GoTransform when generating codec-local conversions (transport <-> public).
		// These are emitted once per package to support recursive types without
		// duplicating helper declarations.
		CodecTransformHelpers []*codegen.TransformFunctionData
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
		// ServerData enumerates server-only payloads emitted alongside the tool
		// result. Server data is never sent to model providers.
		ServerData []*serverDataEntry
		// Classification tags for policy and filtering.
		Tags []string
		// Meta carries arbitrary design-time metadata attached to the tool via DSL.
		// Keys map to one or more values, matching Goa's meta conventions.
		Meta map[string][]string
		// MetaPairs is a deterministic representation of Meta for templates that
		// need stable ordering in emitted Go source.
		MetaPairs []toolMetaPair
		// Whether this tool is exported by an agent (agent-as-tool).
		IsExportedByAgent bool
		// ID of the agent that exports this tool.
		ExportingAgentID string
		// Type metadata for the tool's input arguments.
		Payload *typeData
		// Type metadata for the tool's output result.
		Result *typeData
		// BoundedResult indicates that this tool's result is declared as a bounded
		// view over a potentially larger data set (set via the BoundedResult DSL
		// helper). It is propagated into ToolSpec for runtime consumers.
		BoundedResult bool
		// TerminalRun indicates this tool should terminate the run immediately after
		// execution (no follow-up plan/resume/finalization turn).
		TerminalRun bool
		// Paging describes cursor-based pagination fields for this tool when configured.
		Paging *ToolPagingData
		// ResultReminder is an optional system reminder injected into the
		// conversation after the tool result is returned.
		ResultReminder string
		// Confirmation configures design-time confirmation requirements for this tool.
		Confirmation *ToolConfirmationData
	}

	serverDataEntry struct {
		Kind        string
		Audience    string
		Description string
		Type        *typeData
	}

	toolMetaPair struct {
		Key    string
		Values []string
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
		// ExampleInputGo is a Go expression that evaluates to a map[string]any
		// representing an example payload. It is only populated for payload
		// examples that are JSON objects.
		ExampleInputGo string
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
		// PublicType is the Goa expression for the tool-facing public type as it
		// exists in the specs package. It is used by other generator passes (for
		// example, adapter transforms) to ensure a single source of truth for the
		// tool type shape and local naming.
		PublicType *goaexpr.AttributeExpr
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
		// TransportTypeName is the private Go type name used for the internal
		// server-body transport representation of this tool type. It is used only
		// by codecs to decode/validate JSON with missing-field detection.
		TransportTypeName string
		// TransportDef is the Go type definition for the internal transport type.
		TransportDef string
		// TransportImports are the imports required by the transport type
		// definition. These are used to generate the toolset-local http package.
		TransportImports []*codegen.ImportSpec
		// TransportValidationSrc is validation code (as lines) that validates a
		// pointer to the transport type (variable name: "body").
		TransportValidationSrc []string
		// TransportTypeRef is the reference type used by Validate{{TransportTypeName}}.
		// It matches Goa's conventions: pointers for composite types, values for
		// primitive/alias types.
		TransportTypeRef string
		// TransportPointer reports whether TransportTypeRef is a pointer type.
		TransportPointer bool
		// DecodeTransform is Go code that initializes "out" (public type) from "in"
		// (transport type) using Goa's GoTransform conventions.
		DecodeTransform string
		// EncodeTransform is Go code that initializes "out" (transport type) from "in"
		// (public type) using Goa's GoTransform conventions.
		EncodeTransform string
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
		// ImplementsBounds indicates that this type implements agent.BoundedResult.
		// When true, templates emit a Bounds() method on the result alias type so
		// runtimes can rely on the interface rather than reflection.
		ImplementsBounds bool

		// HasNextCursor reports whether the tool result type declares a top-level
		// cursor field for paging. When non-empty, ResultBounds includes Bounds.NextCursor
		// from the corresponding result field.
		NextCursorGoField string
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
		// transportUnions accumulates all union sum types referenced by transport
		// helper graphs emitted into the toolset-local http package.
		transportUnions map[string]*service.UnionTypeData
		// codecTransformHelpers accumulates unique GoTransform helper functions
		// required by codec-local conversions (transport <-> public).
		codecTransformHelpers    []*codegen.TransformFunctionData
		codecTransformHelperKeys map[string]struct{}
	}

	typeUsage string
)

const (
	usagePayload typeUsage = "payload"
	usageResult  typeUsage = "result"
	usageSidecar typeUsage = "sidecar"
)
