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
		// bookkeeping so the run-level retrieval budget cannot trim them away.
		// It is set via the TerminalRun DSL helper.
		TerminalRun bool

		// Bookkeeping indicates the tool is a structured bookkeeping tool (status
		// updates, findings, terminal commits) and must not be accounted against
		// the run-level MaxToolCalls retrieval budget. Runtimes do not decrement
		// the budget for bookkeeping calls and never drop them during budget
		// trimming. It is set via the Bookkeeping DSL helper.
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
		// "atlas.time_series" for UI charts).
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

	// ToolPagingExpr identifies the cursor field names used by a cursor-paged tool.
	// CursorField always names a payload field. NextCursorField names the
	// canonical next-page cursor identifier for the paging contract, which is
	// projected into runtime-owned bounds metadata rather than the semantic tool
	// result.
	ToolPagingExpr struct {
		// CursorField is the name of the optional String field in the tool payload
		// that carries the paging cursor for retrieving the next page.
		CursorField string
		// NextCursorField is the canonical field name for the next-page cursor in
		// the paging contract.
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

// runtimeMetaFieldNames is the fixed set of runtime.ToolCallMeta Go field
// names (post-Goify) that Inject() compiles to a direct meta read instead of
// a run-label lookup. Kept in lockstep with
// codegen/agent/inject.go:metaFieldByGoName -- expr/agent cannot import
// codegen/agent (which imports expr/agent), so the fixed, rarely-changing set
// is duplicated here rather than shared via import.
var runtimeMetaFieldNames = map[string]struct{}{
	"RunID":            {},
	"SessionID":        {},
	"TurnID":           {},
	"ToolCallID":       {},
	"ParentToolCallID": {},
}

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

// Validate checks that any recorded binding can be resolved to an existing
// service and method, and that any Inject()-ed fields resolve to a concrete,
// required, String field on every attribute set the generated code resolves
// them against (see injectTargets).
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
			validateNoLabelBackedInjectOnBoundTool(t, verr)
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
		if field.Type != goaexpr.String {
			verr.Add(t, "Inject field %q must be a String on the %s", name, target.desc)
			continue
		}
	}
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

// validateNoLabelBackedInjectOnBoundTool rejects label-backed Inject() fields
// on tools bound to a service method via BindTo.
//
// codegen always emits a registry-served Provider.HandleToolCall case for
// every method-backed tool (provider.go is generated unconditionally,
// independent of whether any given deployment actually uses the registry
// topology), and the toolregistry wire protocol (ToolCallMessage/ToolCallMeta)
// carries no run labels today. A label-backed field on a bound tool would
// therefore compile to code that can never receive a value over the wire,
// silently failing at runtime for every registry-served call instead of
// failing loudly here. Until the wire protocol grows label support (a named
// follow-up), bound tools may only Inject() the fixed ToolCallMeta-backed
// names.
func validateNoLabelBackedInjectOnBoundTool(t *ToolExpr, verr *eval.ValidationErrors) {
	for _, name := range t.InjectedFields {
		if name == "" {
			continue
		}
		if _, ok := runtimeMetaFieldNames[codegen.Goify(name, true)]; ok {
			continue
		}
		verr.Add(t, "Inject field %q is label-backed, but tool %q is bound to a service method via BindTo; "+
			"registry-served (bound) tools can only inject the fixed ToolCallMeta-backed names "+
			"(sessionId, runId, turnId, toolCallId, parentToolCallId) because the toolregistry wire "+
			"protocol does not carry run labels yet -- leave this tool unbound to use a label-backed field", name, t.Name)
	}
}

func (t *ToolExpr) validateShapes() error {
	verr := new(eval.ValidationErrors)
	validateToolConfirmation(t, verr)
	check := func(where string, att *goaexpr.AttributeExpr) {
		validateContractShape(t, where, att, verr)
	}
	check("Args", t.Args)
	check("Return", t.Return)
	validateServerDataShapes(t, verr, check)
	validateBoundsShape(t, verr)
	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
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
	for _, sd := range t.ServerData {
		if sd == nil {
			continue
		}
		if sd.Kind == "" {
			verr.Add(t, "ServerData kind must be non-empty")
			continue
		}
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
	validatePagingField("Args", tool.Args, tool.Bounds.Paging.CursorField, true)
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
	validateBoundsField("total", goaexpr.Int, "Int", false, false, true)
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
