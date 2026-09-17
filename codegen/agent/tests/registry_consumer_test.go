// These tests compile complete generated consumers and run their startup and
// tool execution paths against discovered registry schemas.
package tests

import (
	"os/exec"
	"testing"

	. "goa.design/goa-ai/dsl"
	goadsl "goa.design/goa/v3/dsl"
)

func TestGeneratedRegistryConsumer(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, registryConsumerDesign())
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "registryconsumer/consumer_test.go", registryConsumerTestSource)
	runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./..."))
}

// registryConsumerDesign combines generated tools, discovered tools, and a
// child agent that consumes the same provider through another service.
func registryConsumerDesign() func() {
	return func() {
		goadsl.API("registry-consumer", func() {})
		catalog := Registry("corp", func() {
			goadsl.URL("https://registry.example.invalid")
		})
		analytics := Toolset(FromRegistry(catalog, "analytics"), func() {
			goadsl.Version("1.2.3")
		})
		local := Toolset("local", func() {
			Tool("ping", "Check local tool registration.", func() {
				Args(func() {
					goadsl.Attribute("message", goadsl.String, "Message to return.")
					goadsl.Required("message")
				})
				Return(goadsl.String)
			})
		})
		goadsl.Service("beta", func() {
			Agent("worker", "Worker with discovered tools.", func() {
				Use(analytics)
				Export("work", func() {
					Tool("run", "Run delegated work.", func() {
						Args(func() {
							goadsl.Attribute("query", goadsl.String, "Requested work.")
							goadsl.Required("query")
						})
						Return(goadsl.String)
					})
				})
			})
		})
		goadsl.Service("alpha", func() {
			Agent("assistant", "Agent with static and discovered tools.", func() {
				Use(local)
				Use(analytics)
			})
			Agent("parent", "Parent sharing a registry toolset with its child.", func() {
				Use(analytics)
				Use(AgentToolset("beta", "worker", "work"))
			})
		})
	}
}

