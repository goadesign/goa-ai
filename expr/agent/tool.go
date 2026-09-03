package agent

import (
	"fmt"

	"goa.design/goa-ai/boundedresult"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// ToolExpr captures an individual tool declaration within a toolset.
	ToolExpr struct {
		eval.DSLFunc

		// Name is the unique identifier for this tool within its toolset.
		Name string

		// Title is an optional human-friendly display title. When empty, codegen
		// derives a title from Name (e.g., "analyze_sensor_patterns" -> "Analyze Sensor Patterns").
		Title string

		// Description provides a human-readable explanation of what the
		// tool does.
		Description string

		// Tags are labels for categorizing and filtering this tool.
		Tags []string

		// Meta carries arbitrary design-time metadata attached to the tool via DSL.
		// Keys map to one or more values, matching Goa's Meta conventions.
		Meta goaexpr.MetaExpr

		// Args defines the input parameter schema for this tool.
		Args *goaexpr.AttributeExpr

		// Return defines the output result schema for this tool.
		Return *goaexpr.AttributeExpr

		// ServerData declares typed server-only data emitted alongside the canonical
		// tool result. Server data is never serialized into model provider requests.
		//
		// Each entry declares a Kind identifier and a schema type. Code generation
		// produces a JSON codec per entry so values can be marshaled into canonical
		// JSON bytes and decoded reliably by runtimes and downstream consumers.
		ServerData []*ServerDataExpr

		// Toolset is the toolset expression that owns this tool.
		Toolset *ToolsetExpr

		// Method is the resolved Goa service method this tool is bound
		// to, if any.
		Method *goaexpr.MethodExpr

		// ExportPassthrough defines deterministic forwarding for this tool
		// when it is part of an exported toolset.
		ExportPassthrough *ToolPassthroughExpr

		// Optional display hint templates declared in the DSL.
		CallHintTemplate   string
		ResultHintTemplate string

		// InjectedFields are fields marked as infrastructure-only.
		InjectedFields []string

		// Bounds declares the out-of-band bounded-result contract for this tool.
		// When non-nil, runtimes require planner.ToolResult.Bounds and generated
		// method-backed executors project canonical bound fields from service
		// method results without polluting the semantic result schema.
		Bounds *ToolBoundsExpr

		// TerminalRun indicates that once this tool executes, the runtime should
		// terminate the run immediately without requesting a follow-up planner
		// PlanResume/finalization turn. Terminal tools are always treated as
		// bookkeeping and do not contribute to the run-level retrieval budget.
		// It is set via the TerminalRun DSL helper.
		TerminalRun bool

		// Bookkeeping indicates the tool is a structured bookkeeping tool (status
		// updates, findings, terminal commits) and must not be accounted against
		// the run-level MaxToolCalls retrieval budget. Model-authored batches are
		// admitted atomically; bookkeeping calls do not contribute to their budget
		// cost. It is set via the Bookkeeping DSL helper.
		Bookkeeping bool

		// ResultReminder is an optional system reminder that is injected into
		// the conversation after the tool result is returned. It provides
		// backstage guidance to the model about how to interpret or present
		// the result (for example, "The user sees a rendered graph of this
		// data"). The reminder is wrapped in <system-reminder> tags by the
		// runtime.
		ResultReminder string

		// Confirmation configures design-time confirmation requirements for this tool.
		// When non-nil, the runtime requests an external confirmation before executing
		// the tool (unless runtime overrides supersede the confirmation).
		Confirmation *ToolConfirmationExpr

		bindServiceName string
		bindMethodName  string
	}

	// ServerDataExpr declares one server-only data item emitted alongside a tool
	// result.
	ServerDataExpr struct {
		eval.DSLFunc

		// Kind identifies the logical kind of this server data (for example,
		// "charts.time_series" for UI charts).
		Kind string

		// Audience declares who this server-data payload is intended for.
		//
		// Contract:
		//   - "timeline": persisted and eligible for UI rendering and transcript export.
		//   - "internal": tool-composition attachment; not persisted or rendered.
		//   - "evidence": provenance references; persisted separately from timeline cards.
		//
		// Audience is set by the DSL layer. When not explicitly configured, it
		// defaults to "timeline".
		Audience string

		// Description is the observer-facing description of this server-data payload.
		// It is typically used by UIs and sinks to explain rendering behavior.
		Description string

		// Schema describes the typed payload. It must be non-empty.
		Schema *goaexpr.AttributeExpr

		// Source describes how to populate the server-data payload. When set,
		// code generation uses it to derive the server-data payload from the tool's
		// bound method result.
		Source *ServerDataSourceExpr

		// Tool links this server-data declaration to its owning tool. It is set by
		// the DSL layer and used for schema naming and validation.
		Tool *ToolExpr
	}

	// ServerDataSourceExpr describes the producer-side source of a server-data
	// payload.
	ServerDataSourceExpr struct {
		// MethodResultField names the bound method result field used as the source
		// payload (for example, "Evidence").
		MethodResultField string
	}

	// ToolBoundsExpr describes the out-of-band bounded-result contract for a tool.
	ToolBoundsExpr struct {
		// Tool is the owning tool declaration.
		Tool *ToolExpr
		// Paging optionally describes cursor-based pagination for this bounded tool.
		Paging *ToolPagingExpr
	}

	// ToolPagingExpr identifies the continuation operation and cursor field names
	// used by a cursor-paged tool. CursorField names a payload field on either the
	// current tool or ContinueTool. NextCursorField names the
	// canonical next-page cursor identifier for the paging contract, which is
	// projected into runtime-owned bounds metadata rather than the semantic tool
	// result.
	ToolPagingExpr struct {
		// ContinueTool is the sibling tool that accepts CursorField. Empty means the
		// current tool accepts its own continuation cursor.
		ContinueTool string
		// CursorField is the String field that carries the paging cursor.
		CursorField string
		// NextCursorField is the canonical field name for the next-page reference
		// in the projected result contract.
		NextCursorField string
	}

	// ToolPassthroughExpr defines deterministic forwarding for an exported tool.
	ToolPassthroughExpr struct {
		TargetService string
		TargetMethod  string
	}

	// injectTarget names one attribute set generated code resolves injected
	// fields against, so validation errors can point at the exact shape that
	// misses the contract.
	injectTarget struct {
		att  *goaexpr.AttributeExpr
		desc string
	}
)

