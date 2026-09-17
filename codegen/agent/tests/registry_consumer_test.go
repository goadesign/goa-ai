// These tests compile generated registry readers and execute discovered tools
// through the runtime without installing per-tool startup registrations.
package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	goadsl "goa.design/goa/v3/dsl"
)

func TestGeneratedRegistryConsumer(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, registryConsumerDesign())
	root := writeCompleteGeneratedModule(t, files)
	for _, test := range []struct {
		agent string
		read  string
	}{
		{"assistant", `catalog.IncludeToolset(ctx, "corp", "analytics", "1.2.3", true)`},
		{"catalog", `catalog.IncludeRegistry(ctx, "corp", true)`},
	} {
		// #nosec G304 -- paths identify fixed files in this generated test module.
		data, err := os.ReadFile(filepath.Join(root, "gen", "alpha", "agents", test.agent, "agent.go"))
		require.NoError(t, err)
		source := string(data)
		require.Contains(t, source, test.read)
		require.Contains(t, source, "WithRegistryTools(registryTools{})")
		require.NotContains(t, source, "RegistryToolsets")
		require.NotContains(t, source, "\n\tfor ")
	}
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
				Use(analytics, func() { Deferred() })
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
			Agent("catalog", "Consume the whole registry.", func() {
				Use(catalog, func() { Deferred() })
			})
			Agent("assistant", "Agent with static and discovered tools.", func() {
				Use(local)
				Use(analytics, func() { Deferred() })
			})
			Agent("parent", "Parent sharing a registry toolset with its child.", func() {
				Use(analytics, func() { Deferred() })
				Use(AgentToolset("beta", "worker", "work"))
			})
		})
	}
}

