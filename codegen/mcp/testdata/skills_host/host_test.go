// These tests send Skill requests through generated HTTP contracts and execute
// local tools through the generated agent. No remote instructions run as code;
// the synthetic execution dependency records only verified script bytes.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"

	"goa.design/goa-ai/runtime/agent/api"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	genactions "skill-host.local/gen/host_actions"
	genreader "skill-host.local/gen/host_actions/agents/reader"
	genexecutor "skill-host.local/gen/host_actions/agents/reader/local"
	genlocal "skill-host.local/gen/host_actions/toolsets/local"
	genservice "skill-host.local/gen/instructions"
	genclient "skill-host.local/gen/jsonrpc/mcp_instructions/client"
	genserver "skill-host.local/gen/jsonrpc/mcp_instructions/server"
	genprotocol "skill-host.local/gen/mcp_instructions"
)

type (
	// instructionService owns server data; tests replace it under the same lock
	// that generated HTTP handlers use to serve catalog and file requests.
	instructionService struct {
		mu      sync.Mutex
		entries map[string]*genservice.Entry
		files   map[string]string
		visible []string
		reads   atomic.Int32
	}
	executionPlanner struct {
		payload rawjson.Message
		outputs []*planner.ToolOutput
	}
)

const skillURI = "skill://shared/review/SKILL.md"
const scriptURI = "skill://shared/review/run.sh"
const nestedURI = "skill://shared/review/nested/SKILL.md"

func TestSkillHostLazyOriginAndContext(t *testing.T) {
	ctx := t.Context()
	first := newInstructions("first")
	second := newInstructions("second")
	host, err := newSkillHost(ctx, map[string]*genprotocol.Client{
		"first": instructionPeer(t, first), "second": instructionPeer(t, second),
	}, func(context.Context, []byte) error { return nil })
	require.NoError(t, err)
	keys, err := host.list(ctx, "first")
	require.NoError(t, err)
	assert.Equal(t, []skillIdentity{{"first", skillURI}}, keys)
	a := skillIdentity{"first", skillURI}
	b := skillIdentity{"second", skillURI}
	require.NoError(t, host.lookup(ctx, b))
	assert.Error(t, host.load(ctx, a))
	require.NoError(t, host.approveLoad(a))
	require.NoError(t, host.approveLoad(b))
	assert.Zero(t, first.reads.Load())
	assert.Zero(t, second.reads.Load())
	require.NoError(t, host.load(ctx, a))
	require.NoError(t, host.load(ctx, b))
	assert.Equal(t, int32(1), first.reads.Load())
	assert.Equal(t, int32(1), second.reads.Load())
	messages, err := host.modelMessages()
	require.NoError(t, err)
	require.Len(t, messages, 2)
	for i, origin := range []string{"first", "second"} {
		assert.Equal(t, model.ConversationRoleUser, messages[i].Role)
		assert.Equal(t, origin, messages[i].Meta["mcp_skill_origin"])
		assert.Contains(t, messages[i].Parts[0].(model.TextPart).Text, origin)
	}
	messages[0].Meta["mcp_skill_origin"] = "mutated"
	again, err := host.modelMessages()
	require.NoError(t, err)
	assert.Equal(t, "first", again[0].Meta["mcp_skill_origin"])
	bytes, err := host.readSupporting(ctx, a, nestedURI)
	require.NoError(t, err)
	bytes[0] = 'X'
	bytes, err = host.readSupporting(ctx, a, nestedURI)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(bytes), "---"))
	assert.Equal(t, int32(2), first.reads.Load())
	assert.Len(t, host.held, 2)
	nested := skillIdentity{"first", nestedURI}
	require.NoError(t, host.lookup(ctx, nested))
	assert.Error(t, host.load(ctx, nested))
	_, err = host.readSupporting(ctx, a, "skill://other/review/run.sh")
	assert.ErrorContains(t, err, "absent")
	assert.Equal(t, int32(2), first.reads.Load())
	_, err = host.Execute(ctx, &genactions.ExecutePayload{Origin: a.origin, SkillURI: a.uri, ScriptURI: scriptURI})
	assert.ErrorContains(t, err, "explicit consent")
	assert.Equal(t, int32(2), first.reads.Load())
}

