// Package codegen supplies a synthetic service and independent SDK peers. Both
// HTTP and native stdio directions use real handlers and schema codecs; the
// assertions observe caller-visible values rather than inspect generated source text.
package codegen

const sdkInteropDesign = `package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("sdk-peer", func() { Title("Independent MCP interoperability") })
var uri = Type("ResourceURI", String, func() { Format(FormatURI) })
var text = Type("ResourceText", func() {
	Field(1, "uri", uri, "Exact resource address")
	Field(2, "text", String, "Resource text")
	Required("uri", "text")
})
var item = Type("ResourceItem", func() {
	OneOf("content", "Text resource contents", func() { Attribute("text", text, "Text contents") })
	Required("content")
})
var directoryChild = Type("DirectoryChild", func() {
	Field(1, "uri", String, "Full child URI", func() { Format(FormatURI) })
	Field(2, "name", String, "Child resource name")
	Field(3, "mimeType", String, "Child media type")
	Required("uri", "name")
})
var skillFile = Type("File", func() {
	Field(1, "uri", String, "Exact file resource URI", func() { Format(FormatURI) })
	Field(2, "digest", String, "SHA-256 of raw file bytes", func() { Pattern("^sha256:[0-9a-f]{64}$") })
	Field(3, "size", Int64, "Raw file byte length", func() { Minimum(0) })
	Required("uri", "digest", "size")
})
var skillEntry = Type("Entry", func() {
	Field(1, "uri", String, "Exact Skill entry URI", func() { Format(FormatURI) })
	Field(2, "frontmatter", Any, "Every authored YAML field", func() { Meta("struct:field:type", "json.RawMessage", "encoding/json") })
	OneOf("resources", "Complete files or generated content", func() {
		TypeName("Files")
		Meta("oneof:json:untagged")
		Attribute("manifest", ArrayOfRequired(skillFile), "Every Skill file")
		Attribute("dynamic", String, "Content without stable digests", func() { Enum("dynamic") })
	})
	Required("uri", "frontmatter", "resources")
})
var promptText = Type("PromptText", func() {
	Field(1, "text", String, "Instructions shown to the user")
	Required("text")
})
var promptMessage = Type("AuthoredMessage", func() {
	Field(1, "role", String, "Message audience", func() { Enum("user", "assistant") })
	OneOf("content", "Message contents", func() { TypeName("PromptContent"); Attribute("text", promptText, "Text instructions") })
	Required("role", "content")
})
var empty = Type("EmptyAnswer", func() {})
var formContent = Type("FormContent", func() {
	Field(1, "label", String, "Label selected by the host", func() { MinLength(1) })
	Required("label")
})
var formAccepted = Type("FormAccepted", func() {
	Field(1, "content", formContent, "Accepted form values")
	Required("content")
})
var formResponse = Type("FormResponse", func() {
	OneOf("answer", "Host decision", func() {
		TypeName("FormAnswer")
		Attribute("accept", formAccepted, "Accepted form")
		Attribute("decline", empty, "Declined form")
		Attribute("cancel", empty, "Cancelled form")
	})
	Required("answer")
})
var inputAnswers = Type("InputAnswers", func() { Field(1, "question", formResponse, "Question answer") })
var continuation = Type("Continuation", func() {
	Field(1, "state", String, "Exact state returned by the service")
	Field(2, "responses", inputAnswers, "Answers for this invocation")
})
var formQuestion = Type("FormQuestion", func() {
	Field(1, "message", String, "Question shown to the consenting user", func() { MinLength(1) })
	Required("message")
})
var inputQuestions = Type("InputQuestions", func() { Field(1, "question", formQuestion, "Label question") })
var pendingInput = Type("PendingInput", func() {
	Field(1, "state", String, "Opaque service state")
	Field(2, "requests", inputQuestions, "Selected questions")
})
var selectResult = Type("SelectResult", func() {
	OneOf("outcome", "Completed value or unfinished operation", func() {
		TypeName("SelectOutcome")
		Attribute("complete", String, "Completed label")
		Attribute("input_required", pendingInput, "Requested input")
	})
	Required("outcome")
})
var taskQuestion = Type("TaskQuestion", func() {
	OneOf("request", "Typed job question", func() { TypeName("TaskQuestionKind"); Attribute("question", formQuestion, "Request a label") })
	Required("request")
})
var taskResponse = Type("TaskResponse", func() {
	OneOf("response", "Typed host answer", func() { TypeName("TaskResponseKind"); Attribute("question", formResponse, "Answer a label") })
	Required("response")
})
var taskPending = Type("TaskInput", func() {
	Field(1, "requests", MapOf(String, taskQuestion), "All outstanding questions")
	Required("requests")
})
var taskMetadata = Type("TaskMetadata", func() {
	Field(1, "taskId", String, "Native durable job identity")
	Field(2, "createdAt", String, "Creation time")
	Field(3, "lastUpdatedAt", String, "Observation time")
	Field(4, "ttlMs", Int64, "Retention, absent for unlimited")
	Field(5, "pollIntervalMs", Int64, "Suggested polling interval", func() { Default(3) })
	Required("taskId", "createdAt", "lastUpdatedAt")
})
var taskFailure = Type("TaskFailure", func() {
	Field(1, "code", Int, "Explicit JSON-RPC execution error code")
	Field(2, "message", String, "Execution error description")
	Required("code", "message")
})
var taskObservation = Type("TaskObservation", func() {
	Field(1, "task", taskMetadata, "Current job metadata")
	OneOf("outcome", "Current state and associated data", func() {
		TypeName("TaskState")
		Attribute("working", empty, "Work continues")
		Attribute("input_required", taskPending, "Outstanding questions")
		Attribute("complete", String, "Finished label")
		Attribute("failed", taskFailure, "Execution error")
		Attribute("cancelled", empty, "Cancellation completed")
	})
	Required("task", "outcome")
})
var acceptedResources = Type("AcceptedResources", func() { Field(1, "resources", ArrayOf(uri), "Accepted subset of requested URIs") })
var resourceUpdate = Type("ResourceUpdate", func() { Field(1, "uri", uri, "Changed resource address"); Required("uri") })
var resourceEvent = Type("ResourceEvent", func() {
	OneOf("change", "The source event", func() {
		Attribute("acknowledged", acceptedResources, "Authorized URI selection")
		Attribute("updated", resourceUpdate, "Changed resource URI")
	})
	Required("change")
})

var _ = Service("records", func() {
	Description("Reads synthetic records for independent protocol checks.")
	JSONRPC(func() { POST("/mcp") })
	MCP("records", "1")
	Method("read", func() {
		Description("Returns a record identifier supplied by the caller.")
		Payload(func() { Field(1, "name", String, "Record identifier", func() { MinLength(1) }); Required("name") })
		Result(String)
		Tool("read", "Read a synthetic record", func() { ReadOnlyHint(true) })
	})
	Method("names", func() {
		Description("Returns a synthetic list of record names.")
		Result(ArrayOf(String))
		Tool("names", "List synthetic record names", func() { ReadOnlyHint(true) })
	})
	Method("select", func() {
		Description("Returns a label after collecting host input.")
		Payload(func() { Field(1, "continuation", continuation, "Host-owned continuation") })
		Result(selectResult)
		InputExchange("continuation", "outcome")
		Tool("select", "Select a label after host input")
	})
	Method("create_task", func() {
		Description("Starts a durable job whose state is immediately readable.")
		Payload(func() { Field(1, "query", String, "Job to create"); Required("query") })
		Result(taskObservation)
		TaskExchange("task_read", "task_answer", "task_cancel")
		Tool("create_task", "Create a durable synthetic job")
	})
	Method("task_read", func() {
		Description("Reports the current state of an existing job.")
		Payload(func() { Field(1, "taskId", String, "Native job identity"); Required("taskId") })
		Result(taskObservation)
	})
	Method("task_answer", func() {
		Description("Accepts typed answers for outstanding job questions.")
		Payload(func() {
			Field(1, "taskId", String, "Native job identity")
			Field(2, "responses", MapOf(String, taskResponse), "Host answers")
			Required("taskId", "responses")
		})
	})
	Method("task_cancel", func() {
		Description("Cancels the identified job.")
		Payload(func() { Field(1, "taskId", String, "Native job identity"); Required("taskId") })
	})
	Method("document", func() {
		Description("Reads the synthetic reference document.")
		Result(String)
		Resource("document", "record://reference", "text/plain")
	})
	Method("read_uri", func() {
		Description("Reads a parameterized or skill resource by exact URI.")
		Payload(func() { Field(1, "uri", uri, "Exact resource address"); Required("uri") })
		Error("invalid_params", func() { Description("Unknown resource URI") })
		Result(func() { Field(1, "contents", ArrayOfRequired(item), "Ordered resource contents") })
		ResourceTemplate("item", "record://items/{id}", "text/plain")
	})
	Method("read_directory", func() {
		Description("Lists direct children of a directory URI.")
		Payload(func() {
			Field(1, "uri", String, "Directory URI", func() { Format(FormatURI) })
			Field(2, "cursor", String, "Next directory page")
			Required("uri")
		})
		Error("invalid_params", func() { Description("Unknown directory URI") })
		Result(func() {
			Field(1, "resources", ArrayOfRequired(directoryChild), "Direct children")
			Field(2, "nextCursor", String, "Next page")
		})
		ResourceDirectory()
	})
	Method("list_skills", func() {
		Description("Lists complete Skill entries.")
		Payload(func() { Field(1, "cursor", String, "Opaque next-page cursor") })
		Result(func() {
			Field(1, "skills", ArrayOfRequired(skillEntry), "Complete entries in this page")
			Field(2, "nextCursor", String, "Opaque next-page cursor")
		})
		SkillCatalog()
	})
	Method("lookup_skill", func() {
		Description("Resolves an exact Skill URI.")
		Payload(func() { Field(1, "uri", String, "Exact Skill URI", func() { Format(FormatURI) }); Required("uri") })
		Error("invalid_params", func() { Description("Unknown Skill URI") })
		Result(func() { Field(1, "skill", skillEntry, "Complete entry"); Required("skill") })
		SkillLookup()
	})
	StaticPrompt("welcome", "Synthetic welcome", "user", "Read the reference.")
	Method("guide", func() {
		Description("Returns instructions for a topic.")
		Payload(func() { Field(1, "topic", String, "Requested topic"); Required("topic") })
		Result(func() { Field(1, "messages", ArrayOfRequired(promptMessage), "Prompt messages") })
		Prompt("guide", "Instructions for a topic")
	})
	Method("suggest", func() {
		Description("Completes topic prefixes.")
		Payload(func() {
			Field(1, "value", String, "Partial topic text")
			Field(2, "arguments", MapOf(String, String), "Prior arguments")
			Required("value")
		})
		Result(func() { Field(1, "values", ArrayOf(String), "Suggestions", func() { MaxLength(100) }) })
		PromptCompletion("guide", "topic")
	})
	Method("watch", func() {
		Description("Observes resource changes.")
		Payload(func() { Field(1, "resources", ArrayOf(uri), "Resource URI selections") })
		StreamingResult(resourceEvent)
		SubscriptionSource()
	})
})
`

