// This reference host owns one model context and the Skill entries loaded into
// it. Generated clients own protocol decoding; the shared verifier checks every
// fetched file. Trusted UI actions grant consent. Remote text never grants tools.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
	genactions "skill-host.local/gen/host_actions"
	genlocal "skill-host.local/gen/host_actions/toolsets/local"
	genshapes "skill-host.local/gen/host_shapes"
	genprotocol "skill-host.local/gen/mcp_instructions"
)

type (
	// skillIdentity keeps host-assigned server identity with the exact Skill URI.
	skillIdentity struct {
		origin string
		uri    string
	}

	// skillSnapshot owns discovery bytes and a content-binding key for consent.
	skillSnapshot struct {
		entry   *genprotocol.SkillEntry
		encoded json.RawMessage
		binding string
	}

	// fileIdentity keeps independently served bytes separate, even at equal URIs.
	fileIdentity struct {
		origin string
		uri    string
		digest string
	}

	executionRequest struct {
		skill   skillIdentity
		binding string
	}

	// skillHost keeps complete entries as long as its model context exists. It
	// has no individual unload operation or disk cache. Replacing this context
	// requires a new host and fresh consent; saved messages require saved entries.
	skillHost struct {
		mu               sync.Mutex
		sources          map[string]*genprotocol.Client
		entries          map[skillIdentity]skillSnapshot
		held             map[skillIdentity]skillSnapshot
		loadApprovals    map[skillIdentity]string
		executeApprovals map[skillIdentity]string
		pendingExecution map[string]executionRequest
		cache            map[fileIdentity][]byte
		messages         []*model.Message
		runScript        func(context.Context, []byte) error
	}
)

const (
	// These inclusive per-Skill ceilings bound this example's retained file
	// bytes. They implement the extension's supported floor, not a context-wide
	// budget. Several Skills may each consume their own complete allowance.
	maxSkillFiles = 512
	maxSkillBytes = int64(16 * 1024 * 1024)
)

// newSkillHost receives already-built, authenticated clients. Labels come from
// the application's connection registry, never a server's self-reported name.
func newSkillHost(ctx context.Context, sources map[string]*genprotocol.Client, runScript func(context.Context, []byte) error) (*skillHost, error) {
	for origin, client := range sources {
		if origin == "" || client == nil {
			return nil, errors.New("host source requires a label and generated client")
		}
		discovery, err := client.ServerDiscover(ctx, &genprotocol.DiscoverPayload{})
		if err != nil {
			return nil, err
		}
		if discovery.Capabilities.Resources == nil {
			return nil, errors.New("Skill source did not declare resources")
		}
		extensions, err := genshapes.DecodeExtensions(discovery.Capabilities.Extensions)
		if err != nil {
			return nil, err
		}
		declaration, present := extensions["io.modelcontextprotocol/skills"]
		if !present {
			return nil, errors.New("source did not declare the Skills extension")
		}
		if _, err := genshapes.DecodeSkillDeclaration(declaration); err != nil {
			return nil, err
		}
	}
	if runScript == nil {
		return nil, errors.New("host execution dependency is required")
	}
	return &skillHost{
		sources: maps.Clone(sources), entries: make(map[skillIdentity]skillSnapshot),
		held: make(map[skillIdentity]skillSnapshot), loadApprovals: make(map[skillIdentity]string),
		executeApprovals: make(map[skillIdentity]string), pendingExecution: make(map[string]executionRequest),
		cache: make(map[fileIdentity][]byte), runScript: runScript,
	}, nil
}

// list retains complete entries and disambiguates names through origin and URI.
// An empty page does not prevent lookup. It reads no instruction or script file.
func (h *skillHost) list(ctx context.Context, origin string) ([]skillIdentity, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	client, present := h.sources[origin]
	if !present {
		return nil, errors.New("unknown host source")
	}
	var cursor *string
	seen := make(map[string]bool)
	seenCursors := make(map[string]bool)
	var result []skillIdentity
	for {
		page, err := client.SkillsList(ctx, &genprotocol.SkillsListPayload{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Skills {
			key := skillIdentity{origin, entry.URI}
			if seen[entry.URI] {
				return nil, errors.New("duplicate Skill URI in listing")
			}
			seen[entry.URI] = true
			if err := h.remember(ctx, key, entry); err != nil {
				return nil, err
			}
			result = append(result, key)
		}
		if page.NextCursor == nil {
			return result, nil
		}
		if seenCursors[*page.NextCursor] {
			return nil, errors.New("repeated Skill catalog cursor")
		}
		seenCursors[*page.NextCursor] = true
		cursor = page.NextCursor
	}
}

// lookup refreshes discovery for one exact origin and URI without reading files.
// A changed manifest revokes prior consent while the old context keeps its entry.
func (h *skillHost) lookup(ctx context.Context, key skillIdentity) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	client, present := h.sources[key.origin]
	if !present {
		return errors.New("unknown host source")
	}
	result, err := client.SkillsGet(ctx, &genprotocol.SkillsGetPayload{URI: key.uri})
	if err != nil {
		return err
	}
	if result.Skill.URI != key.uri {
		return errors.New("Skill lookup returned a different URI")
	}
	return h.remember(ctx, key, result.Skill)
}

// approveLoad is a trusted UI action after showing the origin and complete
// manifest. Consent is scoped to this exact Skill, including for nested Skills.
func (h *skillHost) approveLoad(key skillIdentity) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, present := h.entries[key]
	if !present {
		return errors.New("Skill discovery is required before consent")
	}
	if err := admitSkill(entry.entry); err != nil {
		return err
	}
	h.loadApprovals[key] = entry.binding
	return nil
}

