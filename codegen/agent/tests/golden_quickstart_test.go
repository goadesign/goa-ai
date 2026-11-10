package tests

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	. "goa.design/goa-ai/dsl"
	evaldsl "goa.design/goa-ai/eval/dsl"
	"goa.design/goa-ai/testutil"
	goadsl "goa.design/goa/v3/dsl"
)

// Validates the Quickstart README via a golden for the stable header section
// and a few structural markers for the rest to avoid brittleness.
func TestQuickstart_Renders_Minimal(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecsMinimal())

	content := fileContent(t, files, "AGENTS_QUICKSTART.md")
	require.NotEmpty(t, content)

	// Compare the header + services overview against a golden with normalization.
	// Split at the start of section 2 to keep the golden focused and stable.
	split := "\n## 2. 🚀 The 3-Step Liftoff"
	var header string
	if idx := strings.Index(content, split); idx > 0 {
		header = content[:idx+1] // include trailing newline before the section header
	} else {
		t.Fatalf("expected quickstart section header %q", split)
	}
	testutil.AssertString(t, "testdata/golden/quickstart/minimal.header.md.golden", header)

	// Sanity markers beyond the header to ensure key content is present.
	require.Contains(t, content, "calc.scribe")
	require.Contains(t, content, "calc.helpers")
	require.Contains(t, content, "client := scribe.NewClient(rt)")
	require.Contains(t, content, "`PlanStart` decodes a generated payload example")
	require.Contains(t, content, "The runtime matches the result to the pending call")
	require.NotContains(t, content, "PriorInput:")
	require.NotContains(t, content, "ExampleJSON:")
	require.Contains(t, content, "## 4. 🧠 The Planner:")
	require.NotContains(t, content, "Service-Side Tool Providers (Registry-Routed Execution)")
	require.Contains(t, content, `Deferred("search", "analyze")`)
	require.Contains(t, content, "but not `FromRegistry` or whole registries")

	// No suites declared: the evaluation section renders the teaser only.
	require.Contains(t, content, "## 8. 🧪 Evaluating Your Agents")
	require.Contains(t, content, "doesn't declare any evaluation suites yet")
	require.NotContains(t, content, "### Suite `")
}

// TestQuickstart_NamedSections pins the section names a Goa plugin uses to
// replace one part of AGENTS_QUICKSTART.md, their order, and that each
// section after the header starts at its separator, so a replaced section
// keeps the guide's layout. Each numbered heading belongs to exactly one
// section, so replacing a section replaces that whole part of the guide.
func TestQuickstart_NamedSections(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecsMinimal())
	var names []string
	headings := make(map[string]string)
	for _, f := range files {
		if f.Path != "AGENTS_QUICKSTART.md" {
			continue
		}
		for i, s := range f.SectionTemplates {
			names = append(names, s.Name)
			if i > 0 {
				assert.True(t, strings.HasPrefix(s.Source, "---\n"), "section %s starts at its separator", s.Name)
			}
			var b strings.Builder
			require.NoError(t, s.Write(&b))
			for _, line := range strings.Split(b.String(), "\n") {
				if strings.HasPrefix(line, "## ") {
					_, dup := headings[line]
					assert.False(t, dup, "heading %q appears once", line)
					headings[line] = s.Name
				}
			}
		}
	}
	require.Equal(t, []string{
		"agents-quickstart-header",
		"agents-quickstart-design",
		"agents-quickstart-liftoff",
		"agents-quickstart-agents",
		"agents-quickstart-planner",
		"agents-quickstart-tools",
		"agents-quickstart-toolbox",
		"agents-quickstart-exports",
		"agents-quickstart-evaluations",
		"agents-quickstart-advanced",
		"agents-quickstart-golden-rules",
		"agents-quickstart-faq",
	}, names)
	for n := 1; n <= 11; n++ {
		var owner string
		for line, section := range headings {
			if strings.HasPrefix(line, fmt.Sprintf("## %d. ", n)) {
				owner = section
			}
		}
		assert.Equal(t, names[n], owner, "heading ## %d. belongs to section %s", n, names[n])
	}
}

