// These tests execute reduced codecs with only the values their JSON contracts
// include. Hidden required objects and unions must never enter conversion.
package tests

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestConsumerInputProjectionSkipsHiddenValues(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", consumerProjectionDesign(true))
	baseline := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", consumerProjectionDesign(false))
	for _, file := range baseline {
		if strings.HasPrefix(file.Path, "gen/reader/toolsets/documents/") {
			assert.Equal(t, sha256.Sum256([]byte(fileContent(t, baseline, file.Path))), sha256.Sum256([]byte(fileContent(t, files, file.Path))), "provider changed: %s", file.Path)
		}
	}
	root := writeGeneratedModuleKeepingGen(t, files)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/reader/agents/bound/documents/inputbinding/projection_test.go"), []byte(consumerProjectionSource), 0600))
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-count=1", "-v", "./gen/reader/agents/bound/documents/inputbinding"))
}

// consumerProjectionDesign gives application and model input the same nested
// object shape, so neither codec can depend on the other half being populated.
func consumerProjectionDesign(bind bool) func() {
	return func() {
		API("documents", func() {})
		detail := Type("Detail", func() {
			Attribute("code", String, "The selected document code.", func() { MinLength(1) })
			Required("code")
		})
		selection := Type("Selection", func() {
			OneOf("choice", func() {
				Attribute("named", detail, "A named document selection.")
				Attribute("text", String, "A textual document selection.")
			})
			Attribute("detail", detail, "Additional document details.")
			OneOf("optional_choice", func() {
				Attribute("named", detail, "An optional named selection.")
				Attribute("text", String, "An optional textual selection.")
			})
			Required("choice")
		})
		provider := Toolset("documents", func() {
			Tool("read", "Read selected company documents.", func() {
				Args(func() {
					Attribute("company_scope", selection, "The application-selected company scope.")
					Attribute("selection", selection, "The model-selected documents.")
					Attribute("optional_selection", selection, "Additional model-selected documents.")
					Required("company_scope", "selection")
				})
				Return(String)
			})
		})
		Service("reader", func() {
			Agent("bound", "Reads company documents.", func() {
				Use(provider, func() {
					if bind {
						BindInput("read", "company_scope")
					}
				})
			})
		})
	}
}