const registryConsumerTestSource = `package registryconsumer

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    genassistant "generated.local/gen/alpha/agents/assistant"
    gencatalog "generated.local/gen/alpha/agents/catalog"
    genparent "generated.local/gen/alpha/agents/parent"
    gencorp "generated.local/gen/alpha/registry/corp"
    genworker "generated.local/gen/beta/agents/worker"
    pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
    genregistry "goa.design/goa-ai/registry/gen/registry"
    engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
    "goa.design/goa-ai/runtime/agent/model"
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/rawjson"
    "goa.design/goa-ai/runtime/agent/runtime"
    aistream "goa.design/goa-ai/runtime/agent/stream"
    "goa.design/goa-ai/runtime/agent/tools"
    storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
    "goa.design/goa-ai/runtime/registry"
    "goa.design/goa-ai/runtime/toolregistry"
    "goa.design/pulse/streaming"
    streamopts "goa.design/pulse/streaming/options"
)

const (
    inputSchema = "{\"type\":\"object\",\"required\":[\"query\"],\"additionalProperties\":false,\"properties\":{\"query\":{\"type\":\"string\"}}}"
    outputSchema = "{\"type\":\"integer\",\"const\":9007199254740993}"
)

type registryPlanner struct { tool string; inspect bool }

func (p registryPlanner) PlanStart(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
    found := false
    for _, tool := range input.Agent.AdvertisedToolDefinitions() {
        if tool.Name == p.tool && tool.Deferred { found = true }
    }
    if !found { return nil, fmt.Errorf("deferred discovered tool %q was not advertised", p.tool) }
    if p.inspect { return finalAnswer(), nil }
    return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
        Name: tools.Ident(p.tool), Payload: rawjson.Message("{\"query\":\"records\"}"),
    }}, SynthesizeAfterTools: true}, nil
}

func (registryPlanner) PlanResume(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
    if len(input.ToolOutputs) == 1 && input.ToolOutputs[0].Failure != nil {
        return nil, fmt.Errorf("remote tool failed: %w", input.ToolOutputs[0].Failure.Error)
    }
    if len(input.ToolOutputs) != 1 || string(input.ToolOutputs[0].Result) != "9007199254740993" {
        return nil, fmt.Errorf("unexpected restored tool output: %+v", input.ToolOutputs[0])
    }
    return finalAnswer(), nil
}

func finalAnswer() *planner.PlanResult {
    return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
        Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}},
    }}}
}

func catalogSchema() *genregistry.Toolset {
    version := genregistry.SemVer("1.2.3")
    description := "Search the available records."
    return &genregistry.Toolset{
        Name: "analytics", Version: &version, RegisteredAt: "2026-09-17T00:00:00Z",
        Tools: []*genregistry.ToolSchema{{
            Name: "analytics.search", Description: &description, Tags: []string{"read"},
            PayloadSchema: []byte(inputSchema), ExecutionPayloadSchema: []byte(inputSchema), ResultSchema: []byte(outputSchema),
            ConsumerContract: &genregistry.ConsumerContract{
                Kind: "service", Title: "Search records",
                Search: &genregistry.ToolSearchDocument{Length: 2, Terms: map[string]int{"records": 1, "search": 1}},
                Payload: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: []byte(inputSchema)},
                Result: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: []byte(outputSchema)},
            },
        }},
    }
}

type resultPulse struct { pulse.Client }
type resultStream struct { pulse.Stream }
type resultReader struct { events <-chan *streaming.Event }
type outputSink struct { deltas atomic.Int64 }

func (s *outputSink) Send(_ context.Context, event aistream.Event) error {
    if delta, ok := event.(aistream.ToolOutputDelta); ok {
        if delta.Data.ToolName != "analytics.search" || delta.Data.Delta != "Searching records" {
            return fmt.Errorf("unexpected output delta: %+v", delta.Data)
        }
        s.deltas.Add(1)
    }
    return nil
}
func (*outputSink) Close(context.Context) error { return nil }

func (resultPulse) Stream(string, ...streamopts.Stream) (pulse.Stream, error) { return resultStream{}, nil }
func (resultStream) NewReader(context.Context, ...streamopts.Reader) (pulse.Reader, error) {
    data, err := json.Marshal(toolregistry.ToolResultMessage{
        ToolUseID: "tool-use", RegistrationToken: strings.Repeat("a", 64), Result: json.RawMessage(outputSchemaValue),
    })
    if err != nil { return nil, err }
    delta, err := json.Marshal(toolregistry.ToolOutputDeltaMessage{
        ToolUseID: "tool-use", RegistrationToken: strings.Repeat("a", 64),
        Stream: "stdout", Delta: "Searching records",
    })
    if err != nil { return nil, err }
    events := make(chan *streaming.Event, 2)
    events <- &streaming.Event{ID: "1-0", EventName: toolregistry.OutputDeltaEventKey, Payload: delta}
    events <- &streaming.Event{ID: "2-0", EventName: toolregistry.ResultEventKey, Payload: data}
    close(events)
    return resultReader{events: events}, nil
}
const outputSchemaValue = "9007199254740993"
func (r resultReader) Subscribe() <-chan *streaming.Event { return r.events }
func (resultReader) Close() {}

func TestDiscoveredToolsExecuteAndNewCatalogAppears(t *testing.T) {
    ctx := t.Context()
    sink := &outputSink{}
    rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()),
        runtime.WithStream(sink, aistream.StreamProfile{ToolOutputDelta: true}))
    current := catalogSchema()
    var reads, lists, executions atomic.Int64
    client := &genregistry.Client{
        ResolveToolsetEndpoint: func(_ context.Context, input any) (any, error) {
            require.Equal(t, "analytics", input.(*genregistry.GetToolsetPayload).Name)
            reads.Add(1)
            return &genregistry.ResolvedToolset{Toolset: current, RegistrationToken: strings.Repeat("a", 64)}, nil
        },
        ListToolsetsEndpoint: func(context.Context, any) (any, error) {
            lists.Add(1)
            return &genregistry.ListToolsetsResult{Toolsets: []*genregistry.ToolsetInfo{{Name: current.Name}}}, nil
        },
        CallResolvedToolEndpoint: func(_ context.Context, input any) (any, error) {
            call := input.(*genregistry.CallResolvedToolPayload)
            require.Equal(t, strings.Repeat("a", 64), call.ExpectedRegistrationToken)
            require.Equal(t, "analytics", call.Toolset)
            require.Equal(t, "analytics.search", call.Tool)
            require.JSONEq(t, "{\"query\":\"records\"}", string(call.PayloadJSON))
            executions.Add(1)
            return &genregistry.CallToolResult{
                ToolUseID: "tool-use", RegistrationToken: call.ExpectedRegistrationToken,
                ExecutionDeadline: time.Now().Add(time.Minute).Truncate(time.Millisecond).Format(time.RFC3339Nano),
                ResultStreamExpiresAt: time.Now().Add(time.Hour).Truncate(time.Millisecond).Format(time.RFC3339Nano),
            }, nil
        },
    }
    require.NoError(t, rt.RegisterRegistry("corp", client, resultPulse{}))
    require.NoError(t, genassistant.RegisterAssistantAgent(ctx, rt, genassistant.AssistantAgentConfig{
        Planner: registryPlanner{tool: "analytics.search"},
    }))
    require.NoError(t, gencatalog.RegisterCatalogAgent(ctx, rt, gencatalog.CatalogAgentConfig{
        Planner: registryPlanner{tool: "analytics.added", inspect: true},
    }))
    local := runtime.ToolCallExecutorFunc(func(context.Context, *runtime.ToolCallMeta, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
        return nil, fmt.Errorf("local tool should not be selected")
    })
    require.NoError(t, genassistant.RegisterUsedToolsets(ctx, rt, genassistant.WithLocalExecutor(local)))
    require.Len(t, rt.ToolSpecsForAgent(genassistant.AgentID), 1, "only compiled local tools are registered")
    messages := []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Find records."}}}}
    out, err := genassistant.NewClient(rt).OneShotRun(ctx, messages)
    require.NoError(t, err)
    require.NotNil(t, out.Final)
    assert.Equal(t, "done", out.Final.Parts[0].(model.TextPart).Text)
    assert.Equal(t, int64(1), executions.Load())
    assert.Equal(t, int64(1), sink.deltas.Load(), "dynamic tools preserve configured output streaming")
    assert.Equal(t, int64(1), reads.Load(), "synthesis must not read the catalog")

    later := catalogSchema()
    second := *later.Tools[0]
    second.Name = "analytics.added"
    later.Tools = append(later.Tools, &second)
    current = later
    _, err = gencatalog.NewClient(rt).OneShotRun(ctx, messages)
    require.NoError(t, err)
    assert.Equal(t, int64(1), lists.Load())
    assert.Equal(t, int64(2), reads.Load())
    require.Len(t, rt.ToolSpecsForAgent(genassistant.AgentID), 1, "activity catalogs never mutate static registration")
}

func TestNamedRegistryConsumerRequiresDeclaredVersion(t *testing.T) {
    rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
    current := catalogSchema()
    wrong := genregistry.SemVer("2.0.0")
    current.Version = &wrong
    require.NoError(t, rt.RegisterRegistry("corp", &genregistry.Client{
        ResolveToolsetEndpoint: func(context.Context, any) (any, error) {
            return &genregistry.ResolvedToolset{Toolset: current, RegistrationToken: strings.Repeat("a", 64)}, nil
        },
    }, resultPulse{}))
    require.NoError(t, genassistant.RegisterAssistantAgent(t.Context(), rt, genassistant.AssistantAgentConfig{
        Planner: registryPlanner{tool: "analytics.search", inspect: true},
    }))
    _, err := rt.PlanStartActivity(t.Context(), &runtime.PlanActivityInput{AgentID: genassistant.AgentID, RunID: "test"})
    require.ErrorContains(t, err, "requires version")
}

func TestParentAndChildDefinitionsNeedNoCatalogAtStartup(t *testing.T) {
    definition := genparent.Definition()
    child, ok := definition.ChildDefinition(genworker.AgentID)
    require.True(t, ok)
    assert.Equal(t, genworker.AgentID, child.Route().ID)
    rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
    require.NoError(t, genworker.RegisterWorkerAgent(t.Context(), rt, genworker.WorkerAgentConfig{
        Planner: registryPlanner{tool: "analytics.search", inspect: true},
    }))
    _, err := rt.PlanStartActivity(t.Context(), &runtime.PlanActivityInput{AgentID: genworker.AgentID, RunID: "test"})
    require.ErrorContains(t, err, "registry \"corp\" is not registered")
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
	schema, err := catalog.GetToolset(t.Context(), "analytics")
	require.NoError(t, err)
	discovered, err := registry.NewToolset(schema)
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
