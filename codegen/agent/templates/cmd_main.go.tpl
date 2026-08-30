{{/* cmd_main.go.tpl - generates cmd/<service>/main.go for runnable example services */}}
// This example main demonstrates running the agent using the generated bootstrap.
// Replace or extend this as needed for your production deployment.
{{- if .Completions }}

// exampleCompletionProvider is a tiny structured-output model provider used by the
// generated example main to demonstrate typed completion helpers without
// requiring a real provider integration.
type exampleCompletionProvider struct {
	name    string
	payload []byte
}

// exampleCompletionStreamer emits one preview delta plus one canonical
// completion payload so the generated example exercises both streaming helper
// paths and final-chunk decoding.
type exampleCompletionStreamer struct {
	name    string
	payload []byte
	step    int
}

// newExampleCompletionClient constructs the example structured-output client
// from the generated completion example payload.
func newExampleCompletionClient(name string, payload []byte) ({{ $.ModelAlias }}.Client, error) {
	if len(payload) == 0 {
		return nil, {{ $.FmtAlias }}.Errorf("completion %q example JSON is required for generated example", name)
	}
	return {{ $.ModelAlias }}.NewClient(&exampleCompletionProvider{
		name:    name,
		payload: append([]byte(nil), payload...),
	})
}

// validateRequest enforces the structured-output contract expected by the
// generated completion helpers.
func (c *exampleCompletionProvider) validateRequest(req *{{ $.ModelAlias }}.Request, stream bool) error {
	if req == nil {
		return {{ $.FmtAlias }}.Errorf("completion %q request is nil", c.name)
	}
	if req.Stream != stream {
		return {{ $.FmtAlias }}.Errorf("completion %q stream=%t; expected %t", c.name, req.Stream, stream)
	}
	if req.StructuredOutput == nil {
		return {{ $.FmtAlias }}.Errorf("completion %q requires structured output", c.name)
	}
	if req.StructuredOutput.Name != c.name {
		return {{ $.FmtAlias }}.Errorf("completion %q received structured output %q", c.name, req.StructuredOutput.Name)
	}
	if len(req.StructuredOutput.Schema) == 0 {
		return {{ $.FmtAlias }}.Errorf("completion %q requires a structured output schema", c.name)
	}
	return nil
}

// Complete returns the canonical assistant JSON payload for unary completion
// examples.
func (c *exampleCompletionProvider) Complete(_ {{ $.ContextAlias }}.Context, req *{{ $.ModelAlias }}.Request) (*{{ $.ModelAlias }}.Response, error) {
	if err := c.validateRequest(req, false); err != nil {
		return nil, err
	}
	return &{{ $.ModelAlias }}.Response{
		Content: []{{ $.ModelAlias }}.Message{
			{
				Role: {{ $.ModelAlias }}.ConversationRoleAssistant,
				Parts: []{{ $.ModelAlias }}.Part{
					{{ $.ModelAlias }}.TextPart{Text: string(c.payload)},
				},
			},
		},
		StopReason: "stop",
	}, nil
}

// Stream emits a preview fragment followed by the canonical completion payload.
func (c *exampleCompletionProvider) Stream(_ {{ $.ContextAlias }}.Context, req *{{ $.ModelAlias }}.Request) ({{ $.ModelAlias }}.Streamer, error) {
	if err := c.validateRequest(req, true); err != nil {
		return nil, err
	}
	return &exampleCompletionStreamer{
		name:    c.name,
		payload: append([]byte(nil), c.payload...),
	}, nil
}

// Recv advances the example stream through preview, completion, and stop.
func (s *exampleCompletionStreamer) Recv() ({{ $.ModelAlias }}.Chunk, error) {
	switch s.step {
	case 0:
		s.step++
		end := min(len(s.payload), 24)
		return {{ $.ModelAlias }}.CompletionDeltaChunk{
			Delta: {{ $.ModelAlias }}.CompletionDelta{
				Name:  s.name,
				Delta: string(s.payload[:end]),
			},
		}, nil
	case 1:
		s.step++
		completion := {{ $.ModelAlias }}.Completion{
			Name:    s.name,
			Payload: {{ $.RawJSONAlias }}.Message(append([]byte(nil), s.payload...)),
		}
		return {{ $.ModelAlias }}.CompletionChunk{Completion: completion}, nil
	case 2:
		s.step++
		return {{ $.ModelAlias }}.StopChunk{Reason: "completed"}, nil
	default:
		return nil, {{ $.IOAlias }}.EOF
	}
}

// Response returns the canonical example response after clean EOF.
func (s *exampleCompletionStreamer) Response() *{{ $.ModelAlias }}.Response {
	if s.step < 3 {
		return nil
	}
	return &{{ $.ModelAlias }}.Response{
		Content: []{{ $.ModelAlias }}.Message{
			{
				Role: {{ $.ModelAlias }}.ConversationRoleAssistant,
				Parts: []{{ $.ModelAlias }}.Part{
					{{ $.ModelAlias }}.TextPart{Text: string(s.payload)},
				},
			},
		},
		StopReason: "completed",
	}
}

