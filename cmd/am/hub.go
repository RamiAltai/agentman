package main

import "sync"

// Hub fans out committed events to connected SSE subscribers (dashboards).
// Delivery is best-effort: a stalled client gets events dropped rather than
// blocking the hub — clients recover missed events via /api/events?since=.
type Hub struct {
	mu   sync.RWMutex
	subs map[*subscriber]struct{}
}

type subscriber struct {
	ch        chan *Event
	projectID int64 // 0 = all projects
}

func NewHub() *Hub {
	return &Hub{subs: make(map[*subscriber]struct{})}
}

func (h *Hub) Subscribe(projectID int64) *subscriber {
	s := &subscriber{ch: make(chan *Event, 64), projectID: projectID}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *Hub) Unsubscribe(s *subscriber) {
	h.mu.Lock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.ch)
	}
	h.mu.Unlock()
}

// Broadcast must be called AFTER the originating transaction commits.
func (h *Hub) Broadcast(e *Event) {
	if e == nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs {
		// project.created reaches every subscriber so new tabs appear live even
		// when a viewer is filtered to a single project.
		if s.projectID != 0 && s.projectID != e.ProjectID && e.Kind != "project.created" {
			continue
		}
		select {
		case s.ch <- e:
		default: // slow/dead client: drop, don't block other subscribers
		}
	}
}
