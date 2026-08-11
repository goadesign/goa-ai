package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// listPayload mirrors a generated tool payload type for tests.
	listPayload struct {
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}

	// listResult mirrors a generated tool result type for tests.
	listResult struct {
		Items []listItem `json:"items"`
	}

	// listItem is one result member.
	listItem struct {
		ID string `json:"id"`
	}
)

// listPayloadCodec and listResultCodec stand in for generated typed codecs.
var (
	listPayloadCodec = tools.JSONCodec[*listPayload]{FromJSON: unmarshalJSON[listPayload]}
	listResultCodec  = tools.JSONCodec[*listResult]{FromJSON: unmarshalJSON[listResult]}
)

func TestExpectTrajectorySemantics(t *testing.T) {
	completedList := ToolCall{
		Name:       "svc.read.list",
		ToolCallID: "call-1",
		Args:       rawjson.Message(`{"kind":"pump","limit":10}`),
		Result:     rawjson.Message(`{"items":[{"id":"a"},{"id":"b"}]}`),
		Bounds:     &agent.Bounds{Returned: 2, Total: intPointer(3), Truncated: true},
		Completed:  true,
	}
	failedList := completedList
	failedList.ToolCallID = "call-0"
	failedList.Result = nil
	failedList.Failure = &planner.ToolFailure{
		Kind:  planner.FailureInvalidCall,
		Error: planner.NewToolError("limit too large"),
	}
	detail := ToolCall{
		Name:       "svc.read.detail",
		ToolCallID: "call-2",
		Args:       rawjson.Message(`{"id":"a"}`),
		Result:     rawjson.Message(`{"id":"a","ok":true}`),
		Completed:  true,
	}
	wantsPump := Decoded(listPayloadCodec, func(p *listPayload) error {
		if p.Kind != "pump" {
			return fmt.Errorf("kind: got %q, want pump", p.Kind)
		}
		return nil
	})
	hasItemA := Decoded(listResultCodec, func(r *listResult) error {
		if !slices.ContainsFunc(r.Items, func(item listItem) bool { return item.ID == "a" }) {
			return errors.New(`no item with id "a"`)
		}
		return nil
	})

	tests := []struct {
		name        string
		expect      Expect
		evidence    Evidence
		wantFailure string
	}{
		{
			name: "exact trajectory matches call for call",
			expect: Expect{Exact: true, Tools: []Tool{
				{Name: "svc.read.list", Args: wantsPump, Result: hasItemA},
				{Name: "svc.read.detail"},
			}},
			evidence: Evidence{ToolCalls: []ToolCall{completedList, detail}, TerminalPhase: "completed"},
		},
		{
			name: "contains trajectory checks bounded-result metadata",
			expect: Expect{Tools: []Tool{{
				Name: "svc.read.list",
				Bounds: func(bounds *agent.Bounds) error {
					if bounds == nil {
						return errors.New("bounds are missing")
					}
					if bounds.Returned != 2 || bounds.Total == nil || *bounds.Total != 3 || !bounds.Truncated {
						return fmt.Errorf("got %+v, want returned=2 total=3 truncated=true", bounds)
					}
					return nil
				},
			}}},
			evidence: Evidence{ToolCalls: []ToolCall{completedList}, TerminalPhase: "completed"},
		},
		{
			name: "contains trajectory reports bounded-result mismatch",
			expect: Expect{Tools: []Tool{{
				Name: "svc.read.list",
				Bounds: func(bounds *agent.Bounds) error {
					return fmt.Errorf("returned: got %d, want 3", bounds.Returned)
				},
			}}},
			evidence:    Evidence{ToolCalls: []ToolCall{completedList}, TerminalPhase: "completed"},
			wantFailure: "svc.read.list bounds: returned: got 2, want 3",
		},
		{
			name:        "exact trajectory rejects extra call",
			expect:      Expect{Exact: true, Tools: []Tool{{Name: "svc.read.list"}}},
			evidence:    Evidence{ToolCalls: []ToolCall{completedList, detail}, TerminalPhase: "completed"},
			wantFailure: "tool call count",
		},
		{
			name:     "contains trajectory binds past failed retry",
			expect:   Expect{Tools: []Tool{{Name: "svc.read.list", Result: hasItemA}}},
			evidence: Evidence{ToolCalls: []ToolCall{failedList, completedList}, TerminalPhase: "completed"},
		},
		{
			name:        "contains trajectory reports missing tool",
			expect:      Expect{Tools: []Tool{{Name: "svc.read.missing"}}},
			evidence:    Evidence{ToolCalls: []ToolCall{completedList}, TerminalPhase: "completed"},
			wantFailure: "required tool svc.read.missing not observed",
		},
		{
			name: "contains trajectory reports first candidate diagnostics",
			expect: Expect{Tools: []Tool{{
				Name: "svc.read.list",
				Args: Decoded(listPayloadCodec, func(p *listPayload) error {
					if p.Limit != 5 {
						return fmt.Errorf("limit: got %d, want 5", p.Limit)
					}
					return nil
				}),
			}}},
			evidence:    Evidence{ToolCalls: []ToolCall{completedList}, TerminalPhase: "completed"},
			wantFailure: "limit: got 10, want 5",
		},
		{
			name:        "forbidden failure kind rejects skipped retry",
			expect:      Expect{Tools: []Tool{{Name: "svc.read.list", ForbidFailureKinds: []planner.FailureKind{planner.FailureInvalidCall}}}},
			evidence:    Evidence{ToolCalls: []ToolCall{failedList, completedList}, TerminalPhase: "completed"},
			wantFailure: "forbidden kind invalid_call",
		},
		{
			name:        "all attempts successful rejects failed attempt",
			expect:      Expect{Tools: []Tool{{Name: "svc.read.list", RequireAllAttemptsSuccessful: true}}},
			evidence:    Evidence{ToolCalls: []ToolCall{failedList, completedList}, TerminalPhase: "completed"},
			wantFailure: "failed with kind invalid_call",
		},
		{
			name:     "declared failure kind requires classified failure",
			expect:   Expect{Tools: []Tool{{Name: "svc.read.list", FailureKind: planner.FailureInvalidCall}}},
			evidence: Evidence{ToolCalls: []ToolCall{failedList}, TerminalPhase: "completed"},
		},
		{
			name:        "declared failure kind rejects success",
			expect:      Expect{Tools: []Tool{{Name: "svc.read.list", FailureKind: planner.FailureInvalidCall}}},
			evidence:    Evidence{ToolCalls: []ToolCall{completedList}, TerminalPhase: "completed"},
			wantFailure: "got success, want failure kind invalid_call",
		},
		{
			name: "failure kind with result assertion is a contradiction",
			expect: Expect{Tools: []Tool{{
				Name:        "svc.read.list",
				FailureKind: planner.FailureInvalidCall,
				Result:      hasItemA,
			}}},
			evidence:    Evidence{ToolCalls: []ToolCall{failedList}, TerminalPhase: "completed"},
			wantFailure: "declares both a result assertion and failure kind",
		},
		{
			name:        "incomplete call reports missing result",
			expect:      Expect{Tools: []Tool{{Name: "svc.read.list"}}},
			evidence:    Evidence{ToolCalls: []ToolCall{{Name: "svc.read.list", ToolCallID: "call-3", Args: rawjson.Message(`{}`)}}},
			wantFailure: "has no tool result",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checks := test.expect.Checks(&test.evidence)
			trajectory := findCheck(t, checks, "trajectory")
			if test.wantFailure == "" {
				assert.True(t, trajectory.Passed, trajectory.Diagnostic)
				return
			}
			require.False(t, trajectory.Passed)
			assert.Contains(t, trajectory.Diagnostic, test.wantFailure)
		})
	}
}

