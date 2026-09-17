package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type hintRecordingStreamSink struct {
	events []stream.Event
}

func (s *hintRecordingStreamSink) Send(_ context.Context, event stream.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *hintRecordingStreamSink) Close(context.Context) error {
	return nil
}

func TestScheduledCallHintRendersHintForNilAndEmptyPayload(t *testing.T) {
	toolID := tools.Ident("runtime.hints.test.empty_payload")
	rthints.RegisterCallHint(toolID, mustTemplate(t, toolID, "Checking active alarms"))

	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			toolID: newAnyJSONSpec(toolID),
		},
		logger: telemetry.NoopLogger{},
	}

	cases := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "nil payload",
			payload: nil,
		},
		{
			name:    "empty payload",
			payload: []byte{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := hooks.NewToolCallScheduledEvent("run-1", "svc.agent", "session-1", toolID, "call-1", tc.payload, "queue", "", 0)
			changed, err := rt.enrichToolCallScheduledHint(t.Context(), event)
			require.NoError(t, err)
			assert.True(t, changed)
			assert.Equal(t, "Checking active alarms", event.DisplayHint)
		})
	}
}

func TestAddToolsetLockedRegistersHints(t *testing.T) {
	toolID := tools.Ident("runtime.hints.test.canonical_registration")
	rt := New(newTestStore())
	registration := ToolsetRegistration{
		Name:  "runtime.hints.test",
		Specs: []tools.ToolSpec{newAnyJSONSpec(toolID)},
		CallHints: map[tools.Ident]*template.Template{
			toolID: mustTemplate(t, toolID, "Checking {{.target}}"),
		},
	}

	rt.mu.Lock()
	rt.addToolsetLocked(registration, mustToolDefinitions(registration.Specs))
	rt.mu.Unlock()

	hint, ok, err := rthints.RenderCallHint(toolID, map[string]any{
		"target": "registration",
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "Checking registration", hint)
}

func TestScheduledCallHintRendersHintForRawJSONPayload(t *testing.T) {
	toolID := tools.Ident("runtime.hints.test.rawjson")
	rthints.RegisterCallHint(toolID, mustTemplate(t, toolID, "Checking {{.Resolution}} energy rates"))

	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			toolID: newTypedHintSpec(toolID),
		},
		logger: telemetry.NoopLogger{},
	}

	event := hooks.NewToolCallScheduledEvent("run-1", "svc.agent", "session-1", toolID, "call-1", rawjson.Message(`{"resolution":"hourly"}`), "queue", "", 0)
	changed, err := rt.enrichToolCallScheduledHint(t.Context(), event)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "Checking hourly energy rates", event.DisplayHint)
}

func TestScheduledCallHintUsesToolTitleForMalformedPayload(t *testing.T) {
	toolID := tools.Ident("runtime.hints.test.malformed_payload")
	rthints.RegisterCallHint(toolID, mustTemplate(t, toolID, "Checking {{.Resolution}} energy rates"))

	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			toolID: newTypedHintSpec(toolID),
		},
		policyToolMetadata: map[tools.Ident]policy.ToolMetadata{
			toolID: {
				ID:    toolID,
				Title: "Check Energy Rates",
			},
		},
		logger: telemetry.NoopLogger{},
	}

	event := hooks.NewToolCallScheduledEvent("run-1", "svc.agent", "session-1", toolID, "call-1", rawjson.Message(`{"resolution":42}`), "queue", "", 0)
	changed, err := rt.enrichToolCallScheduledHint(t.Context(), event)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "Check Energy Rates", event.DisplayHint)
}

func TestScheduledCallHintRendersHintForToolUnavailable(t *testing.T) {
	rt := New(newTestStore())
	event := hooks.NewToolCallScheduledEvent("run-1", "svc.agent", "session-1", tools.ToolUnavailable, "call-1", rawjson.Message(`{"requested_tool":"catalog.resolve_sources"}`), "queue", "", 0)
	changed, err := rt.enrichToolCallScheduledHint(t.Context(), event)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "Tool not available: catalog.resolve_sources", event.DisplayHint)
}

func TestScheduledCallHintOverrideWins(t *testing.T) {
	toolID := tools.Ident("runtime.hints.test.override")
	rthints.RegisterCallHint(toolID, mustTemplate(t, toolID, "Checking {{.Resolution}} energy rates"))

	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			toolID: newTypedHintSpec(toolID),
		},
		logger: telemetry.NoopLogger{},
		hintOverrides: map[tools.Ident]HintOverrideFunc{
			toolID: func(ctx context.Context, tool tools.Ident, payload any) (string, bool) {
				return "Overridden hint", true
			},
		},
	}

	event := hooks.NewToolCallScheduledEvent("run-1", "svc.agent", "session-1", toolID, "call-1", rawjson.Message(`{"resolution":"hourly"}`), "queue", "", 0)
	changed, err := rt.enrichToolCallScheduledHint(t.Context(), event)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "Overridden hint", event.DisplayHint)
}

