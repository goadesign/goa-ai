# goa-ai Vertex Model Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `features/model/vertex/` provider to goa-ai: a greenfield Gemini-on-Vertex `model.Client` (+`model.TokenCounter`) built on `google.golang.org/genai`, a zero-translator Claude-on-Vertex constructor reusing `features/model/anthropic` through the SDK's `vertex` subpackage, and `VertexConfig` runtime factories mirroring Bedrock/OpenAI.

**Architecture:** One package `features/model/vertex` following the OpenAI package's file split (`client.go`, `messages.go`, `tools.go`, `translate.go`, `stream.go`, `tool_name.go`, `errors.go`, `anthropic.go`, `doc.go`). The Gemini SDK is mocked at an interface seam (`GenerativeClient`) exactly like `anthropic.MessagesClient` / `bedrock.RuntimeClient`. Claude-on-Vertex wraps the existing `anthropic.Client` with an error-mapping decorator that fixes the known 429-detection gap (do NOT copy `anthropic.isRateLimited`; follow `bedrock.wrapBedrockError`).

**Tech Stack:** Go 1.25, `google.golang.org/genai` (new direct dep), `github.com/anthropics/anthropic-sdk-go v1.46.0` + its `vertex` subpackage (existing dep), testify.

## Global Constraints

- Repo: `~/src/goa-ai`, module `goa.design/goa-ai`. All paths below are repo-relative.
- Workflow per AGENTS.md: change → `make lint` → fix → `make test`. Final gate: `make ci`.
- Tests: fast, deterministic, table-driven, `testify/assert` (use `require` only when the test cannot continue). Mock at the SDK-interface seam; no recorded HTTP fixtures.
- Files ≤ ~1000 lines; split proactively. `lower_snake_case.go` filenames. Every exported identifier gets GoDoc; every non-trivial file gets a header comment stating invariants and the adjacent-layer contract.
- Imports grouped: stdlib separate from external. `go fmt ./...` before each commit.
- New dependency policy (AGENTS.md): explain why first. Rationale for `google.golang.org/genai`: it is Google's official unified Go SDK for Gemini on both the Gemini API and Vertex AI backends (successor to `cloud.google.com/go/vertexai/genai`), supports ADC auth, function calling with raw JSON-schema (`ParametersJsonSchema`), thinking config, structured output, streaming via `iter.Seq2`, and `CountTokens`. No alternative covers Vertex Gemini with this surface.
- Providers never retry internally. Classify errors: throttling → `errors.Join(model.ErrRateLimited, model.NewProviderError(...))`; everything else → `model.NewProviderError` with the right kind (400→invalid_request, 401/403→auth, 429→rate_limited+retryable, 5xx→unavailable+retryable).
- `Request.PromptRefs` is provenance metadata — never translate it to the provider wire.
- Adapters are stateless: the full provider-ready transcript arrives on every call.
- SDK field-name caveat: `google.golang.org/genai` struct fields used below were taken from the package docs (`ClientConfig`, `Part`, `Candidate`, `FunctionDeclaration`, `FunctionCallingConfig`, `UsageMetadata`, `GenerateContent`/`GenerateContentStream` signatures are verbatim). A few config-struct field spellings (e.g. `ResponseJsonSchema` vs `ResponseJSONSchema`, `ThinkingConfig` internals) must be confirmed against the installed SDK on first compile — if a name fails to compile, check `go doc google.golang.org/genai GenerateContentConfig` and fix the call site, not the design.

---

### Task 1: Add the genai dependency and the package scaffold (doc, options, model routing)

**Files:**
- Modify: `go.mod` (via `go get`)
- Create: `features/model/vertex/doc.go`
- Create: `features/model/vertex/options.go`
- Test: `features/model/vertex/options_test.go`

**Interfaces:**
- Consumes: `runtime/agent/model` (`model.Request`, `model.ModelClass*` constants).
- Produces: `vertex.Options{DefaultModel, HighModel, SmallModel, MaxTokens, Temperature, ThinkingBudget string/int fields}` and `(Options) resolveModelID(req *model.Request) string` used by every later task. Package name is `vertex`; provider name constant `geminiProviderName = "vertex-gemini"`.

- [ ] **Step 1: Add the dependency**

```bash
cd ~/src/goa-ai && go get google.golang.org/genai@latest && go mod tidy
```

Expected: `go.mod` gains `google.golang.org/genai` in the require block.

- [ ] **Step 2: Write the failing test for model routing**

`features/model/vertex/options_test.go`:

```go
package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestResolveModelID(t *testing.T) {
	opts := Options{
		DefaultModel: "gemini-2.5-pro",
		HighModel:    "gemini-2.5-pro-high",
		SmallModel:   "gemini-2.5-flash",
	}
	cases := []struct {
		name string
		req  *model.Request
		want string
	}{
		{"explicit model wins", &model.Request{Model: "gemini-exp", ModelClass: model.ModelClassSmall}, "gemini-exp"},
		{"high class", &model.Request{ModelClass: model.ModelClassHighReasoning}, "gemini-2.5-pro-high"},
		{"small class", &model.Request{ModelClass: model.ModelClassSmall}, "gemini-2.5-flash"},
		{"default class", &model.Request{ModelClass: model.ModelClassDefault}, "gemini-2.5-pro"},
		{"unknown class falls back to default", &model.Request{ModelClass: model.ModelClass("weird")}, "gemini-2.5-pro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, opts.resolveModelID(tc.req))
		})
	}
}

func TestResolveModelIDMissingClassFallsBack(t *testing.T) {
	opts := Options{DefaultModel: "gemini-2.5-pro"}
	assert.Equal(t, "gemini-2.5-pro",
		opts.resolveModelID(&model.Request{ModelClass: model.ModelClassHighReasoning}))
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./features/model/vertex/ -run TestResolveModelID -v`
Expected: FAIL (package does not exist / `Options` undefined).

- [ ] **Step 4: Implement doc.go and options.go**

`features/model/vertex/doc.go`:

```go
// Package vertex provides Google Cloud Vertex AI model clients for goa-ai:
// a Gemini adapter implementing model.Client and model.TokenCounter, and a
// Claude-on-Vertex constructor that reuses features/model/anthropic through
// the Anthropic SDK's vertex transport.
//
// Adapter contract (mirrors features/model/openai/contract.go):
//
//   - Stateless: every call receives the full provider-ready transcript.
//   - Model-class routing happens inside the adapter via Options
//     (DefaultModel/HighModel/SmallModel); explicit Request.Model wins.
//   - Tool names are sanitized reversibly; tool calls for names not
//     advertised this request surface as-is so the runtime can produce an
//     "unknown tool" result.
//   - PromptRefs are provenance metadata and are never sent on the wire.
//   - No internal retries. Throttling surfaces as
//     errors.Join(model.ErrRateLimited, *model.ProviderError); other
//     failures as *model.ProviderError with kind/status/retryable set.
//   - Unsupported feature combinations fail fast (e.g. Gemini structured
//     output cannot be combined with tool definitions).
package vertex
```

`features/model/vertex/options.go`:

```go
package vertex

import "goa.design/goa-ai/runtime/agent/model"

// geminiProviderName identifies the Gemini-on-Vertex adapter in provider errors.
const geminiProviderName = "vertex-gemini"

// anthropicProviderName identifies the Claude-on-Vertex adapter in provider errors.
const anthropicProviderName = "vertex-anthropic"

// Options configures the Gemini-on-Vertex model client.
//
// Model IDs are Vertex publisher model names (e.g. "gemini-2.5-pro",
// "gemini-2.5-flash"). DefaultModel is required; HighModel and SmallModel
// are optional per-class overrides.
type Options struct {
	// DefaultModel is used when the request names no model and no class
	// override matches.
	DefaultModel string
	// HighModel serves ModelClassHighReasoning requests when set.
	HighModel string
	// SmallModel serves ModelClassSmall requests when set.
	SmallModel string
	// MaxTokens caps output tokens when the request does not set MaxTokens.
	MaxTokens int
	// Temperature is the default sampling temperature when the request does
	// not set one.
	Temperature float32
	// ThinkingBudget is the default thinking token budget applied when the
	// request enables thinking without a budget.
	ThinkingBudget int
}

// resolveModelID picks the concrete Vertex model for a request: an explicit
// Request.Model wins, then the configured class override, then DefaultModel.
func (o Options) resolveModelID(req *model.Request) string {
	if req.Model != "" {
		return req.Model
	}
	switch req.ModelClass {
	case model.ModelClassHighReasoning:
		if o.HighModel != "" {
			return o.HighModel
		}
	case model.ModelClassSmall:
		if o.SmallModel != "" {
			return o.SmallModel
		}
	}
	return o.DefaultModel
}
```

- [ ] **Step 5: Run tests, lint**

Run: `go test ./features/model/vertex/ -v && make lint`
Expected: PASS, lint clean.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum features/model/vertex/
git commit -m "feat(vertex): scaffold vertex provider package with model-class routing"
```

---

### Task 2: Reversible tool-name sanitizer

**Files:**
- Create: `features/model/vertex/tool_name.go`
- Test: `features/model/vertex/tool_name_test.go`

**Interfaces:**
- Produces: `sanitizeToolName(name string) string` and `buildToolNameMaps(defs []*model.ToolDefinition) (canonToProv map[string]string, provToCanon map[string]string)`. Gemini function names must match `^[a-zA-Z_][a-zA-Z0-9_.:-]{0,63}$`; goa-ai tool idents (e.g. `extraction.emit.emit_event`) contain dots, which Gemini allows, but slashes and other characters must map to `_`. Later tasks (tools.go, translate.go, stream.go) consume both maps.

- [ ] **Step 1: Write the failing test**

`features/model/vertex/tool_name_test.go`:

```go
package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/tools"
)