func TestExpectForbiddenTools(t *testing.T) {
	expect := Expect{ForbidTools: []tools.Ident{"svc.write.update"}}
	evidence := Evidence{
		ToolCalls:     []ToolCall{{Name: "svc.write.update", ToolCallID: "call-1", Completed: true}},
		TerminalPhase: "completed",
	}
	forbidden := findCheck(t, expect.Checks(&evidence), "forbidden_tools")
	require.False(t, forbidden.Passed)
	assert.Contains(t, forbidden.Diagnostic, "forbidden tool svc.write.update was called")
}

func TestExpectTerminalSemantics(t *testing.T) {
	t.Run("completed run passes", func(t *testing.T) {
		terminal := findCheck(t, Expect{}.Checks(&Evidence{TerminalPhase: "completed"}), "terminal")
		assert.True(t, terminal.Passed)
	})
	t.Run("failed run reports failure", func(t *testing.T) {
		evidence := Evidence{TerminalPhase: "failed"}
		terminal := findCheck(t, Expect{}.Checks(&evidence), "terminal")
		require.False(t, terminal.Passed)
		assert.Contains(t, terminal.Diagnostic, `got "failed", want completed`)
	})
	t.Run("declared confirmation must be pending", func(t *testing.T) {
		expect := Expect{Confirmation: &ExpectedConfirmation{
			Tool: "svc.write.update",
			Args: Decoded(listPayloadCodec, func(p *listPayload) error {
				if p.Limit != 7 {
					return fmt.Errorf("limit: got %d, want 7", p.Limit)
				}
				return nil
			}),
		}}
		evidence := Evidence{Confirmation: &Confirmation{
			ToolName: "svc.write.update",
			Payload:  rawjson.Message(`{"kind":"pump","limit":7}`),
		}}
		terminal := findCheck(t, expect.Checks(&evidence), "terminal")
		assert.True(t, terminal.Passed, terminal.Diagnostic)

		missing := findCheck(t, expect.Checks(&Evidence{TerminalPhase: "completed"}), "terminal")
		require.False(t, missing.Passed)
		assert.Contains(t, missing.Diagnostic, "no pending confirmation observed")
	})
}