// AddMeta adds metadata to the tool expression.
//
// This method exists so Goa's standard Meta DSL helper can attach metadata to
// goa-ai agent tool expressions without goa-ai introducing a parallel Meta DSL.
func (t *ToolExpr) AddMeta(name string, value ...string) {
	if t.Meta == nil {
		t.Meta = make(goaexpr.MetaExpr)
	}
	t.Meta[name] = append(t.Meta[name], value...)
}

// DeleteMeta removes the metadata entry identified by name.
//
// This method exists so Goa's standard RemoveMeta DSL helper can remove metadata
// from goa-ai agent tool expressions.
func (t *ToolExpr) DeleteMeta(name string) {
	delete(t.Meta, name)
}

// EvalName implements eval.Expression.
func (t *ToolExpr) EvalName() string {
	// Be resilient in error reporting: EvalName is used in diagnostics and
	// may be called before the owning structures are fully wired.
	ts := ""
	svc := ""
	if t != nil && t.Toolset != nil {
		ts = t.Toolset.Name
		if t.Toolset.Agent != nil && t.Toolset.Agent.Service != nil {
			svc = t.Toolset.Agent.Service.Name
		}
	}
	if svc != "" {
		return fmt.Sprintf("tool %q in toolset %q and service %q", t.Name, ts, svc)
	}
	return fmt.Sprintf("tool %q in toolset %q", t.Name, ts)
}