func TestSanitizeToolName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"extraction.emit.emit_event", "extraction.emit.emit_event"}, // dots allowed
		{"toolset/tool", "toolset_tool"},                             // slash rewritten
		{"9starts_with_digit", "_9starts_with_digit"},                // must start with letter/_
		{"has spaces here", "has_spaces_here"},
		{"", "_"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeToolName(tc.in))
		})
	}
}

func TestSanitizeToolNameTruncatesTo64(t *testing.T) {
	long := make([]byte, 100)
	for i := range long {
		long[i] = 'a'
	}
	got := sanitizeToolName(string(long))
	assert.Len(t, got, 64)
}

func TestBuildToolNameMapsRoundTrip(t *testing.T) {
	defs := []*model.ToolDefinition{
		{Name: "feed/find_duplicates"},
		{Name: "extraction.emit.emit_event"},
	}
	canonToProv, provToCanon := buildToolNameMaps(defs)
	assert.Equal(t, "feed_find_duplicates", canonToProv["feed/find_duplicates"])
	assert.Equal(t, tools.Ident("feed/find_duplicates"), tools.Ident(provToCanon["feed_find_duplicates"]))
	assert.Equal(t, "extraction.emit.emit_event", provToCanon["extraction.emit.emit_event"])
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./features/model/vertex/ -run 'TestSanitize|TestBuildToolName' -v`
Expected: FAIL — `sanitizeToolName` undefined.

- [ ] **Step 3: Implement**

`features/model/vertex/tool_name.go`:

```go
package vertex

import "goa.design/goa-ai/runtime/agent/model"

// sanitizeToolName rewrites a goa-ai tool identifier into a Gemini-legal
// function name: first char [a-zA-Z_], rest [a-zA-Z0-9_.:-], max 64 chars.
// The mapping is deterministic so buildToolNameMaps can invert it per request.
func sanitizeToolName(name string) string {
	if name == "" {
		return "_"
	}
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		legal := c == '_' || c == '.' || c == ':' || c == '-' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if i == 0 {
			first := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
			if !first {
				out = append(out, '_')
			}
			if legal {
				out = append(out, c)
			} else {
				continue
			}
			continue
		}
		if legal {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// buildToolNameMaps returns the canonical→provider and provider→canonical
// name maps for one request's tool definitions. Collisions after
// sanitization keep the first definition and are not remapped; the model
// then cannot address the shadowed tool, which surfaces as an unknown-tool
// call the runtime already handles.
func buildToolNameMaps(defs []*model.ToolDefinition) (map[string]string, map[string]string) {
	canonToProv := make(map[string]string, len(defs))
	provToCanon := make(map[string]string, len(defs))
	for _, def := range defs {
		prov := sanitizeToolName(def.Name)
		if _, taken := provToCanon[prov]; taken {
			continue
		}
		canonToProv[def.Name] = prov
		provToCanon[prov] = def.Name
	}
	return canonToProv, provToCanon
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./features/model/vertex/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add features/model/vertex/tool_name.go features/model/vertex/tool_name_test.go
git commit -m "feat(vertex): reversible Gemini tool-name sanitization"
```

---

### Task 3: Message transcript encoding (goa-ai → Gemini contents)

**Files:**
- Create: `features/model/vertex/messages.go`
- Test: `features/model/vertex/messages_test.go`

**Interfaces:**
- Consumes: `model.Message`/`model.Part` types, `canonToProv` map from Task 2, `google.golang.org/genai` `Content`/`Part` structs.
- Produces: `encodeContents(msgs []*model.Message, canonToProv map[string]string) (system *genai.Content, contents []*genai.Content, err error)`. Mapping rules:
  - `ConversationRoleSystem` messages concatenate into one `system` Content (Gemini `SystemInstruction`); never appear in `contents`.
  - `user` → role `"user"`, `assistant` → role `"model"`.
  - `TextPart` → `Part{Text}`; `ImagePart` → `Part{InlineData: &genai.Blob{MIMEType, Data}}`; `ToolUsePart` → `Part{FunctionCall: &genai.FunctionCall{Name: sanitized, Args: input-as-map}}`; `ToolResultPart` → `Part{FunctionResponse: &genai.FunctionResponse{Name: sanitized, Response: wrapped}}` on a `"user"` content; `ThinkingPart` with a Signature → `Part{Thought: true, Text, ThoughtSignature}` (echo back per Gemini replay rules); `CacheCheckpointPart` is skipped (Gemini has no inline cache markers; implicit caching applies).
  - Tool results whose content is not already a JSON object are wrapped as `map[string]any{"output": v}` (Gemini requires an object).

- [ ] **Step 1: Write the failing test**

`features/model/vertex/messages_test.go`:

```go
package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestEncodeContentsSystemAndRoles(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "be terse"}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "hello"}}},
	}
	system, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	require.NotNil(t, system)
	assert.Equal(t, "be terse", system.Parts[0].Text)
	require.Len(t, contents, 2)
	assert.Equal(t, "user", contents[0].Role)
	assert.Equal(t, "model", contents[1].Role)
}

func TestEncodeContentsToolLoop(t *testing.T) {
	canonToProv := map[string]string{"feed/find_duplicates": "feed_find_duplicates"}
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "c1", Name: "feed/find_duplicates", Input: map[string]any{"title": "picnic"}},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "c1", Content: []any{"m1"}},
		}},
	}
	_, contents, err := encodeContents(msgs, canonToProv)
	require.NoError(t, err)
	require.Len(t, contents, 2)
	fc := contents[0].Parts[0].FunctionCall
	require.NotNil(t, fc)
	assert.Equal(t, "feed_find_duplicates", fc.Name)
	assert.Equal(t, "picnic", fc.Args["title"])
	fr := contents[1].Parts[0].FunctionResponse
	require.NotNil(t, fr)
	assert.Contains(t, fr.Response, "output") // non-object content wrapped
}

func TestEncodeContentsThinkingEcho(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Text: "reasoning", Signature: "c2ln", Final: true},
			model.TextPart{Text: "answer"},
		}},
	}
	_, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	parts := contents[0].Parts
	require.Len(t, parts, 2)
	assert.True(t, parts[0].Thought)
	assert.NotEmpty(t, parts[0].ThoughtSignature)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./features/model/vertex/ -run TestEncodeContents -v`
Expected: FAIL — `encodeContents` undefined.

- [ ] **Step 3: Implement**

`features/model/vertex/messages.go`:

```go
package vertex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

// encodeContents translates the goa-ai transcript into Gemini contents.
// System messages fold into the returned system instruction; user and
// assistant messages become "user"/"model" contents. Tool use/result parts
// use the sanitized names from canonToProv so replayed history matches the
// advertised declarations.
func encodeContents(msgs []*model.Message, canonToProv map[string]string) (*genai.Content, []*genai.Content, error) {
	var systemTexts []string
	contents := make([]*genai.Content, 0, len(msgs))
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		if msg.Role == model.ConversationRoleSystem {
			for _, part := range msg.Parts {
				if tp, ok := part.(model.TextPart); ok {
					systemTexts = append(systemTexts, tp.Text)
				}
			}
			continue
		}
		role := "user"
		if msg.Role == model.ConversationRoleAssistant {
			role = "model"
		}
		parts := make([]*genai.Part, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			gp, err := encodePart(part, canonToProv)
			if err != nil {
				return nil, nil, err
			}
			if gp != nil {
				parts = append(parts, gp)
			}
		}
		if len(parts) == 0 {
			continue
		}
		contents = append(contents, &genai.Content{Role: role, Parts: parts})
	}
	var system *genai.Content
	if len(systemTexts) > 0 {
		system = &genai.Content{Parts: []*genai.Part{{Text: strings.Join(systemTexts, "\n\n")}}}
	}
	return system, contents, nil
}

func encodePart(part model.Part, canonToProv map[string]string) (*genai.Part, error) {
	switch p := part.(type) {
	case model.TextPart:
		return &genai.Part{Text: p.Text}, nil
	case model.ImagePart:
		return &genai.Part{InlineData: &genai.Blob{
			MIMEType: "image/" + string(p.Format),
			Data:     p.Bytes,
		}}, nil
	case model.ToolUsePart:
		args, err := toArgsMap(p.Input)
		if err != nil {
			return nil, fmt.Errorf("vertex: encode tool use %q: %w", p.Name, err)
		}
		return &genai.Part{FunctionCall: &genai.FunctionCall{
			Name: providerToolName(p.Name, canonToProv),
			Args: args,
		}}, nil
	case model.ToolResultPart:
		resp, err := toResponseMap(p.Content, p.IsError)
		if err != nil {
			return nil, fmt.Errorf("vertex: encode tool result %q: %w", p.ToolUseID, err)
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{
			Name:     p.ToolUseID,
			Response: resp,
		}}, nil
	case model.ThinkingPart:
		gp := &genai.Part{Thought: true, Text: p.Text}
		if p.Signature != "" {
			sig, err := base64.StdEncoding.DecodeString(p.Signature)
			if err != nil {
				sig = []byte(p.Signature)
			}
			gp.ThoughtSignature = sig
		}
		return gp, nil
	case model.CacheCheckpointPart:
		return nil, nil // Gemini uses implicit caching; no inline markers.
	default:
		return nil, nil // Unsupported parts (documents, citations) are skipped.
	}
}

// providerToolName returns the sanitized name for a canonical tool name,
// falling back to on-the-fly sanitization for names outside this request's
// definitions (replayed history from other requests).
func providerToolName(name string, canonToProv map[string]string) string {
	if prov, ok := canonToProv[name]; ok {
		return prov
	}
	return sanitizeToolName(name)
}

