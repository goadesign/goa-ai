package temporaltrace

import (
	"testing"
)

func TestParseTraceParent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		traceparent string
		wantErr     bool
	}{
		{
			name:        "valid",
			traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		},
		{
			name:        "invalid_empty",
			traceparent: "",
			wantErr:     true,
		},
		{
			name:        "invalid_version_ff",
			traceparent: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			wantErr:     true,
		},
		{
			name:        "invalid_trace_id_length",
			traceparent: "00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01",
			wantErr:     true,
		},
		{
			name:        "invalid_span_id_length",
			traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902-01",
			wantErr:     true,
		},
		{
			name:        "invalid_flags_length",
			traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0",
			wantErr:     true,
		},
		{
			name:        "invalid_hex",
			traceparent: "00-4bf92f3577b34da6a3ce929d0e0e473z-00f067aa0ba902b7-01",
			wantErr:     true,
		},
		{
			name:        "invalid_shape_version_00_extra_parts",
			traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra",
			wantErr:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sc, err := ParseTraceParent(tc.traceparent)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (spanContext=%v)", sc)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if !sc.IsValid() {
				t.Fatalf("expected valid span context")
			}
			if !sc.IsRemote() {
				t.Fatalf("expected remote span context")
			}
		})
	}
}
