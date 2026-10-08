// These tests generate independent consumers to check saved observation identity
// and scenario-specific validation through the generated capture and replay APIs.
package codegen_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	evaldsl "goa.design/goa-ai/eval/dsl"
	goadsl "goa.design/goa/v3/dsl"
	goaexpr "goa.design/goa/v3/expr"
)

func TestGeneratedObservationContract(t *testing.T) {
	t.Run("primitive_identity_and_replay", func(t *testing.T) {
		root := observationContractModule(t)
		for _, revision := range []struct {
			name      string
			primitive goaexpr.Primitive
		}{
			{"original", goadsl.Float64},
			{"unchanged", goadsl.Float64},
			{"narrow", goadsl.Float32},
			{"text", goadsl.String},
			{"bytes", goadsl.Bytes},
		} {
			roots := runDesign(t, func() {
				reading := goadsl.Type("Reading", func() {
					goadsl.Attribute("value", revision.primitive, "Captured value.")
					goadsl.Required("value")
				})
				evaldsl.Suite("measurement", func() {
					goadsl.Description("Checks the interpretation of saved observations.")
					goadsl.Timeout("1m")
					evaldsl.Scenario("sample", func() {
						goadsl.Description("Captures one value.")
						evaldsl.Observation(reading)
						if revision.primitive == goadsl.Float64 || revision.primitive == goadsl.Float32 {
							evaldsl.Check("bounded", "The value is at most one.")
						} else {
							evaldsl.Requirement("meaning", "The value is supported.", func() {
								evaldsl.Subject("value")
							})
						}
					})
					if revision.primitive == goadsl.Float64 || revision.primitive == goadsl.Float32 {
						evaldsl.Scenario("semantic", func() {
							goadsl.Description("Supplies a semantic requirement identity.")
							evaldsl.Observation(reading)
							evaldsl.Requirement("meaning", "The value is supported.", func() {
								evaldsl.Subject("value")
							})
						})
					}
				})
			})
			for _, file := range generateEvalFiles(t, roots, false) {
				_, err := file.Render(filepath.Join(root, revision.name))
				require.NoError(t, err)
			}
		}
		require.NoError(t, os.WriteFile(filepath.Join(root, "contract_test.go"), []byte(observationIdentityConsumer), 0o600))
		runObservationContractConsumer(t, root)
	})

	for _, order := range []struct {
		name           string
		scenarios      []string
		definitionEnum bool
	}{
		{"unrestricted_first", []string{"open", "restricted", "alternate", "twin"}, false},
		{"restricted_first", []string{"alternate", "restricted", "open", "twin"}, false},
		{"single_constrained_root", []string{"restricted"}, false},
		{"named_definition_override", []string{"open", "restricted", "alternate", "twin"}, true},
	} {
		t.Run(order.name, func(t *testing.T) {
			root := observationContractModule(t)
			roots := runDesign(t, func() {
				status := goadsl.Type("Status", goadsl.String, func() {
					if order.definitionEnum {
						goadsl.Enum("online", "offline")
					}
				})
				evaldsl.Suite("statuses", func() {
					goadsl.Description("Checks occurrence-specific observation rules.")
					goadsl.Timeout("1m")
					for _, name := range order.scenarios {
						evaldsl.Scenario(name, func() {
							goadsl.Description("Captures a status.")
							switch name {
							case "restricted", "twin":
								evaldsl.Observation(status, func() { goadsl.Enum("online", "offline") })
							case "alternate":
								evaldsl.Observation(status, func() { goadsl.Enum("online", "unexpected") })
							default:
								evaldsl.Observation(status)
							}
							evaldsl.Requirement("meaning", "The status is supported.", func() {
								evaldsl.Subject("$")
							})
						})
					}
				})
			})
			var encoders, types int
			for _, file := range generateEvalFiles(t, roots, false) {
				content := render(t, file)
				parsed, err := parser.ParseFile(token.NewFileSet(), file.Path, content, 0)
				require.NoError(t, err)
				ast.Inspect(parsed, func(node ast.Node) bool {
					switch declaration := node.(type) {
					case *ast.TypeSpec:
						if declaration.Name.Name == "Status" {
							types++
						}
					case *ast.FuncDecl:
						// Each capture encoder takes the shared Status value and
						// returns bytes plus an error; validators return only error.
						if declaration.Type.Params.NumFields() == 1 && declaration.Type.Results.NumFields() == 2 {
							if parameter, ok := declaration.Type.Params.List[0].Type.(*ast.Ident); ok && parameter.Name == "Status" {
								if result, ok := declaration.Type.Results.List[0].Type.(*ast.ArrayType); ok {
									if element, ok := result.Elt.(*ast.Ident); ok && element.Name == "byte" {
										encoders++
									}
								}
							}
						}
					}
					return true
				})
				_, err = file.Render(root)
				require.NoError(t, err)
			}
			assert.Equal(t, 1, types, "occurrence rules must not duplicate the Go type")
			expectedCodecs := 3
			if len(order.scenarios) == 1 {
				expectedCodecs = 1
			}
			assert.Equal(t, expectedCodecs, encoders, "only identical complete contracts may reuse a codec")
			t.Logf("generated %d Go Status type and %d distinct contract codecs", types, encoders)
			consumer := observationValidationConsumer + "\nconst definitionRestricted = " + strconv.FormatBool(order.definitionEnum) + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(root, "gen", "evals", "statuses", "contract_test.go"), []byte(consumer), 0o600))
			runObservationContractConsumer(t, root)
		})
	}
}

