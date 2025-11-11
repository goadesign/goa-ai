package runtime

import "testing"

func TestRootRunID(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := RootRunID(""); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})
	t.Run("top-level", func(t *testing.T) {
		in := "chat-run-123"
		if got := RootRunID(in); got != in {
			t.Fatalf("expected %q, got %q", in, got)
		}
	})
	t.Run("single-nested", func(t *testing.T) {
		in := "chat-run-123/agent/atlas_data_agent.ada"
		want := "chat-run-123"
		if got := RootRunID(in); got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	})
	t.Run("multi-nested", func(t *testing.T) {
		in := "A/agent/B/agent/C"
		want := "A"
		if got := RootRunID(in); got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	})
}