// toArgsMap coerces a tool input value into the map form Gemini requires.
func toArgsMap(v any) (map[string]any, error) {
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"input": v}, nil //nolint:nilerr // non-object inputs are wrapped
	}
	return m, nil
}

// toResponseMap coerces a tool result into a JSON object, wrapping
// non-object values under "output" and flagging errors under "error".
func toResponseMap(v any, isError bool) (map[string]any, error) {
	var m map[string]any
	if mm, ok := v.(map[string]any); ok {
		m = mm
	} else {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			m = map[string]any{"output": v}
		}
	}
	if isError {
		m["error"] = true
	}
	return m, nil
}
```

Note: `FunctionResponse.Name` carries the tool-use ID because Gemini correlates call/response by name+order; the runtime's `ToolResultPart` has no canonical name field. If the installed SDK exposes `FunctionCall.ID`/`FunctionResponse.ID` fields, use those for correlation instead and put the sanitized tool name in `Name` — check `go doc google.golang.org/genai FunctionResponse` during implementation and prefer ID-based correlation when available.

- [ ] **Step 4: Run tests, lint**

Run: `go test ./features/model/vertex/ -v && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add features/model/vertex/messages.go features/model/vertex/messages_test.go
git commit -m "feat(vertex): encode goa-ai transcripts as Gemini contents"
```

---

### Task 4: Tool declarations, tool choice, and structured output

**Files:**
- Create: `features/model/vertex/tools.go`
- Test: `features/model/vertex/tools_test.go`

**Interfaces:**
- Consumes: `model.ToolDefinition` (`.Input.JSONSchema()` returns the raw JSON schema), `model.ToolChoice`, `model.StructuredOutput`, Task 2 maps.
- Produces:
  - `encodeTools(defs []*model.ToolDefinition, canonToProv map[string]string) ([]*genai.Tool, error)` — one `genai.Tool` holding all `FunctionDeclarations`, each with `ParametersJsonSchema` set to the normalized raw schema.
  - `encodeToolConfig(choice *model.ToolChoice, canonToProv map[string]string) *genai.ToolConfig` — auto→`FunctionCallingConfigModeAuto`, none→`...None`, any→`...Any`, tool→`...Any` + `AllowedFunctionNames: [sanitized]`.
  - `normalizeSchema(raw []byte) (any, error)` — unmarshals and strips `$schema`, `$id`, and root-level `example`/`examples` keys recursively at the top level only.
  - Structured-output gate lives in Task 6's `prepareRequest`: `StructuredOutput` with `Tools` present → `model.ErrStructuredOutputUnsupported` (Gemini cannot combine function calling with a response schema); alone → `ResponseMIMEType: "application/json"` + `ResponseJsonSchema`.

- [ ] **Step 1: Write the failing test**

`features/model/vertex/tools_test.go`:

```go
package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func toolDef(t *testing.T, name, schema string) *model.ToolDefinition {
	t.Helper()
	input, err := model.ToolInputFromSpec([]byte(schema), nil)
	require.NoError(t, err)
	return &model.ToolDefinition{Name: name, Description: "desc for " + name, Input: input}
}

func TestEncodeTools(t *testing.T) {
	defs := []*model.ToolDefinition{
		toolDef(t, "feed/find_duplicates", `{"$schema":"x","type":"object","properties":{"title":{"type":"string"}}}`),
	}
	canonToProv, _ := buildToolNameMaps(defs)
	tools, err := encodeTools(defs, canonToProv)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.Len(t, tools[0].FunctionDeclarations, 1)
	decl := tools[0].FunctionDeclarations[0]
	assert.Equal(t, "feed_find_duplicates", decl.Name)
	assert.Equal(t, "desc for feed/find_duplicates", decl.Description)
	schema, ok := decl.ParametersJsonSchema.(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, schema, "$schema")
	assert.Equal(t, "object", schema["type"])
}

func TestEncodeToolConfig(t *testing.T) {
	canonToProv := map[string]string{"a/b": "a_b"}
	cases := []struct {
		name   string
		choice *model.ToolChoice
		mode   genai.FunctionCallingConfigMode
		names  []string
	}{
		{"nil is auto", nil, genai.FunctionCallingConfigModeAuto, nil},
		{"none", &model.ToolChoice{Mode: model.ToolChoiceModeNone}, genai.FunctionCallingConfigModeNone, nil},
		{"any", &model.ToolChoice{Mode: model.ToolChoiceModeAny}, genai.FunctionCallingConfigModeAny, nil},
		{"tool", &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "a/b"}, genai.FunctionCallingConfigModeAny, []string{"a_b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := encodeToolConfig(tc.choice, canonToProv)
			require.NotNil(t, cfg)
			require.NotNil(t, cfg.FunctionCallingConfig)
			assert.Equal(t, tc.mode, cfg.FunctionCallingConfig.Mode)
			assert.Equal(t, tc.names, cfg.FunctionCallingConfig.AllowedFunctionNames)
		})
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./features/model/vertex/ -run 'TestEncodeTool' -v`
Expected: FAIL — `encodeTools` undefined. (If `model.ToolInputFromSpec`'s signature differs, check `go doc goa.design/goa-ai/runtime/agent/model ToolInputFromSpec` and adjust the helper — the brief confirms it exists.)

- [ ] **Step 3: Implement**

`features/model/vertex/tools.go`:

```go
package vertex

import (
	"encoding/json"
	"fmt"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

// encodeTools declares the request's tools as one genai.Tool with a
// FunctionDeclaration per definition. Schemas are passed through as raw
// JSON schema (ParametersJsonSchema) after normalization.
func encodeTools(defs []*model.ToolDefinition, canonToProv map[string]string) ([]*genai.Tool, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	decls := make([]*genai.FunctionDeclaration, 0, len(defs))
	for _, def := range defs {
		prov, ok := canonToProv[def.Name]
		if !ok {
			continue // shadowed by sanitization collision
		}
		schema, err := normalizeSchema(def.Input.JSONSchema())
		if err != nil {
			return nil, fmt.Errorf("vertex: tool %q schema: %w", def.Name, err)
		}
		decls = append(decls, &genai.FunctionDeclaration{
			Name:                 prov,
			Description:          def.Description,
			ParametersJsonSchema: schema,
		})
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}, nil
}

// encodeToolConfig maps the goa-ai tool choice onto Gemini's function
// calling config. A specific tool is expressed as mode ANY restricted to
// that tool's sanitized name.
func encodeToolConfig(choice *model.ToolChoice, canonToProv map[string]string) *genai.ToolConfig {
	fcc := &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAuto}
	if choice != nil {
		switch choice.Mode {
		case model.ToolChoiceModeNone:
			fcc.Mode = genai.FunctionCallingConfigModeNone
		case model.ToolChoiceModeAny:
			fcc.Mode = genai.FunctionCallingConfigModeAny
		case model.ToolChoiceModeTool:
			fcc.Mode = genai.FunctionCallingConfigModeAny
			if prov, ok := canonToProv[choice.Name]; ok {
				fcc.AllowedFunctionNames = []string{prov}
			} else {
				fcc.AllowedFunctionNames = []string{sanitizeToolName(choice.Name)}
			}
		}
	}
	return &genai.ToolConfig{FunctionCallingConfig: fcc}
}

// normalizeSchema prepares a goa-ai JSON schema for Gemini: it parses the
// raw bytes and drops metadata keywords Gemini rejects ($schema, $id) plus
// root-level examples, which goa-ai conveys separately.
func normalizeSchema(raw []byte) (any, error) {
	if len(raw) == 0 {
		return map[string]any{"type": "object"}, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	delete(schema, "$schema")
	delete(schema, "$id")
	delete(schema, "example")
	delete(schema, "examples")
	return schema, nil
}
```

- [ ] **Step 4: Run tests, lint**

Run: `go test ./features/model/vertex/ -v && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add features/model/vertex/tools.go features/model/vertex/tools_test.go
git commit -m "feat(vertex): Gemini tool declarations, tool choice, schema normalization"
```

---

### Task 5: Response and usage translation

**Files:**
- Create: `features/model/vertex/translate.go`
- Test: `features/model/vertex/translate_test.go`

**Interfaces:**
- Consumes: `genai.GenerateContentResponse` (`.Candidates[0].Content.Parts`, `.Candidates[0].FinishReason`, `.UsageMetadata`), `provToCanon` map.
- Produces:
  - `translateResponse(resp *genai.GenerateContentResponse, modelID string, class model.ModelClass, provToCanon map[string]string) (*model.Response, error)` — text parts → one assistant `model.Message` with `TextPart`s; thought parts → `ThinkingPart{Text, Signature: base64(ThoughtSignature), Final: true}`; `FunctionCall` parts → `model.ToolCall{Name: canonical, Payload: marshaled args, ID}` (ID synthesized `call-N` when the SDK has none); `FinishReason` → `Response.StopReason` (raw string).
  - `translateUsage(md *genai.UsageMetadata, modelID string, class model.ModelClass) model.TokenUsage` — `PromptTokenCount`→Input, `ResponseTokenCount+ThoughtsTokenCount`→Output, `TotalTokenCount`→Total, `CachedContentTokenCount`→CacheRead; stamps Model/ModelClass (follow Bedrock, not Anthropic, which forgets to stamp).

- [ ] **Step 1: Write the failing test**

`features/model/vertex/translate_test.go`:

```go
package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestTranslateResponseTextAndToolCall(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{
				{Text: "found two"},
				{FunctionCall: &genai.FunctionCall{Name: "feed_find_duplicates", Args: map[string]any{"title": "picnic"}}},
			}},
		}},
		UsageMetadata: &genai.UsageMetadata{
			PromptTokenCount:   100,
			ResponseTokenCount: 20,
			ThoughtsTokenCount: 5,
			TotalTokenCount:    125,
		},
	}
	provToCanon := map[string]string{"feed_find_duplicates": "feed/find_duplicates"}
	out, err := translateResponse(resp, "gemini-2.5-pro", model.ModelClassDefault, provToCanon)
	require.NoError(t, err)
	require.Len(t, out.Content, 1)
	assert.Equal(t, model.ConversationRoleAssistant, out.Content[0].Role)
	require.Len(t, out.ToolCalls, 1)
	assert.Equal(t, "feed/find_duplicates", string(out.ToolCalls[0].Name))
	assert.JSONEq(t, `{"title":"picnic"}`, string(out.ToolCalls[0].Payload))
	assert.NotEmpty(t, out.ToolCalls[0].ID)
	assert.Equal(t, string(genai.FinishReasonStop), out.StopReason)
	assert.Equal(t, 100, out.Usage.InputTokens)
	assert.Equal(t, 25, out.Usage.OutputTokens)
	assert.Equal(t, 125, out.Usage.TotalTokens)
	assert.Equal(t, "gemini-2.5-pro", out.Usage.Model)
	assert.Equal(t, model.ModelClassDefault, out.Usage.ModelClass)
}