// EvalName implements eval.Expression.
func (b *ToolBoundsExpr) EvalName() string {
	if b == nil || b.Tool == nil {
		return "tool bounds"
	}
	return fmt.Sprintf("bounded result for %s", b.Tool.EvalName())
}

// EvalName implements eval.Expression.
func (s *ServerDataExpr) EvalName() string {
	toolName := ""
	toolsetName := ""
	serviceName := ""
	if s != nil && s.Tool != nil {
		toolName = s.Tool.Name
		if s.Tool.Toolset != nil {
			toolsetName = s.Tool.Toolset.Name
			if s.Tool.Toolset.Agent != nil && s.Tool.Toolset.Agent.Service != nil {
				serviceName = s.Tool.Toolset.Agent.Service.Name
			}
		}
	}
	if serviceName != "" {
		return fmt.Sprintf("server data %q for tool %q in toolset %q and service %q", s.Kind, toolName, toolsetName, serviceName)
	}
	if toolName != "" {
		return fmt.Sprintf("server data %q for tool %q in toolset %q", s.Kind, toolName, toolsetName)
	}
	return fmt.Sprintf("server data %q", s.Kind)
}

// SetDescription implements goa.design/goa/v3/expr.DescriptionHolder so the Goa
// Description DSL helper can be used inside ServerData configuration blocks.
func (s *ServerDataExpr) SetDescription(d string) {
	s.Description = d
}

// RecordBinding records the service and method names specified via the DSL.
func (t *ToolExpr) RecordBinding(serviceName, methodName string) {
	t.bindServiceName = serviceName
	t.bindMethodName = methodName
}

