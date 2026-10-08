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

func TestGeneratedObservationCodecsAndOfflineSelectors(t *testing.T) {
	roots := runDesign(t, func() {
		evaldsl.Suite("observations", func() {
			goadsl.Description("Exercises typed observation binding.")
			goadsl.Timeout("1m")
			evaldsl.Scenario("reply", func() {
				goadsl.Description("Captures a reply, including incomplete outcomes.")
				evaldsl.Observation(func() {
					goadsl.Attribute("answer", goadsl.String, "Captured answer, when present.")
					goadsl.Attribute("facts", goadsl.ArrayOf(goadsl.String), "Captured facts.")
					goadsl.Attribute("labels", goadsl.MapOf(goadsl.String, goadsl.String), "Captured labels.")
					goadsl.Attribute("messages", goadsl.ArrayOf(goadsl.String), "Captured messages.")
					goadsl.Attribute("context", func() {
						goadsl.Attribute("notes", goadsl.String, "Captured context.")
					})
					goadsl.Attribute("group", func() {
						goadsl.Attribute("messages", goadsl.ArrayOf(goadsl.String), "Messages in this observed group.")
					})
				})
				evaldsl.Requirement("grounded", "The answer agrees with facts.", func() {
					evaldsl.Subject("answer")
					evaldsl.Evidence("facts", "labels")
				})
				evaldsl.Requirement("each_message", "The message agrees with facts.", func() {
					evaldsl.ForEach("messages")
					evaldsl.Subject("@")
					evaldsl.Evidence("$.facts")
				})
				evaldsl.Requirement("contextual", "The answer agrees with context.", func() {
					evaldsl.Subject("answer")
					evaldsl.Evidence("context.notes")
				})
				evaldsl.Requirement("grouped", "The message is complete.", func() {
					evaldsl.ForEach("group.messages")
					evaldsl.Subject("@")
				})
			})
		})
	})
	files := generateEvalFiles(t, roots, false)
	root := t.TempDir()
	for _, file := range files {
		_, err := file.Render(root)
		require.NoError(t, err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/project\n\ngo 1.26\n\nrequire goa.design/goa-ai v0.0.0\n\nreplace goa.design/goa-ai => "+repoRoot+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen", "evals", "observations", "observation_test.go"),
		[]byte(observationConsumerTest), 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./gen/evals/observations")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

const observationConsumerTest = `package observations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"
)

type captures struct{}

func (*captures) Reply(context.Context) (*ReplyObservation, error) {
	value := string([]byte{0xff})
	return &ReplyObservation{Answer: &value}, nil
}

func TestTypedOfflineBindings(t *testing.T) {
	suite, err := ForAssessment()
	require.NoError(t, err)
	scenario := suite.Scenarios[0]
	binding, err := scenario.Bind([]byte("{\"context\":{\"notes\":\"Saved.\"},\"group\":{}}"))
	require.NoError(t, err)
	assert.Empty(t, binding.Subjects["observations/reply/grounded"][0].Content)
	assert.JSONEq(t, "{\"facts\":[],\"labels\":{}}", binding.Subjects["observations/reply/grounded"][0].Reference)
	assert.Empty(t, binding.Subjects["observations/reply/each_message"])
	assert.Contains(t, binding.Subjects, "observations/reply/each_message")
	assert.Empty(t, binding.Subjects["observations/reply/grouped"])
	for _, requirement := range scenario.Requirements {
		assert.NotEmpty(t, requirement.SchemaID)
	}
	full, err := scenario.Bind([]byte("{\"answer\":\"Done.\",\"facts\":[],\"labels\":{},\"messages\":[\"One.\",\"Two.\"],\"context\":{\"notes\":\"Saved.\"},\"group\":{\"messages\":[\"Three.\"]}}"))
	require.NoError(t, err)
	assert.Equal(t, "One.", full.Subjects["observations/reply/each_message"][0].Content)
	assert.Equal(t, "Two.", full.Subjects["observations/reply/each_message"][1].Content)
	assert.Equal(t, "Three.", full.Subjects["observations/reply/grouped"][0].Content)
	assert.Equal(t, binding.Subjects["observations/reply/grounded"][0].Reference, full.Subjects["observations/reply/grounded"][0].Reference)
}

func TestMissingContextCannotTurnIntoAnEmptyCollection(t *testing.T) {
	suite, err := ForAssessment()
	require.NoError(t, err)
	_, err = suite.Scenarios[0].Bind([]byte("{\"context\":{\"notes\":\"Saved.\"}}"))
	assert.ErrorContains(t, err, "missing captured collection group.messages")
	_, err = suite.Scenarios[0].Bind([]byte("{\"group\":{}}"))
	assert.ErrorContains(t, err, "missing captured evidence context.notes")
}

func TestStrictObservationDecoding(t *testing.T) {
	suite, err := ForAssessment()
	require.NoError(t, err)
	for _, encoded := range []string{
		"{\"answer\":123}", "{\"unexpected\":true}", "{\"answer\":\"one\",\"answer\":\"two\"}",
		"{\"answer\":\"\\ud800\"}", "{\"messages\":[123]}",
	} {
		_, err := suite.Scenarios[0].Bind([]byte(encoded))
		assert.Error(t, err, encoded)
	}
}

func TestInvalidCaptureCannotProducePassingEvidence(t *testing.T) {
	suite, err := New(&captures{}, Inputs{})
	require.NoError(t, err)
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{MaxConcurrency: 1})
	require.NoError(t, err)
	archive, err := runner.Capture(t.Context(), suite)
	require.NoError(t, err)
	require.Len(t, archive.Observations, 1)
	assert.Contains(t, archive.Observations[0].Error, "UTF-8")
	assert.Empty(t, archive.Observations[0].Data)
}
`
