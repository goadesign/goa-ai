package runtime

import (
	"bytes"
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	goahttp "goa.design/goa/v3/http"
)

type (
	// Notification describes a server-initiated status update that can be
	// broadcast to connected MCP clients via the Events stream. It carries a
	// machine-usable type, an optional human-readable message, and optional
	// structured data.
	Notification struct {
		Type    string  `json:"type"`
		Message *string `json:"message,omitempty"`
		Data    any     `json:"data,omitempty"`
	}

	// Broadcaster publishes server-initiated events to multiple subscribers.
	// Implementations must be safe for concurrent use.
	Broadcaster interface {
		Subscribe(ctx context.Context) (Subscription, error)
		Publish(ev any)
		Close() error
	}

	// Subscription exposes a channel of events and a Close method that removes the
	// subscriber from the broadcaster and closes the channel exactly once.
	Subscription interface {
		C() <-chan any
		Close() error
	}

	// Private types
	channelBroadcaster struct {
		mu     sync.RWMutex
		subs   map[chan any]struct{}
		buf    int
		drop   bool
		closed bool
	}

	subscription struct {
		ch     chan any
		parent *channelBroadcaster
		once   sync.Once
	}

	bufferResponseWriter struct {
		headers http.Header
		buf     bytes.Buffer
	}
)

// NewChannelBroadcaster constructs an in-memory Broadcaster backed by buffered
// channels. When drop is true, slow subscribers are skipped instead of blocking
// publishers (best effort). Use for single-process adapters.
func NewChannelBroadcaster(buf int, drop bool) Broadcaster {
	if buf < 0 {
		buf = 0
	}
	return &channelBroadcaster{subs: make(map[chan any]struct{}), buf: buf, drop: drop}
}

// EncodeJSONToString encodes v into JSON using the provided encoder factory.
// The factory should produce an Encoder bound to the given ResponseWriter.
func EncodeJSONToString(
	ctx context.Context,
	newEncoder func(context.Context, http.ResponseWriter) goahttp.Encoder,
	v any,
) (string, error) {
	bw := &bufferResponseWriter{}
	if err := newEncoder(ctx, bw).Encode(v); err != nil {
		return "", err
	}
	return bw.buf.String(), nil
}

// CoerceQuery converts a URL query map into a JSON-friendly object:
// - Repeated parameters become arrays preserving input order
// - "true"/"false" (case-insensitive) become booleans
// - RFC3339/RFC3339Nano values become time.Time
// - Numeric strings become int64 or float64 when obvious
// It does not coerce "0"/"1" to booleans.
func CoerceQuery(m map[string][]string) map[string]any {
	out := make(map[string]any, len(m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := m[k]
		if len(vals) == 1 {
			out[k] = coerce(vals[0])
			continue
		}
		arr := make([]any, len(vals))
		for i := range vals {
			arr[i] = coerce(vals[i])
		}
		out[k] = arr
	}
	return out
}

func coerce(s string) any {
	// Trim but preserve original if no coercion applies.
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	// Booleans: only true/false, case-insensitive.
	if strings.EqualFold(t, "true") {
		return true
	}
	if strings.EqualFold(t, "false") {
		return false
	}
	// RFC3339 timestamps.
	if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
		return ts
	}
	if ts, err := time.Parse(time.RFC3339, t); err == nil {
		return ts
	}
	// Numbers: prefer int if it looks integral; otherwise float.
	if looksIntegral(t) {
		if i, err := strconv.ParseInt(t, 10, 64); err == nil {
			return i
		}
	}
	if looksFloat(t) {
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f
		}
	}
	return s
}

func looksIntegral(s string) bool {
	if s == "" {
		return false
	}
	start := 0
	if s[0] == '-' {
		if len(s) == 1 {
			return false
		}
		start = 1
	}
	for i := start; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func looksFloat(s string) bool {
	// Heuristic: contains a dot or exponent. Delegate validation to ParseFloat.
	return strings.ContainsAny(s, ".eE")
}

func (b *channelBroadcaster) Subscribe(ctx context.Context) (Subscription, error) {
	ch := make(chan any, b.buf)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return &subscription{ch: ch, parent: nil}, nil
	}
	b.subs[ch] = struct{}{}
	s := &subscription{ch: ch, parent: b}
	b.mu.Unlock()
	// Auto-unsubscribe when the context is cancelled.
	if ctx.Done() != nil {
		go func() { <-ctx.Done(); _ = s.Close() }()
	}
	return s, nil
}

// Publish broadcasts the given event to all current subscribers.
// When drop is false, a slow subscriber will block publishing to all others.
// Prefer drop=true for best-effort server-initiated events.
func (b *channelBroadcaster) Publish(ev any) {
	if ev == nil {
		return
	}
	b.mu.RLock()
	for ch := range b.subs {
		if b.drop {
			select {
			case ch <- ev:
			default:
			}
		} else {
			ch <- ev
		}
	}
	b.mu.RUnlock()
}

func (b *channelBroadcaster) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	for ch := range b.subs {
		close(ch)
		delete(b.subs, ch)
	}
	b.mu.Unlock()
	return nil
}

func (s *subscription) C() <-chan any { return s.ch }

func (s *subscription) Close() error {
	s.once.Do(func() {
		if s.parent != nil {
			s.parent.mu.Lock()
			delete(s.parent.subs, s.ch)
			close(s.ch)
			s.parent.mu.Unlock()
		}
	})
	return nil
}

func (w *bufferResponseWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(http.Header)
	}
	return w.headers
}

// WriteHeader is a no-op because only the body is captured for encoding.
func (w *bufferResponseWriter) WriteHeader(statusCode int)  {}
func (w *bufferResponseWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