// Prepare ensures Args and Return are always non-nil attributes and applies
// canonical tool normalization before validation/codegen.
func (t *ToolExpr) Prepare() {
	if t.Args == nil {
		t.Args = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
	if t.Return == nil {
		t.Return = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
	if t.TerminalRun {
		t.Bookkeeping = true
	}
}

// Validate checks that Args has the object shape required by model tool calls,
// that any recorded binding resolves to an existing service method, and that
// every Inject()-ed field is a concrete, required String field.
func (t *ToolExpr) Validate() error {
	if t.bindMethodName == "" {
		verr := new(eval.ValidationErrors)
		validateInjectedFields(t, injectTargets(t, nil), verr)
		if err := t.validateShapes(); err != nil {
			verr.AddError(t, err)
		}
		if len(verr.Errors) > 0 {
			return verr
		}
		return nil
	}
	verr := new(eval.ValidationErrors)
	var svc *goaexpr.ServiceExpr
	if t.bindServiceName != "" {
		svc = goaexpr.Root.Service(t.bindServiceName)
	} else {
		svc = t.Toolset.Agent.Service
	}
	if svc == nil {
		verr.Add(t, "BindTo could not resolve target service")
		return verr
	}
	desired := codegen.Goify(t.bindMethodName, true)
	for _, m := range svc.Methods {
		if codegen.Goify(m.Name, true) == desired {
			t.Method = m
			validateInjectedFields(t, injectTargets(t, m), verr)
			if err := t.validateShapes(); err != nil {
				verr.AddError(t, err)
				return verr
			}
			if len(verr.Errors) > 0 {
				return verr
			}
			return nil
		}
	}
	verr.Add(t, "service method %q not found in service %q", t.bindMethodName, svc.Name)
	return verr
}

// injectTargets returns every attribute set the generated code resolves
// Inject() fields against, mirroring codegen exactly:
//
//   - Unbound tool: the tool's own Args (the generated tool payload type).
//   - Bound tool without explicit Args: the bound method payload — codegen
//     Prepare copies it into Args, so it IS the effective tool payload.
//   - Bound tool with explicit Args: BOTH sets. The generated per-toolset
//     inject.go populates the tool payload built from Args, while the
//     generated registry provider.go populates the bound method payload
//     directly; a name missing from either set would generate code that does
//     not compile, so divergence must fail here, at design time.
//
// m is nil for unbound tools.
func injectTargets(t *ToolExpr, m *goaexpr.MethodExpr) []injectTarget {
	if m == nil {
		return []injectTarget{{att: t.Args, desc: "tool payload"}}
	}
	if t.Args == nil || t.Args.Type == nil || t.Args.Type == goaexpr.Empty {
		return []injectTarget{{att: m.Payload, desc: "bound method payload"}}
	}
	return []injectTarget{
		{att: t.Args, desc: "tool Args"},
		{att: m.Payload, desc: "bound method payload"},
	}
}

// validateInjectedFields enforces the generation-time contract for Inject():
// every injected name must be declared exactly once and must exist, be
// required, and be a String on every target attribute set. These invariants
// let codegen compile injection (direct ToolCallMeta reads or label lookups)
// without any runtime schema introspection, and guarantee the generated
// population code compiles for every topology.
func validateInjectedFields(t *ToolExpr, targets []injectTarget, verr *eval.ValidationErrors) {
	if t == nil || len(t.InjectedFields) == 0 {
		return
	}

	names := make([]string, 0, len(t.InjectedFields))
	seen := make(map[string]struct{})
	for _, name := range t.InjectedFields {
		if name == "" {
			verr.Add(t, "Inject requires non-empty field names")
			continue
		}
		if _, ok := seen[name]; ok {
			verr.Add(t, "Inject field %q is declared more than once", name)
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	for i, target := range targets {
		validateInjectedFieldsAgainst(t, target, names, otherTargets(targets, i), verr)
	}
}

// validateInjectedFieldsAgainst checks each injected name against one target
// attribute set. When a name is missing from this target but present on
// another (bound tools with explicit Args that diverge from the bound method
// payload), the error names both shapes so the divergence is obvious.
func validateInjectedFieldsAgainst(t *ToolExpr, target injectTarget, names []string, others []injectTarget, verr *eval.ValidationErrors) {
	if target.att == nil || target.att.Type == nil || target.att.Type == goaexpr.Empty {
		verr.Add(t, "Inject requires a non-empty %s", target.desc)
		return
	}

	att := target.att
	if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
		att = ut.Attribute()
	}
	obj, ok := att.Type.(*goaexpr.Object)
	if !ok || obj == nil {
		verr.Add(t, "Inject requires the %s to be an object", target.desc)
		return
	}

	required := make(map[string]struct{})
	if att.Validation != nil {
		for _, r := range att.Validation.Required {
			required[r] = struct{}{}
		}
	}

	for _, name := range names {
		field := obj.Attribute(name)
		if field == nil || field.Type == nil || field.Type == goaexpr.Empty {
			if other, found := targetDefiningField(others, name); found {
				verr.Add(t, "Inject field %q does not exist on the %s even though the %s defines it; the two shapes diverge — declare %q on the %s or remove Inject(%q)", name, target.desc, other.desc, name, target.desc, name)
				continue
			}
			verr.Add(t, "Inject field %q does not exist on the %s", name, target.desc)
			continue
		}
		if _, ok := required[name]; !ok {
			verr.Add(t, "Inject field %q must be required on the %s; injected fields are always server-populated and hidden from the model, so an optional injected field is a contradiction", name, target.desc)
			continue
		}
		if !isInjectedString(field.Type) {
			verr.Add(t, "Inject field %q must be a String on the %s", name, target.desc)
			continue
		}
		if custom := injectedCustomGoType(field); custom != "" {
			verr.Add(t, "Inject field %q on the %s uses struct:field:type %q; injected fields support Goa String and named Goa String types only", name, target.desc, custom)
		}
	}
}

// isInjectedString reports whether dataType is String or a named String type.
func isInjectedString(dataType goaexpr.DataType) bool {
	for {
		switch actual := dataType.(type) {
		case goaexpr.Primitive:
			return actual == goaexpr.String
		case *goaexpr.ResultTypeExpr:
			return false
		case goaexpr.UserType:
			dataType = actual.Attribute().Type
		default:
			return false
		}
	}
}

// injectedCustomGoType returns the first Go type override on a String field or
// one of its named String types. Injection cannot construct these types from
// the string supplied by call metadata or run labels.
func injectedCustomGoType(field *goaexpr.AttributeExpr) string {
	for field != nil {
		if custom, _ := codegen.GetMetaType(field); custom != "" {
			return custom
		}
		userType, ok := field.Type.(goaexpr.UserType)
		if !ok {
			return ""
		}
		field = userType.Attribute()
	}
	return ""
}

// otherTargets returns targets without the entry at index i.
func otherTargets(targets []injectTarget, i int) []injectTarget {
	if len(targets) <= 1 {
		return nil
	}
	out := make([]injectTarget, 0, len(targets)-1)
	out = append(out, targets[:i]...)
	return append(out, targets[i+1:]...)
}

// targetDefiningField returns the first target whose (unwrapped) object
// defines a concrete field named name.
func targetDefiningField(targets []injectTarget, name string) (injectTarget, bool) {
	for _, target := range targets {
		if target.att == nil || target.att.Type == nil || target.att.Type == goaexpr.Empty {
			continue
		}
		att := target.att
		if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
			att = ut.Attribute()
		}
		obj, ok := att.Type.(*goaexpr.Object)
		if !ok || obj == nil {
			continue
		}
		if field := obj.Attribute(name); field != nil && field.Type != nil && field.Type != goaexpr.Empty {
			return target, true
		}
	}
	return injectTarget{}, false
}

func (t *ToolExpr) validateShapes() error {
	verr := new(eval.ValidationErrors)
	validateToolConfirmation(t, verr)
	check := func(where string, att *goaexpr.AttributeExpr) {
		validateContractShape(t, where, att, verr)
	}
	validateToolArgsShape(t, verr)
	check("Return", t.Return)
	validateServerDataShapes(t, verr, check)
	validateBoundsShape(t, verr)
	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}

// validateToolArgsShape requires the JSON object that model providers use for
// tool arguments. Return values and server data may use any supported shape.
func validateToolArgsShape(tool *ToolExpr, verr *eval.ValidationErrors) {
	args := tool.Args
	if (args == nil || args.Type == nil || args.Type == goaexpr.Empty) && tool.Method != nil {
		args = tool.Method.Payload
	}
	if args == nil || args.Type == nil || args.Type == goaexpr.Empty {
		return
	}
	if goaexpr.AsObject(args.Type) != nil {
		return
	}
	verr.Add(tool, "Args must define an object; primitive, array, map, and union arguments are not supported")
}

func validateContractShape(owner eval.Expression, where string, att *goaexpr.AttributeExpr, verr *eval.ValidationErrors) {
	if verr == nil || att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	if _, ok := att.Type.(goaexpr.UserType); ok {
		return
	}
	if goaexpr.IsPrimitive(att.Type) {
		return
	}
	// Allow composite inline shapes (arrays, maps, objects, and composites).
	switch att.Type.(type) {
	case *goaexpr.Array, *goaexpr.Map, *goaexpr.Object, goaexpr.CompositeExpr:
		return
	}
	verr.Add(owner, "%s must be a user type, primitive, or composite shape", where)
}

func validateServerDataShapes(t *ToolExpr, verr *eval.ValidationErrors, check func(where string, att *goaexpr.AttributeExpr)) {
	if t == nil || verr == nil {
		return
	}
	if len(t.ServerData) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(t.ServerData))
	for _, sd := range t.ServerData {
		if sd == nil {
			continue
		}
		if sd.Kind == "" {
			verr.Add(t, "ServerData kind must be non-empty")
			continue
		}
		if _, duplicate := seen[sd.Kind]; duplicate {
			verr.Add(t, "ServerData kind %q must be unique within a tool", sd.Kind)
			continue
		}
		seen[sd.Kind] = struct{}{}
		check("ServerData", sd.Schema)
		if sd.Schema == nil || sd.Schema.Type == nil || sd.Schema.Type == goaexpr.Empty {
			verr.Add(t, "ServerData(%q) must declare a schema type", sd.Kind)
		}
		if sd.Source != nil && sd.Source.MethodResultField != "" {
			if t.Method == nil {
				verr.Add(t, "ServerData(%q) with FromMethodResultField requires a bound method (BindTo)", sd.Kind)
				continue
			}
			field := t.Method.Result.Find(sd.Source.MethodResultField)
			if field == nil || field.Type == nil || field.Type == goaexpr.Empty {
				verr.Add(t, "ServerData(%q) FromMethodResultField(%q) does not exist on method result", sd.Kind, sd.Source.MethodResultField)
			}
		}
	}
}

func validateBoundsShape(tool *ToolExpr, verr *eval.ValidationErrors) {
	if tool == nil || verr == nil || tool.Bounds == nil {
		return
	}
	validateMethodResultBoundsShape(tool, verr)
	validateToolReturnBoundsShape(tool, verr)
	if tool.Bounds.Paging == nil {
		return
	}
	validatePagingField := func(where string, att *goaexpr.AttributeExpr, name string, required bool) {
		if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
			if required {
				verr.Add(tool, "%s must be non-empty when configuring paging", where)
			}
			return
		}

		field := att.Find(name)
		if field == nil || field.Type == nil || field.Type == goaexpr.Empty {
			if required {
				verr.Add(tool, "%s must define an optional String field named %q when configuring paging", where, name)
			}
			return
		}
		if field.Type != goaexpr.String {
			verr.Add(tool, "%s field %q must be a String when configuring paging", where, name)
			return
		}

		root := att
		if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
			root = ut.Attribute()
		}
		if root != nil && root.Validation != nil {
			for _, req := range root.Validation.Required {
				if req == name {
					verr.Add(tool, "%s field %q must be optional when configuring paging", where, name)
					return
				}
			}
		}
	}

	if tool.Bounds.Paging.CursorField == "" {
		verr.Add(tool, "Cursor() is required when configuring paging")
		return
	}
	if tool.Bounds.Paging.NextCursorField == "" {
		verr.Add(tool, "NextCursor() is required when configuring paging")
		return
	}
	if tool.Bounds.Paging.ContinueTool == "" {
		sources := continuationSources(tool)
		if len(sources) > 0 {
			if len(sources) > 1 {
				verr.Add(tool, "continuation tool %q is referenced by more than one bounded tool", tool.Name)
				return
			}
			if isCursorOnlyPayload(tool, tool.Bounds.Paging.CursorField) {
				validateRequiredPagingField("Args", tool.Args, tool.Bounds.Paging.CursorField, verr, tool)
			} else {
				validatePagingField("Args", tool.Args, tool.Bounds.Paging.CursorField, true)
				validateContinuationPayloadMatch(sources[0], tool, verr)
			}
		} else {
			validatePagingField("Args", tool.Args, tool.Bounds.Paging.CursorField, true)
		}
		return
	}
	continuation := findSiblingTool(tool, tool.Bounds.Paging.ContinueTool)
	if continuation == nil {
		verr.Add(tool, "ContinueWith() references unknown tool %q", tool.Bounds.Paging.ContinueTool)
		return
	}
	if isCursorOnlyPayload(continuation, tool.Bounds.Paging.CursorField) {
		validateRequiredPagingField("continuation Args", continuation.Args, tool.Bounds.Paging.CursorField, verr, tool)
	} else {
		validatePagingField("continuation Args", continuation.Args, tool.Bounds.Paging.CursorField, true)
		validateContinuationPayloadMatch(tool, continuation, verr)
	}
	if continuation.Bounds == nil || continuation.Bounds.Paging == nil || continuation.Bounds.Paging.CursorField != tool.Bounds.Paging.CursorField {
		verr.Add(tool, "continuation tool %q must declare Cursor(%q) inside BoundedResult", continuation.Name, tool.Bounds.Paging.CursorField)
	}
}