const sdkInteropRuntime = `package sdkpeer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
	genserver "sdk-peer.local/gen/jsonrpc/mcp_records/server"
	genmcp "sdk-peer.local/gen/mcp_records"
	genrecords "sdk-peer.local/gen/records"
)

type (
	recordService struct {
		calls    atomic.Int64
		gateOnce sync.Once
		gate     chan struct{}
		taskMu   sync.Mutex
		taskDone bool
		taskAns  string
	}
)

const skillManifestDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newRecordService() *recordService {
	return &recordService{gate: make(chan struct{})}
}

func (s *recordService) Read(ctx context.Context, p *genrecords.ReadPayload) (string, error) {
	s.calls.Add(1)
	switch p.Name {
	case "denied":
		return "", goa.PermanentError("record_denied", "record denied")
	case "slow-cancel":
		_ = mcpruntime.ReportProgress(ctx, 1, nil, nil)
		<-ctx.Done()
		return "", ctx.Err()
	case "wait-gate":
		_ = mcpruntime.ReportProgress(ctx, 1, nil, nil)
		<-s.gate
		return "gated", nil
	case "fast":
		s.gateOnce.Do(func() { close(s.gate) })
		return "fast-done", nil
	case "progress":
		total := 100.0
		msg := "halfway"
		if err := mcpruntime.ReportProgress(ctx, 50, &total, &msg); err != nil {
			return "", err
		}
		return "progressed", nil
	default:
		return p.Name, nil
	}
}

func (s *recordService) Names(context.Context) ([]string, error) { return []string{"record-one"}, nil }
func (s *recordService) Document(context.Context) (string, error) { return "reference", nil }

func (s *recordService) Select(_ context.Context, p *genrecords.SelectPayload) (*genrecords.SelectResult, error) {
	if p.Continuation == nil || p.Continuation.State == nil {
		state := "select-round-1"
		return &genrecords.SelectResult{
			Outcome: genrecords.NewSelectOutcomeInputRequired(&genrecords.PendingInput{
				State:    &state,
				Requests: &genrecords.InputQuestions{Question: &genrecords.FormQuestion{Message: "Choose a label"}},
			}),
		}, nil
	}
	if *p.Continuation.State != "select-round-1" || p.Continuation.Responses == nil || p.Continuation.Responses.Question == nil {
		return nil, goa.PermanentError("invalid_continuation", "continuation state or response missing")
	}
	accepted, ok := p.Continuation.Responses.Question.Answer.AsAccept()
	if !ok || accepted.Content == nil {
		return nil, goa.PermanentError("invalid_continuation", "expected accepted form answer")
	}
	return &genrecords.SelectResult{
		Outcome: genrecords.NewSelectOutcomeComplete(genrecords.SelectOutcomeBranchComplete(accepted.Content.Label)),
	}, nil
}

func (s *recordService) CreateTask(_ context.Context, p *genrecords.CreateTaskPayload) (*genrecords.TaskObservation, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	s.taskDone, s.taskAns = false, ""
	return &genrecords.TaskObservation{
		Task: &genrecords.TaskMetadata{TaskID: "job-1", CreatedAt: "2026-07-28T00:00:00Z", LastUpdatedAt: "2026-07-28T00:00:00Z", PollIntervalMs: 3},
		Outcome: genrecords.NewTaskStateInputRequired(&genrecords.TaskInput{
			Requests: map[string]*genrecords.TaskQuestion{
				"question": {Request: genrecords.NewTaskQuestionKindQuestion(&genrecords.FormQuestion{Message: "Confirm " + p.Query})},
			},
		}),
	}, nil
}

func (s *recordService) TaskRead(_ context.Context, p *genrecords.TaskReadPayload) (*genrecords.TaskObservation, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if p.TaskID != "job-1" {
		return nil, goa.PermanentError("invalid_params", "unknown task")
	}
	meta := &genrecords.TaskMetadata{TaskID: "job-1", CreatedAt: "2026-07-28T00:00:00Z", LastUpdatedAt: "2026-07-28T00:00:01Z", PollIntervalMs: 3}
	if s.taskDone {
		return &genrecords.TaskObservation{Task: meta, Outcome: genrecords.NewTaskStateCancelled(&genrecords.EmptyAnswer{})}, nil
	}
	if s.taskAns != "" {
		return &genrecords.TaskObservation{Task: meta, Outcome: genrecords.NewTaskStateComplete(genrecords.TaskStateBranchComplete(s.taskAns))}, nil
	}
	return &genrecords.TaskObservation{
		Task: meta,
		Outcome: genrecords.NewTaskStateInputRequired(&genrecords.TaskInput{
			Requests: map[string]*genrecords.TaskQuestion{
				"question": {Request: genrecords.NewTaskQuestionKindQuestion(&genrecords.FormQuestion{Message: "Confirm job"})},
			},
		}),
	}, nil
}

func (s *recordService) TaskAnswer(_ context.Context, p *genrecords.TaskAnswerPayload) error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if p.TaskID != "job-1" {
		return goa.PermanentError("invalid_params", "unknown task")
	}
	resp := p.Responses["question"]
	if resp == nil {
		return goa.PermanentError("invalid_params", "missing question response")
	}
	form, ok := resp.Response.AsQuestion()
	if !ok {
		return goa.PermanentError("invalid_params", "unexpected response kind")
	}
	accepted, ok := form.Answer.AsAccept()
	if !ok || accepted.Content == nil {
		return goa.PermanentError("invalid_params", "expected accepted answer")
	}
	s.taskAns = accepted.Content.Label
	return nil
}

func (s *recordService) TaskCancel(_ context.Context, p *genrecords.TaskCancelPayload) error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if p.TaskID != "job-1" {
		return goa.PermanentError("invalid_params", "unknown task")
	}
	s.taskDone = true
	return nil
}

func (s *recordService) ReadURI(_ context.Context, p *genrecords.ReadURIPayload) (*genrecords.ReadURIResult, error) {
	switch string(p.URI) {
	case "record://items/alpha":
		return &genrecords.ReadURIResult{Contents: []*genrecords.ResourceItem{{Content: genrecords.NewContentText(&genrecords.ResourceText{URI: p.URI, Text: "item:alpha"})}}}, nil
	case "skill://review/SKILL.md":
		return &genrecords.ReadURIResult{Contents: []*genrecords.ResourceItem{{Content: genrecords.NewContentText(&genrecords.ResourceText{URI: p.URI, Text: "---\nname: review\n---\nReview instructions."})}}}, nil
	default:
		return nil, genrecords.MakeInvalidParams(errors.New("unknown resource URI"))
	}
}

func (s *recordService) ReadDirectory(_ context.Context, p *genrecords.ReadDirectoryPayload) (*genrecords.ReadDirectoryResult, error) {
	if p.URI != "record://dir" {
		return nil, genrecords.MakeInvalidParams(errors.New("unknown directory URI"))
	}
	mime := "text/plain"
	return &genrecords.ReadDirectoryResult{Resources: []*genrecords.DirectoryChild{{URI: "record://dir/child.txt", Name: "child.txt", MimeType: &mime}}}, nil
}

func (s *recordService) ListSkills(context.Context, *genrecords.ListSkillsPayload) (*genrecords.ListSkillsResult, error) {
	return &genrecords.ListSkillsResult{Skills: []*genrecords.Entry{s.skillEntry()}}, nil
}

func (s *recordService) LookupSkill(_ context.Context, p *genrecords.LookupSkillPayload) (*genrecords.LookupSkillResult, error) {
	if p.URI != "skill://review/SKILL.md" {
		return nil, genrecords.MakeInvalidParams(errors.New("unknown skill URI"))
	}
	return &genrecords.LookupSkillResult{Skill: s.skillEntry()}, nil
}

func (s *recordService) skillEntry() *genrecords.Entry {
	return &genrecords.Entry{
		URI:         "skill://review/SKILL.md",
		Frontmatter: json.RawMessage("{\"name\":\"review\",\"description\":\"Synthetic review skill\"}"),
		Resources:   genrecords.NewFilesManifest([]*genrecords.File{{URI: "skill://review/SKILL.md", Digest: skillManifestDigest, Size: 40}}),
	}
}

func (s *recordService) Guide(_ context.Context, p *genrecords.GuidePayload) (*genrecords.GuideResult, error) {
	return &genrecords.GuideResult{
		Messages: []*genrecords.AuthoredMessage{{Role: "user", Content: genrecords.NewPromptContentText(&genrecords.PromptText{Text: "Topic: " + p.Topic})}},
	}, nil
}

func (s *recordService) Suggest(_ context.Context, p *genrecords.SuggestPayload) (*genrecords.SuggestResult, error) {
	return &genrecords.SuggestResult{Values: []string{p.Value + "-complete"}}, nil
}

func (s *recordService) Watch(_ context.Context, p *genrecords.WatchPayload, stream genrecords.WatchServerStream) error {
	defer func() { _ = stream.Close() }()
	if len(p.Resources) == 1 && string(p.Resources[0]) == "record://deny" {
		return goa.PermanentError("invalid_params", "denied before ack")
	}
	if err := stream.Send(&genrecords.ResourceEvent{Change: genrecords.NewChangeAcknowledged(&genrecords.AcceptedResources{Resources: p.Resources})}); err != nil {
		return err
	}
	for _, uri := range p.Resources {
		if err := stream.Send(&genrecords.ResourceEvent{Change: genrecords.NewChangeUpdated(&genrecords.ResourceUpdate{URI: uri + "/child"})}); err != nil {
			return err
		}
	}
	return nil
}

func TestSDKClientReadsGeneratedServer(t *testing.T) {
	for _, version := range []string{mcpruntime.ProtocolVersion, mcpruntime.LegacyProtocolVersion} {
		t.Run(version, func(t *testing.T) { testSDKClientReadsGeneratedServer(t, version) })
	}
}

func testSDKClientReadsGeneratedServer(t *testing.T, version string) {
	service := newRecordService()
	endpoints := genrecords.NewEndpoints(service)
	var middleware atomic.Int64
	endpoints.Use(func(next goa.Endpoint) goa.Endpoint {
		return func(ctx context.Context, input any) (any, error) { middleware.Add(1); return next(ctx, input) }
	})
	adapter := genmcp.NewMCPAdapter(endpoints, nil)
	mux := goahttp.NewMuxer()
	server := genserver.New(genmcp.NewEndpoints(adapter), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil)
	genserver.Mount(mux, server)
	peer := httptest.NewServer(mux)
	defer peer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "independent-peer", Version: "1"}, &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{}, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
	})
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: peer.URL + "/mcp", HTTPClient: peer.Client(), MaxRetries: -1}, &sdk.ClientSessionOptions{ProtocolVersion: version})
	require.NoError(t, err)
	defer func() { assert.NoError(t, session.Close()) }()
	assert.Equal(t, version, session.InitializeResult().ProtocolVersion)
	catalog, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, catalog.Tools, 4)
	var read *sdk.Tool
	for _, tool := range catalog.Tools {
		if tool.Name == "read" {
			read = tool
		}
	}
	require.NotNil(t, read)
	if version == mcpruntime.ProtocolVersion {
		assert.Equal(t, "private", catalog.CacheScope)
	} else {
		assert.Empty(t, catalog.CacheScope)
		var schema struct {
			Type     string
			Required []string
		}
		raw, err := json.Marshal(read.OutputSchema)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &schema))
		assert.Equal(t, "object", schema.Type)
		assert.Equal(t, []string{"value"}, schema.Required)
	}
	result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "read", Arguments: json.RawMessage("{\"name\":\"record-one\"}")})
	require.NoError(t, err)
	if version == mcpruntime.ProtocolVersion {
		assert.Equal(t, "record-one", result.StructuredContent)
	} else {
		assert.Equal(t, map[string]any{"value": "record-one"}, result.StructuredContent)
		require.Len(t, result.Content, 1)
		text, ok := result.Content[0].(*sdk.TextContent)
		require.True(t, ok)
		assert.JSONEq(t, "{\"value\":\"record-one\"}", text.Text)
	}
	assert.False(t, result.IsError)
	assert.EqualValues(t, 1, service.calls.Load())
	assert.EqualValues(t, 1, middleware.Load())
	invalid, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "read", Arguments: json.RawMessage("{\"name\":\"\"}")})
	require.NoError(t, err)
	assert.True(t, invalid.IsError)
	assert.EqualValues(t, 1, service.calls.Load())
	denied, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "read", Arguments: json.RawMessage("{\"name\":\"denied\"}")})
	require.NoError(t, err)
	assert.True(t, denied.IsError)
	_, err = session.CallTool(t.Context(), &sdk.CallToolParams{Name: "absent", Arguments: json.RawMessage("{}")})
	assert.Error(t, err)
	names, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "names"})
	require.NoError(t, err)
	if version == mcpruntime.ProtocolVersion {
		assert.Equal(t, []any{"record-one"}, names.StructuredContent)
	} else {
		assert.Equal(t, map[string]any{"value": []any{"record-one"}}, names.StructuredContent)
	}
	document, err := session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "record://reference"})
	require.NoError(t, err)
	require.Len(t, document.Contents, 1)
	assert.Equal(t, "reference", document.Contents[0].Text)
	prompts, err := session.ListPrompts(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, prompts.Prompts, 2)
	prompt, err := session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "welcome"})
	require.NoError(t, err)
	require.Len(t, prompt.Messages, 1)
	text, ok := prompt.Messages[0].Content.(*sdk.TextContent)
	require.True(t, ok)
	assert.Equal(t, "Read the reference.", text.Text)
}

func TestBothRevisionsShareOneEndpoint(t *testing.T) {
	endpoints := genrecords.NewEndpoints(newRecordService())
	mux := goahttp.NewMuxer()
	server := genserver.New(genmcp.NewEndpoints(genmcp.NewMCPAdapter(endpoints, nil)), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil)
	genserver.Mount(mux, server)
	peer := httptest.NewServer(mux)
	defer peer.Close()
	for _, version := range []string{mcpruntime.LegacyProtocolVersion, mcpruntime.ProtocolVersion, mcpruntime.LegacyProtocolVersion} {
		client := sdk.NewClient(&sdk.Implementation{Name: "independent-peer", Version: "1"}, &sdk.ClientOptions{
			Capabilities: &sdk.ClientCapabilities{}, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
		})
		session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: peer.URL + "/mcp", HTTPClient: peer.Client(), MaxRetries: -1}, &sdk.ClientSessionOptions{ProtocolVersion: version})
		require.NoError(t, err)
		catalog, err := session.ListTools(t.Context(), nil)
		require.NoError(t, err)
		require.Len(t, catalog.Tools, 4)
		result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "names"})
		require.NoError(t, err)
		if version == mcpruntime.ProtocolVersion {
			assert.Equal(t, []any{"record-one"}, result.StructuredContent)
			assert.Equal(t, "private", catalog.CacheScope)
		} else {
			assert.Equal(t, map[string]any{"value": []any{"record-one"}}, result.StructuredContent)
			assert.Empty(t, catalog.CacheScope)
		}
		assert.NoError(t, session.Close())
	}
}

func TestFrameworkCallerCompletesSDKInputRound(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "independent-server", Version: "1"}, &sdk.ServerOptions{SupportedProtocolVersions: []string{mcpruntime.ProtocolVersion}})
	var rounds atomic.Int64
	server.AddTool(&sdk.Tool{Name: "select", InputSchema: json.RawMessage("{\"type\":\"object\",\"properties\":{},\"additionalProperties\":false}"), OutputSchema: json.RawMessage("{\"type\":\"string\"}")}, func(_ context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		rounds.Add(1)
		if request.Params.RequestState == "" {
			return &sdk.CallToolResult{RequestState: "opaque-for-this-invocation", InputRequests: sdk.InputRequestMap{
				"question": &sdk.ElicitParams{Mode: "form", Message: "Choose a label", RequestedSchema: json.RawMessage("{\"type\":\"object\",\"properties\":{\"label\":{\"type\":\"string\"}},\"required\":[\"label\"]}")},
			}}, nil
		}
		if request.Params.RequestState != "opaque-for-this-invocation" {
			return nil, errors.New("state changed")
		}
		response, ok := request.Params.InputResponses["question"].(*sdk.ElicitResult)
		if !ok || response.Action != "accept" || response.Content["label"] != "accepted" {
			return nil, errors.New("answer changed")
		}
		return &sdk.CallToolResult{Content: []sdk.Content{}, StructuredContent: "accepted"}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true})
	peer := httptest.NewServer(handler)
	defer peer.Close()
	caller, err := mcpruntime.NewHTTPCaller(mcpruntime.HTTPOptions{Endpoint: peer.URL, Client: peer.Client(), ClientInfo: mcpruntime.ClientInfo{Name: "framework-peer", Version: "1"}, InputSupport: mcpruntime.InputSupport{Form: true}})
	require.NoError(t, err)
	initial, err := caller.CallTool(t.Context(), mcpruntime.CallRequest{Tool: "select", Payload: json.RawMessage("{}")})
	require.NoError(t, err)
	require.NotNil(t, initial.InputRequired)
	assert.Empty(t, initial.StructuredContent)
	assert.EqualValues(t, 1, rounds.Load())
	answers := map[string]json.RawMessage{"question": json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"accepted\"}}")}
	require.NoError(t, initial.InputRequired.ValidateResponses(answers))
	complete, err := caller.CallTool(t.Context(), mcpruntime.CallRequest{Tool: "select", Payload: json.RawMessage("{}"), Continuation: &mcpruntime.CallContinuation{RequestState: initial.InputRequired.RequestState, InputResponses: answers}})
	require.NoError(t, err)
	assert.Nil(t, complete.InputRequired)
	assert.JSONEq(t, "\"accepted\"", string(complete.StructuredContent))
	assert.EqualValues(t, 2, rounds.Load())
}

// TestSDKCommandTransportReadsGeneratedStdioServer connects the official Go MCP
// SDK over CommandTransport (2025-11-25 stdio handshake and basic methods) to a
// subprocess running the generated ServeStdio server.
func TestSDKCommandTransportReadsGeneratedStdioServer(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGeneratedStdioServerSubprocess$")
	cmd.Env = append(os.Environ(), "GOA_AI_GENERATED_STDIO_SERVER=1")
	client := sdk.NewClient(&sdk.Implementation{Name: "stdio-sdk-peer", Version: "1"}, &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{},
	})
	session, err := client.Connect(t.Context(), &sdk.CommandTransport{Command: cmd}, &sdk.ClientSessionOptions{
		ProtocolVersion: mcpruntime.LegacyProtocolVersion,
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, session.Close()) }()

	assert.Equal(t, mcpruntime.LegacyProtocolVersion, session.InitializeResult().ProtocolVersion)
	assert.Equal(t, "records", session.InitializeResult().ServerInfo.Name)
	require.NoError(t, session.Ping(t.Context(), nil))

	catalog, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, catalog.Tools, 4)
	assert.Empty(t, catalog.CacheScope)

	readRes, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "read", Arguments: json.RawMessage("{\"name\":\"record-one\"}")})
	require.NoError(t, err)
	assert.False(t, readRes.IsError)
	assert.Equal(t, map[string]any{"value": "record-one"}, readRes.StructuredContent)
	require.Len(t, readRes.Content, 1)
	readText, ok := readRes.Content[0].(*sdk.TextContent)
	require.True(t, ok)
	assert.JSONEq(t, "{\"value\":\"record-one\"}", readText.Text)

	namesRes, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "names"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"value": []any{"record-one"}}, namesRes.StructuredContent)

	invalidRes, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "read", Arguments: json.RawMessage("{\"name\":\"\"}")})
	require.NoError(t, err)
	assert.True(t, invalidRes.IsError)

	deniedRes, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "read", Arguments: json.RawMessage("{\"name\":\"denied\"}")})
	require.NoError(t, err)
	assert.True(t, deniedRes.IsError)

	_, err = session.CallTool(t.Context(), &sdk.CallToolParams{Name: "absent", Arguments: json.RawMessage("{}")})
	require.Error(t, err)

	resources, err := session.ListResources(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, resources.Resources, 1)
	assert.Equal(t, "record://reference", resources.Resources[0].URI)

	templates, err := session.ListResourceTemplates(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, templates.ResourceTemplates, 1)
	assert.Equal(t, "record://items/{id}", templates.ResourceTemplates[0].URITemplate)

	doc, err := session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "record://reference"})
	require.NoError(t, err)
	require.Len(t, doc.Contents, 1)
	assert.Equal(t, "reference", doc.Contents[0].Text)

	templated, err := session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "record://items/alpha"})
	require.NoError(t, err)
	require.Len(t, templated.Contents, 1)
	assert.Equal(t, "item:alpha", templated.Contents[0].Text)

	prompts, err := session.ListPrompts(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, prompts.Prompts, 2)

	welcome, err := session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "welcome"})
	require.NoError(t, err)
	require.Len(t, welcome.Messages, 1)
	assert.Equal(t, "Read the reference.", welcome.Messages[0].Content.(*sdk.TextContent).Text)

	guide, err := session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "guide", Arguments: map[string]string{"topic": "stdio"}})
	require.NoError(t, err)
	require.Len(t, guide.Messages, 1)
	assert.Equal(t, "Topic: stdio", guide.Messages[0].Content.(*sdk.TextContent).Text)

	completed, err := session.Complete(t.Context(), &sdk.CompleteParams{
		Ref:      &sdk.CompleteReference{Type: "ref/prompt", Name: "guide"},
		Argument: sdk.CompleteParamsArgument{Name: "topic", Value: "std"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"std-complete"}, completed.Completion.Values)
}

func TestGeneratedStdioServerStatelessProtocol(t *testing.T) {
	caller, err := mcpruntime.NewStdioCaller(t.Context(), mcpruntime.StdioOptions{
		Command:      os.Args[0],
		Args:         []string{"-test.run=^TestGeneratedStdioServerSubprocess$"},
		Env:          []string{"GOA_AI_GENERATED_STDIO_SERVER=1"},
		ClientInfo:   mcpruntime.ClientInfo{Name: "framework-stdio", Version: "1"},
		InputSupport: mcpruntime.InputSupport{Form: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})

	readRes, err := caller.CallTool(t.Context(), mcpruntime.CallRequest{Tool: "read", Payload: json.RawMessage("{\"name\":\"record-one\"}")})
	require.NoError(t, err)
	assert.JSONEq(t, "\"record-one\"", string(readRes.StructuredContent))

	namesRes, err := caller.CallTool(t.Context(), mcpruntime.CallRequest{Tool: "names", Payload: json.RawMessage("{}")})
	require.NoError(t, err)
	assert.JSONEq(t, "[\"record-one\"]", string(namesRes.StructuredContent))

	initial, err := caller.CallTool(t.Context(), mcpruntime.CallRequest{Tool: "select", Payload: json.RawMessage("{}")})
	require.NoError(t, err)
	require.NotNil(t, initial.InputRequired)
	answers := map[string]json.RawMessage{"question": json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"accepted\"}}")}
	require.NoError(t, initial.InputRequired.ValidateResponses(answers))
	continued, err := caller.CallTool(t.Context(), mcpruntime.CallRequest{
		Tool:         "select",
		Payload:      json.RawMessage("{}"),
		Continuation: &mcpruntime.CallContinuation{RequestState: initial.InputRequired.RequestState, InputResponses: answers},
	})
	require.NoError(t, err)
	assert.Nil(t, continued.InputRequired)
	assert.JSONEq(t, "\"accepted\"", string(continued.StructuredContent))

	cancelCtx, cancelSlow := context.WithCancel(t.Context())
	defer cancelSlow()
	slowStarted := make(chan struct{})
	slowCtx := mcpruntime.WithProgress(cancelCtx, func(context.Context, mcpruntime.Progress) error { close(slowStarted); return nil })
	slowErrCh := make(chan error, 1)
	go func() {
		_, callErr := caller.CallTool(slowCtx, mcpruntime.CallRequest{Tool: "read", Payload: json.RawMessage("{\"name\":\"slow-cancel\"}")})
		slowErrCh <- callErr
	}()
	select {
	case <-slowStarted:
	case callErr := <-slowErrCh:
		t.Fatalf("slow-cancel failed before starting: %v", callErr)
	}
	cancelSlow()
	require.ErrorIs(t, <-slowErrCh, context.Canceled)

	gatedStarted := make(chan struct{})
	gatedCtx := mcpruntime.WithProgress(t.Context(), func(context.Context, mcpruntime.Progress) error { close(gatedStarted); return nil })
	gatedResCh := make(chan mcpruntime.CallResponse, 1)
	gatedErrCh := make(chan error, 1)
	go func() {
		res, callErr := caller.CallTool(gatedCtx, mcpruntime.CallRequest{Tool: "read", Payload: json.RawMessage("{\"name\":\"wait-gate\"}")})
		gatedResCh <- res
		gatedErrCh <- callErr
	}()
	select {
	case <-gatedStarted:
	case callErr := <-gatedErrCh:
		t.Fatalf("wait-gate failed before starting: %v", callErr)
	}

	fastRes, err := caller.CallTool(t.Context(), mcpruntime.CallRequest{Tool: "read", Payload: json.RawMessage("{\"name\":\"fast\"}")})
	require.NoError(t, err)
	assert.JSONEq(t, "\"fast-done\"", string(fastRes.StructuredContent))
	require.NoError(t, <-gatedErrCh)
	assert.JSONEq(t, "\"gated\"", string((<-gatedResCh).StructuredContent))

	var updates []mcpruntime.Progress
	progCtx := mcpruntime.WithProgress(t.Context(), func(_ context.Context, update mcpruntime.Progress) error {
		updates = append(updates, update)
		return nil
	})
	progRes, err := caller.CallTool(progCtx, mcpruntime.CallRequest{Tool: "read", Payload: json.RawMessage("{\"name\":\"progress\"}")})
	require.NoError(t, err)
	assert.JSONEq(t, "\"progressed\"", string(progRes.StructuredContent))
	require.Len(t, updates, 1)
	assert.Equal(t, 50.0, updates[0].Value)

	var events []mcpruntime.SubscriptionEvent
	err = caller.Listen(t.Context(), mcpruntime.SubscriptionFilter{ResourceSubscriptions: []string{"record://reference"}}, func(_ context.Context, event mcpruntime.SubscriptionEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, mcpruntime.SubscriptionAcknowledged, events[0].Kind)
	assert.Equal(t, mcpruntime.SubscriptionResourceUpdated, events[1].Kind)
	assert.Equal(t, "record://reference/child", events[1].URI)

	service := newRecordService()
	var middlewareCalls atomic.Int64
	endpoints := genrecords.NewEndpoints(service)
	endpoints.Use(func(next goa.Endpoint) goa.Endpoint {
		return func(ctx context.Context, input any) (any, error) { middlewareCalls.Add(1); return next(ctx, input) }
	})
	mcpEndpoints := genmcp.NewEndpoints(genmcp.NewMCPAdapter(endpoints, nil))
	serverIn, clientOut, err := os.Pipe()
	require.NoError(t, err)
	clientIn, serverOut, err := os.Pipe()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- genserver.ServeStdio(t.Context(), serverIn, serverOut, mcpEndpoints) }()

	reader := bufio.NewReader(clientIn)
	sendRPC := func(id int, method string, params map[string]any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		require.NoError(t, err)
		_, err = clientOut.Write(append(raw, '\n'))
		require.NoError(t, err)
		line, err := reader.ReadBytes('\n')
		require.NoError(t, err)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(line, &resp))
		return resp
	}
	stdMeta := map[string]any{
		"io.modelcontextprotocol/protocolVersion":    mcpruntime.ProtocolVersion,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	taskMeta := map[string]any{
		"io.modelcontextprotocol/protocolVersion": mcpruntime.ProtocolVersion,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{
			"elicitation": map[string]any{"form": map[string]any{}},
			"extensions":  map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}},
		},
	}

	disc := sendRPC(1, "server/discover", map[string]any{"_meta": stdMeta})
	require.Nil(t, disc["error"])
	assert.Equal(t, "complete", disc["result"].(map[string]any)["resultType"])

	resList := sendRPC(2, "resources/list", map[string]any{"_meta": stdMeta})
	require.Nil(t, resList["error"])
	assert.Len(t, resList["result"].(map[string]any)["resources"].([]any), 1)

	tplList := sendRPC(3, "resources/templates/list", map[string]any{"_meta": stdMeta})
	require.Nil(t, tplList["error"])
	assert.Len(t, tplList["result"].(map[string]any)["resourceTemplates"].([]any), 1)

	resRead := sendRPC(4, "resources/read", map[string]any{"uri": "record://items/alpha", "_meta": stdMeta})
	require.Nil(t, resRead["error"])
	assert.Equal(t, "item:alpha", resRead["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)["text"])

	dirRead := sendRPC(5, "resources/directory/read", map[string]any{"uri": "record://dir", "_meta": stdMeta})
	require.Nil(t, dirRead["error"])
	assert.Equal(t, "record://dir/child.txt", dirRead["result"].(map[string]any)["resources"].([]any)[0].(map[string]any)["uri"])

	promptList := sendRPC(6, "prompts/list", map[string]any{"_meta": stdMeta})
	require.Nil(t, promptList["error"])
	assert.Len(t, promptList["result"].(map[string]any)["prompts"].([]any), 2)

	promptGet := sendRPC(7, "prompts/get", map[string]any{"name": "guide", "arguments": map[string]any{"topic": "pipes"}, "_meta": stdMeta})
	require.Nil(t, promptGet["error"])
	assert.Equal(t, "Topic: pipes", promptGet["result"].(map[string]any)["messages"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"])

	comp := sendRPC(8, "completion/complete", map[string]any{
		"ref":      map[string]any{"type": "ref/prompt", "name": "guide"},
		"argument": map[string]any{"name": "topic", "value": "pip"},
		"_meta":     stdMeta,
	})
	require.Nil(t, comp["error"])
	assert.Equal(t, []any{"pip-complete"}, comp["result"].(map[string]any)["completion"].(map[string]any)["values"])

	skillsList := sendRPC(9, "skills/list", map[string]any{"_meta": stdMeta})
	require.Nil(t, skillsList["error"])
	assert.Len(t, skillsList["result"].(map[string]any)["skills"].([]any), 1)

	skillsGet := sendRPC(10, "skills/get", map[string]any{"uri": "skill://review/SKILL.md", "_meta": stdMeta})
	require.Nil(t, skillsGet["error"])
	assert.Equal(t, "skill://review/SKILL.md", skillsGet["result"].(map[string]any)["skill"].(map[string]any)["uri"])

	missingCap := sendRPC(11, "tools/call", map[string]any{"name": "create_task", "arguments": map[string]any{"query": "deploy"}, "_meta": stdMeta})
	require.NotNil(t, missingCap["error"])
	assert.Equal(t, float64(mcpruntime.MissingRequiredClientCapability), missingCap["error"].(map[string]any)["code"])

	created := sendRPC(12, "tools/call", map[string]any{"name": "create_task", "arguments": map[string]any{"query": "deploy"}, "_meta": taskMeta})
	require.Nil(t, created["error"])
	createdRes := created["result"].(map[string]any)
	assert.Equal(t, "task", createdRes["resultType"])
	assert.Equal(t, "input_required", createdRes["status"])
	taskID := createdRes["taskId"].(string)

	pendingTask := sendRPC(13, "tasks/get", map[string]any{"taskId": taskID, "_meta": taskMeta})
	require.Nil(t, pendingTask["error"])
	pendingRes := pendingTask["result"].(map[string]any)
	assert.Equal(t, "input_required", pendingRes["status"])
	var questionKey string
	for k := range pendingRes["inputRequests"].(map[string]any) {
		questionKey = k
	}
	require.NotEmpty(t, questionKey)

	updated := sendRPC(14, "tasks/update", map[string]any{
		"taskId":         taskID,
		"inputResponses": map[string]any{questionKey: map[string]any{"action": "accept", "content": map[string]any{"label": "approved"}}},
		"_meta":          taskMeta,
	})
	require.Nil(t, updated["error"])

	fetched := sendRPC(15, "tasks/get", map[string]any{"taskId": taskID, "_meta": taskMeta})
	require.Nil(t, fetched["error"])
	fetchedRes := fetched["result"].(map[string]any)
	assert.Equal(t, "completed", fetchedRes["status"])
	assert.Equal(t, "approved", fetchedRes["result"].(map[string]any)["structuredContent"])

	cancelledTask := sendRPC(16, "tasks/cancel", map[string]any{"taskId": taskID, "_meta": taskMeta})
	require.Nil(t, cancelledTask["error"])

	fetchedAfterCancel := sendRPC(17, "tasks/get", map[string]any{"taskId": taskID, "_meta": taskMeta})
	require.Nil(t, fetchedAfterCancel["error"])
	assert.Equal(t, "cancelled", fetchedAfterCancel["result"].(map[string]any)["status"])
	assert.Positive(t, middlewareCalls.Load())

	require.NoError(t, clientOut.Close())
	require.NoError(t, <-done)
	require.NoError(t, clientIn.Close())
}

func TestGeneratedStdioServerSubprocess(_ *testing.T) {
	if os.Getenv("GOA_AI_GENERATED_STDIO_SERVER") != "1" {
		return
	}
	adapter := genmcp.NewMCPAdapter(genrecords.NewEndpoints(newRecordService()), nil)
	if err := genserver.ServeStdio(context.Background(), os.Stdin, os.Stdout, genmcp.NewEndpoints(adapter)); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}
`