func TestQuickstart_DocumentsDeclaredEvalSuites(t *testing.T) {
	design := func() {
		goadsl.Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("add", "Add two numbers", func() {
						Args(func() {
							goadsl.Attribute("a", goadsl.Int, "Left operand")
							goadsl.Attribute("b", goadsl.Int, "Right operand")
							goadsl.Required("a", "b")
						})
						Return(func() {
							goadsl.Attribute("sum", goadsl.Int, "Sum of the operands")
							goadsl.Required("sum")
						})
					})
				})
				evaldsl.Suite("math_quality", func() {
					goadsl.Description("Evaluates arithmetic answers end to end.")
					goadsl.Timeout("30s")
					evaldsl.Scenario("simple_sum", func() {
						goadsl.Description("The agent answers a simple addition question.")
						evaldsl.Input(func() {
							goadsl.Attribute("question", goadsl.String, "Question to ask")
							goadsl.Required("question")
						})
						Tags("smoke")
					})
					evaldsl.Scenario("tool_contract", func() {
						goadsl.Description("The add tool contract is reachable from the agent.")
						Tags("contract")
					})
				})
			})
		})
	}
	files := buildAndGenerate(t, design)

	content := fileContent(t, files, "AGENTS_QUICKSTART.md")
	require.Contains(t, content, "## 8. 🧪 Evaluating Your Agents")
	require.Contains(t, content, "### Suite `math_quality` (agent `calc.scribe`)")
	require.Contains(t, content, "Evaluates arithmetic answers end to end.")
	require.Contains(t, content, "**`simple_sum`** (tags: `smoke`): The agent answers a simple addition question. Supply its typed input when constructing the suite in `cmd/math_quality-evals`.")
	require.Contains(t, content, "**`tool_contract`** (tags: `contract`): The add tool contract is reachable from the agent.")
	require.Contains(t, content, "gen/evals/math_quality/")
	require.Contains(t, content, "go run ./cmd/math_quality-evals")
	require.Contains(t, content, "judge.New(modelClient, maxOutputTokens)")
	require.Contains(t, content, "handle its returned error")
	require.NotContains(t, content, "doesn't declare any evaluation suites yet")
	// Sections after the inserted one keep sequential numbering.
	require.Contains(t, content, "## 9. Ready for Prime Time")
	require.Contains(t, content, "## 10. 📜 The Golden Rules")
	require.Contains(t, content, "## 11. 🤔 Stuck?")
}

func TestQuickstart_Disabled(t *testing.T) {
	design := func() {
		goadsl.API("calc", func() {
			DisableAgentDocs()
		})
		goadsl.Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {})
			})
		})
	}
	files := buildAndGenerate(t, design)

	// Ensure quickstart is not emitted
	for _, f := range files {
		require.NotEqual(t, "AGENTS_QUICKSTART.md", f.Path, "AGENTS_QUICKSTART.md should not be generated when DisableAgentDocs is set")
	}
}

func TestQuickstart_IncludesProvidersSection_WhenGenerated(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceToolsetBindSelf())

	content := fileContent(t, files, "AGENTS_QUICKSTART.md")
	require.NotEmpty(t, content)
	require.Contains(t, content, "Service-Side Tool Providers (Registry-Routed Execution)")
	require.Contains(t, content, "gen/<service>/toolsets/<toolset>/provider.go")
	beforeServe, _, found := strings.Cut(content, "go func() {")
	require.True(t, found)
	require.Contains(t, beforeServe, "registrywire.ValidateAdmissionRevision(admissionRevision)")
	require.Contains(t, content, "Register: func(ctx context.Context, toolset, providerID, incarnationID string)")
	require.Contains(t, content, "AdmissionRevision: admissionRevision,")
	require.NotContains(t, content, "incarnationID, admissionRevision string")

	// The generated example must renew the existing lease through the small
	// typed operation; it cannot upload tool definitions after startup.
	_, renewal, found := strings.Cut(content, "Renew: func(")
	require.True(t, found, "the provider example must supply the required Renew callback")
	renewal, _, found = strings.Cut(renewal, "Drain: func(")
	require.True(t, found, "the provider example must retain its shutdown callbacks")
	require.Contains(t, renewal, "(time.Duration, error)")
	require.Contains(t, renewal, "registryClient.RenewProvider(ctx, &registry.RenewProviderPayload{")
	for _, field := range []string{"Name:", "ProviderID:", "ProviderIncarnationID:", "ExpectedRegistrationToken:"} {
		require.Contains(t, renewal, field)
	}
	require.Contains(t, renewal, "LeaseDurationMs")
	require.NotContains(t, renewal, "registryClient.Register(")
	require.NotContains(t, renewal, "RegistrationLease")
	require.NotContains(t, renewal, "Tools:")
	require.NotContains(t, renewal, "SchemaFingerprint")

	// Completion distinguishes a retained provider result from deadline settlement.
	_, completion, found := strings.Cut(content, "Complete: func(")
	require.True(t, found, "the provider example must supply the required Complete callback")
	completion, _, found = strings.Cut(completion, "PublishOutputDelta: func(")
	require.True(t, found, "the provider example must retain its output callback")
	completion = strings.Join(strings.Fields(completion), " ")
	require.Contains(t, completion, "registrywire.ToolResultMessage) (bool, error)")
	require.Contains(t, completion, "resultJSON, err := json.Marshal(result) if err != nil { return false, err }")
	require.Contains(t, completion, "completion, err := registryClient.CompleteToolCall(ctx, &registry.CompleteToolCallPayload{")
	require.Contains(t, completion, "}) if err != nil { return false, err } return completion.Accepted, nil")
	require.NotContains(t, completion, "err == nil")
}
