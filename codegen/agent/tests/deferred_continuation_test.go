// These tests run generated pagination tools to ensure authored deferral does
// not change the runtime's immediately available continuation actions.
package tests

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestGeneratedDeferredContinuation(t *testing.T) {
	for _, selection := range []string{"query", "continuation", "both", "all"} {
		t.Run(selection, func(t *testing.T) {
			files := buildCompleteGeneratedFiles(t, func() {
				API("discovery", func() {})
				page := Type("Page", func() {
					Attribute("items", ArrayOf(String), "Matching records.")
					Required("items")
				})
				Service("discovery", func() {
					Agent("reader", "Search records.", func() {
						Use("records", func() {
							switch selection {
							case "query":
								Deferred("search")
							case "continuation":
								Deferred("continue_search")
							case "both":
								Deferred("search")
								Deferred("continue_search")
							case "all":
								Deferred()
							}
							Tool("search", "Search records.", func() {
								Args(func() {
									Attribute("query", String, "Text to search.")
									Required("query")
								})
								Return(page)
								BoundedResult(func() {
									ContinueWith("continue_search", "cursor")
									NextCursor("next_cursor")
								})
							})
							Tool("continue_search", "Continue searching records.", func() {
								Args(func() {
									Attribute("cursor", String, "Position supplied by the runtime.")
									Required("cursor")
								})
								Return(page)
								BoundedResult(func() {
									Cursor("cursor")
									NextCursor("next_cursor")
								})
							})
						})
					})
				})
			})
			root := writeCompleteGeneratedModule(t, files)
			source := strings.ReplaceAll(deferredContinuationTestSource, "QUERY_DEFERRED", strconv.FormatBool(selection != "continuation"))
			writeGeneratedPackageTest(t, root, "discovery/continuation_test.go", source)
			runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./..."))
		})
	}
}

const deferredContinuationTestSource = `package discovery

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	genreader "generated.local/gen/discovery/agents/reader"
	genrecords "generated.local/gen/discovery/toolsets/records"
	"goa.design/goa-ai/runtime/agent"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
)

type pagingPlanner struct{}

func (pagingPlanner) PlanStart(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	definitions := input.Agent.AdvertisedToolDefinitions()
	if len(definitions) != 1 || definitions[0].Name != "records.search" || definitions[0].Deferred != QUERY_DEFERRED {
		return nil, fmt.Errorf("unexpected initial tools: %#v", definitions)
	}
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name: genrecords.Search, Payload: rawjson.Message("{\"query\":\"notes\"}"),
	}}}, nil
}

func (pagingPlanner) PlanResume(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	if len(input.ToolOutputs) != 1 {
		return nil, fmt.Errorf("query returned %d outputs", len(input.ToolOutputs))
	}
	if input.ToolOutputs[0].Failure != nil {
		var causes []string
		for cause := input.ToolOutputs[0].Failure.Error; cause != nil; cause = cause.Cause {
			causes = append(causes, cause.Message)
		}
		return nil, fmt.Errorf("query failed: %s", strings.Join(causes, ": "))
	}
	definitions := input.Agent.AdvertisedToolDefinitions()
	if len(definitions) != 2 {
		return nil, fmt.Errorf("expected query and continuation: %#v", definitions)
	}
	for _, definition := range definitions {
		switch {
		case definition.Name == "records.search":
			if definition.Deferred != QUERY_DEFERRED {
				return nil, fmt.Errorf("query loading choice changed")
			}
		case runtime.IsGeneratedContinuationToolName(tools.Ident(definition.Name)):
			if definition.Deferred || !definition.NoArguments {
				return nil, fmt.Errorf("continuation must remain eager and take no arguments: %#v", definition)
			}
		default:
			return nil, fmt.Errorf("unexpected tool %q", definition.Name)
		}
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "continuation checked"}},
	}}}, nil
}

func TestGeneratedContinuationRemainsEager(t *testing.T) {
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	require.NoError(t, genreader.RegisterReaderAgent(t.Context(), rt, genreader.ReaderAgentConfig{Planner: pagingPlanner{}}))
	executor := runtime.ToolCallExecutorFunc(func(_ context.Context, _ *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		if call.Name != genrecords.Search {
			return nil, fmt.Errorf("unexpected execution %q", call.Name)
		}
		page, err := genrecords.SpecSearch().Result.Codec.FromJSON([]byte("{\"items\":[\"first\"],\"returned\":1,\"truncated\":true}"))
		if err != nil {
			return nil, err
		}
		cursor := "page-2"
		return &runtime.ToolExecutionResult{ToolResult: &planner.ToolResult{
			Name: call.Name, Result: page,
			Bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: &cursor},
		}}, nil
	})
	require.NoError(t, genreader.RegisterUsedToolsets(t.Context(), rt, genreader.WithRecordsExecutor(executor)))
	_, err := rt.MustClient(genreader.AgentID).OneShotRun(t.Context(), []*model.Message{{
		Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "find notes"}},
	}})
	require.NoError(t, err)
}
`
