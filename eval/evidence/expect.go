// This file owns the deterministic assertion engine: declarative expectations
// evaluated against Evidence into eval.Check results. Exact trajectories
// compare the complete causal trajectory call for call; contains trajectories
// bind declared tools to an in-order subsequence of observed calls. Payload
// assertions are typed Go predicates over values decoded by the generated
// tool codecs, so a design change that renames or retypes a field breaks the
// evaluation suite at compile time instead of silently never matching.

package evidence

import (
	"fmt"
	"strings"

	"goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Expect declares every deterministic assertion for one scenario.
	Expect struct {
		// Exact requires the complete observed trajectory to equal the
		// declared tool sequence call for call. False binds declared tools as
		// an in-order subsequence and leaves undeclared calls unconstrained.
		Exact bool
		// Tools are the declared tool expectations in causal order.
		Tools []Tool
		// ForbidTools rejects any observed call of the named tools. Use the
		// generated toolset constants so references are compile-checked.
		ForbidTools []tools.Ident
		// Confirmation identifies the tool whose confirmation dialog must be
		// pending when the scenario ends. Nil scenarios end with a completed
		// run instead.
		Confirmation *ExpectedConfirmation
	}

	// Assert applies one deterministic assertion to a canonical JSON payload.
	// Nil error means the payload satisfies the assertion. Build asserts with
	// Decoded and the generated typed codec; write a raw Assert by hand only
	// for registry-discovered tools that have no generated types.
	Assert func(rawjson.Message) error

	// Tool constrains one declared tool call and its result. Use the
	// generated toolset constants for Name so references are compile-checked.
	Tool struct {
		// Name is the fully qualified tool identifier.
		Name tools.Ident
		// Args asserts against the call's canonical JSON arguments. Nil
		// leaves the arguments unconstrained.
		Args Assert
		// Result asserts against the call's canonical JSON result payload.
		// Nil leaves the result unconstrained. Result asserts apply only to
		// successful calls; declaring both Result and FailureKind is a
		// contradiction the engine reports as a failure.
		Result Assert
		// FailureKind requires the call to end as a classified failure of
		// exactly this kind. Empty requires a successful result.
		FailureKind planner.FailureKind
		// RequireAllAttemptsSuccessful rejects every failed or missing result
		// for this tool, including attempts a contains trajectory would skip.
		RequireAllAttemptsSuccessful bool
		// ForbidFailureKinds rejects the listed failure kinds across every
		// attempt of this tool while leaving other retries bindable.
		ForbidFailureKinds []planner.FailureKind
	}

	// ExpectedConfirmation identifies the pending confirmation boundary.
	ExpectedConfirmation struct {
		// Tool is the tool whose execution must await approval.
		Tool tools.Ident
		// Args asserts against the pending call's canonical JSON payload.
		Args Assert
	}
)

// Decoded adapts a typed predicate into an Assert by decoding the canonical
// payload with the tool's generated codec (for example
// helpers.AnswerPayloadCodec). The codec owns JSON boundary validation, and
// the compiler checks the predicate against the codec's value type.
func Decoded[T any](codec tools.JSONCodec[T], assert func(T) error) Assert {
	return func(raw rawjson.Message) error {
		value, err := codec.FromJSON(raw)
		if err != nil {
			return fmt.Errorf("decode canonical payload: %w", err)
		}
		return assert(value)
	}
}

// Checks converts the expectation and the observed evidence into the
// scenario's deterministic checks. Every scenario receives a trajectory check
// and a terminal check; forbidden tools add a third when declared.
func (e Expect) Checks(evidence *Evidence) []eval.Check {
	checks := []eval.Check{check("trajectory", e.evaluateTrajectory(evidence))}
	if len(e.ForbidTools) > 0 {
		checks = append(checks, check("forbidden_tools", e.evaluateForbiddenTools(evidence)))
	}
	checks = append(checks, check("terminal", e.evaluateTerminal(evidence)))
	return checks
}

// evaluateTrajectory dispatches to exact or contains trajectory semantics.
func (e Expect) evaluateTrajectory(evidence *Evidence) []string {
	if e.Exact {
		return evaluateExactTrajectory(e.Tools, evidence)
	}
	return evaluateContainsTrajectory(e.Tools, evidence)
}

