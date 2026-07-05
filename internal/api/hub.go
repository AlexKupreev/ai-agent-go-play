package api

import (
	"sync"

	"ai-agent-go-play/internal/agent"
)

// Hub is a per-run event fan-out. It implements agent.Observer, so the engine can
// attach it to a run; every emitted event is recorded and broadcast to all current
// subscribers. New subscribers replay the history first, then receive live events,
// so a client that connects after the run starts (or after it finishes) still sees
// the full stream — runs are short and we favor completeness over backpressure.
//
// Hub is transport-neutral: SSE and any future adapter subscribe the same way.
type Hub struct {
	mu      sync.Mutex
	history []Event
	subs    map[chan Event]struct{}
	closed  bool
}

func newHub() *Hub {
	return &Hub{subs: make(map[chan Event]struct{})}
}

// Emit implements agent.Observer: record the event and push it to live subscribers.
// Internal events (background deliberation — the chat planner's planner/critic steps) are
// dropped from the wire: they stay in the on-disk transcript (a separate logger observer)
// for debugging, but a client never sees the raw plan/verdict machinery. The rendered brief
// is surfaced separately as a first-class KindBrief event (see cmd's deliberate turn runner).
func (h *Hub) Emit(e agent.Event) {
	if e.Internal {
		return
	}
	h.publish(fromAgentEvent(e))
}

// publish appends to history and broadcasts. Sends are buffered (channels are sized
// generously) and non-blocking so a slow subscriber can never stall the run loop.
func (h *Hub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.history = append(h.history, e)
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe returns a channel of events: the full history so far, then live events
// until the hub closes (at which point the channel is closed). cancel detaches the
// subscriber early. The returned channel is buffered to hold the replayed history
// plus headroom for live events.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan Event, len(h.history)+32)
	for _, e := range h.history {
		ch <- e
	}
	if h.closed {
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if _, ok := h.subs[ch]; ok {
				delete(h.subs, ch)
				close(ch)
			}
		})
	}
	return ch, cancel
}

// Close ends the run's stream: no further events are accepted and every live
// subscriber channel is closed so readers terminate.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subs {
		close(ch)
	}
	h.subs = nil
}
