// These tests run generated input installers through the public registration and
// planning APIs, so helper compilation alone cannot hide missing runtime wiring.
package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
)

func TestConsumerInputGeneratedRuntimeIntegration(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", consumerInputDesign(true))
	root := writeGeneratedModuleKeepingGen(t, files)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/reader/agents/bound/input_runtime_test.go"), []byte(consumerInputRuntimeSource), 0600))
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-count=1", "-v", "./gen/reader/agents/bound"))
}

const consumerInputRuntimeSource = `package bound_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	genbound "generated.local/gen/reader/agents/bound"
	genbinding "generated.local/gen/reader/agents/bound/records/inputbinding"
	genrecords "generated.local/gen/reader/toolsets/records"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	integrationPlanner struct {
		t                    *testing.T
		textOnly, finalizing bool
		registered            bool
		starts, resumes      atomic.Int32
	}
	integrationSources struct {
		reads     atomic.Int32
		registered []*genregistry.RegisteredToolsetSelection
	}
	unusedPulse struct{}
)

func (p *integrationPlanner) PlanStart(_ context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
	p.starts.Add(1)
	found := map[string]bool{}
	for _, definition := range in.Agent.AdvertisedToolDefinitions() {
		found[definition.Name] = true
		schema := string(definition.Input.Contract().Schema)
		if strings.Contains(definition.Name, "_616e6f746865722d70726f7669646572_") {
			assert.Contains(p.t, schema, "scope")
			continue
		}
		assert.NotContains(p.t, schema, "scope", definition.Name)
		if p.textOnly {
			assert.NotContains(p.t, schema, "render", definition.Name)
		}
	}
	assert.True(p.t, found["records.read"])
	if p.registered {
		assert.True(p.t, found["records.read.636174616c6f67_service_70726f7669646572_6669727374.read"])
		assert.True(p.t, found["records.read.636174616c6f67_service_70726f7669646572_7365636f6e64.read"])
		assert.True(p.t, found["records.read.636174616c6f67_service_616e6f746865722d70726f7669646572_666f726569676e.read"])
		return finished(), nil
	}
	payload := rawjson.Message("{\"query\":\"active\",\"render\":true}")
	if p.textOnly {
		payload = rawjson.Message("{\"query\":\"active\"}")
	}
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "records.read", Payload: payload}}}, nil
}
func (p *integrationPlanner) PlanResume(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.resumes.Add(1)
	assert.Equal(p.t, p.finalizing, in.Finalize != nil)
	return finished(), nil
}
func finished() *planner.PlanResult {
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "finished"}}}}}
}
func (s *integrationSources) Resolve(_ context.Context, c *runtime.RegistryCatalog) error {
	s.reads.Add(1)
	for _, resource := range s.registered {
		if err := c.IncludeRegistered("catalog", resource.Source, resource.Resolved, false); err != nil {
			return err
		}
	}
	return nil
}
func (*integrationSources) Allows(registry, name, version string) bool {
	return registry == "catalog" && name == genrecords.Toolset().Name && version == ""
}
func (unusedPulse) Stream(string, ...streamopts.Stream) (pulsec.Stream, error) {
	return nil, errors.New("no registered execution requested")
}
func (unusedPulse) Close(context.Context) error { return nil }

func TestGeneratedBindingRunsBeforePlanningAndFinalization(t *testing.T) {
	for _, textOnly := range []bool{false, true} {
		for _, finalizing := range []bool{false, true} {
			name := "interactive"
			if textOnly {
				name = "text-only"
			}
			if finalizing {
				name += "/finalization"
			}
			t.Run(name, func(t *testing.T) {
				rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
				pl := &integrationPlanner{t: t, textOnly: textOnly, finalizing: finalizing}
				sources := &integrationSources{}
				_, missingErr := genbound.NewBoundAgentRegistration(rt, genbound.BoundAgentConfig{Planner: pl})
				require.ErrorContains(t, missingErr, "input preparation is required")
				var preparations, executions atomic.Int32
				cfg := genbound.BoundAgentConfig{Planner: pl, PrepareInputs: func(_ context.Context, c *runtime.RegistryCatalog) error {
					preparations.Add(1)
					if err := genbinding.BindReadInput(c, &genrecords.ScopeRef{Name: "accepted", Details: &genrecords.DetailsRef{Code: "saved"}}); err != nil {
						return err
					}
					return genbinding.BindReadBoundValuesInput(c, "accepted")
				}}
				registration, err := genbound.NewBoundAgentRegistration(rt, cfg)
				require.NoError(t, err)
				registration.Definition = registration.Definition.WithRegistryTools(sources)
				client := rt.MustClientFor(registration.Definition)
				execute := runtime.ToolCallExecutorFunc(func(_ context.Context, _ *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
					executions.Add(1)
					value, err := genrecords.ReadPayloadCodec().FromJSON(call.Payload)
					require.NoError(t, err)
					assert.Equal(t, "accepted", value.Scope.Name)
					assert.Equal(t, "saved", value.Scope.Details.Code)
					assert.Equal(t, "active", value.Query)
					require.NotNil(t, value.Render)
					assert.Equal(t, !textOnly, *value.Render)
					expected := "{\"query\":\"active\",\"render\":true}"
					if textOnly {
						expected = "{\"query\":\"active\"}"
					}
					assert.Equal(t, expected, string(call.ModelPayload))
					result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: "records"}
					if finalizing {
						result.Result = nil
						result.Failure = &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolError("source unavailable"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryFinish}}
					}
					return runtime.Executed(result), nil
				})
				require.NoError(t, genbound.RegisterUsedToolsets(t.Context(), rt, genbound.WithRecordsExecutor(execute)))
				require.NoError(t, rt.RegisterAgent(t.Context(), registration))
				out, err := client.OneShotRun(t.Context(), []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "read records"}}}}, runtime.WithTextOnly(textOnly))
				require.NoError(t, err)
				require.NotNil(t, out)
				assert.Equal(t, "finished", out.Final.Text())
				assert.EqualValues(t, 1, executions.Load())
				assert.EqualValues(t, 1, pl.starts.Load())
				assert.EqualValues(t, 1, pl.resumes.Load())
				assert.EqualValues(t, 2, preparations.Load())
				expectedReads := int32(2)
				if finalizing {
					expectedReads = 1
				}
				assert.Equal(t, expectedReads, sources.reads.Load())
				_, err = genrecords.ReadPayloadCodec().FromJSON([]byte("{\"query\":\"active\"}"))
				require.Error(t, err, "provider must still require scope")
			})
		}
	}
}

func resource(name, owner string) *genregistry.RegisteredToolsetSelection {
	declaration := genrecords.Toolset()
	declaration.RegisteredAt = "2026-10-11T00:00:00Z"
	// The synthetic remote host serves the same generated operation as a service.
	// The input and output schemas remain the generated provider declarations.
	for _, tool := range declaration.Tools {
		tool.ConsumerContract.Kind = "service"
	}
	token := strings.Repeat("a", 64)
	return &genregistry.RegisteredToolsetSelection{Resolved: &genregistry.ResolvedToolset{Toolset: declaration, RegistrationToken: token},
		Source: &genregistry.CatalogSource{Scope: owner, Name: name, Target: genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: declaration.Name, RegistrationToken: token})},
	}
}

func TestGeneratedRegisteredHelperPreservesForeignSource(t *testing.T) {
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	require.NoError(t, rt.RegisterRegistry("catalog", &genregistry.Client{}, unusedPulse{}))
	first := resource("first", "provider")
	second := resource("second", "provider")
	foreign := resource("foreign", "another-provider")
	sources := &integrationSources{registered: []*genregistry.RegisteredToolsetSelection{first, second, foreign}}
	pl := &integrationPlanner{t: t, registered: true}
	cfg := genbound.BoundAgentConfig{Planner: pl, PrepareInputs: func(_ context.Context, c *runtime.RegistryCatalog) error {
		scope := &genrecords.ScopeRef{Name: "accepted"}
		if err := genbinding.BindReadInput(c, scope); err != nil {
			return err
		}
		if err := genbinding.BindReadBoundValuesInput(c, "accepted"); err != nil {
			return err
		}
		return c.ForEachRegisteredSource(func(registry string, source *genregistry.CatalogSource) error {
			if source.Scope != "provider" {
				return nil
			}
			if err := genbinding.BindReadSourceInput(c, registry, source, scope); err != nil {
				return err
			}
			return genbinding.BindReadBoundValuesSourceInput(c, registry, source, "accepted")
		})
	}}
	registration, err := genbound.NewBoundAgentRegistration(rt, cfg)
	require.NoError(t, err)
	registration.Definition = registration.Definition.WithRegistryTools(sources)
	client := rt.MustClientFor(registration.Definition)
	execute := runtime.ToolCallExecutorFunc(func(context.Context, *runtime.ToolCallMeta, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		return nil, errors.New("planner must finish without execution")
	})
	require.NoError(t, genbound.RegisterUsedToolsets(t.Context(), rt, genbound.WithRecordsExecutor(execute)))
	require.NoError(t, rt.RegisterAgent(t.Context(), registration))
	_, err = client.OneShotRun(t.Context(), []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "inspect tools"}}}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, pl.starts.Load())
	assert.EqualValues(t, 1, sources.reads.Load())
}

// Each generated declaration requires both installers on every activity. The
// first tool may run before finalization, but an incomplete finalizer must stop
// before asking the planner for another decision.
func TestGeneratedBindingRejectsOmittedInstallers(t *testing.T) {
	for _, textOnly := range []bool{false, true} {
		for _, finalizing := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				name := "interactive"
				if textOnly {
					name = "text-only"
				}
				if finalizing {
					name += "/finalizer"
				}
				if partial {
					name += "/partial"
				} else {
					name += "/noop"
				}
				t.Run(name, func(t *testing.T) {
					rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
					pl := &integrationPlanner{t: t, textOnly: textOnly, finalizing: finalizing}
					sources := &integrationSources{}
					var preparations, executions atomic.Int32
					cfg := genbound.BoundAgentConfig{Planner: pl, PrepareInputs: func(_ context.Context, c *runtime.RegistryCatalog) error {
						attempt := preparations.Add(1)
						omit := !finalizing || attempt > 1
						if omit && !partial {
							return nil
						}
						if err := genbinding.BindReadInput(c, &genrecords.ScopeRef{Name: "accepted"}); err != nil {
							return err
						}
						if omit {
							return nil
						}
						return genbinding.BindReadBoundValuesInput(c, "accepted")
					}}
					registration, err := genbound.NewBoundAgentRegistration(rt, cfg)
					require.NoError(t, err)
					registration.Definition = registration.Definition.WithRegistryTools(sources)
					client := rt.MustClientFor(registration.Definition)
					execute := runtime.ToolCallExecutorFunc(func(_ context.Context, _ *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
						executions.Add(1)
						value, err := genrecords.ReadPayloadCodec().FromJSON(call.Payload)
						require.NoError(t, err)
						assert.Equal(t, "accepted", value.Scope.Name)
						return runtime.Executed(&planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Failure: &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolError("source unavailable"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryFinish}}}), nil
					})
					require.NoError(t, genbound.RegisterUsedToolsets(t.Context(), rt, genbound.WithRecordsExecutor(execute)))
					require.NoError(t, rt.RegisterAgent(t.Context(), registration))
					_, err = client.OneShotRun(t.Context(), []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "read records"}}}}, runtime.WithTextOnly(textOnly))
					require.ErrorContains(t, err, "required input binding")
					missing := "records.read"
					if partial {
						missing = "records.read_bound_values"
					}
					assert.Contains(t, err.Error(), missing)
					assert.Zero(t, pl.resumes.Load())
					if finalizing {
						assert.EqualValues(t, 1, pl.starts.Load())
						assert.EqualValues(t, 1, executions.Load())
						assert.EqualValues(t, 2, preparations.Load())
					} else {
						assert.Zero(t, pl.starts.Load())
						assert.Zero(t, executions.Load())
						assert.EqualValues(t, 1, preparations.Load())
					}
					assert.EqualValues(t, 1, sources.reads.Load())
				})
			}
		}
	}
}
`