// evaluateForbiddenTools rejects every observed call of a forbidden tool.
func (e Expect) evaluateForbiddenTools(evidence *Evidence) []string {
	forbidden := make(map[tools.Ident]struct{}, len(e.ForbidTools))
	for _, name := range e.ForbidTools {
		forbidden[name] = struct{}{}
	}
	var failures []string
	for _, call := range evidence.ToolCalls {
		if _, prohibited := forbidden[call.Name]; prohibited {
			failures = append(failures, fmt.Sprintf("forbidden tool %s was called", call.Name))
		}
	}
	return failures
}

// evaluateTerminal verifies the scenario's single completing boundary: a
// completed run, or the declared pending confirmation. Answer emptiness is
// deliberately not asserted here; the eval runner labels claims not_addressed
// when the judged output is empty.
func (e Expect) evaluateTerminal(evidence *Evidence) []string {
	if e.Confirmation != nil {
		if evidence.Confirmation == nil {
			return []string{fmt.Sprintf(
				"no pending confirmation observed (terminal phase %q)",
				evidence.TerminalPhase,
			)}
		}
		var failures []string
		if evidence.Confirmation.ToolName != e.Confirmation.Tool {
			failures = append(failures, fmt.Sprintf(
				"confirmation tool: got %s, want %s",
				evidence.Confirmation.ToolName,
				e.Confirmation.Tool,
			))
		}
		if e.Confirmation.Args != nil {
			if err := e.Confirmation.Args(evidence.Confirmation.Payload); err != nil {
				failures = append(failures, fmt.Sprintf("confirmation payload: %v", err))
			}
		}
		return failures
	}
	if evidence.TerminalPhase != "completed" {
		diagnostic := fmt.Sprintf("run terminal phase: got %q, want completed", evidence.TerminalPhase)
		if evidence.TerminalFailure != nil {
			diagnostic += fmt.Sprintf(" (failure: %s)", evidence.TerminalFailure.Message)
		}
		return []string{diagnostic}
	}
	return nil
}

// check folds accumulated failure diagnostics into one named eval.Check.
func check(name string, failures []string) eval.Check {
	if len(failures) == 0 {
		return eval.Check{Name: name, Passed: true}
	}
	return eval.Check{Name: name, Passed: false, Diagnostic: strings.Join(failures, "; ")}
}

// evaluateExactTrajectory requires the complete observed trajectory to equal
// the declared sequence call for call, so hidden retries and extra tools are
// exact failures.
func evaluateExactTrajectory(expected []Tool, evidence *Evidence) []string {
	var failures []string
	calls := evidence.ToolCalls
	if len(calls) != len(expected) {
		failures = append(failures, fmt.Sprintf(
			"tool call count: got %d (%s), want %d (%s)",
			len(calls), observedToolNames(calls), len(expected), expectedToolNames(expected),
		))
	}
	for index := 0; index < min(len(calls), len(expected)); index++ {
		if calls[index].Name != expected[index].Name {
			failures = append(failures, fmt.Sprintf(
				"tool call %d: got %s, want %s", index+1, calls[index].Name, expected[index].Name,
			))
			continue
		}
		failures = append(failures, evaluateCall(calls[index], expected[index])...)
	}
	return failures
}

// evaluateContainsTrajectory requires the declared sequence as an in-order
// subsequence of the observed trajectory: each declared tool binds to the
// first later observed call with the same name whose argument and result
// assertions all pass, so a run may split work across several calls of one
// tool or recover from a rejected attempt. Undeclared calls are
// unconstrained; the scenario's claims guard the final outcome.
func evaluateContainsTrajectory(expected []Tool, evidence *Evidence) []string {
	failures := evaluateForbiddenFailureKinds(expected, evidence)
	next := 0
	for _, exp := range expected {
		if exp.RequireAllAttemptsSuccessful {
			failures = append(failures, evaluateAllAttemptsSuccessful(exp.Name, evidence)...)
		}
		matched := -1
		var candidateFailures []string
		for index := next; index < len(evidence.ToolCalls); index++ {
			if evidence.ToolCalls[index].Name != exp.Name {
				continue
			}
			callFailures := evaluateCall(evidence.ToolCalls[index], exp)
			if len(callFailures) == 0 {
				matched = index
				break
			}
			if candidateFailures == nil {
				candidateFailures = callFailures
			}
		}
		switch {
		case matched >= 0:
			next = matched + 1
		case candidateFailures != nil:
			failures = append(failures, fmt.Sprintf(
				"no %s call after position %d satisfies its assertions; first candidate failed: %s",
				exp.Name, next, strings.Join(candidateFailures, "; "),
			))
		default:
			failures = append(failures, fmt.Sprintf(
				"required tool %s not observed after position %d (%s)",
				exp.Name, next, observedToolNames(evidence.ToolCalls),
			))
		}
	}
	return failures
}

