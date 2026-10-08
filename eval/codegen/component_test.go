package codegen_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	evaldsl "goa.design/goa-ai/eval/dsl"
	goadsl "goa.design/goa/v3/dsl"
)

func TestGeneratedComponentPredicatesAndScopedSelectors(t *testing.T) {
	roots := runDesign(t, func() {
		answer := goadsl.Type("AnswerObservation", func() {
			goadsl.Attribute("text", goadsl.String, "Captured answer.")
			goadsl.Attribute("facts", goadsl.String, "Captured facts.")
			goadsl.Attribute("messages", goadsl.ArrayOf(goadsl.String), "Captured messages.")
			goadsl.Required("facts")
		})
		quality := evaldsl.Component("answer_quality", func() {
			goadsl.Description("Checks captured answers against facts.")
			evaldsl.Observation(answer)
			evaldsl.Check("present", "An answer was captured.")
			evaldsl.Requirement("grounded", "The answer agrees with captured facts.", func() {
				evaldsl.Subject("text")
				evaldsl.Evidence("facts")
			})
			evaldsl.Requirement("messages", "The message agrees with captured facts.", func() {
				evaldsl.ForEach("messages")
				evaldsl.Subject("@")
				evaldsl.Evidence("$.facts")
				evaldsl.Reasoning()
			})
		})
		text := goadsl.Type("AnswerText", goadsl.String)
		capturedText := evaldsl.Component("captured_text", func() {
			goadsl.Description("Checks the selected captured text.")
			evaldsl.Observation(text)
			evaldsl.Check("present", "Captured text is present.")
		})
		evaldsl.Suite("components", func() {
			goadsl.Description("Reuses assertions over focused answers and complete flows.")
			goadsl.Timeout("1m")
			evaldsl.Scenario("focused", func() {
				goadsl.Description("Captures one answer.")
				evaldsl.Observation(answer)
				evaldsl.Assess("answer", quality, "$")
			})
			evaldsl.Scenario("flow", func() {
				goadsl.Description("Captures two answers during one product run.")
				evaldsl.Observation(func() {
					goadsl.Attribute("first", answer, "First answer.")
					goadsl.Attribute("second", answer, "Second answer.")
					goadsl.Attribute("facts", goadsl.String, "Flow facts, outside the component.")
					goadsl.Attribute("optional_text", text, "Optional captured text.")
					goadsl.Attribute("required_text", text, "Required captured text.")
					goadsl.Required("first", "facts", "required_text")
				})
				evaldsl.Assess("first", quality, "first")
				evaldsl.Assess("second", quality, "second")
				evaldsl.Assess("optional_text", capturedText, "optional_text")
				evaldsl.Assess("required_text", capturedText, "required_text")
				evaldsl.Requirement("goal", "The flow satisfies the request.", func() {
					evaldsl.Subject("$")
					evaldsl.Evidence("facts")
					evaldsl.Reasoning()
				})
			})
		})
	})
	files := generateEvalFiles(t, roots, false)
	files = append(files, generateEvalFiles(t, roots, true)...)
	root := t.TempDir()
	for _, file := range files {
		_, err := file.Render(root)
		require.NoError(t, err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/project\n\ngo 1.26\n\nrequire goa.design/goa-ai v0.0.0\n\nreplace goa.design/goa-ai => "+repoRoot+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen", "evals", "components", "component_test.go"),
		[]byte(componentConsumerTest), 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./gen/evals/components", "./cmd/components-evals")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

const componentConsumerTest = `package components

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type captures struct { executions, predicates int }

func (c *captures) CheckComponentAnswerQualityPresent(observed *AnswerObservation) string {
	c.predicates++
	if observed.Text == nil || *observed.Text == "" { return "The answer is missing." }
	return ""
}

func (c *captures) CheckComponentCapturedTextPresent(observed AnswerText) string {
	c.predicates++
	if observed == "" { return "The captured text is missing." }
	return ""
}

func (c *captures) Focused(context.Context) (*AnswerObservation, error) {
	c.executions++
	text := "focused"
	return &AnswerObservation{Text: &text, Facts: "focus facts"}, nil
}

func (c *captures) Flow(context.Context) (*FlowObservation, error) {
	c.executions++
	first, second := "first", "second"
	optional := AnswerText("optional")
	return &FlowObservation{
		First: &AnswerObservation{Text: &first, Facts: "first facts", Messages: []string{"one"}},
		Second: &AnswerObservation{Text: &second, Facts: "second facts", Messages: []string{"two"}},
		Facts: "flow facts",
		OptionalText: &optional,
		RequiredText: AnswerText("required"),
	}, nil
}

func TestComponentsReusePredicatesAndCaptureOnce(t *testing.T) {
	hooks := &captures{}
	suite, err := New(hooks, Inputs{})
	require.NoError(t, err)
	for _, scenario := range suite.Scenarios {
		observed, err := scenario.Capture(t.Context())
		require.NoError(t, err)
		_, err = scenario.Bind(observed)
		require.NoError(t, err)
	}
	assert.Equal(t, 2, hooks.executions)
	assert.Equal(t, 5, hooks.predicates)
}

func TestComponentsKeepLocalEvidenceAndDistinctIdentities(t *testing.T) {
	hooks := &captures{}
	suite, err := ForAssessment(hooks)
	require.NoError(t, err)
	flow := suite.Scenarios[1]
	binding, err := flow.Bind(rawjson.Message(` + "`" + `{"first":{"text":"first","facts":"first facts","messages":["one"]},"second":{"text":"second","facts":"second facts","messages":["two"]},"facts":"outer facts","optional_text":"optional","required_text":"required"}` + "`" + `))
	require.NoError(t, err)
	assert.Equal(t, "first", binding.Subjects["components/flow/first/grounded"][0].Content)
	assert.Equal(t, "second", binding.Subjects["components/flow/second/grounded"][0].Content)
	assert.Equal(t, ` + "`" + `{"$.facts":"first facts"}` + "`" + `, binding.Subjects["components/flow/first/messages"][0].Reference)
	assert.Equal(t, ` + "`" + `{"$.facts":"second facts"}` + "`" + `, binding.Subjects["components/flow/second/messages"][0].Reference)
	assert.Equal(t, []string{"first/present", "second/present", "optional_text/present", "required_text/present"}, flow.CheckNames)
	for _, requirement := range flow.Requirements {
		assert.Equal(t, strings.HasSuffix(requirement.ID, "/messages") || strings.HasSuffix(requirement.ID, "/goal"), requirement.Reasoning)
	}
	assert.Equal(t, 0, hooks.executions)
	assert.Equal(t, 4, hooks.predicates)
}

func TestMissingComponentCannotDropItsAssertions(t *testing.T) {
	suite, err := ForAssessment(&captures{})
	require.NoError(t, err)
	_, err = suite.Scenarios[1].Bind(rawjson.Message(` + "`" + `{"first":{"facts":"first facts"},"facts":"outer facts","required_text":"required"}` + "`" + `))
	require.ErrorContains(t, err, "missing captured component second")
}
`