// Close releases example streamer resources.
func (s *exampleCompletionStreamer) Close() error {
	return nil
}

{{- end }}

func main() {
	ctx := {{ $.ContextAlias }}.Background()
	store := {{ $.StorageAlias }}.New()
	if _, err := store.CreateSession(ctx, "demo-session", {{ $.TimeAlias }}.Now().UTC()); err != nil {
		{{ $.LogAlias }}.Fatalf("failed to create session: %v", err)
	}

	// Initialize the runtime using the generated bootstrap which wires
	// planners and toolsets for all agents.
	rt, cleanup, err := {{ $.BootstrapAlias }}.New(ctx, store)
	if err != nil {
		{{ $.LogAlias }}.Fatalf("failed to initialize runtime: %v", err)
	}
	defer cleanup()

	// Example: run the first registered agent with a simple message.
	// Replace this with your own CLI, HTTP server, or integration.
{{ range .Agents }}
	{
		client := {{ .Alias }}.NewClient(rt)
		out, err := client.Run(ctx, "demo-session", []*{{ $.ModelAlias }}.Message{
			{
				Role:  {{ $.ModelAlias }}.ConversationRoleUser,
				Parts: []{{ $.ModelAlias }}.Part{ {{ $.ModelAlias }}.TextPart{Text: "What is the capital of Japan?"}},
			},
		}, {{ $.RuntimeAlias }}.WithRunID("demo-{{ .Name }}-run"))
		if err != nil {
			{{ $.LogAlias }}.Fatalf("agent run failed: %v", err)
		}
		{{ $.FmtAlias }}.Println("RunID:", out.RunID)
		if out.Final != nil && len(out.Final.Parts) > 0 {
			if tp, ok := out.Final.Parts[0].({{ $.ModelAlias }}.TextPart); ok {
				{{ $.FmtAlias }}.Println("Assistant:", tp.Text)
			}
		}
	}
{{ end }}
{{- range .Completions }}

	{
		client, err := newExampleCompletionClient(
			string({{ $.CompletionsAlias }}.{{ .ConstName }}),
			{{ $.CompletionsAlias }}.{{ .ExampleFunc }}(),
		)
		if err != nil {
			{{ $.LogAlias }}.Fatalf("completion client setup failed: %v", err)
		}
		out, err := {{ $.CompletionsAlias }}.{{ .CompleteFunc }}(ctx, client, &{{ $.ModelAlias }}.Request{
			Messages: []*{{ $.ModelAlias }}.Message{
				{
					Role:  {{ $.ModelAlias }}.ConversationRoleUser,
					Parts: []{{ $.ModelAlias }}.Part{ {{ $.ModelAlias }}.TextPart{Text: "Draft a task for preparing a launch checklist."}},
				},
			},
		})
		if err != nil {
			{{ $.LogAlias }}.Fatalf("completion run failed: %v", err)
		}
		{{ $.FmtAlias }}.Printf("Completion %s: %+v\n", {{ $.CompletionsAlias }}.{{ .ConstName }}, out.Value)
	}

	{
		client, err := newExampleCompletionClient(
			string({{ $.CompletionsAlias }}.{{ .ConstName }}),
			{{ $.CompletionsAlias }}.{{ .ExampleFunc }}(),
		)
		if err != nil {
			{{ $.LogAlias }}.Fatalf("completion stream client setup failed: %v", err)
		}
		stream, err := {{ $.CompletionsAlias }}.{{ .StreamFunc }}(ctx, client, &{{ $.ModelAlias }}.Request{
			Messages: []*{{ $.ModelAlias }}.Message{
				{
					Role:  {{ $.ModelAlias }}.ConversationRoleUser,
					Parts: []{{ $.ModelAlias }}.Part{ {{ $.ModelAlias }}.TextPart{Text: "Draft a task for preparing a launch checklist."}},
				},
			},
		})
		if err != nil {
			{{ $.LogAlias }}.Fatalf("completion stream failed to start: %v", err)
		}
		for {
			chunk, err := stream.Recv()
			if err == {{ $.IOAlias }}.EOF {
				break
			}
			if err != nil {
				{{ $.LogAlias }}.Fatalf("completion stream failed: %v", err)
			}
			if delta, ok := chunk.({{ $.ModelAlias }}.CompletionDeltaChunk); ok {
				{{ $.FmtAlias }}.Printf("Completion delta %s: %s\n", {{ $.CompletionsAlias }}.{{ .ConstName }}, delta.Delta.Delta)
			}
		}
		value, ok := stream.Value()
		if !ok {
			{{ $.LogAlias }}.Fatal("completion stream ended without a typed value")
		}
		{{ $.FmtAlias }}.Printf("Completion stream %s: %+v\n", {{ $.CompletionsAlias }}.{{ .ConstName }}, value)
		if err := stream.Close(); err != nil {
			{{ $.LogAlias }}.Fatalf("completion stream close failed: %v", err)
		}
	}
{{- end }}
}