// continuationSources returns the bounded tools that delegate their next page
// to tool. Dedicated continuation tools have exactly one source so a no-argument
// continuation always advances one unambiguous result set.
func continuationSources(tool *ToolExpr) []*ToolExpr {
	if tool == nil || tool.Toolset == nil {
		return nil
	}
	var sources []*ToolExpr
	for _, candidate := range tool.Toolset.Tools {
		if candidate.Bounds != nil && candidate.Bounds.Paging != nil && candidate.Bounds.Paging.ContinueTool == tool.Name {
			sources = append(sources, candidate)
		}
	}
	return sources
}

// isCursorOnlyPayload reports whether the continuation cursor carries the
// complete query state and no prior query payload must be retained.
func isCursorOnlyPayload(tool *ToolExpr, cursorField string) bool {
	root := tool.Args
	if ut, ok := root.Type.(goaexpr.UserType); ok {
		root = ut.Attribute()
	}
	obj, ok := root.Type.(*goaexpr.Object)
	return ok && len(*obj) == 1 && (*obj)[0].Name == cursorField
}

// validateContinuationPayloadMatch requires replaying continuations to use the
// exact source payload type, so retaining the prior canonical payload cannot
// change query semantics.
func validateContinuationPayloadMatch(source, continuation *ToolExpr, verr *eval.ValidationErrors) {
	if source.Args == nil || continuation.Args == nil || source.Args.Type == nil || continuation.Args.Type == nil ||
		source.Args.Type.Hash() != continuation.Args.Type.Hash() {
		verr.Add(
			continuation,
			"continuation Args must match source tool %q Args or contain only the cursor field %q",
			source.Name,
			continuation.Bounds.Paging.CursorField,
		)
	}
}