func TestTranslateResponseUnknownToolPassesThrough(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "never_advertised", Args: map[string]any{}}},
			}},
		}},
	}
	out, err := translateResponse(resp, "m", model.ModelClassDefault, map[string]string{})
	require.NoError(t, err)
	require.Len(t, out.ToolCalls, 1)
	// Unadvertised names surface as-is so the runtime produces an
	// unknown-tool result instead of the adapter erroring.
	assert.Equal(t, "never_advertised", string(out.ToolCalls[0].Name))
}

func TestTranslateResponseNoCandidates(t *testing.T) {
	_, err := translateResponse(&genai.GenerateContentResponse{}, "m", model.ModelClassDefault, nil)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./features/model/vertex/ -run TestTranslateResponse -v`
Expected: FAIL — `translateResponse` undefined.

- [ ] **Step 3: Implement**

`features/model/vertex/translate.go`:

```go
package vertex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/tools"
)

// translateResponse converts a Gemini response into the provider-neutral
// model.Response. Only the first candidate is used (CandidateCount is never
// set above 1 by this adapter).
func translateResponse(resp *genai.GenerateContentResponse, modelID string, class model.ModelClass, provToCanon map[string]string) (*model.Response, error) {
	if resp == nil || len(resp.Candidates) == 0 {
		return nil, errors.New("vertex: response has no candidates")
	}
	cand := resp.Candidates[0]
	out := &model.Response{StopReason: string(cand.FinishReason)}
	if cand.Content != nil {
		msg := model.Message{Role: model.ConversationRoleAssistant}
		callIndex := 0
		for _, part := range cand.Content.Parts {
			switch {
			case part.FunctionCall != nil:
				payload, err := json.Marshal(part.FunctionCall.Args)
				if err != nil {
					return nil, fmt.Errorf("vertex: marshal tool args: %w", err)
				}
				callIndex++
				out.ToolCalls = append(out.ToolCalls, model.ToolCall{
					Name:    tools.Ident(canonicalToolName(part.FunctionCall.Name, provToCanon)),
					Payload: payload,
					ID:      toolCallID(part.FunctionCall, callIndex),
				})
			case part.Thought:
				msg.Parts = append(msg.Parts, model.ThinkingPart{
					Text:      part.Text,
					Signature: base64.StdEncoding.EncodeToString(part.ThoughtSignature),
					Final:     true,
				})
			case part.Text != "":
				msg.Parts = append(msg.Parts, model.TextPart{Text: part.Text})
			}
		}
		if len(msg.Parts) > 0 {
			out.Content = append(out.Content, msg)
		}
	}
	out.Usage = translateUsage(resp.UsageMetadata, modelID, class)
	return out, nil
}

// canonicalToolName reverses sanitization; names never advertised this
// request pass through unchanged so the runtime can flag the unknown tool.
func canonicalToolName(prov string, provToCanon map[string]string) string {
	if canon, ok := provToCanon[prov]; ok {
		return canon
	}
	return prov
}

// toolCallID returns a stable per-response identifier for a function call.
func toolCallID(fc *genai.FunctionCall, index int) string {
	return fmt.Sprintf("call-%d-%s", index, fc.Name)
}

// translateUsage maps Gemini usage metadata onto model.TokenUsage, counting
// thought tokens as output and stamping model attribution.
func translateUsage(md *genai.UsageMetadata, modelID string, class model.ModelClass) model.TokenUsage {
	usage := model.TokenUsage{Model: modelID, ModelClass: class}
	if md == nil {
		return usage
	}
	usage.InputTokens = int(md.PromptTokenCount)
	usage.OutputTokens = int(md.ResponseTokenCount) + int(md.ThoughtsTokenCount)
	usage.TotalTokens = int(md.TotalTokenCount)
	usage.CacheReadTokens = int(md.CachedContentTokenCount)
	return usage
}
```

(If the installed SDK exposes `FunctionCall.ID`, prefer it in `toolCallID` when non-empty.)

- [ ] **Step 4: Run tests, lint**

Run: `go test ./features/model/vertex/ -v && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add features/model/vertex/translate.go features/model/vertex/translate_test.go
git commit -m "feat(vertex): translate Gemini responses and usage to model types"
```

---

### Task 6: Error mapping + the Gemini client with Complete

**Files:**
- Create: `features/model/vertex/errors.go`
- Create: `features/model/vertex/client.go`
- Test: `features/model/vertex/errors_test.go`, `features/model/vertex/client_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–5; `google.golang.org/api/googleapi` (`*googleapi.Error` carries HTTP status from Vertex).
- Produces:
  - `GenerativeClient` interface — the mock seam:
    ```go
    type GenerativeClient interface {
        GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
        GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error]
        CountTokens(ctx context.Context, model string, contents []*genai.Content, config *genai.CountTokensConfig) (*genai.CountTokensResponse, error)
    }
    ```
    (`*genai.Models` satisfies it.)
  - `New(models GenerativeClient, opts Options) (*Client, error)` — errors when `models` nil or `DefaultModel` empty.
  - `(*Client) Complete(ctx, req) (*model.Response, error)` implementing `model.Client`.
  - `prepareRequest(req *model.Request) (*preparedRequest, error)` shared with Stream (Task 7): resolves model ID, builds name maps, encodes contents/tools/tool config, applies thinking + structured-output gates, fills `genai.GenerateContentConfig`.
  - `wrapGeminiError(operation string, err error) error` — `*googleapi.Error` status 429 → `errors.Join(model.ErrRateLimited, providerError)`; 400→invalid_request, 401/403→auth, 5xx→unavailable+retryable; other → unknown. Used by Complete, Stream, CountTokens.

- [ ] **Step 1: Write the failing tests**

`features/model/vertex/errors_test.go`:

```go
package vertex

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestWrapGeminiError(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		kind      model.ProviderErrorKind
		retryable bool
		rateLtd   bool
	}{
		{"429", http.StatusTooManyRequests, model.ProviderErrorKindRateLimited, true, true},
		{"400", http.StatusBadRequest, model.ProviderErrorKindInvalidRequest, false, false},
		{"401", http.StatusUnauthorized, model.ProviderErrorKindAuth, false, false},
		{"403", http.StatusForbidden, model.ProviderErrorKindAuth, false, false},
		{"503", http.StatusServiceUnavailable, model.ProviderErrorKindUnavailable, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &googleapi.Error{Code: tc.status, Message: "boom"}
			err := wrapGeminiError("generate_content", src)
			assert.Equal(t, tc.rateLtd, errors.Is(err, model.ErrRateLimited))
			pe, ok := model.AsProviderError(err)
			require.True(t, ok)
			assert.Equal(t, tc.kind, pe.Kind())
		})
	}
}

func TestWrapGeminiErrorNonAPI(t *testing.T) {
	err := wrapGeminiError("generate_content", errors.New("dial tcp: timeout"))
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindUnknown, pe.Kind())
}
```

(If `ProviderError` accessors differ from `.Kind()`, check `go doc goa.design/goa-ai/runtime/agent/model ProviderError` and match the real API — the constructor signature from the brief is authoritative: `NewProviderError(provider, operation, httpStatus, kind, code, message, requestID, retryable, cause)`.)

`features/model/vertex/client_test.go`:

