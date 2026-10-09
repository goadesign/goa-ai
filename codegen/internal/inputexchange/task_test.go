// Package inputexchange_test verifies job questions using evaluated Goa types.
// Question output and answer input retain their separate method owners, native
// field names, typed unions and exact form schemas through final name assignment.
package inputexchange_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/internal/inputexchange"
	"goa.design/goa-ai/codegen/testhelpers"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

func TestTaskQuestionsRetainReadAndAnswerMethodOwners(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, testscenarios.NativeTaskExchange(false))
	generation, err := codegen.NewGeneration(genpkg, roots)
	require.NoError(t, err)
	services, err := service.NewPlan(expr.Root, generation, expr.NewExampleGenerator(expr.Root.API.RandomizerFactory))
	require.NoError(t, err)
	output, err := generation.ClaimPackage(genpkg + "/taskadapter")
	require.NoError(t, err)
	jobs := expr.Root.Service("jobs")
	serviceImport, _, err := services.ServicePackageImports(jobs)
	require.NoError(t, err)
	require.NoError(t, output.ReserveGeneratedImport(serviceImport))
	codecs, err := jsoncodec.NewPlan(generation, output.ImportPath()+"/internal/inputcodec")
	require.NoError(t, err)
	binding, err := mcpinput.TaskExchange(jobs.Method("create"))
	require.NoError(t, err)
	plan, err := inputexchange.NewTask(services, expr.Root.API, binding, binding.Pending, codecs)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	require.NoError(t, services.Link())
	codecTypes := services.Services().ServiceAttributor(jobs.Name, output.ImportPath()+"/internal/inputcodec")
	require.NoError(t, plan.BindCodecs(codecTypes, codecTypes))
	nativeTypes := services.Services().ServiceAttributor(jobs.Name, output.ImportPath())
	require.NoError(t, plan.Bind(generation, nativeTypes, output.ImportPath(), output.ImportName, "geninputcodec"))

	require.Len(t, plan.Questions, 2)
	question := plan.Questions[0]
	assert.Equal(t, "profile", question.Name)
	assert.Equal(t, "PromptText", question.MessageField)
	assert.Equal(t, "AsProfile", question.RequestAccessor)
	assert.Contains(t, question.ResponseConstructor, "NewJobResponseKindProfile")
	assert.Contains(t, question.Decode, "geninputcodec.DecodeCreateTaskAnswer0")
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"name":{"type":"string","description":"Name supplied by the host."}},"required":["name"]}`, question.Schema)
	assert.Equal(t, "HostAnswers", plan.ResponsesField)
	assert.Contains(t, plan.ResponseKeyRef, "JobQuestionID")
	files, err := codecs.Files("inputcodec")
	require.NoError(t, err)
	assert.NotEmpty(t, files)
}