func TestSkillHostCachedSupportingFileCannotActivateMismatchedEntry(t *testing.T) {
	source := newInstructions("first")
	source.entries[nestedURI].Frontmatter = json.RawMessage(`{"name":"nested","description":"different"}`)
	host := loadedHost(t, source)
	_, err := host.readSupporting(t.Context(), skillIdentity{"first", skillURI}, nestedURI)
	require.NoError(t, err)
	nested := skillIdentity{"first", nestedURI}
	require.NoError(t, host.lookup(t.Context(), nested))
	require.NoError(t, host.approveLoad(nested))
	assert.Error(t, host.load(t.Context(), nested))
	assert.Equal(t, int32(2), source.reads.Load())
	assert.Len(t, host.held, 1)
	assert.Len(t, host.messages, 1)
}

func TestSkillHostChangedManifestRevokesCompleteConsent(t *testing.T) {
	for _, change := range []string{"digest", "addition", "removal"} {
		t.Run(change, func(t *testing.T) {
			source := newInstructions("first")
			host := loadedHost(t, source)
			key := skillIdentity{"first", skillURI}
			payload, err := genlocal.MarshalExecutePayload(&genlocal.ExecutePayload{Origin: key.origin, SkillURI: key.uri, ScriptURI: scriptURI})
			require.NoError(t, err)
			_, err = host.executionPrompt(t.Context(), &runtime.ToolCall{ToolCallID: "pending", Payload: payload})
			require.NoError(t, err)
			source.mu.Lock()
			switch change {
			case "digest":
				source.files[scriptURI] = "changed script"
			case "addition":
				source.files["skill://shared/review/new.txt"] = "new"
			case "removal":
				delete(source.files, scriptURI)
			}
			source.entries[skillURI] = instructionEntry(skillURI, "review", "first", source.files)
			source.mu.Unlock()
			require.NoError(t, host.lookup(t.Context(), key))
			assert.ErrorContains(t, host.approveExecution("pending"), "changed")
			_, err = host.Execute(t.Context(), &genactions.ExecutePayload{Origin: key.origin, SkillURI: key.uri, ScriptURI: scriptURI})
			assert.ErrorContains(t, err, "consent")
			require.NoError(t, host.approveLoad(key))
			assert.ErrorContains(t, host.load(t.Context(), key), "fresh model context")
			_, err = host.readSupporting(t.Context(), key, "skill://shared/review/new.txt")
			assert.ErrorContains(t, err, "absent")
			assert.Equal(t, int32(1), source.reads.Load())
			assert.Len(t, host.held, 1)
			// A new host asks again and can load the changed version independently.
			fresh := loadedHost(t, source)
			assert.Len(t, fresh.held, 1)
		})
	}
}

func TestSkillHostManifestOrderDoesNotChangeConsent(t *testing.T) {
	source := newInstructions("first")
	host := loadedHost(t, source)
	key := skillIdentity{"first", skillURI}
	source.mu.Lock()
	files, ok := source.entries[skillURI].Resources.AsManifest()
	require.True(t, ok)
	slices.Reverse(files)
	source.entries[skillURI].Resources = genservice.NewFilesManifest(files)
	source.mu.Unlock()
	require.NoError(t, host.lookup(t.Context(), key))
	require.NoError(t, host.load(t.Context(), key))
	assert.Equal(t, int32(1), source.reads.Load())
}

func TestSkillHostPerSkillMemoryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		count    int
		size     int64
		accepted bool
	}{
		{"below file count", 511, 0, true}, {"at file count", 512, 0, true}, {"above file count", 513, 0, false},
		{"below bytes", 1, maxSkillBytes - 1, true}, {"at bytes", 1, maxSkillBytes, true},
		{"above bytes", 1, maxSkillBytes + 1, false}, {"overflow", 2, 1 << 62, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := make([]*genprotocol.SkillFile, tc.count)
			for i := range files {
				files[i] = &genprotocol.SkillFile{Size: tc.size}
			}
			err := admitSkill(&genprotocol.SkillEntry{Resources: genprotocol.NewSkillResourcesManifest(files)})
			assert.Equal(t, tc.accepted, err == nil)
		})
	}
	// Each entry receives its own allowance even though together they exceed it.
	for range 2 {
		require.NoError(t, admitSkill(&genprotocol.SkillEntry{Resources: genprotocol.NewSkillResourcesManifest([]*genprotocol.SkillFile{{Size: 13}, {Size: maxSkillBytes - 13}})}))
	}
	assert.Error(t, admitSkill(&genprotocol.SkillEntry{Resources: genprotocol.NewSkillResourcesDynamic("dynamic")}))
}

func TestSkillHostGeneratedExecutionRequiresConfirmation(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprintf("approved=%v", approved), func(t *testing.T) {
			ctx := t.Context()
			source := newInstructions("first")
			var executions atomic.Int32
			host, err := newSkillHost(ctx, map[string]*genprotocol.Client{"first": instructionPeer(t, source)}, func(_ context.Context, content []byte) error {
				if string(content) != "verified script" {
					return fmt.Errorf("unexpected execution bytes %q", content)
				}
				executions.Add(1)
				return nil
			})
			require.NoError(t, err)
			key := skillIdentity{"first", skillURI}
			require.NoError(t, host.lookup(ctx, key))
			require.NoError(t, host.approveLoad(key))
			require.NoError(t, host.load(ctx, key))
			store := storageinmem.New()
			_, err = store.CreateSession(ctx, "session", time.Now())
			require.NoError(t, err)
			rt := runtime.New(store, runtime.WithEngine(engineinmem.New()), runtime.WithToolConfirmation(host.executionConfirmation()))
			executor := genexecutor.NewReaderLocalExec(genexecutor.WithClient(genactions.NewClient(genactions.NewEndpoints(host).Execute)))
			require.NoError(t, genreader.RegisterUsedToolsets(ctx, rt, genreader.WithLocalExecutor(executor)))
			payload, err := genlocal.MarshalExecutePayload(&genlocal.ExecutePayload{Origin: key.origin, SkillURI: key.uri, ScriptURI: scriptURI})
			require.NoError(t, err)
			plan := &executionPlanner{payload: payload}
			registration, err := genreader.NewReaderAgentRegistration(rt, genreader.ReaderAgentConfig{Planner: plan})
			require.NoError(t, err)
			// Arbitrary scripts cannot be replayed safely after an uncertain result.
			registration.ExecuteToolActivityOptions.RetryPolicy.MaxAttempts = 1
			require.NoError(t, rt.RegisterAgent(ctx, registration))
			messages, err := host.modelMessages()
			require.NoError(t, err)
			client := genreader.NewClient(rt)
			first, err := client.Run(ctx, "session", messages, runtime.WithRunID("first"))
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			require.Len(t, first.Suspension.Pending, 1)
			pending := first.Suspension.Pending[0].Confirmation
			assert.Zero(t, executions.Load())
			assert.Equal(t, int32(1), source.reads.Load())
			if approved {
				require.NoError(t, host.approveExecution(pending.ToolCallID))
			}
			last, err := client.Continue(ctx, "session", "first", "second", "turn-second", &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{ID: pending.ID, Approved: approved, RequestedBy: "operator"}}, runtime.WorkflowOptions{})
			require.NoError(t, err)
			assert.Nil(t, last.Suspension)
			require.Len(t, plan.outputs, 1)
			assert.Nil(t, plan.outputs[0].Failure)
			result, err := genlocal.UnmarshalExecuteResult(plan.outputs[0].Result)
			require.NoError(t, err)
			if approved {
				assert.Equal(t, "Executed", result.Outcome)
				assert.Equal(t, int32(1), executions.Load())
				assert.Equal(t, int32(2), source.reads.Load())
			} else {
				assert.Equal(t, "Denied", result.Outcome)
				assert.Zero(t, executions.Load())
				assert.Equal(t, int32(1), source.reads.Load())
			}
		})
	}
}