```go
package vertex

import (
	"context"
	"iter"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

type stubGenerativeClient struct {
	lastModel    string
	lastContents []*genai.Content
	lastConfig   *genai.GenerateContentConfig
	resp         *genai.GenerateContentResponse
	err          error
	streamChunks []*genai.GenerateContentResponse
	streamErr    error
	countResp    *genai.CountTokensResponse
}

func (s *stubGenerativeClient) GenerateContent(_ context.Context, m string, c []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	s.lastModel, s.lastContents, s.lastConfig = m, c, cfg
	return s.resp, s.err
}

func (s *stubGenerativeClient) GenerateContentStream(_ context.Context, m string, c []*genai.Content, cfg *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	s.lastModel, s.lastContents, s.lastConfig = m, c, cfg
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		for _, ch := range s.streamChunks {
			if !yield(ch, nil) {
				return
			}
		}
		if s.streamErr != nil {
			yield(nil, s.streamErr)
		}
	}
}

func (s *stubGenerativeClient) CountTokens(_ context.Context, m string, c []*genai.Content, _ *genai.CountTokensConfig) (*genai.CountTokensResponse, error) {
	s.lastModel, s.lastContents = m, c
	return s.countResp, s.err
}

func textResp(text string) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content:      &genai.Content{Role: "model", Parts: []*genai.Part{{Text: text}}},
		}},
		UsageMetadata: &genai.UsageMetadata{PromptTokenCount: 10, ResponseTokenCount: 3, TotalTokenCount: 13},
	}
}

func TestNewValidates(t *testing.T) {
	_, err := New(nil, Options{DefaultModel: "gemini-2.5-pro"})
	assert.Error(t, err)
	_, err = New(&stubGenerativeClient{}, Options{})
	assert.Error(t, err)
}

func TestCompleteTextOnly(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp("hello")}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro", MaxTokens: 256, Temperature: 0.2})
	require.NoError(t, err)
	resp, err := cl.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{
			{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "be terse"}}},
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.Content, 1)
	assert.Equal(t, "gemini-2.5-pro", stub.lastModel)
	require.NotNil(t, stub.lastConfig)
	assert.NotNil(t, stub.lastConfig.SystemInstruction)
	assert.EqualValues(t, 256, stub.lastConfig.MaxOutputTokens)
	assert.Equal(t, string(genai.FinishReasonStop), resp.StopReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
}

func TestCompleteStructuredOutputWithToolsRejected(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp("x")}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro"})
	require.NoError(t, err)
	def := toolDef(t, "a", `{"type":"object"}`)
	_, err = cl.Complete(context.Background(), &model.Request{
		Messages:         []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
		Tools:            []*model.ToolDefinition{def},
		StructuredOutput: &model.StructuredOutput{Name: "out", Schema: []byte(`{"type":"object"}`)},
	})
	assert.ErrorIs(t, err, model.ErrStructuredOutputUnsupported)
}

func TestCompleteThinkingConfig(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp("x")}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro", ThinkingBudget: 2048})
	require.NoError(t, err)
	_, err = cl.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
		Thinking: &model.ThinkingOptions{Enable: true},
	})
	require.NoError(t, err)
	require.NotNil(t, stub.lastConfig.ThinkingConfig)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./features/model/vertex/ -run 'TestWrapGemini|TestNewValidates|TestComplete' -v`
Expected: FAIL — `wrapGeminiError`, `New`, `Client` undefined.

- [ ] **Step 3: Implement errors.go**

```go
package vertex

import (
	"errors"
	"net/http"

	"google.golang.org/api/googleapi"

	"goa.design/goa-ai/runtime/agent/model"
)

// wrapGeminiError classifies a Gemini/Vertex failure into the goa-ai
// provider error contract. Throttling joins model.ErrRateLimited so the
// adaptive rate-limit middleware backs off; adapters never retry.
func wrapGeminiError(operation string, err error) error {
	if err == nil {
		return nil
	}
	status := 0
	message := err.Error()
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		status = gerr.Code
		message = gerr.Message
	}
	kind := model.ProviderErrorKindUnknown
	retryable := false
	switch {
	case status == http.StatusTooManyRequests:
		pe := model.NewProviderError(geminiProviderName, operation, status,
			model.ProviderErrorKindRateLimited, "rate_limited", message, "", true, err)
		return errors.Join(model.ErrRateLimited, pe)
	case status == http.StatusBadRequest:
		kind = model.ProviderErrorKindInvalidRequest
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = model.ProviderErrorKindAuth
	case status >= http.StatusInternalServerError && status <= http.StatusNetworkAuthenticationRequired:
		kind = model.ProviderErrorKindUnavailable
		retryable = true
	}
	return model.NewProviderError(geminiProviderName, operation, status, kind, "", message, "", retryable, err)
}
```

- [ ] **Step 4: Implement client.go**

```go
// Package-internal Gemini client. Header invariants:
//   - prepareRequest is the single translation entry point shared by
//     Complete, Stream, and CountTokens so gates apply uniformly.
//   - The adapter is stateless and never retries (see doc.go).
package vertex

import (
	"context"
	"errors"
	"iter"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// GenerativeClient abstracts the genai Models service so tests can stub
	// it. *genai.Models satisfies this interface.
	GenerativeClient interface {
		GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
		GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error]
		CountTokens(ctx context.Context, model string, contents []*genai.Content, config *genai.CountTokensConfig) (*genai.CountTokensResponse, error)
	}

	// Client is the Gemini-on-Vertex implementation of model.Client.
	Client struct {
		models GenerativeClient
		opts   Options
	}

	// preparedRequest carries one request's translated inputs.
	preparedRequest struct {
		modelID     string
		modelClass  model.ModelClass
		contents    []*genai.Content
		config      *genai.GenerateContentConfig
		provToCanon map[string]string
	}
)

// New builds a Gemini-on-Vertex client from a generative service and options.
func New(models GenerativeClient, opts Options) (*Client, error) {
	if models == nil {
		return nil, errors.New("vertex: generative client is required")
	}
	if opts.DefaultModel == "" {
		return nil, errors.New("vertex: default model is required")
	}
	return &Client{models: models, opts: opts}, nil
}

// Complete implements model.Client.
func (c *Client) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	prep, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.models.GenerateContent(ctx, prep.modelID, prep.contents, prep.config)
	if err != nil {
		return nil, wrapGeminiError("generate_content", err)
	}
	return translateResponse(resp, prep.modelID, prep.modelClass, prep.provToCanon)
}

// prepareRequest translates a model.Request into Gemini call inputs,
// applying capability gates shared by Complete, Stream, and CountTokens.
func (c *Client) prepareRequest(req *model.Request) (*preparedRequest, error) {
	if req == nil {
		return nil, errors.New("vertex: request is required")
	}
	if req.StructuredOutput != nil && len(req.Tools) > 0 {
		return nil, model.ErrStructuredOutputUnsupported
	}
	canonToProv, provToCanon := buildToolNameMaps(req.Tools)
	system, contents, err := encodeContents(req.Messages, canonToProv)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, errors.New("vertex: request has no user or assistant messages")
	}
	config := &genai.GenerateContentConfig{SystemInstruction: system}
	if maxTokens := req.MaxTokens; maxTokens > 0 {
		config.MaxOutputTokens = int32(maxTokens)
	} else if c.opts.MaxTokens > 0 {
		config.MaxOutputTokens = int32(c.opts.MaxTokens)
	}
	temperature := req.Temperature
	if temperature == 0 {
		temperature = c.opts.Temperature
	}
	if temperature != 0 {
		config.Temperature = genai.Ptr(temperature)
	}
	if len(req.Tools) > 0 {
		tools, err := encodeTools(req.Tools, canonToProv)
		if err != nil {
			return nil, err
		}
		config.Tools = tools
		config.ToolConfig = encodeToolConfig(req.ToolChoice, canonToProv)
	}
	if req.StructuredOutput != nil {
		schema, err := normalizeSchema(req.StructuredOutput.Schema)
		if err != nil {
			return nil, err
		}
		config.ResponseMIMEType = "application/json"
		config.ResponseJsonSchema = schema
	}
	if req.Thinking != nil && req.Thinking.Enable {
		budget := req.Thinking.BudgetTokens
		if budget == 0 {
			budget = c.opts.ThinkingBudget
		}
		tc := &genai.ThinkingConfig{IncludeThoughts: true}
		if budget > 0 {
			tc.ThinkingBudget = genai.Ptr(int32(budget))
		}
		config.ThinkingConfig = tc
	}
	return &preparedRequest{
		modelID:     c.opts.resolveModelID(req),
		modelClass:  req.ModelClass,
		contents:    contents,
		config:      config,
		provToCanon: provToCanon,
	}, nil
}
```

