package main

import "sync"

// Hub fans out committed events to connected SSE subscribers (dashboards).
// Delivery never blocks the hub: when a stalled client's buffer is full the
// event is dropped and the subscriber is marked lagged, so handleStream ends
// the connection and the client's reconnect (Last-Event-ID / ?since=) replays
// the gap from the durable events table.
type Hub struct {
	mu   sync.RWMutex
	subs map[*subscriber]struct{}
}

type subscriber struct {
	ch         chan *Event
	projectID  int64          // 0 = all projects
	categoryID int64          // 0 = not category-scoped
	projectIDs map[int64]bool // category's project-id set, resolved once at Subscribe
	lagged     chan struct{}  // closed once, on the first dropped event
	lagOnce    sync.Once
}

// subFilter is the scope a subscriber wants. projectID narrows to one project;
// categoryID (with projectIDs, the category's project-id set resolved once by the
// caller) narrows to a category. The set is captured at Subscribe time so
// Broadcast does a pure in-memory membership check — no per-event DB hits.
type subFilter struct {
	projectID  int64
	categoryID int64
	projectIDs map[int64]bool
}

func NewHub() *Hub {
	return &Hub{subs: make(map[*subscriber]struct{})}
}

func (h *Hub) Subscribe(f subFilter) *subscriber {
	s := &subscriber{
		ch:         make(chan *Event, 64),
		projectID:  f.projectID,
		categoryID: f.categoryID,
		projectIDs: f.projectIDs,
		lagged:     make(chan struct{}),
	}
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
		// when a viewer is filtered to a single project or category.
		if e.Kind != "project.created" {
			if s.projectID != 0 && s.projectID != e.ProjectID {
				continue
			}
			// Category scope: deliver only events whose project is in the
			// category's set (resolved at Subscribe). Cross-category and
			// category-level (ProjectID==0) events are dropped — they belong to
			// the All/overview feed. A project created AFTER this subscription
			// opened won't be in projectIDs; that post-open staleness window is
			// acceptable (the dashboard re-opens the stream on view change, and
			// the REST snapshot is the source of truth).
			if s.categoryID != 0 && (e.ProjectID == 0 || !s.projectIDs[e.ProjectID]) {
				continue
			}
		}
		select {
		case s.ch <- e:
		default: // slow/dead client: drop, don't block other subscribers, and
			// flag it so its stream reconnects and replays the dropped event.
			s.lagOnce.Do(func() { close(s.lagged) })
		}
	}
}
