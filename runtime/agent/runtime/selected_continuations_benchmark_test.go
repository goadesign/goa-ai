package runtime

// These tests and benchmarks recover completed paging results from saved run
// records through the normal history reader. All tools and records are synthetic.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

func BenchmarkSelectedContinuationHistory(b *testing.B) {
	for _, form := range []string{"direct", "compiled", "static"} {
		for _, count := range []int{16, 64} {
			b.Run(fmt.Sprintf("%s/%d", form, count), func(b *testing.B) {
				rt, input := selectedContinuationHistoryFixture(b, form, count)
				outputs, err := rt.loadHistoricalContinuationOutputs(b.Context(), input, rt.toolSpecs)
				require.NoError(b, err)
				require.Len(b, outputs, count)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					outputs, err := rt.loadHistoricalContinuationOutputs(b.Context(), input, rt.toolSpecs)
					if err != nil || len(outputs) != count {
						b.Fatalf("read history: outputs=%d error=%v", len(outputs), err)
					}
				}
			})
		}
	}
}

func TestSelectedContinuationRestoresIndependentSavedResults(t *testing.T) {
	for _, form := range []string{"direct", "compiled", "static"} {
		t.Run(form, func(t *testing.T) {
			rt, input := selectedContinuationHistoryFixture(t, form, 4)
			outputs, err := rt.loadHistoricalContinuationOutputs(t.Context(), input, rt.toolSpecs)
			require.NoError(t, err)
			require.Len(t, outputs, 4)
			for index, output := range outputs {
				assert.Equal(t, fmt.Sprintf("query-%d", index), output.ToolCallID)
				assert.Equal(t, rawjson.Message(`{"value":1}`), output.Result)
				assert.Equal(t, "next", *output.Bounds.NextCursor)
			}
		})
	}
}

func TestSelectedContinuationSavedDeclarationValidation(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		bounds       *agent.Bounds
		failed       bool
		malformed    bool
		want         string
	}{
		{
			name:   "complete",
			bounds: &agent.Bounds{Returned: 1},
		},
		{name: "without bounds"},
		{
			name: "unfinished",
			bounds: &agent.Bounds{
				Returned:   1,
				Truncated:  true,
				NextCursor: pointer("next"),
			},
		},
		{
			name:   "changed declaration",
			change: "metadata",
			bounds: &agent.Bounds{
				Returned:   1,
				Truncated:  true,
				NextCursor: pointer("next"),
			},
		},
		{
			name:   "changed complete declaration",
			change: "metadata",
			bounds: &agent.Bounds{Returned: 1},
		},
		{
			name: "empty cursor",
			bounds: &agent.Bounds{
				Returned:   1,
				Truncated:  true,
				NextCursor: pointer(""),
			},
			want: "empty next_cursor",
		},
		{
			name: "untruncated cursor",
			bounds: &agent.Bounds{
				Returned:   1,
				NextCursor: pointer("next"),
			},
			want: "without truncation",
		},
		{
			name:   "truncated without next step",
			bounds: &agent.Bounds{Truncated: true},
			want:   "without next_cursor or refinement_hint",
		},
		{
			name:   "changed invalid metadata",
			change: "metadata",
			bounds: &agent.Bounds{NextCursor: pointer("")},
			want:   "empty next_cursor",
		},
		{
			name:   "retained failure",
			failed: true,
		},
		{
			name:      "malformed final binding",
			malformed: true,
			want:      "decode selected registry tool",
		},
		{
			name:      "malformed failed binding",
			malformed: true,
			failed:    true,
			want:      "decode selected registry tool",
		},
	} {
		for _, form := range historicalSavedForms {
			t.Run(tc.name+"/"+form, func(t *testing.T) {
				rt, definition, binding := historicalStaticFixture(t, tc.change, form)
				if tc.malformed {
					binding.Resolution = rawjson.Message(`{}`)
				}
				subject := genrunidentity.RunSubject{
					Executor: genrunidentity.NewRunExecutorAgent(
						genrunidentity.RunExecutorBranchAgent(definition.route.ID),
					),
				}
				call := ToolCall{
					Name:       "records.find",
					ToolCallID: "saved-query",
					Registry:   binding,
					Payload:    rawjson.Message(`{"value":1}`),
				}
				appendHistoricalHookEvent(t, rt.Store, newToolCallScheduledEvent("saved", subject, "session-1", call, "", "", 0), "call", 1)
				result := hooks.NewToolResultReceivedEvent("saved", subject, "session-1", "saved", call.Name, call.ToolCallID, "", rawjson.Message(`{"saved_value":1}`), nil, nil, "Saved page", tc.bounds, 0, nil, nil)
				if tc.failed {
					result.ResultJSON = nil
					result.ResultBytes = 0
					result.Failure = testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "Recorded failure")
				}
				appendHistoricalHookEvent(t, rt.Store, result, "result", 2)
				page, err := rt.Store.ListRunRecords(t.Context(), "saved", "", 256)
				require.NoError(t, err)
				input := &resolvedPlanActivityInput{
					PlanActivityInput: &PlanActivityInput{
						AgentID:      definition.route.ID,
						RunID:        "saved",
						HistoryEndID: page.Events[len(page.Events)-1].ID,
						RunContext: run.Context{
							RunID:     "saved",
							SessionID: "session-1",
						},
					},
				}
				outputs, err := rt.loadHistoricalContinuationOutputs(t.Context(), input, rt.toolSpecs)
				if tc.want != "" {
					assert.ErrorContains(t, err, tc.want)
					return
				}
				require.NoError(t, err)
				if tc.failed {
					assert.Empty(t, outputs)
					return
				}
				require.Len(t, outputs, 1)
				assert.Equal(t, result.ResultJSON, outputs[0].Result)
				assert.Equal(t, tc.bounds, outputs[0].Bounds)
				assert.Equal(t, binding, outputs[0].Registry)
			})
		}
	}
}