// findSiblingTool returns the named tool from the same defining toolset.
func findSiblingTool(tool *ToolExpr, name string) *ToolExpr {
	if tool == nil || tool.Toolset == nil {
		return nil
	}
	for _, candidate := range tool.Toolset.Tools {
		if candidate.Name == name {
			return candidate
		}
	}
	return nil
}

// validateRequiredPagingField enforces the cursor-only continuation boundary.
func validateRequiredPagingField(where string, att *goaexpr.AttributeExpr, name string, verr *eval.ValidationErrors, tool *ToolExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		verr.Add(tool, "%s must define a required String field named %q", where, name)
		return
	}
	field := att.Find(name)
	if field == nil || field.Type != goaexpr.String {
		verr.Add(tool, "%s must define a required String field named %q", where, name)
		return
	}
	root := att
	if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
		root = ut.Attribute()
	}
	for _, required := range root.Validation.Required {
		if required == name {
			return
		}
	}
	verr.Add(tool, "%s field %q must be required", where, name)
}

// validateToolReturnBoundsShape enforces that the explicit tool-facing Return
// shape stays semantic while BoundedResult owns the canonical bounded fields.
func validateToolReturnBoundsShape(tool *ToolExpr, verr *eval.ValidationErrors) {
	if tool == nil || verr == nil || tool.Bounds == nil || tool.Return == nil {
		return
	}
	for _, name := range canonicalBoundedResultFieldNames(tool.Bounds) {
		field := tool.Return.Find(name)
		if field == nil || field.Type == nil || field.Type == goaexpr.Empty {
			continue
		}
		verr.Add(tool, "bounded tool return must not define canonical bounds field %q; use planner.ToolResult.Bounds instead", name)
	}
}