func TestTypedConstructorsBindDescriptorPairing(t *testing.T) {
	descriptor := tools.TypedTool[*listPayload, *listResult]{
		Name:    "svc.read.list",
		Payload: listPayloadCodec,
		Result:  listResultCodec,
	}

	t.Run("ExpectCall checks both sides against the descriptor codecs", func(t *testing.T) {
		expectation := ExpectCall(descriptor,
			func(p *listPayload) error {
				if p.Kind != "pump" {
					return fmt.Errorf("kind: got %q, want pump", p.Kind)
				}
				return nil
			},
			func(r *listResult) error {
				if len(r.Items) == 0 {
					return errors.New("no items")
				}
				return nil
			},
		)
		assert.Equal(t, tools.Ident("svc.read.list"), expectation.Name)
		require.NotNil(t, expectation.Args)
		require.NotNil(t, expectation.Result)
		require.NoError(t, expectation.Args(rawjson.Message(`{"kind":"pump","limit":1}`)))
		require.ErrorContains(t, expectation.Args(rawjson.Message(`{"kind":"fan","limit":1}`)), "want pump")
		require.ErrorContains(t, expectation.Result(rawjson.Message(`{"items":[]}`)), "no items")
	})
	t.Run("ExpectCall leaves nil predicates unconstrained", func(t *testing.T) {
		expectation := ExpectCall(descriptor, nil, nil)
		assert.Nil(t, expectation.Args)
		assert.Nil(t, expectation.Result)
	})
	t.Run("ExpectFailure declares the kind without a result assertion", func(t *testing.T) {
		expectation := ExpectFailure(descriptor, planner.FailureInvalidCall, nil)
		assert.Equal(t, planner.FailureInvalidCall, expectation.FailureKind)
		assert.Nil(t, expectation.Result)
	})
	t.Run("ExpectConfirmation checks the pending payload", func(t *testing.T) {
		confirmation := ExpectConfirmation(descriptor, func(p *listPayload) error {
			if p.Limit != 7 {
				return fmt.Errorf("limit: got %d, want 7", p.Limit)
			}
			return nil
		})
		assert.Equal(t, tools.Ident("svc.read.list"), confirmation.Tool)
		require.NoError(t, confirmation.Args(rawjson.Message(`{"kind":"pump","limit":7}`)))
		require.ErrorContains(t, confirmation.Args(rawjson.Message(`{"kind":"pump","limit":9}`)), "want 7")
	})
}

func TestDecodedReportsCodecRejections(t *testing.T) {
	assert := Decoded(listPayloadCodec, func(*listPayload) error { return nil })
	err := assert(rawjson.Message(`{"kind":`))
	require.Error(t, err)
	require.ErrorContains(t, err, "decode canonical payload")
}

// unmarshalJSON is a test stand-in for generated FromJSON functions.
func unmarshalJSON[T any](data []byte) (*T, error) {
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func intPointer(value int) *int {
	return &value
}

// findCheck returns the named check and fails the test when it is missing.
func findCheck(t *testing.T, checks []eval.Check, name string) eval.Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in %s", name, checkNames(checks))
	return eval.Check{}
}

// checkNames formats check names for failure messages.
func checkNames(checks []eval.Check) string {
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}