// load fetches only SKILL.md after consent, verifies every byte and YAML field,
// and appends ordinary user context with an explicit origin. A changed version
// requires a fresh model context rather than replacing a still-held manifest.
func (h *skillHost) load(ctx context.Context, key skillIdentity) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, present := h.entries[key]
	if !present || h.loadApprovals[key] != entry.binding {
		return errors.New("Skill loading requires current explicit consent")
	}
	if held, active := h.held[key]; active {
		if held.binding != entry.binding {
			return errors.New("changed Skill requires a fresh model context and consent")
		}
		return nil
	}
	content, err := h.read(ctx, key, entry, key.uri)
	if err != nil {
		return err
	}
	h.held[key] = entry
	h.messages = append(h.messages, &model.Message{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: fmt.Sprintf("Untrusted Skill content from server %q, resource %q:\n\n%s", key.origin, key.uri, content)}},
		Meta:  map[string]any{"mcp_skill_origin": key.origin, "mcp_skill_uri": key.uri},
	})
	return nil
}

// readSupporting uses only the entry held with this model context. A directory
// listing or refreshed catalog cannot add a file or activate nested instructions.
func (h *skillHost) readSupporting(ctx context.Context, key skillIdentity, uri string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, present := h.held[key]
	if !present {
		return nil, errors.New("Skill must be loaded in this model context")
	}
	return h.read(ctx, key, entry, uri)
}

// executionConfirmation integrates the local tool with the runtime's existing
// trusted-host confirmation flow. The pending tool-call ID retains the complete
// manifest binding shown by the prompt; it never comes from model arguments.
func (h *skillHost) executionConfirmation() *runtime.ToolConfirmationConfig {
	return &runtime.ToolConfirmationConfig{Confirm: map[tools.Ident]*runtime.ToolConfirmation{
		genlocal.Execute: {Prompt: h.executionPrompt, DeniedResult: deniedExecution},
	}}
}

// approveExecution is called by the trusted UI before it sends an affirmative
// runtime continuation. A changed manifest cannot consume an earlier prompt.
func (h *skillHost) approveExecution(callID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	request, present := h.pendingExecution[callID]
	if !present {
		return errors.New("execution confirmation is unknown")
	}
	entry, present := h.entries[request.skill]
	if !present || entry.binding != request.binding || h.held[request.skill].binding != request.binding {
		return errors.New("Skill changed after execution consent was requested")
	}
	h.executeApprovals[request.skill] = request.binding
	delete(h.pendingExecution, callID)
	return nil
}

// Execute is the native method called by the generated tool executor. It checks
// current per-Skill consent, reads the exact listed script through its originating
// client and passes only verified bytes to the host's built execution dependency.
func (h *skillHost) Execute(ctx context.Context, payload *genactions.ExecutePayload) (*genactions.ExecuteResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := skillIdentity{payload.Origin, payload.SkillURI}
	entry, present := h.held[key]
	if !present || h.executeApprovals[key] != entry.binding {
		return nil, errors.New("local execution requires explicit consent for this Skill manifest")
	}
	content, err := h.read(ctx, key, entry, payload.ScriptURI)
	if err != nil {
		return nil, err
	}
	if err := h.runScript(ctx, content); err != nil {
		return nil, err
	}
	return &genactions.ExecuteResult{Outcome: "Executed"}, nil
}

// modelMessages returns an independent view of this host-owned context. The
// host retains its entries and original bytes while callers use these messages.
func (h *skillHost) modelMessages() ([]*model.Message, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return model.CloneMessages(h.messages)
}

