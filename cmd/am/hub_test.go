package main

import "testing"

// recv drains one event from the subscriber without blocking; ok is false when
// no event is buffered. Broadcast is synchronous (RLock + non-blocking send), so
// by the time it returns the event is already in the channel buffer.
func recv(s *subscriber) (*Event, bool) {
	select {
	case e := <-s.ch:
		return e, true
	default:
		return nil, false
	}
}

func TestHubCategoryScopedBroadcast(t *testing.T) {
	h := NewHub()
	// Subscriber scoped to category 1, whose projects are {10, 11}.
	sub := h.Subscribe(subFilter{categoryID: 1, projectIDs: map[int64]bool{10: true, 11: true}})
	defer h.Unsubscribe(sub)

	// In-category event is delivered.
	h.Broadcast(&Event{ID: 1, ProjectID: 10, Kind: "task.created"})
	if e, ok := recv(sub); !ok || e.ID != 1 {
		t.Fatalf("in-category event not delivered: ok=%v e=%v", ok, e)
	}

	// Out-of-category event is dropped.
	h.Broadcast(&Event{ID: 2, ProjectID: 99, Kind: "task.created"})
	if e, ok := recv(sub); ok {
		t.Fatalf("out-of-category event delivered: %v", e)
	}

	// Category-level event (NULL project → ProjectID 0) is dropped — it belongs
	// to the All/overview feed, not a category drill-down.
	h.Broadcast(&Event{ID: 3, ProjectID: 0, Kind: "category.archived"})
	if e, ok := recv(sub); ok {
		t.Fatalf("category-level event delivered to category-scoped sub: %v", e)
	}

	// project.created reaches the subscriber regardless of category (carve-out),
	// so a brand-new project's tab can appear live.
	h.Broadcast(&Event{ID: 4, ProjectID: 0, Kind: "project.created"})
	if e, ok := recv(sub); !ok || e.Kind != "project.created" {
		t.Fatalf("project.created not delivered to category-scoped sub: ok=%v e=%v", ok, e)
	}
}

func TestHubProjectScopedBroadcast(t *testing.T) {
	h := NewHub()
	sub := h.Subscribe(subFilter{projectID: 7})
	defer h.Unsubscribe(sub)

	h.Broadcast(&Event{ID: 1, ProjectID: 7, Kind: "task.created"})
	if e, ok := recv(sub); !ok || e.ProjectID != 7 {
		t.Fatalf("in-project event not delivered: ok=%v e=%v", ok, e)
	}
	h.Broadcast(&Event{ID: 2, ProjectID: 8, Kind: "task.created"})
	if _, ok := recv(sub); ok {
		t.Fatalf("out-of-project event delivered")
	}
	// project.created carve-out reaches a project-scoped subscriber too.
	h.Broadcast(&Event{ID: 3, ProjectID: 8, Kind: "project.created"})
	if e, ok := recv(sub); !ok || e.Kind != "project.created" {
		t.Fatalf("project.created not delivered to project-scoped sub: ok=%v e=%v", ok, e)
	}
}

func TestHubUnscopedBroadcast(t *testing.T) {
	h := NewHub()
	sub := h.Subscribe(subFilter{}) // no scope: sees everything
	defer h.Unsubscribe(sub)

	for _, e := range []*Event{
		{ID: 1, ProjectID: 5, Kind: "task.created"},
		{ID: 2, ProjectID: 0, Kind: "category.created"},
	} {
		h.Broadcast(e)
		if _, ok := recv(sub); !ok {
			t.Fatalf("unscoped sub missed event id=%d", e.ID)
		}
	}
}

func TestHubBroadcastNilNoPanic(t *testing.T) {
	h := NewHub()
	sub := h.Subscribe(subFilter{})
	defer h.Unsubscribe(sub)
	h.Broadcast(nil) // must not panic
	if _, ok := recv(sub); ok {
		t.Fatalf("nil broadcast delivered an event")
	}
}

// TestHubOverflowMarksLagged: an event dropped on a full buffer flags the
// subscriber instead of vanishing silently, so its stream can reconnect and
// replay the gap from the events table.
func TestHubOverflowMarksLagged(t *testing.T) {
	h := NewHub()
	sub := h.Subscribe(subFilter{})
	defer h.Unsubscribe(sub)
	for i := 1; i <= cap(sub.ch); i++ {
		h.Broadcast(&Event{ID: int64(i), ProjectID: 1, Kind: "task.created"})
	}
	select {
	case <-sub.lagged:
		t.Fatal("lagged before any event was dropped")
	default:
	}
	h.Broadcast(&Event{ID: int64(cap(sub.ch) + 1), ProjectID: 1, Kind: "task.created"})
	h.Broadcast(&Event{ID: int64(cap(sub.ch) + 2), ProjectID: 1, Kind: "task.created"}) // second drop: no double close
	select {
	case <-sub.lagged:
	default:
		t.Fatal("overflow dropped an event without marking the subscriber lagged")
	}
}
