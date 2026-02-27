package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type hintRecordingStreamSink struct {
	events []stream.Event
}

func (s *hintRecordingStreamSink) Send(ctx context.Context, event stream.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *hintRecordingStreamSink) Close(ctx context.Context) error {
	return nil
}

func TestHintingSinkRendersHintForNilAndEmptyPayload(t *testing.T) {
	toolID := tools.Ident("runtime.hints.test.empty_payload")
	rthints.RegisterCallHint(toolID, mustTemplate(t, toolID, "Checking active alarms"))

	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			toolID: newAnyJSONSpec(toolID, "test"),
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
			sink := &hintRecordingStreamSink{}
			decorated := newHintingSink(rt, sink)

			payload := stream.ToolStartPayload{
				ToolCallID: "call-1",
				ToolName:   string(toolID),
				Payload:    tc.payload,
			}
			ev := stream.ToolStart{
				Base: stream.NewBase(stream.EventToolStart, "run-1", "session-1", payload),
				Data: payload,
			}

			require.NoError(t, decorated.Send(context.Background(), ev))
			require.Len(t, sink.events, 1)

			out, ok := sink.events[0].(stream.ToolStart)
			require.True(t, ok)
			assert.Equal(t, "Checking active alarms", out.Data.DisplayHint)
		})
	}
}

func TestHintingSinkRendersHintForRawJSONPayload(t *testing.T) {
	toolID := tools.Ident("runtime.hints.test.rawjson")
	rthints.RegisterCallHint(toolID, mustTemplate(t, toolID, "Checking {{.Resolution}} energy rates"))

	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			toolID: newTypedHintSpec(toolID),
		},
		logger: telemetry.NoopLogger{},
	}

	sink := &hintRecordingStreamSink{}
	decorated := newHintingSink(rt, sink)
	payload := stream.ToolStartPayload{
		ToolCallID: "call-rawjson-1",
		ToolName:   string(toolID),
		Payload:    rawjson.RawJSON([]byte(`{"resolution":"hourly"}`)),
	}
	ev := stream.ToolStart{
		Base: stream.NewBase(stream.EventToolStart, "run-1", "session-1", payload),
		Data: payload,
	}

	require.NoError(t, decorated.Send(context.Background(), ev))
	require.Len(t, sink.events, 1)

	out, ok := sink.events[0].(stream.ToolStart)
	require.True(t, ok)
	assert.Equal(t, "Checking hourly energy rates", out.Data.DisplayHint)
}

func TestHintingSinkOverrideWins(t *testing.T) {
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

	sink := &hintRecordingStreamSink{}
	decorated := newHintingSink(rt, sink)
	payload := stream.ToolStartPayload{
		ToolCallID: "call-override-1",
		ToolName:   string(toolID),
		Payload:    rawjson.RawJSON([]byte(`{"resolution":"hourly"}`)),
	}
	ev := stream.ToolStart{
		Base: stream.NewBase(stream.EventToolStart, "run-1", "session-1", payload),
		Data: payload,
	}

	require.NoError(t, decorated.Send(context.Background(), ev))
	require.Len(t, sink.events, 1)

	out, ok := sink.events[0].(stream.ToolStart)
	require.True(t, ok)
	assert.Equal(t, "Overridden hint", out.Data.DisplayHint)
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
		Name:    name,
		Toolset: "runtime.hints",
		Payload: tools.TypeSpec{
			Name:  string(name) + "_payload",
			Codec: codec,
		},
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