// admitSkill bounds the example's retained bytes for one activated Skill. No
// context-wide count or byte ceiling is derived from these per-Skill limits.
func admitSkill(entry *genprotocol.SkillEntry) error {
	files, stable := entry.Resources.AsManifest()
	if !stable {
		return errors.New("host declines dynamic Skills without stable file digests")
	}
	if len(files) > maxSkillFiles {
		return errors.New("Skill exceeds the host's 512-file per-Skill memory policy")
	}
	var total int64
	for _, file := range files {
		if file.Size > maxSkillBytes-total {
			return errors.New("Skill exceeds the host's 16 MiB per-Skill memory policy")
		}
		total += file.Size
	}
	return nil
}

// skillBinding covers exact file identities, digests and byte lengths independent
// of manifest order. Consent never depends on a label or a subset of the files.
func skillBinding(entry *genprotocol.SkillEntry) string {
	files, stable := entry.Resources.AsManifest()
	if !stable {
		return "dynamic"
	}
	parts := make([]string, 0, len(files))
	for _, file := range files {
		parts = append(parts, fmt.Sprintf("%q:%q:%d", file.URI, file.Digest, file.Size))
	}
	sort.Strings(parts)
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(strings.Join(parts, "\n"))))
}

func deniedExecution(context.Context, *runtime.ToolCall) (any, error) {
	return &genlocal.ExecuteResult{Outcome: "Denied"}, nil
}

// remember validates and copies a generated discovery value before owning it.
// A changed complete file set revokes both loading and local-execution consent.
func (h *skillHost) remember(ctx context.Context, key skillIdentity, entry *genprotocol.SkillEntry) error {
	encoded, err := genprotocol.EncodeSkillEntry(entry)
	if err != nil {
		return err
	}
	if err := mcp.ValidateSkillEntry(ctx, encoded); err != nil {
		return err
	}
	owned, err := genprotocol.DecodeSkillEntry(encoded)
	if err != nil {
		return err
	}
	snapshot := skillSnapshot{entry: owned, encoded: encoded, binding: skillBinding(owned)}
	if previous, present := h.entries[key]; present && previous.binding != snapshot.binding {
		delete(h.loadApprovals, key)
		delete(h.executeApprovals, key)
	}
	h.entries[key] = snapshot
	return nil
}

// read requires exact held membership before any network call. The generated
// resource decoder owns text/blob presence; the verifier owns original bytes.
// Cached bytes stay private and every caller receives its own copy.
func (h *skillHost) read(ctx context.Context, key skillIdentity, entry skillSnapshot, uri string) ([]byte, error) {
	files, stable := entry.entry.Resources.AsManifest()
	if !stable {
		return nil, errors.New("host declines dynamic Skill content")
	}
	var digest string
	for _, file := range files {
		if file.URI == uri {
			digest = file.Digest
			break
		}
	}
	if digest == "" {
		return nil, errors.New("file is absent from the held Skill manifest")
	}
	cacheKey := fileIdentity{key.origin, uri, digest}
	if content, cached := h.cache[cacheKey]; cached {
		// Another Skill may list the same file. Its held size and frontmatter
		// must also match; a supporting read never proves a later activation.
		if err := mcp.VerifySkillFile(ctx, entry.encoded, uri, content); err != nil {
			return nil, err
		}
		return slices.Clone(content), nil
	}
	result, err := h.sources[key.origin].ResourcesRead(ctx, &genprotocol.ResourcesReadPayload{URI: uri})
	if err != nil {
		return nil, err
	}
	complete, completed := result.Outcome.AsComplete()
	if !completed || len(complete.Contents) != 1 || complete.Contents[0].URI != uri {
		return nil, errors.New("Skill file read did not return one completed exact resource")
	}
	resource := complete.Contents[0]
	var content []byte
	if resource.Text != nil {
		content = []byte(*resource.Text)
	} else {
		content, err = base64.StdEncoding.DecodeString(*resource.Blob)
		if err != nil {
			return nil, err
		}
	}
	if err := mcp.VerifySkillFile(ctx, entry.encoded, uri, content); err != nil {
		return nil, err
	}
	h.cache[cacheKey] = content
	return slices.Clone(content), nil
}

// executionPrompt saves the content binding before awaiting the human decision.
// The prompt identifies the host label, Skill URI, script URI and exact manifest.
func (h *skillHost) executionPrompt(_ context.Context, call *runtime.ToolCall) (string, error) {
	payload, err := genlocal.UnmarshalExecutePayload(call.Payload)
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := skillIdentity{payload.Origin, payload.SkillURI}
	entry, present := h.held[key]
	if !present || h.entries[key].binding != entry.binding {
		return "", errors.New("execution requires a loaded, current Skill")
	}
	h.pendingExecution[call.ToolCallID] = executionRequest{skill: key, binding: entry.binding}
	return fmt.Sprintf("Allow local code execution for server %q, Skill %q, script %q, manifest %s?", key.origin, key.uri, payload.ScriptURI, entry.binding), nil
}