func validateMethodResultBoundsShape(tool *ToolExpr, verr *eval.ValidationErrors) {
	if tool == nil || verr == nil || tool.Bounds == nil || tool.Method == nil {
		return
	}
	if tool.Method.Result == nil {
		verr.Add(tool, "bounded method result requires a non-empty bound method result")
		return
	}
	validateBoundsField := func(name string, expected goaexpr.DataType, label string, existsRequired bool, mustBeRequired bool, mustBeOptional bool) {
		field := tool.Method.Result.Find(name)
		if field == nil || field.Type == nil || field.Type == goaexpr.Empty {
			if existsRequired {
				verr.Add(tool, "bounded method result must define %q on the bound method result", name)
			}
			return
		}
		if field.Type != expected {
			verr.Add(tool, "bounded method result field %q must be a %s", name, label)
			return
		}
		isRequired := tool.Method.Result.IsRequired(name)
		if mustBeRequired && !isRequired {
			verr.Add(tool, "bounded method result field %q must be required", name)
			return
		}
		if mustBeOptional && isRequired {
			verr.Add(tool, "bounded method result field %q must be optional", name)
		}
	}
	validateBoundsField("returned", goaexpr.Int, "Int", true, true, false)
	validateBoundsField("truncated", goaexpr.Boolean, "Boolean", true, true, false)
	// Services that know the exact cardinality may require total. Services whose
	// underlying provider cannot determine it keep the field optional; generated
	// projection handles both shapes as *agent.Bounds.Total.
	validateBoundsField("total", goaexpr.Int, "Int", false, false, false)
	// Without paging, refinement_hint is the only continuation channel for
	// truncated results, so the bound method result must define it; the
	// runtime rejects truncated bounded results that carry neither a next
	// cursor nor a refinement hint.
	validateBoundsField("refinement_hint", goaexpr.String, "String", tool.Bounds.Paging == nil, false, true)
	if tool.Bounds.Paging != nil && tool.Bounds.Paging.NextCursorField != "" {
		validateBoundsField(tool.Bounds.Paging.NextCursorField, goaexpr.String, "String", true, false, true)
	}
}