const registryConsumerTestSource = `package registryconsumer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genassistant "generated.local/gen/alpha/agents/assistant"
	genparent "generated.local/gen/alpha/agents/parent"
	gencorp "generated.local/gen/alpha/registry/corp"
	genanalytics "generated.local/gen/alpha/toolsets/analytics"
	genlocal "generated.local/gen/alpha/toolsets/local"
	genworker "generated.local/gen/beta/agents/worker"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/registry"
)

const (
	inputSchema = "{\"type\":\"object\",\"required\":[\"query\"],\"additionalProperties\":false,\"properties\":{\"query\":{\"type\":\"string\"}}}"
	outputSchema = "{\"type\":\"integer\",\"const\":9007199254740993}"
)

type registryPlanner struct{}

func (registryPlanner) PlanStart(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	found := false
	for _, tool := range input.Agent.AdvertisedToolDefinitions() {
		if tool.Name == "analytics.search" {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("discovered tool was not advertised")
	}
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name: "analytics.search",
		Payload: rawjson.Message("{\"query\":\"records\"}"),
	}}}, nil
}

func (registryPlanner) PlanResume(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	if len(input.ToolOutputs) != 1 || string(input.ToolOutputs[0].Result) != "9007199254740993" {
		return nil, fmt.Errorf("unexpected restored tool output: %#v", input.ToolOutputs)
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "done"}},
	}}}, nil
}

func catalogSchema() *genregistry.Toolset {
	version := genregistry.SemVer("1.2.3")
	description := "Search the available records."
	return &genregistry.Toolset{
		Name: "analytics",
		Version: &version,
		Tools: []*genregistry.ToolSchema{{
			Name: "analytics.search",
			Description: &description,
			Tags: []string{"read"},
			PayloadSchema: []byte(inputSchema),
			ExecutionPayloadSchema: []byte(inputSchema),
			ResultSchema: []byte(outputSchema),
		}},
	}
}

func catalogClient(schema *genregistry.Toolset) *registry.Client {
	return registry.NewClient(&genregistry.Client{
		GetToolsetEndpoint: func(_ context.Context, input any) (any, error) {
			if input.(*genregistry.GetToolsetPayload).Name != "analytics" {
				return nil, fmt.Errorf("wrong requested toolset")
			}
			return schema, nil
		},
	})
}

func TestDiscoveredToolsExecuteWithStaticTools(t *testing.T) {
	ctx := t.Context()
	discovered, err := genanalytics.Discover(ctx, catalogClient(catalogSchema()))
	require.NoError(t, err)
	toolsets := genassistant.RegistryToolsets{Analytics: discovered}
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	require.NoError(t, genassistant.RegisterAssistantAgent(ctx, rt, genassistant.AssistantAgentConfig{
		Planner: registryPlanner{},
		RegistryToolsets: toolsets,
	}))
	require.Len(t, rt.ToolSpecsForAgent(genassistant.AgentID), 2)
	var executions atomic.Int64
	remote := runtime.ToolCallExecutorFunc(func(_ context.Context, _ *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		executions.Add(1)
		if call.Name != "analytics.search" || string(call.Payload) != "{\"query\":\"records\"}" {
			return nil, fmt.Errorf("unexpected call: %#v", call)
		}
		return runtime.Executed(&planner.ToolResult{
			Name: call.Name,
			ToolCallID: call.ToolCallID,
			Result: int64(9007199254740993),
		}), nil
	})
	local := runtime.ToolCallExecutorFunc(func(context.Context, *runtime.ToolCallMeta, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		return nil, fmt.Errorf("local tool should not be selected")
	})
	require.NoError(t, genassistant.RegisterUsedToolsets(ctx, rt, toolsets,
		genassistant.WithAnalyticsExecutor(remote),
		genassistant.WithLocalExecutor(local),
	))
	client, err := genassistant.NewClient(rt, toolsets)
	require.NoError(t, err)
	out, err := client.OneShotRun(ctx, []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "Find records."}},
	}}, runtime.WithRunID("registry-consumer-run"))
	require.NoError(t, err)
	require.NotNil(t, out.Final)
	assert.Equal(t, int64(1), executions.Load())
	assert.Equal(t, "done", out.Final.Parts[0].(model.TextPart).Text)

	// A later catalog read cannot change the already registered definitions.
	later := catalogSchema()
	second := *later.Tools[0]
	second.Name = "analytics.added"
	later.Tools = append(later.Tools, &second)
	updated, err := genanalytics.Discover(ctx, catalogClient(later))
	require.NoError(t, err)
	require.Len(t, updated.Specs(), 2)
	require.Len(t, discovered.Specs(), 1)
	require.Len(t, rt.ToolSpecsForAgent(genassistant.AgentID), 2)
}

func TestRegistryInputsAreRequiredAndPinned(t *testing.T) {
	ctx := t.Context()
	_, err := genassistant.Definition(genassistant.RegistryToolsets{})
	require.ErrorContains(t, err, "required")
	wrong := catalogSchema()
	version := genregistry.SemVer("2.0.0")
	wrong.Version = &version
	_, err = genanalytics.Discover(ctx, catalogClient(wrong))
	require.ErrorContains(t, err, "requires version")
	wrong = catalogSchema()
	wrong.Name = "other"
	_, err = genanalytics.Discover(ctx, catalogClient(wrong))
	require.ErrorContains(t, err, "returned toolset")
	wrong = catalogSchema()
	wrong.Tools[0].ExecutionPayloadSchema = nil
	_, err = genanalytics.Discover(ctx, catalogClient(wrong))
	require.ErrorContains(t, err, "execution payload")
}

func TestRegistryInputRejectsAnotherToolset(t *testing.T) {
	source := &registry.ToolsetSchema{
		Name: genlocal.Ping.Toolset(),
		Version: "1.2.3",
		Tools: []*registry.ToolSchema{{
			Name: string(genlocal.Ping),
			PayloadSchema: rawjson.Message(inputSchema),
			ExecutionPayloadSchema: rawjson.Message(inputSchema),
			ResultSchema: rawjson.Message(outputSchema),
		}},
	}
	discovered, err := registry.NewToolset(source)
	require.NoError(t, err)
	_, err = genassistant.Definition(genassistant.RegistryToolsets{Analytics: discovered})
	require.Error(t, err)
}

func TestParentAndChildShareOneDiscoveredInput(t *testing.T) {
	ctx := t.Context()
	discovered, err := genanalytics.Discover(ctx, catalogClient(catalogSchema()))
	require.NoError(t, err)
	definition, err := genparent.Definition(genparent.RegistryToolsets{Analytics: discovered})
	require.NoError(t, err)
	child, ok := definition.ChildDefinition(genworker.AgentID)
	require.True(t, ok)
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	assert.Equal(t, genworker.AgentID, child.Route().ID)
	require.NoError(t, genworker.RegisterWorkerAgent(ctx, rt, genworker.WorkerAgentConfig{
		Planner: registryPlanner{},
		RegistryToolsets: genworker.RegistryToolsets{Analytics: discovered},
	}))
	names := make([]string, 0)
	for _, spec := range rt.ToolSpecsForAgent(genworker.AgentID) {
		names = append(names, string(spec.Name))
	}
	assert.Contains(t, names, "analytics.search")
}

func TestGeneratedHTTPClientUsesTheDiscoveryContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/toolsets/analytics" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprintf(w, "{\"name\":\"analytics\",\"version\":\"1.2.3\",\"tools\":[{\"name\":\"analytics.search\",\"payloadSchema\":%s,\"executionPayloadSchema\":%s,\"resultSchema\":%s}]}", inputSchema, inputSchema, outputSchema)
		if err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := gencorp.NewClient(gencorp.WithEndpoint(server.URL))
	var catalog registry.RegistryClient = client
	discovered, err := genanalytics.Discover(t.Context(), catalog)
	require.NoError(t, err)
	require.Len(t, discovered.Specs(), 1)
}

func TestGeneratedHTTPClientDoesNotRetryRejectedRequests(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 501} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				http.Error(w, "request rejected", status)
			}))
			defer server.Close()
			client := gencorp.NewClient(gencorp.WithEndpoint(server.URL), gencorp.WithRetry(2, 1))
			_, err := client.GetToolset(t.Context(), "analytics")
			require.Error(t, err)
			assert.Equal(t, int64(1), requests.Load())
		})
	}
}

func TestGeneratedHTTPClientRetriesTemporaryFailures(t *testing.T) {
	for _, status := range []int{408, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				http.Error(w, "temporarily unavailable", status)
			}))
			defer server.Close()
			client := gencorp.NewClient(gencorp.WithEndpoint(server.URL), gencorp.WithRetry(2, 1))
			_, err := client.GetToolset(t.Context(), "analytics")
			require.Error(t, err)
			assert.Equal(t, int64(3), requests.Load())
		})
	}
}
`