func newInstructions(description string) *instructionService {
	files := map[string]string{
		skillURI:  "---\nname: review\ndescription: " + description + "\nallowed-tools: all\n---\nInstructions from " + description,
		scriptURI: "verified script",
		nestedURI: "---\nname: nested\ndescription: supporting\n---\nNested instructions",
	}
	return &instructionService{files: files, visible: []string{skillURI}, entries: map[string]*genservice.Entry{
		skillURI:  instructionEntry(skillURI, "review", description, files),
		nestedURI: instructionEntry(nestedURI, "nested", "supporting", map[string]string{nestedURI: files[nestedURI]}),
	}}
}

// instructionEntry derives exact size and digest from the bytes served by this
// synthetic server. Frontmatter stays complete, including untrusted permissions.
func instructionEntry(uri, name, description string, contents map[string]string) *genservice.Entry {
	files := make([]*genservice.File, 0, len(contents))
	for uri, content := range contents {
		files = append(files, &genservice.File{URI: uri, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content))), Size: int64(len(content))})
	}
	slices.SortFunc(files, func(a, b *genservice.File) int { return strings.Compare(a.URI, b.URI) })
	frontmatter := fmt.Sprintf(`{"name":%q,"description":%q}`, name, description)
	if uri == skillURI {
		frontmatter = fmt.Sprintf(`{"name":%q,"description":%q,"allowed-tools":"all"}`, name, description)
	}
	return &genservice.Entry{URI: uri, Frontmatter: json.RawMessage(frontmatter), Resources: genservice.NewFilesManifest(files)}
}

func loadedHost(t *testing.T, source *instructionService) *skillHost {
	t.Helper()
	host, err := newSkillHost(t.Context(), map[string]*genprotocol.Client{"first": instructionPeer(t, source)}, func(context.Context, []byte) error { return nil })
	require.NoError(t, err)
	key := skillIdentity{"first", skillURI}
	require.NoError(t, host.lookup(t.Context(), key))
	require.NoError(t, host.approveLoad(key))
	require.NoError(t, host.load(t.Context(), key))
	return host
}

func instructionPeer(t *testing.T, source *instructionService) *genprotocol.Client {
	t.Helper()
	mux := goahttp.NewMuxer()
	adapter := genprotocol.NewMCPAdapter(genservice.NewEndpoints(source), nil)
	server := genserver.New(genprotocol.NewEndpoints(adapter), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil)
	genserver.Mount(mux, server)
	peer := httptest.NewServer(mux)
	t.Cleanup(peer.Close)
	address, err := url.Parse(peer.URL)
	require.NoError(t, err)
	transport := genclient.NewClient(address.Scheme, address.Host, peer.Client(), goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	return genprotocol.NewClient(transport.ServerDiscover(), transport.ResourcesList(), transport.ResourcesRead(), transport.ResourcesTemplatesList(), transport.SkillsList(), transport.SkillsGet())
}

func (s *instructionService) List(_ context.Context, _ *genservice.ListPayload) (*genservice.ListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := make([]*genservice.Entry, 0, len(s.visible))
	for _, uri := range s.visible {
		entries = append(entries, s.entries[uri])
	}
	return &genservice.ListResult{Skills: entries}, nil
}
func (s *instructionService) Lookup(_ context.Context, p *genservice.LookupPayload) (*genservice.LookupResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, present := s.entries[p.URI]
	if !present {
		return nil, goa.PermanentError("invalid_params", "unknown Skill")
	}
	return &genservice.LookupResult{Skill: entry}, nil
}
func (s *instructionService) Read(_ context.Context, p *genservice.ReadPayload) (*genservice.ReadResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, present := s.files[p.URI]
	if !present {
		return nil, goa.PermanentError("invalid_params", "unknown file")
	}
	s.reads.Add(1)
	return &genservice.ReadResult{Contents: []*genservice.Item{{Content: genservice.NewContentText(&genservice.Text{URI: p.URI, Text: content})}}}, nil
}

func (p *executionPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: genlocal.Execute, Payload: p.payload}}}, nil
}
func (p *executionPlanner) PlanResume(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.outputs = input.ToolOutputs
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "complete"}}}}}, nil
}