func TestScheduledCallHintRejectsEmptyOverride(t *testing.T) {
	toolID := tools.Ident("runtime.hints.test.empty_override")

	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			toolID: newTypedHintSpec(toolID),
		},
		policyToolMetadata: map[tools.Ident]policy.ToolMetadata{
			toolID: {
				ID:    toolID,
				Title: "Check Energy Rates",
			},
		},
		logger: telemetry.NoopLogger{},
		hintOverrides: map[tools.Ident]HintOverrideFunc{
			toolID: func(ctx context.Context, tool tools.Ident, payload any) (string, bool) {
				return "", true
			},
		},
	}

	event := hooks.NewToolCallScheduledEvent("run-1", "svc.agent", "session-1", toolID, "call-1", rawjson.Message(`{"resolution":"hourly"}`), "queue", "", 0)
	changed, err := rt.enrichToolCallScheduledHint(t.Context(), event)
	require.ErrorContains(t, err, "returned empty display hint")
	assert.False(t, changed)
}

func TestScheduledCallHintStoredAndStreamedOnce(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		name := "static"
		if dynamic {
			name = "registry"
		}
		t.Run(name, func(t *testing.T) {
			toolID := tools.Ident("runtime.hints.test.stored." + name)
			sink := &hintRecordingStreamSink{}
			overrideCalls := 0
			rt := New(newTestStore(), WithStream(sink, stream.AgentDebugProfile()),
				WithHintOverrides(map[tools.Ident]HintOverrideFunc{
					toolID: func(context.Context, tools.Ident, any) (string, bool) {
						overrideCalls++
						return "Stored override", true
					},
				}))
			rt.toolSpecs[toolID] = newTypedHintSpec(toolID)
			event := hooks.NewToolCallScheduledEvent("run", "svc.agent", "session", toolID,
				"call", rawjson.Message(`{"resolution":"hourly"}`), "queue", "", 0)
			want := "Stored override"
			if dynamic {
				resolved, err := registrycontract.Resolve(testRuntimeRegistryResolution(toolID.String(), strings.Repeat("a", 64)))
				require.NoError(t, err)
				event.Registry, err = resolved.Select("company", toolID)
				require.NoError(t, err)
				want = "Find records"
			}
			record, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{EventKey: "call", TimestampMS: 1})
			require.NoError(t, err)
			_, err = rt.recordResult(t.Context(), record)
			require.NoError(t, err)
			require.Len(t, sink.events, 1)
			start, ok := sink.events[0].(stream.ToolStart)
			require.True(t, ok)
			assert.Equal(t, want, start.Data.DisplayHint)
			assert.Equal(t, "call", start.EventKey())
			page, err := rt.Store.ListRunRecords(t.Context(), "run", "", 10)
			require.NoError(t, err)
			require.Len(t, page.Events, 2)
			var saved struct {
				DisplayHint string
			}
			require.NoError(t, json.Unmarshal(page.Events[1].Payload, &saved))
			assert.Equal(t, want, saved.DisplayHint)
			if dynamic {
				assert.Zero(t, overrideCalls)
			} else {
				assert.Equal(t, 1, overrideCalls)
			}
		})
	}
}

func newTypedHintSpec(name tools.Ident) tools.ToolSpec {
	codec := tools.JSONCodec[any]{
		ToJSON: json.Marshal,
		FromJSON: func(data []byte) (any, error) {
			var out struct {
				Resolution string `json:"resolution"`
			}
			if err := json.Unmarshal(data, &out); err != nil {
				return nil, err
			}
			return &out, nil
		},
	}
	return tools.ToolSpec{
		Name: name,
		Payload: tools.TypeSpec{
			Name:  string(name) + "_payload",
			Codec: codec,
		},
		ExecutionPayloadCodec: codec,
		Result: tools.TypeSpec{
			Name:  string(name) + "_result",
			Codec: codec,
		},
	}
}

func mustTemplate(t *testing.T, id tools.Ident, src string) *template.Template {
	t.Helper()

	tpl, err := template.New(string(id)).Option("missingkey=error").Parse(src)
	require.NoError(t, err)
	return tpl
}