const consumerProjectionSource = `package inputbinding

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	genbound "generated.local/gen/reader/agents/bound"
	gendocuments "generated.local/gen/reader/toolsets/documents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	streamopts "goa.design/pulse/streaming/options"
)

const selectionJSON = "{\"selection\":{\"choice\":{\"type\":\"named\",\"value\":{\"code\":\"requested\"}}}}"
const fullJSON = "{\"company_scope\":{\"choice\":{\"type\":\"named\",\"value\":{\"code\":\"accepted\"}},\"detail\":{\"code\":\"saved\"},\"optional_choice\":{\"type\":\"text\",\"value\":\"optional\"}},\"selection\":{\"choice\":{\"type\":\"named\",\"value\":{\"code\":\"requested\"}}}}"

func TestBoundValuesIgnoreUnboundObjects(t *testing.T) {
	full, err := gendocuments.ReadPayloadCodec().FromJSON([]byte(fullJSON))
	require.NoError(t, err)
	original := gendocuments.SpecRead()
	schema := append([]byte(nil), original.ExecutionPayloadSchema...)
	bound, err := prepareReadInput(original, full.CompanyScope)
	require.NoError(t, err)
	full.CompanyScope.Detail.Code = "changed"
	for _, selected := range []struct {
		name     string
		textOnly bool
	}{{"ordinary", false}, {"text-only", true}} {
		t.Run(selected.name, func(t *testing.T) {
			spec := bound
			if selected.textOnly {
				spec = bound.ForTextOnly()
			}
			decoded, err := spec.Payload.Codec.FromJSON([]byte(selectionJSON))
			require.NoError(t, err)
			execution, err := spec.PrepareInput(decoded)
			require.NoError(t, err)
			complete, err := gendocuments.ReadPayloadCodec().FromJSON(execution)
			require.NoError(t, err)
			assert.JSONEq(t, fullJSON, string(execution))
			assert.Equal(t, "saved", complete.CompanyScope.Detail.Code)
			assert.Nil(t, decoded.(*gendocuments.ReadPayload).CompanyScope)
		})
	}
	assert.Equal(t, sha256.Sum256(schema), sha256.Sum256(gendocuments.SpecRead().ExecutionPayloadSchema))
	for _, invalid := range []*gendocuments.Selection{nil, {}, {Detail: &gendocuments.Detail{Code: "invalid without choice"}}} {
		_, err := prepareReadInput(original, invalid)
		require.Error(t, err)
	}
	_, err = prepareReadInput(original, &gendocuments.Selection{Choice: gendocuments.NewChoiceNamed(&gendocuments.Detail{})})
	require.Error(t, err)
	_, err = prepareReadInput(original, &gendocuments.Selection{Choice: gendocuments.NewChoiceNamed(nil)})
	require.Error(t, err)
	_, err = prepareReadInput(original, &gendocuments.Selection{Choice: gendocuments.NewChoiceText("valid"), Detail: &gendocuments.Detail{}})
	require.Error(t, err)
	_, err = prepareReadInput(original, &gendocuments.Selection{Choice: gendocuments.NewChoiceText("valid"), OptionalChoice: gendocuments.NewOptionalChoiceNamed(&gendocuments.Detail{})})
	require.Error(t, err)
}

func TestModelCodecIgnoresBoundObjects(t *testing.T) {
	codec := ReadModelInputCodec()
	decoded, err := codec.FromJSON([]byte(selectionJSON))
	require.NoError(t, err)
	require.Nil(t, decoded.CompanyScope)
	encoded, err := codec.ToJSON(decoded)
	require.NoError(t, err)
	assert.JSONEq(t, selectionJSON, string(encoded))
	full, err := gendocuments.ReadPayloadCodec().FromJSON([]byte(fullJSON))
	require.NoError(t, err)
	encoded, err = codec.ToJSON(full)
	require.NoError(t, err)
	assert.JSONEq(t, selectionJSON, string(encoded))
	_, err = codec.FromJSON([]byte(fullJSON))
	require.Error(t, err)
	for _, data := range []string{"{}", "{\"selection\":{}}", "{\"selection\":{\"choice\":{}}}"} {
		_, err := codec.FromJSON([]byte(data))
		require.Error(t, err)
	}
	_, err = codec.ToJSON(&gendocuments.ReadPayload{Selection: &gendocuments.Selection{}})
	require.Error(t, err)
	_, err = gendocuments.ReadPayloadCodec().FromJSON([]byte(selectionJSON))
	require.Error(t, err)
}

type (
	projectionPlanner struct{ t *testing.T }
	projectionSources struct {
		resource *genregistry.RegisteredToolsetSelection
	}
	unusedPulse struct{}
)

func (p *projectionPlanner) PlanStart(_ context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
	seen := map[string]bool{}
	for _, definition := range in.Agent.AdvertisedToolDefinitions() {
		seen[definition.Name] = true
		assert.NotContains(p.t, string(definition.Input.Contract().Schema), "company_scope")
	}
	assert.True(p.t, seen["documents.read"])
	assert.True(p.t, seen["documents.read.636174616c6f67_service_6f776e6572_646f63756d656e7473.read"])
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "ready"}}}}}, nil
}
func (*projectionPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return nil, errors.New("no tool execution requested")
}
func (s *projectionSources) Resolve(_ context.Context, c *runtime.RegistryCatalog) error {
	return c.IncludeRegistered("catalog", s.resource.Source, s.resource.Resolved, false)
}
func (*projectionSources) Allows(registry, name, version string) bool {
	return registry == "catalog" && name == gendocuments.Toolset().Name && version == ""
}
func (unusedPulse) Stream(string, ...streamopts.Stream) (pulsec.Stream, error) {
	return nil, errors.New("no registered execution requested")
}
func (unusedPulse) Close(context.Context) error { return nil }

func TestGeneratedLocalAndRegisteredBind(t *testing.T) {
	full, err := gendocuments.ReadPayloadCodec().FromJSON([]byte(fullJSON))
	require.NoError(t, err)
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	require.NoError(t, rt.RegisterRegistry("catalog", &genregistry.Client{}, unusedPulse{}))
	declaration := gendocuments.Toolset()
	declaration.RegisteredAt = "2026-10-11T00:00:00Z"
	for _, tool := range declaration.Tools {
		tool.ConsumerContract.Kind = "service"
	}
	token := strings.Repeat("a", 64)
	source := &genregistry.CatalogSource{Scope: "owner", Name: "documents", Target: genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: declaration.Name, RegistrationToken: token})}
	resource := &genregistry.RegisteredToolsetSelection{Resolved: &genregistry.ResolvedToolset{Toolset: declaration, RegistrationToken: token}, Source: source}
	prepared := 0
	registration, err := genbound.NewBoundAgentRegistration(rt, genbound.BoundAgentConfig{Planner: &projectionPlanner{t: t}, PrepareInputs: func(_ context.Context, c *runtime.RegistryCatalog) error {
		prepared++
		if err := BindReadInput(c, full.CompanyScope); err != nil {
			return err
		}
		return BindReadSourceInput(c, "catalog", source, full.CompanyScope)
	}})
	require.NoError(t, err)
	registration.Definition = registration.Definition.WithRegistryTools(&projectionSources{resource: resource})
	client := rt.MustClientFor(registration.Definition)
	execute := runtime.ToolCallExecutorFunc(func(context.Context, *runtime.ToolCallMeta, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		return nil, errors.New("no execution requested")
	})
	require.NoError(t, genbound.RegisterUsedToolsets(t.Context(), rt, genbound.WithDocumentsExecutor(execute)))
	require.NoError(t, rt.RegisterAgent(t.Context(), registration))
	result, err := client.OneShotRun(t.Context(), []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "inspect documents"}}}})
	require.NoError(t, err)
	assert.Equal(t, "ready", result.Final.Text())
	assert.Equal(t, 1, prepared)
}
`