// observationContractModule creates a consumer module using this checkout, so
// its generated code is compiled against the implementation under test.
func observationContractModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/project\n\ngo 1.26.0\n\nrequire goa.design/goa-ai v0.0.0\n\nreplace goa.design/goa-ai => "+repository+"\n"), 0o600))
	return root
}

// runObservationContractConsumer compiles only the generated proof module and
// includes each consumer assertion result in the outer test's retained output.
func runObservationContractConsumer(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1", "-v", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	t.Logf("generated consumer output:\n%s", output)
	require.NoError(t, err)
}

const observationIdentityConsumer = `package contract_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genoriginal "example.com/project/original/gen/evals/measurement"
	genunchanged "example.com/project/unchanged/gen/evals/measurement"
	gennarrow "example.com/project/narrow/gen/evals/measurement"
	gentext "example.com/project/text/gen/evals/measurement"
	genbytes "example.com/project/bytes/gen/evals/measurement"
	"goa.design/goa-ai/eval"
)

type (
	originalHooks struct { value float64; calls int }
	unchangedChecks struct { calls int }
	narrowChecks struct { calls int }
)

func (h *originalHooks) Sample(context.Context) (*genoriginal.Reading, error) {
	return &genoriginal.Reading{Value: h.value}, nil
}
func (h *originalHooks) Semantic(context.Context) (*genoriginal.Reading, error) {
	return &genoriginal.Reading{Value: h.value}, nil
}
func (h *originalHooks) CheckSampleBounded(value *genoriginal.Reading) string {
	h.calls++
	if value.Value > 1 { return "above one" }
	return ""
}
func (c *unchangedChecks) CheckSampleBounded(value *genunchanged.Reading) string {
	c.calls++
	if value.Value > 1 { return "above one" }
	return ""
}
func (c *narrowChecks) CheckSampleBounded(value *gennarrow.Reading) string {
	c.calls++
	if value.Value > 1 { return "above one" }
	return ""
}

func TestFloatWidthReplay(t *testing.T) {
	for _, value := range []float64{0.75, 1.00000001} {
		t.Run(fmtValue(value), func(t *testing.T) {
			hooks := &originalHooks{value: value}
			original, err := genoriginal.New(hooks, genoriginal.Inputs{})
			require.NoError(t, err)
			sameChecks := &unchangedChecks{}
			same, err := genunchanged.ForAssessment(sameChecks)
			require.NoError(t, err)
			changedChecks := &narrowChecks{}
			changed, err := gennarrow.ForAssessment(changedChecks)
			require.NoError(t, err)
			assert.Equal(t, original.Scenarios[0].Schema, same.Scenarios[0].Schema)
			assert.Equal(t, original.Scenarios[1].Requirements[0].SchemaID, same.Scenarios[1].Requirements[0].SchemaID)
			assert.NotEqual(t, original.Scenarios[0].Schema, changed.Scenarios[0].Schema)
			assert.NotEqual(t, original.Scenarios[1].Requirements[0].SchemaID, changed.Scenarios[1].Requirements[0].SchemaID)
			_, err = same.Scenarios[0].Bind([]byte("{}"))
			assert.Error(t, err, "required fields must be checked before conversion")
			sameChecks.calls = 0
			runner, err := eval.NewRunner(nil, eval.RunnerConfig{MaxConcurrency: 1})
			require.NoError(t, err)
			archive, err := runner.CaptureScenarios(t.Context(), original, "sample")
			require.NoError(t, err)
			require.Empty(t, archive.Observations[0].Error)
			assert.Zero(t, hooks.calls, "capture does not run checks")
			report, err := runner.Assess(t.Context(), same, archive)
			require.NoError(t, err)
			require.Len(t, report.Scenarios, 1)
			assert.Empty(t, report.Scenarios[0].Error)
			assert.Equal(t, value <= 1, report.Passed)
			assert.Equal(t, 1, sameChecks.calls)
			_, err = runner.Assess(t.Context(), changed, archive)
			assert.ErrorContains(t, err, "schema")
			assert.Zero(t, changedChecks.calls, "reject before executing a changed predicate")
			if value > 1 {
				binding, err := changed.Scenarios[0].Bind(archive.Observations[0].Data)
				require.NoError(t, err)
				require.Len(t, binding.Checks, 1)
				assert.True(t, binding.Checks[0].Passed, "binary32 rounds this saved number to one")
			}
		})
	}
}

func fmtValue(value float64) string {
	if value <= 1 { return "passing_value" }
	return "rounding_counterexample"
}

func TestStringAndBytesIdentity(t *testing.T) {
	text, err := gentext.ForAssessment()
	require.NoError(t, err)
	bytes, err := genbytes.ForAssessment()
	require.NoError(t, err)
	assert.NotEqual(t, text.Scenarios[0].Schema, bytes.Scenarios[0].Schema)
	assert.NotEqual(t, text.Scenarios[0].Requirements[0].SchemaID, bytes.Scenarios[0].Requirements[0].SchemaID)
	textBinding, err := text.Scenarios[0].Bind([]byte("{\"value\":\"aGk=\"}"))
	require.NoError(t, err)
	bytesBinding, err := bytes.Scenarios[0].Bind([]byte("{\"value\":\"aGk=\"}"))
	require.NoError(t, err)
	assert.Equal(t, "aGk=", textBinding.Subjects["measurement/sample/meaning"][0].Content)
	assert.Equal(t, "\"aGk=\"", bytesBinding.Subjects["measurement/sample/meaning"][0].Content)
}
`