// canonicalBoundedResultFieldNames returns the reserved model-visible fields
// owned by BoundedResult for schema and runtime projection.
func canonicalBoundedResultFieldNames(bounds *ToolBoundsExpr) []string {
	nextCursorField := ""
	if bounds != nil && bounds.Paging != nil {
		nextCursorField = bounds.Paging.NextCursorField
	}
	return boundedresult.CanonicalFieldNames(nextCursorField)
}

// Finalize materializes tool shapes and resolves method bindings.
//
// Contract:
//   - Args/Return are finalized before codegen so Extend-composed fields are
//     materialized once at the expression layer.
//   - Method bindings are resolved after validation and must be deterministic.
func (t *ToolExpr) Finalize() {
	finalizeToolShape(t.Args)
	finalizeToolShape(t.Return)

	if t.bindMethodName == "" {
		return
	}
	var svc *goaexpr.ServiceExpr
	if t.bindServiceName != "" {
		svc = goaexpr.Root.Service(t.bindServiceName)
	} else {
		svc = t.Toolset.Agent.Service
	}
	desired := codegen.Goify(t.bindMethodName, true)
	for _, m := range svc.Methods {
		if codegen.Goify(m.Name, true) == desired {
			t.Method = m
			return
		}
	}
	panic(fmt.Sprintf("tool %q: method %q not found in service %q after successful validation", t.Name, t.bindMethodName, svc.Name))
}

func finalizeToolShape(att *goaexpr.AttributeExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	att.Finalize()
}

// BoundServiceName returns the service name specified via BindTo, if any.
func (t *ToolExpr) BoundServiceName() string {
	return t.bindServiceName
}

// SetTitle implements expr.TitleHolder, allowing the Title() DSL function
// to set the tool title.
func (t *ToolExpr) SetTitle(title string) {
	t.Title = title
}