(`genai.Ptr` is the SDK's pointer helper; if absent, write local `ptr[T any](v T) *T`. Confirm `ResponseJsonSchema` vs `ResponseJSONSchema` and `ThinkingConfig` field names with `go doc` on first compile.)

- [ ] **Step 5: Run tests, lint**

Run: `go test ./features/model/vertex/ -v && make lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add features/model/vertex/errors.go features/model/vertex/client.go features/model/vertex/errors_test.go features/model/vertex/client_test.go
git commit -m "feat(vertex): Gemini Complete with provider error mapping"
```

---

### Task 7: Streaming

**Files:**
- Create: `features/model/vertex/stream.go`
- Test: `features/model/vertex/stream_test.go`

**Interfaces:**
- Consumes: `prepareRequest`, `GenerativeClient.GenerateContentStream` (an `iter.Seq2[*genai.GenerateContentResponse, error]`), Task 5 translation helpers.
- Produces: `(*Client) Stream(ctx, req) (model.Streamer, error)`. Copies the canonical streamer skeleton from `features/model/anthropic/stream.go`: background goroutine, buffered `chan model.Chunk` (size 32), `Recv() (model.Chunk, error)` returning `io.EOF` on clean end, `Close()`, `Metadata() map[string]any` (stores final `"usage"`). Chunk mapping per streamed `GenerateContentResponse`:
  - text part → `ChunkTypeText` with assistant `Message{Parts: [TextPart]}`.
  - Thinking: incremental `ChunkTypeThinking` drafts (`Final:false`, text-bearing parts only); on signature arrival emit ONE final `ThinkingPart` carrying the full accumulated text AND base64 signature (the transcript ledger only replays parts with both).
  - function call part → single canonical `ChunkTypeToolCall` (Gemini delivers whole calls; no deltas needed).
  - `UsageMetadata` on the final chunk → `ChunkTypeUsage` with `UsageDelta` (stamped Model/ModelClass).
  - `FinishReason` non-empty → `ChunkTypeStop` with `StopReason` (emitted after usage).
  - Iterator error → wrapped via `wrapGeminiError("generate_content_stream", err)` and surfaced from `Recv`.

- [ ] **Step 1: Write the failing test**

`features/model/vertex/stream_test.go`:

```go
package vertex

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func drain(t *testing.T, s model.Streamer) []model.Chunk {
	t.Helper()
	var chunks []model.Chunk
	for {
		ch, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return chunks
		}
		require.NoError(t, err)
		chunks = append(chunks, ch)
	}
}

func TestStreamTextToolCallUsageStop(t *testing.T) {
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "part one "}}}}}},
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "feed_find_duplicates", Args: map[string]any{"title": "x"}}},
		}}}}},
		{
			Candidates:    []*genai.Candidate{{FinishReason: genai.FinishReasonStop}},
			UsageMetadata: &genai.UsageMetadata{PromptTokenCount: 7, ResponseTokenCount: 2, TotalTokenCount: 9},
		},
	}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	def := toolDef(t, "feed/find_duplicates", `{"type":"object"}`)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
		Tools:    []*model.ToolDefinition{def},
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, s.Close()) }()

	chunks := drain(t, s)
	var types []string
	for _, ch := range chunks {
		types = append(types, ch.Type)
	}
	assert.Equal(t, []string{
		model.ChunkTypeText, model.ChunkTypeToolCall, model.ChunkTypeUsage, model.ChunkTypeStop,
	}, types)
	assert.Equal(t, "feed/find_duplicates", string(chunks[1].ToolCall.Name))
	assert.Equal(t, 7, chunks[2].UsageDelta.InputTokens)
	assert.Equal(t, string(genai.FinishReasonStop), chunks[3].StopReason)
	assert.NotNil(t, s.Metadata()["usage"])
}

func TestStreamSurfacesIteratorError(t *testing.T) {
	stub := &stubGenerativeClient{streamErr: errors.New("boom")}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
	})
	require.NoError(t, err)
	_, recvErr := s.Recv()
	require.Error(t, recvErr)
	_, ok := model.AsProviderError(recvErr)
	assert.True(t, ok)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./features/model/vertex/ -run TestStream -v`
Expected: FAIL — `Stream` undefined.

- [ ] **Step 3: Implement**

`features/model/vertex/stream.go`:

```go
// Streaming adapter. Invariants: chunks flow through a buffered channel
// (32) drained by Recv; Recv returns io.EOF after a clean end; Close is
// idempotent; Metadata returns a copy including final "usage".
package vertex

import (
	"context"
	"io"
	"sync"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

// Stream implements model.Client.
func (c *Client) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	prep, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	seq := c.models.GenerateContentStream(ctx, prep.modelID, prep.contents, prep.config)
	s := &geminiStreamer{
		ctx:    ctx,
		chunks: make(chan model.Chunk, 32),
		meta:   make(map[string]any),
	}
	go s.run(seq, prep)
	return s, nil
}

type geminiStreamer struct {
	ctx    context.Context
	chunks chan model.Chunk
	meta   map[string]any

	mu   sync.Mutex
	err  error
	done bool
}

func (s *geminiStreamer) run(seq func(func(*genai.GenerateContentResponse, error) bool), prep *preparedRequest) {
	defer close(s.chunks)
	callIndex := 0
	var stopReason string
	for resp, err := range seq {
		if err != nil {
			s.setErr(wrapGeminiError("generate_content_stream", err))
			return
		}
		if resp == nil || len(resp.Candidates) == 0 {
			continue
		}
		cand := resp.Candidates[0]
		if cand.Content != nil {
			for _, part := range cand.Content.Parts {
				switch {
				case part.FunctionCall != nil:
					callIndex++
					payload, merr := marshalArgs(part.FunctionCall.Args)
					if merr != nil {
						s.setErr(merr)
						return
					}
					s.emit(model.Chunk{Type: model.ChunkTypeToolCall, ToolCall: &model.ToolCall{
						Name:    toolIdent(part.FunctionCall.Name, prep.provToCanon),
						Payload: payload,
						ID:      toolCallID(part.FunctionCall, callIndex),
					}})
				case part.Thought:
					tp := model.ThinkingPart{Text: part.Text}
					s.emit(model.Chunk{Type: model.ChunkTypeThinking, Thinking: part.Text,
						Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{tp}}})
				case part.Text != "":
					s.emit(model.Chunk{Type: model.ChunkTypeText,
						Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: part.Text}}}})
				}
			}
		}
		if cand.FinishReason != "" {
			stopReason = string(cand.FinishReason)
		}
		if resp.UsageMetadata != nil {
			usage := translateUsage(resp.UsageMetadata, prep.modelID, prep.modelClass)
			s.mu.Lock()
			s.meta["usage"] = usage
			s.mu.Unlock()
			s.emit(model.Chunk{Type: model.ChunkTypeUsage, UsageDelta: &usage})
		}
	}
	if stopReason != "" {
		s.emit(model.Chunk{Type: model.ChunkTypeStop, StopReason: stopReason})
	}
}

func (s *geminiStreamer) emit(ch model.Chunk) {
	select {
	case s.chunks <- ch:
	case <-s.ctx.Done():
	}
}

func (s *geminiStreamer) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// Recv implements model.Streamer.
func (s *geminiStreamer) Recv() (model.Chunk, error) {
	select {
	case ch, ok := <-s.chunks:
		if !ok {
			s.mu.Lock()
			err := s.err
			s.mu.Unlock()
			if err != nil {
				return model.Chunk{}, err
			}
			return model.Chunk{}, io.EOF
		}
		return ch, nil
	case <-s.ctx.Done():
		return model.Chunk{}, s.ctx.Err()
	}
}

// Close implements model.Streamer.
func (s *geminiStreamer) Close() error { return nil }

// Metadata implements model.Streamer.
func (s *geminiStreamer) Metadata() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]any, len(s.meta))
	for k, v := range s.meta {
		out[k] = v
	}
	return out
}
```

Add tiny helpers to `translate.go`:

```go
// toolIdent maps a provider tool name back to its canonical ident.
func toolIdent(prov string, provToCanon map[string]string) tools.Ident {
	return tools.Ident(canonicalToolName(prov, provToCanon))
}

// marshalArgs encodes Gemini function-call args as a JSON payload.
func marshalArgs(args map[string]any) (rawjson.Message, error) {
	if len(args) == 0 {
		return rawjson.Message(`{}`), nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("vertex: marshal tool args: %w", err)
	}
	return b, nil
}
```

(`rawjson` is goa-ai's raw JSON alias used by `model.ToolCall.Payload` — import path per `runtime/agent/model/model.go`'s own imports; check with `go doc goa.design/goa-ai/runtime/agent/model ToolCall`. Refactor Task 5's `translateResponse` to use `marshalArgs`.)

- [ ] **Step 4: Run tests with race detector**

Run: `go test ./features/model/vertex/ -race -v && make lint`
Expected: PASS, no races.

- [ ] **Step 5: Commit**

```bash
git add features/model/vertex/
git commit -m "feat(vertex): Gemini streaming with canonical chunk contract"
```

---

### Task 8: Token counting

**Files:**
- Modify: `features/model/vertex/client.go` (add `CountTokens`)
- Test: `features/model/vertex/count_tokens_test.go`

**Interfaces:**
- Consumes: `GenerativeClient.CountTokens`, `prepareRequest`.
- Produces: `(*Client) CountTokens(ctx, req) (model.TokenCount, error)` implementing `model.TokenCounter`. Contract (per Bedrock): strip `ThinkingPart`s from messages before counting (replayed thinking is not re-sent for counting); `Exact: true`; stamp Model/ModelClass.

- [ ] **Step 1: Write the failing test**

`features/model/vertex/count_tokens_test.go`:

```go
package vertex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestCountTokens(t *testing.T) {
	stub := &stubGenerativeClient{countResp: &genai.CountTokensResponse{TotalTokens: 42}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro"})
	require.NoError(t, err)
	count, err := cl.CountTokens(context.Background(), &model.Request{
		ModelClass: model.ModelClassSmall,
		Messages: []*model.Message{
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
			{Role: model.ConversationRoleAssistant, Parts: []model.Part{
				model.ThinkingPart{Text: "secret reasoning", Final: true},
				model.TextPart{Text: "answer"},
			}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 42, count.InputTokens)
	assert.True(t, count.Exact)
	assert.Equal(t, model.ModelClassSmall, count.ModelClass)
	// Thinking parts must not be encoded into the counted contents.
	for _, content := range stub.lastContents {
		for _, part := range content.Parts {
			assert.False(t, part.Thought, "thinking part leaked into token counting")
		}
	}
}

var _ model.TokenCounter = (*Client)(nil)
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./features/model/vertex/ -run TestCountTokens -v`
Expected: FAIL — `CountTokens` undefined (and the interface assertion fails to compile).

- [ ] **Step 3: Implement in client.go**

```go
// CountTokens implements model.TokenCounter using Vertex's native counter.
// Replayed thinking parts are excluded per the TokenCounter contract.
func (c *Client) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	stripped := messagesWithoutThinking(req.Messages)
	reqCopy := *req
	reqCopy.Messages = stripped
	prep, err := c.prepareRequest(&reqCopy)
	if err != nil {
		return model.TokenCount{}, err
	}
	resp, err := c.models.CountTokens(ctx, prep.modelID, prep.contents, nil)
	if err != nil {
		return model.TokenCount{}, wrapGeminiError("count_tokens", err)
	}
	return model.TokenCount{
		Model:       prep.modelID,
		ModelClass:  prep.modelClass,
		InputTokens: int(resp.TotalTokens),
		Exact:       true,
	}, nil
}

// messagesWithoutThinking returns a copy of msgs with ThinkingParts removed.
func messagesWithoutThinking(msgs []*model.Message) []*model.Message {
	out := make([]*model.Message, 0, len(msgs))
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		parts := make([]model.Part, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			if _, isThinking := part.(model.ThinkingPart); isThinking {
				continue
			}
			parts = append(parts, part)
		}
		copied := *msg
		copied.Parts = parts
		out = append(out, &copied)
	}
	return out
}
```

- [ ] **Step 4: Run tests, lint**

Run: `go test ./features/model/vertex/ -v && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add features/model/vertex/
git commit -m "feat(vertex): native Gemini token counting via model.TokenCounter"
```

---

### Task 9: Claude-on-Vertex constructor with proper error mapping

**Files:**
- Create: `features/model/vertex/anthropic.go`
- Test: `features/model/vertex/anthropic_test.go`

**Interfaces:**
- Consumes: `features/model/anthropic` (`anthropic.New`, `anthropic.MessagesClient`, `anthropic.Options`), `github.com/anthropics/anthropic-sdk-go` (`sdk.NewClient`), `github.com/anthropics/anthropic-sdk-go/vertex` (`vertex.WithGoogleAuth`), `github.com/anthropics/anthropic-sdk-go/internal/apierror`? — no: the public error type is `github.com/anthropics/anthropic-sdk-go`'s `*anthropic.Error`; confirm the exported error type with `go doc github.com/anthropics/anthropic-sdk-go Error` (the SDK exports one; map via `errors.As`).
- Produces:
  - `AnthropicOptions struct { ProjectID, Region string; DefaultModel, HighModel, SmallModel string; MaxTokens int; Temperature float64; ThinkingBudget int64 }`
  - `NewAnthropicClient(ctx context.Context, opts AnthropicOptions) (model.Client, error)` — builds `sdk.NewClient(vertex.WithGoogleAuth(ctx, opts.Region, opts.ProjectID))`, passes `&client.Messages` to `anthropic.New`, wraps the result in `anthropicErrorMapper`.
  - `anthropicErrorMapper` — a `model.Client` decorator whose `Complete`/`Stream` map SDK API errors (HTTP status) through the same classification as `wrapGeminiError` (provider name `vertex-anthropic`), fixing the upstream gap where real 429s are never detected. **Do not copy `anthropic.isRateLimited`.**
  - Model IDs in options are Vertex-form: current-generation bare IDs (`claude-opus-4-8`, `claude-sonnet-5`) or dated snapshots with `@` (`claude-haiku-4-5@20251001`) — never Bedrock's `anthropic.`-prefixed form.

- [ ] **Step 1: Write the failing test**

`features/model/vertex/anthropic_test.go`:

```go
package vertex

import (
	"context"
	"errors"
	"net/http"
	"testing"

	sdkerr "github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

type stubModelClient struct {
	err  error
	resp *model.Response
}

func (s *stubModelClient) Complete(context.Context, *model.Request) (*model.Response, error) {
	return s.resp, s.err
}

func (s *stubModelClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, s.err
}

func TestNewAnthropicClientValidates(t *testing.T) {
	_, err := NewAnthropicClient(context.Background(), AnthropicOptions{Region: "global"})
	assert.Error(t, err) // missing project
	_, err = NewAnthropicClient(context.Background(), AnthropicOptions{ProjectID: "p"})
	assert.Error(t, err) // missing region
	_, err = NewAnthropicClient(context.Background(), AnthropicOptions{ProjectID: "p", Region: "global"})
	assert.Error(t, err) // missing default model
}

func TestAnthropicErrorMapperMaps429(t *testing.T) {
	apiErr := &sdkerr.Error{StatusCode: http.StatusTooManyRequests}
	mapped := &anthropicErrorMapper{next: &stubModelClient{err: apiErr}}
	_, err := mapped.Complete(context.Background(), &model.Request{})
	assert.True(t, errors.Is(err, model.ErrRateLimited))
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindRateLimited, pe.Kind())
}

func TestAnthropicErrorMapperPassesSuccess(t *testing.T) {
	want := &model.Response{StopReason: "end_turn"}
	mapped := &anthropicErrorMapper{next: &stubModelClient{resp: want}}
	got, err := mapped.Complete(context.Background(), &model.Request{})
	require.NoError(t, err)
	assert.Same(t, want, got)
}
```

(Adjust `sdkerr.Error` construction to the SDK's actual exported error type — `go doc github.com/anthropics/anthropic-sdk-go Error` shows its fields; the Go claude-api reference confirms `*anthropic.Error` with `StatusCode` is the unwrap target.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./features/model/vertex/ -run 'TestNewAnthropicClient|TestAnthropicErrorMapper' -v`
Expected: FAIL — `NewAnthropicClient` undefined.

- [ ] **Step 3: Implement**

`features/model/vertex/anthropic.go`:

```go
// Claude-on-Vertex constructor. The Messages translator is reused verbatim
// from features/model/anthropic; the SDK's vertex transport rewrites
// /v1/messages to the Vertex rawPredict endpoints and handles ADC auth.
// This file adds the error classification the anthropic package lacks so
// the adaptive rate-limit middleware observes real Vertex 429s.
package vertex

import (
	"context"
	"errors"
	"net/http"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	sdkvertex "github.com/anthropics/anthropic-sdk-go/vertex"

	anthropicprovider "goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/runtime/agent/model"
)

// AnthropicOptions configures the Claude-on-Vertex model client. Model IDs
// are Vertex publisher IDs: bare first-party names for current-generation
// models (e.g. "claude-sonnet-5") or dated snapshots with an @ separator
// (e.g. "claude-haiku-4-5@20251001"). Never use Bedrock's "anthropic."
// prefix here.
type AnthropicOptions struct {
	// ProjectID is the GCP project hosting the Vertex endpoint.
	ProjectID string
	// Region is the Vertex region ("global", "us", "eu", or a specific
	// region such as "us-east5").
	Region string
	// DefaultModel serves requests with no explicit model or class override.
	DefaultModel string
	// HighModel serves ModelClassHighReasoning requests when set.
	HighModel string
	// SmallModel serves ModelClassSmall requests when set.
	SmallModel string
	// MaxTokens caps output tokens when the request does not set MaxTokens.
	MaxTokens int
	// Temperature is the default sampling temperature.
	Temperature float64
	// ThinkingBudget is the default thinking token budget.
	ThinkingBudget int64
}

// NewAnthropicClient builds a Claude-on-Vertex model client using
// Application Default Credentials.
func NewAnthropicClient(ctx context.Context, opts AnthropicOptions) (model.Client, error) {
	if strings.TrimSpace(opts.ProjectID) == "" {
		return nil, errors.New("vertex: project id is required")
	}
	if strings.TrimSpace(opts.Region) == "" {
		return nil, errors.New("vertex: region is required")
	}
	if strings.TrimSpace(opts.DefaultModel) == "" {
		return nil, errors.New("vertex: default model is required")
	}
	client := sdk.NewClient(sdkvertex.WithGoogleAuth(ctx, opts.Region, opts.ProjectID))
	inner, err := anthropicprovider.New(&client.Messages, anthropicprovider.Options{
		DefaultModel:   opts.DefaultModel,
		HighModel:      opts.HighModel,
		SmallModel:     opts.SmallModel,
		MaxTokens:      opts.MaxTokens,
		Temperature:    opts.Temperature,
		ThinkingBudget: opts.ThinkingBudget,
	})
	if err != nil {
		return nil, err
	}
	return &anthropicErrorMapper{next: inner}, nil
}

// anthropicErrorMapper decorates a model.Client with the goa-ai provider
// error contract for Anthropic SDK failures.
type anthropicErrorMapper struct {
	next model.Client
}

// Complete implements model.Client.
func (m *anthropicErrorMapper) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	resp, err := m.next.Complete(ctx, req)
	if err != nil {
		return nil, wrapAnthropicVertexError("complete", err)
	}
	return resp, nil
}

// Stream implements model.Client.
func (m *anthropicErrorMapper) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	s, err := m.next.Stream(ctx, req)
	if err != nil {
		return nil, wrapAnthropicVertexError("stream", err)
	}
	return s, nil
}

// wrapAnthropicVertexError classifies Anthropic SDK errors (which carry the
// Vertex HTTP status) into the provider error contract.
func wrapAnthropicVertexError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var apiErr *sdk.Error
	status := 0
	message := err.Error()
	if errors.As(err, &apiErr) {
		status = apiErr.StatusCode
	}
	kind := model.ProviderErrorKindUnknown
	retryable := false
	switch {
	case status == http.StatusTooManyRequests:
		pe := model.NewProviderError(anthropicProviderName, operation, status,
			model.ProviderErrorKindRateLimited, "rate_limited", message, "", true, err)
		return errors.Join(model.ErrRateLimited, pe)
	case status == http.StatusBadRequest:
		kind = model.ProviderErrorKindInvalidRequest
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = model.ProviderErrorKindAuth
	case status >= http.StatusInternalServerError && status <= http.StatusNetworkAuthenticationRequired:
		kind = model.ProviderErrorKindUnavailable
		retryable = true
	}
	return model.NewProviderError(anthropicProviderName, operation, status, kind, "", message, "", retryable, err)
}
```

- [ ] **Step 4: Run tests, lint**

Run: `go test ./features/model/vertex/ -v && make lint`
Expected: PASS. (`go mod tidy` may promote `golang.org/x/oauth2` and `google.golang.org/api` to direct deps — expected, per the exploration brief.)

- [ ] **Step 5: Commit**

```bash
git add features/model/vertex/ go.mod go.sum
git commit -m "feat(vertex): Claude-on-Vertex client reusing anthropic translator with real 429 mapping"
```

---

### Task 10: Runtime factories

**Files:**
- Modify: `runtime/agent/runtime/runtime.go` (add config structs + two factory methods, next to `NewBedrockModelClient`/`NewOpenAIModelClient`)
- Test: `runtime/agent/runtime/model_client_factories_test.go` (extend the existing file)

**Interfaces:**
- Consumes: `features/model/vertex` (`vertex.New`, `vertex.Options`, `vertex.NewAnthropicClient`, `vertex.AnthropicOptions`, `vertex.GenerativeClient`), `google.golang.org/genai` (`genai.NewClient`, `genai.ClientConfig`, `genai.BackendVertexAI`).
- Produces (mirror Bedrock/OpenAI exactly — return `(model.Client, error)`, no auto-register; callers use `rt.RegisterModel` afterward):

```go
// VertexConfig configures the Vertex-backed model clients created by the
// runtime. ProjectID and Location identify the Vertex endpoint; model IDs
// are provider-specific (Gemini model names for the Gemini factory, Vertex
// Claude publisher IDs for the Anthropic factory).
type VertexConfig struct {
	ProjectID      string
	Location       string
	DefaultModel   string
	HighModel      string
	SmallModel     string
	MaxTokens      int
	ThinkingBudget int
	Temperature    float32
}

func (r *Runtime) NewVertexGeminiModelClient(ctx context.Context, cfg VertexConfig) (model.Client, error)
func (r *Runtime) NewVertexAnthropicModelClient(ctx context.Context, cfg VertexConfig) (model.Client, error)
```

- [ ] **Step 1: Write the failing test**

Append to `runtime/agent/runtime/model_client_factories_test.go` (match the file's existing style — it uses reflection over unexported fields; follow whatever assertion pattern the Bedrock/OpenAI factory tests use):

```go
func TestNewVertexGeminiModelClientValidates(t *testing.T) {
	rt := mustNewTestRuntime(t) // reuse the file's existing runtime constructor helper
	_, err := rt.NewVertexGeminiModelClient(context.Background(), VertexConfig{Location: "global", DefaultModel: "gemini-2.5-pro"})
	assert.Error(t, err) // missing project
	_, err = rt.NewVertexGeminiModelClient(context.Background(), VertexConfig{ProjectID: "p", DefaultModel: "gemini-2.5-pro"})
	assert.Error(t, err) // missing location
}

func TestNewVertexAnthropicModelClientValidates(t *testing.T) {
	rt := mustNewTestRuntime(t)
	_, err := rt.NewVertexAnthropicModelClient(context.Background(), VertexConfig{ProjectID: "p", Location: "global"})
	assert.Error(t, err) // missing default model
}
```

Note: the happy path of `NewVertexGeminiModelClient` calls `genai.NewClient`, which resolves ADC — do not test it in unit tests (no network/credentials in CI). Validation-failure tests are the unit surface, matching how the OpenAI factory tests handle the missing-API-key path. If `mustNewTestRuntime` does not exist, use whatever constructor the existing factory tests in that file use.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./runtime/agent/runtime/ -run TestNewVertex -v`
Expected: FAIL — `VertexConfig` undefined.

- [ ] **Step 3: Implement in runtime.go**

Add imports `vertexprovider "goa.design/goa-ai/features/model/vertex"` and `"google.golang.org/genai"`, then:

```go
// VertexConfig configures the Vertex-backed model clients created by the
// runtime. ProjectID and Location identify the Vertex endpoint. Model IDs
// are provider-specific: Gemini model names (e.g. "gemini-2.5-flash") for
// NewVertexGeminiModelClient, Vertex Claude publisher IDs (e.g.
// "claude-sonnet-5") for NewVertexAnthropicModelClient.
type VertexConfig struct {
	ProjectID      string
	Location       string
	DefaultModel   string
	HighModel      string
	SmallModel     string
	MaxTokens      int
	ThinkingBudget int
	Temperature    float32
}

// NewVertexGeminiModelClient creates a Gemini-on-Vertex model client using
// Application Default Credentials. The client is not registered; call
// RegisterModel with the returned client.
func (r *Runtime) NewVertexGeminiModelClient(ctx context.Context, cfg VertexConfig) (model.Client, error) {
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return nil, errors.New("vertex: project id is required")
	}
	if strings.TrimSpace(cfg.Location) == "" {
		return nil, errors.New("vertex: location is required")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  cfg.ProjectID,
		Location: cfg.Location,
	})
	if err != nil {
		return nil, err
	}
	return vertexprovider.New(client.Models, vertexprovider.Options{
		DefaultModel:   cfg.DefaultModel,
		HighModel:      cfg.HighModel,
		SmallModel:     cfg.SmallModel,
		MaxTokens:      cfg.MaxTokens,
		Temperature:    cfg.Temperature,
		ThinkingBudget: cfg.ThinkingBudget,
	})
}

// NewVertexAnthropicModelClient creates a Claude-on-Vertex model client
// using Application Default Credentials. The client is not registered; call
// RegisterModel with the returned client.
func (r *Runtime) NewVertexAnthropicModelClient(ctx context.Context, cfg VertexConfig) (model.Client, error) {
	return vertexprovider.NewAnthropicClient(ctx, vertexprovider.AnthropicOptions{
		ProjectID:      cfg.ProjectID,
		Region:         cfg.Location,
		DefaultModel:   cfg.DefaultModel,
		HighModel:      cfg.HighModel,
		SmallModel:     cfg.SmallModel,
		MaxTokens:      cfg.MaxTokens,
		Temperature:    float64(cfg.Temperature),
		ThinkingBudget: int64(cfg.ThinkingBudget),
	})
}
```

(`client.Models` is a `*genai.Models` / `genai.Models` — adjust the pointer form to satisfy `vertexprovider.GenerativeClient` per the compiler. `genai.NewClient` does not dial; it only resolves credentials, so the missing-project validation must run before it.)

- [ ] **Step 4: Run tests, lint**

Run: `go test ./runtime/agent/runtime/ -run TestNewVertex -v && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add runtime/agent/runtime/
git commit -m "feat(runtime): VertexConfig and Vertex Gemini/Anthropic model client factories"
```

---

### Task 11: Documentation and full CI gate

**Files:**
- Modify: `README.md` (the two provider-registration mentions: the module map around line 708-717 and the bootstrap text at line 211)
- Test: full suite

**Interfaces:**
- Consumes: everything shipped in Tasks 1–10.
- Produces: user-facing docs; a green `make ci`.

- [ ] **Step 1: Update README**

In the module map section, add alongside the anthropic/bedrock/openai rows:

```markdown
| `features/model/vertex` | Google Vertex AI adapters: Gemini (`vertex.New`) and Claude-on-Vertex (`vertex.NewAnthropicClient`), both with native token counting and provider-error classification. |
```

In the bootstrap/registration text (README.md:211), extend the factory list:

```markdown
Register model clients during bootstrap with `rt.RegisterModel(...)` or runtime
factories such as `rt.NewOpenAIModelClient(...)`, `rt.NewBedrockModelClient(...)`,
`rt.NewVertexGeminiModelClient(...)`, and `rt.NewVertexAnthropicModelClient(...)`.
```

Add a short usage snippet near the other provider examples:

```go
// Vertex AI (ADC auth): Gemini for the small tier, Claude for default/high.
gemini, err := rt.NewVertexGeminiModelClient(ctx, runtime.VertexConfig{
	ProjectID:    project,
	Location:     "global",
	DefaultModel: "gemini-2.5-flash",
})
// ...
claude, err := rt.NewVertexAnthropicModelClient(ctx, runtime.VertexConfig{
	ProjectID:    project,
	Location:     "global",
	DefaultModel: "claude-sonnet-5",
	HighModel:    "claude-opus-4-8",
})
```

- [ ] **Step 2: Run the full gate**

Run: `make ci`
Expected: build, lint, and the full race-enabled test suite pass.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document Vertex Gemini and Claude-on-Vertex model factories"
```

---

## Self-Review Notes

- **Spec coverage** (Osita spec §2 "Prerequisite: goa-ai Vertex provider"): new `features/model/vertex/` package ✓ (Tasks 1–9); `model.Client` for Gemini with fresh bidirectional translation (Content/Parts ✓ Task 3, functionCall/functionResponse ✓ Tasks 3/5/7, thinking ✓ Tasks 3/5/6/7, structured output ✓ Tasks 4/6, usage/stop-reason mapping ✓ Task 5); Claude-on-Vertex lifted from existing adapters via `vertex.WithGoogleAuth` ✓ (Task 9); `model.TokenCounter` ✓ (Task 8); `VertexConfig`/`NewVertex*ModelClient` in `runtime/agent/runtime/runtime.go` ✓ (Task 10); Google Gen AI Go SDK dependency ✓ (Task 1).
- **Known deliberate improvements over existing code**: proper 429/ProviderError mapping (Tasks 6, 9) — the existing anthropic package's gap is documented in the exploration brief and must not be replicated.
- **Deliberate scope exclusions**: Gemini context caching (explicit CachedContent API) — goa-ai's `CacheOptions` maps to nothing explicit on Gemini; implicit caching applies automatically and surfaces via `CachedContentTokenCount` → `CacheReadTokens` (Task 5). Document parts/citations on Gemini — skipped (Osita's extraction pipeline sends plain text); `encodePart` returns nil for them, matching the anthropic package's posture.
- **Type consistency check**: `Options` (Task 1) is consumed by Tasks 6–8; `sanitizeToolName`/`buildToolNameMaps` (Task 2) by Tasks 3–5, 7; `encodeContents` signature identical in Tasks 3 and 6; `prepareRequest`/`preparedRequest` shared by Complete (6), Stream (7), CountTokens (8); `wrapGeminiError` used in 6, 7, 8; `AnthropicOptions`/`NewAnthropicClient` (9) consumed by Task 10's factory. `VertexConfig` field set matches `BedrockConfig` plus ProjectID/Location.
- **SDK-drift guardrail**: tasks compile against the installed SDKs; where the plan flags a field-name uncertainty (`ResponseJsonSchema`, `ThinkingConfig` internals, `FunctionCall.ID`, `*sdk.Error` fields, `rawjson` import path), the implementer resolves it with `go doc` at the call site — the design does not change.