const observationValidationConsumer = `package statuses

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type statusHooks struct { value Status }

func (h *statusHooks) Open(context.Context) (Status, error) { return h.value, nil }
func (h *statusHooks) Restricted(context.Context) (Status, error) { return h.value, nil }
func (h *statusHooks) Alternate(context.Context) (Status, error) { return h.value, nil }
func (h *statusHooks) Twin(context.Context) (Status, error) { return h.value, nil }

func TestScenarioRootOccurrenceValidation(t *testing.T) {
	for _, value := range []string{"online", "offline", "unexpected"} {
		hooks := &statusHooks{value: Status(value)}
		suite, err := New(hooks, Inputs{})
		require.NoError(t, err)
		for _, scenario := range suite.Scenarios {
			t.Run(scenario.ID+"/"+value, func(t *testing.T) {
				valid := value == "online" || scenario.ID == "open" && !definitionRestricted
				if scenario.ID == "alternate" {
					valid = value != "offline"
				} else if value == "offline" {
					valid = true
				}
				_, decodeErr := scenario.Bind([]byte(strconv.Quote(value)))
				_, encodeErr := scenario.Capture(t.Context())
				if valid {
					assert.NoError(t, decodeErr)
					assert.NoError(t, encodeErr)
				} else {
					assert.Error(t, decodeErr, "root occurrence Enum must reject the saved value")
					assert.Error(t, encodeErr, "root occurrence Enum must reject the captured value")
				}
			})
		}
	}
}
`