func TestSelectedContinuationStopsBeforeLaterSavedResults(t *testing.T) {
	rt, input := selectedContinuationHistoryFixture(t, "compiled", 2)
	page, err := rt.Store.ListRunRecords(t.Context(), "saved", "", 256)
	require.NoError(t, err)
	for _, event := range page.Events {
		if event.Type == hooks.ToolResultReceived {
			input.HistoryEndID = event.ID
			break
		}
	}
	outputs, err := rt.loadHistoricalContinuationOutputs(t.Context(), input, rt.toolSpecs)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	assert.Equal(t, "query-0", outputs[0].ToolCallID)
}

// selectedContinuationHistoryFixture stores count independent completed
// queries in one selected run. The returned input has no current outputs, so
// each read reconstructs saved work.
func selectedContinuationHistoryFixture(t testing.TB, form string, count int) (*Runtime, *resolvedPlanActivityInput) {
	t.Helper()
	rt := newTestRuntimeWithPlanner("records.agent", &stubPlanner{})
	var binding *tools.RegistryBinding
	search, continuation := continuationTestSpecs()
	if form != "static" {
		registered := testRegistryPagingResolution(strings.Repeat("a", 64))
		resolve := registrycontract.Resolve
		if form == "compiled" {
			resolve = registrycontract.ResolveCompiled
		}
		resolved, err := resolve(registered)
		require.NoError(t, err)
		binding, err = resolved.Select("company", "records.find")
		require.NoError(t, err)
		search, continuation = resolved.Specs["records.find"], resolved.Specs["records.continue"]
		def := testAgentDefinition("records.agent", "records.workflow", "records.queue", []tools.ToolSpec{search, continuation}, nil).
			WithRegistryDeclarations(testDeclarations(registered.Toolset.Tools))
		registration := rt.agents[def.route.ID]
		registration.Definition = def
		rt.agents[def.route.ID] = registration
	}
	seedTestToolSpecs(rt, search, continuation)
	subject := genrunidentity.RunSubject{
		Executor: genrunidentity.NewRunExecutorAgent(
			genrunidentity.RunExecutorBranchAgent("records.agent"),
		),
	}
	endID := ""
	for index := range count {
		id := fmt.Sprintf("query-%d", index)
		call := ToolCall{
			Name:       search.Name,
			ToolCallID: id,
			Registry:   binding,
			Payload:    rawjson.Message(`{"value":1}`),
		}
		for offset, event := range []hooks.Event{
			newToolCallScheduledEvent("saved", subject, "session-1", call, "", "", 0),
			hooks.NewToolResultReceivedEvent(
				"saved", subject, "session-1", "saved", search.Name, id, "",
				rawjson.Message(`{"value":1}`), nil, nil, "Saved page",
				&agent.Bounds{
					Returned:   1,
					Truncated:  true,
					NextCursor: pointer("next"),
				},
				0, nil, nil,
			),
		} {
			record := testHookRecord(t, event, fmt.Sprintf("%s-%d", id, offset), time.UnixMilli(int64(index*2+offset+1)))
			appended, err := rt.Store.AppendRunRecord(t.Context(), record)
			require.NoError(t, err)
			endID = appended.ID
		}
	}
	return rt, &resolvedPlanActivityInput{
		PlanActivityInput: &PlanActivityInput{
			AgentID:      "records.agent",
			RunID:        "saved",
			HistoryEndID: endID,
			RunContext: run.Context{
				RunID:     "saved",
				SessionID: "session-1",
			},
		},
	}
}