// evaluateForbiddenFailureKinds rejects protected failure kinds across every
// attempt of a declared tool while leaving other correctable retries eligible
// for contains-trajectory binding.
func evaluateForbiddenFailureKinds(expected []Tool, evidence *Evidence) []string {
	forbidden := make(map[tools.Ident]map[planner.FailureKind]struct{})
	for _, exp := range expected {
		if len(exp.ForbidFailureKinds) == 0 {
			continue
		}
		if forbidden[exp.Name] == nil {
			forbidden[exp.Name] = make(map[planner.FailureKind]struct{}, len(exp.ForbidFailureKinds))
		}
		for _, kind := range exp.ForbidFailureKinds {
			forbidden[exp.Name][kind] = struct{}{}
		}
	}
	var failures []string
	for _, call := range evidence.ToolCalls {
		kinds := forbidden[call.Name]
		if len(kinds) == 0 || call.Failure == nil {
			continue
		}
		if _, prohibited := kinds[call.Failure.Kind]; prohibited {
			failures = append(failures, fmt.Sprintf(
				"%s attempt %s failed with forbidden kind %s",
				call.Name, call.ToolCallID, call.Failure.Kind,
			))
		}
	}
	return failures
}

// evaluateAllAttemptsSuccessful rejects every missing or failed result for one
// declared tool, including attempts that a contains trajectory would otherwise
// skip while binding a later successful retry.
func evaluateAllAttemptsSuccessful(toolName tools.Ident, evidence *Evidence) []string {
	var failures []string
	for _, call := range evidence.ToolCalls {
		if call.Name != toolName {
			continue
		}
		switch {
		case !call.Completed:
			failures = append(failures, fmt.Sprintf("%s attempt %s has no tool result", toolName, call.ToolCallID))
		case call.Failure != nil:
			failures = append(failures, fmt.Sprintf(
				"%s attempt %s failed with kind %s: %s",
				toolName, call.ToolCallID, call.Failure.Kind, call.Failure.Error.Error(),
			))
		}
	}
	return failures
}

// evaluateCall applies one declared expectation's argument and result
// assertions to one observed call. Expectations without a FailureKind require
// a successful paired result; a declared FailureKind instead requires the call
// to end as a classified failure of exactly that kind.
func evaluateCall(call ToolCall, expected Tool) []string {
	var failures []string
	if expected.Args != nil {
		if err := expected.Args(call.Args); err != nil {
			failures = append(failures, fmt.Sprintf("%s arguments: %v", call.Name, err))
		}
	}
	if !call.Completed {
		return append(failures, fmt.Sprintf("%s has no tool result", call.Name))
	}
	if expected.FailureKind != "" {
		if expected.Result != nil {
			return append(failures, fmt.Sprintf(
				"%s expectation declares both a result assertion and failure kind %s",
				expected.Name, expected.FailureKind,
			))
		}
		if call.Failure == nil {
			return append(failures, fmt.Sprintf(
				"%s result: got success, want failure kind %s", call.Name, expected.FailureKind,
			))
		}
		if call.Failure.Kind != expected.FailureKind {
			return append(failures, fmt.Sprintf(
				"%s failure kind: got %s, want %s", call.Name, call.Failure.Kind, expected.FailureKind,
			))
		}
		return failures
	}
	if call.Failure != nil {
		return append(failures, fmt.Sprintf(
			"%s result: failed with kind %s: %s", call.Name, call.Failure.Kind, call.Failure.Error.Error(),
		))
	}
	if expected.Result != nil {
		if err := expected.Result(call.Result); err != nil {
			failures = append(failures, fmt.Sprintf("%s result: %v", call.Name, err))
		}
	}
	return failures
}

// observedToolNames formats the observed trajectory for one-line diagnostics.
func observedToolNames(calls []ToolCall) string {
	names := make([]string, len(calls))
	for index, call := range calls {
		names[index] = string(call.Name)
	}
	return strings.Join(names, " -> ")
}

// expectedToolNames formats the declared trajectory for one-line diagnostics.
func expectedToolNames(expected []Tool) string {
	names := make([]string, len(expected))
	for index, tool := range expected {
		names[index] = string(tool.Name)
	}
	return strings.Join(names, " -> ")
}
