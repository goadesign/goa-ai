package agent

import (
	"fmt"

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

		// Args defines the input parameter schema for this tool.
		Args *goaexpr.AttributeExpr

		// Return defines the output result schema for this tool.
		Return *goaexpr.AttributeExpr

		// Sidecar defines the optional typed artifact schema for this tool.
		// Sidecar data is never sent to the model provider; it is attached to
		// planner.ToolResult.Artifacts only and surfaced to UIs or policy
		// engines as auxiliary data (for example, full-fidelity artifacts
		// alongside bounded model-facing results).
		Sidecar *goaexpr.AttributeExpr
		// SidecarKind identifies the logical kind of the sidecar artifact
		// (for example, "atlas.time_series"). When empty, codegen derives a
		// default from the tool identifier.
		SidecarKind string

		// ArtifactsDefault controls whether sidecar artifacts are produced when
		// the caller does not explicitly set the reserved `artifacts` mode (or
		// sets it to "auto"). Valid values are "on" and "off". When empty, the
		// default is "on".
		ArtifactsDefault string

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

		// BoundedResult indicates that this tool's result is intended to be a
		// bounded view over a potentially larger data set. It is set by the
		// BoundedResult DSL helper and propagated into tool metadata so runtimes
		// and services can enforce and surface bounds consistently.
		BoundedResult bool

		// Paging optionally describes cursor-based pagination for this tool.
		// When set, codegen and runtimes can surface paging-aware guidance and
		// fill Bounds.NextCursor from the configured result field.
		Paging *ToolPagingExpr

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

	// ToolPagingExpr identifies the cursor field names used by a cursor-paged tool.
	// Field names refer to the tool payload and tool result schemas respectively.
	// The values carried by these fields are opaque cursors.
	ToolPagingExpr struct {
		// CursorField is the name of the optional String field in the tool payload
		// that carries the paging cursor for retrieving the next page.
		CursorField string
		// NextCursorField is the name of the optional String field in the tool result
		// that carries the cursor for the next page.
		NextCursorField string
	}

	// ToolPassthroughExpr defines deterministic forwarding for an exported tool.
	ToolPassthroughExpr struct {
		TargetService string
		TargetMethod  string
	}
)

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

// RecordBinding records the service and method names specified via the DSL.
func (t *ToolExpr) RecordBinding(serviceName, methodName string) {
	t.bindServiceName = serviceName
	t.bindMethodName = methodName
}

// Prepare ensures Args and Return are always non-nil attributes.
func (t *ToolExpr) Prepare() {
	if t.Args == nil {
		t.Args = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
	if t.Return == nil {
		t.Return = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
}

// Validate checks that any recorded binding can be resolved to an existing
// service and method.
func (t *ToolExpr) Validate() error {
	if t.bindMethodName == "" {
		return t.validateShapes()
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
			validateInjectedFields(t, m, verr)
			if err := t.validateShapes(); err != nil {
				verr.AddError(t, err)
				return verr
			}
			return nil
		}
	}
	verr.Add(t, "service method %q not found in service %q", t.bindMethodName, svc.Name)
	return verr
}

func validateInjectedFields(t *ToolExpr, m *goaexpr.MethodExpr, verr *eval.ValidationErrors) {
	if t == nil || len(t.InjectedFields) == 0 {
		return
	}
	if m == nil || m.Payload == nil || m.Payload.Type == nil || m.Payload.Type == goaexpr.Empty {
		verr.Add(t, "Inject requires a non-empty bound method payload")
		return
	}

	att := m.Payload
	if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
		att = ut.Attribute()
	}
	obj, ok := att.Type.(*goaexpr.Object)
	if !ok || obj == nil {
		verr.Add(t, "Inject requires the bound method payload to be an object")
		return
	}

	required := make(map[string]struct{})
	if att.Validation != nil {
		for _, r := range att.Validation.Required {
			required[r] = struct{}{}
		}
	}

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

		if !isSupportedInjectedField(name) {
			verr.Add(t, "Inject field %q is not supported (supported: %s)", name, supportedInjectedFieldsList())
			continue
		}

		var field *goaexpr.NamedAttributeExpr
		for _, na := range *obj {
			if na.Name == name {
				field = na
				break
			}
		}
		if field == nil || field.Attribute == nil || field.Attribute.Type == nil || field.Attribute.Type == goaexpr.Empty {
			verr.Add(t, "Inject field %q does not exist on bound method payload", name)
			continue
		}
		if _, ok := required[name]; !ok {
			verr.Add(t, "Inject field %q must be required on the bound method payload", name)
			continue
		}
		if field.Attribute.Type != goaexpr.String {
			verr.Add(t, "Inject field %q must be a String on the bound method payload", name)
			continue
		}
	}
}

func isSupportedInjectedField(name string) bool {
	switch name {
	case "run_id", "session_id", "turn_id", "tool_call_id", "parent_tool_call_id":
		return true
	default:
		return false
	}
}

func supportedInjectedFieldsList() string {
	return `"run_id", "session_id", "turn_id", "tool_call_id", "parent_tool_call_id"`
}

func (t *ToolExpr) validateShapes() error {
	verr := new(eval.ValidationErrors)
	validateToolConfirmation(t, verr)
	check := func(where string, att *goaexpr.AttributeExpr) {
		if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
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
		verr.Add(t, "%s must be a user type, primitive, or composite shape", where)
	}
	check("Args", t.Args)
	check("Return", t.Return)
	check("Sidecar", t.Sidecar)
	validatePagingShape(t, verr)
	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}

func validatePagingShape(tool *ToolExpr, verr *eval.ValidationErrors) {
	if tool == nil || verr == nil || tool.Paging == nil {
		return
	}
	if !tool.BoundedResult {
		verr.Add(tool, "Paging configuration requires BoundedResult()")
		return
	}
	validatePagingField := func(where string, att *goaexpr.AttributeExpr, name string) {
		if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
			verr.Add(tool, "%s must be non-empty when configuring paging", where)
			return
		}

		field := att.Find(name)
		if field == nil || field.Type == nil || field.Type == goaexpr.Empty {
			verr.Add(tool, "%s must define an optional String field named %q when configuring paging", where, name)
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

	if tool.Paging.CursorField == "" {
		verr.Add(tool, "Cursor() is required when configuring paging")
		return
	}
	if tool.Paging.NextCursorField == "" {
		verr.Add(tool, "NextCursor() is required when configuring paging")
		return
	}
	validatePagingField("Args", tool.Args, tool.Paging.CursorField)
	validatePagingField("Return", tool.Return, tool.Paging.NextCursorField)
}

// Finalize resolves and assigns the bound method after successful validation.
func (t *ToolExpr) Finalize() {
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

// BoundServiceName returns the service name specified via BindTo, if any.
func (t *ToolExpr) BoundServiceName() string {
	return t.bindServiceName
}

// SetTitle implements expr.TitleHolder, allowing the Title() DSL function
// to set the tool title.
func (t *ToolExpr) SetTitle(title string) {
	t.Title = title
}
