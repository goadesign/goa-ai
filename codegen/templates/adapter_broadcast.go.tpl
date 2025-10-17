{{ comment "Broadcaster and publish helpers for server-initiated events" }}

// Broadcaster defines a simple publish/subscribe API for server-initiated events.
type Broadcaster interface {
    Subscribe(ctx context.Context) (Subscription, error)
    Publish(ev *EventsStreamResult)
    Close() error
}

// Subscription represents a subscriber to broadcast events.
type Subscription interface {
    C() <-chan *EventsStreamResult
    Close() error
}

// channelBroadcaster is a default in-memory broadcaster.
type channelBroadcaster struct {
    mu    sync.RWMutex
    subs  map[chan *EventsStreamResult]struct{}
    buf   int
    drop  bool
    closed bool
}

func newChannelBroadcaster(buf int, drop bool) *channelBroadcaster {
    return &channelBroadcaster{subs: make(map[chan *EventsStreamResult]struct{}), buf: buf, drop: drop}
}

func (b *channelBroadcaster) Subscribe(ctx context.Context) (Subscription, error) {
    ch := make(chan *EventsStreamResult, b.buf)
    b.mu.Lock()
    if b.closed {
        b.mu.Unlock()
        close(ch)
        return &subscription{ch: ch, parent: b}, nil
    }
    b.subs[ch] = struct{}{}
    b.mu.Unlock()
    return &subscription{ch: ch, parent: b}, nil
}

func (b *channelBroadcaster) Publish(ev *EventsStreamResult) {
    if ev == nil { return }
    b.mu.RLock()
    for ch := range b.subs {
        if b.drop {
            select { case ch <- ev: default: }
        } else {
            ch <- ev
        }
    }
    b.mu.RUnlock()
}

func (b *channelBroadcaster) Close() error {
    b.mu.Lock()
    if b.closed { b.mu.Unlock(); return nil }
    b.closed = true
    for ch := range b.subs { close(ch); delete(b.subs, ch) }
    b.mu.Unlock()
    return nil
}

type subscription struct {
    ch     chan *EventsStreamResult
    parent *channelBroadcaster
    once   sync.Once
}

func (s *subscription) C() <-chan *EventsStreamResult { return s.ch }

func (s *subscription) Close() error {
    s.once.Do(func() {
        if s.parent != nil {
            s.parent.mu.Lock()
            delete(s.parent.subs, s.ch)
            s.parent.mu.Unlock()
        }
        close(s.ch)
    })
    return nil
}

// Publish sends an event to all event stream subscribers.
func (a *MCPAdapter) Publish(ev *EventsStreamResult) {
    if a == nil || a.broadcaster == nil {
        return
    }
    a.broadcaster.Publish(ev)
}

// PublishStatus is a convenience to publish a status_update message.
func (a *MCPAdapter) PublishStatus(ctx context.Context, typ string, message string, data any) {
    m := map[string]any{"type": typ, "message": message}
    if data != nil {
        m["data"] = data
    }
    s, err := encodeJSONToString(ctx, m)
    if err != nil {
        return
    }
    a.Publish(&EventsStreamResult{
        Content: []*ContentItem{ buildContentItem(a, s) },
    })
}


