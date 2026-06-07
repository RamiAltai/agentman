package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sseEvent holds the parsed fields of a single SSE event.
type sseEvent struct {
	id   int64
	data string
	kind string // decoded from data JSON "kind" field
}

// readSSEEvents reads lines from br until it accumulates count events with a
// non-empty "data:" line, or until the context is done (in which case the
// test fails). Each SSE event is terminated by a blank line.
func readSSEUntil(t *testing.T, ctx context.Context, br *bufio.Reader, until func(sseEvent) bool) sseEvent {
	t.Helper()
	var curID int64
	var curData string
	done := make(chan sseEvent, 1)

	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "id: "):
				curID, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			case strings.HasPrefix(line, "data: "):
				curData = strings.TrimPrefix(line, "data: ")
			case line == "":
				// Blank line = end of event.
				if curData != "" {
					var parsed struct {
						Kind string `json:"kind"`
					}
					json.Unmarshal([]byte(curData), &parsed)
					ev := sseEvent{id: curID, data: curData, kind: parsed.Kind}
					if until(ev) {
						done <- ev
						return
					}
				}
				curID = 0
				curData = ""
			}
		}
	}()

	select {
	case ev := <-done:
		return ev
	case <-ctx.Done():
		t.Fatalf("readSSEUntil: context done before condition met: %v", ctx.Err())
		return sseEvent{}
	}
}

// waitForRetry reads SSE lines until a "retry:" line is seen, proving the
// subscription is live. Returns without error on success, fatals on timeout.
func waitForRetry(t *testing.T, ctx context.Context, br *bufio.Reader) {
	t.Helper()
	done := make(chan struct{}, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(strings.TrimRight(line, "\r\n"), "retry:") {
				done <- struct{}{}
				return
			}
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("waitForRetry: timeout before retry: line seen: %v", ctx.Err())
	}
}

func TestSSEDeliversLiveEvent(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "sseproj")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	br := bufio.NewReader(resp.Body)
	waitForRetry(t, ctx, br)

	// Now create a task — this should produce a task.created event.
	mustCreateTask(t, ts, "sseproj", "SSE Live Task")

	ev := readSSEUntil(t, ctx, br, func(e sseEvent) bool {
		return e.kind == "task.created"
	})
	if ev.kind != "task.created" {
		t.Fatalf("expected task.created event, got kind=%q", ev.kind)
	}
}

func TestSSEReplayOnReconnect(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "replayproj")

	// --- First connection ---
	ctx1, cancel1 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel1()

	req1, err := http.NewRequestWithContext(ctx1, http.MethodGet, ts.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp1, err := ts.Client().Do(req1)
	if err != nil {
		t.Fatalf("GET /api/stream: %v", err)
	}

	br1 := bufio.NewReader(resp1.Body)
	waitForRetry(t, ctx1, br1)

	// Create a task and capture its event id.
	mustCreateTask(t, ts, "replayproj", "Task One")
	firstEv := readSSEUntil(t, ctx1, br1, func(e sseEvent) bool {
		return e.kind == "task.created"
	})
	firstID := firstEv.id

	// Close the first stream.
	cancel1()
	resp1.Body.Close()

	// While disconnected, create two more tasks.
	mustCreateTask(t, ts, "replayproj", "Task Two")
	mustCreateTask(t, ts, "replayproj", "Task Three")

	// --- Reconnect with Last-Event-ID ---
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	req2, err := http.NewRequestWithContext(ctx2, http.MethodGet, ts.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest reconnect: %v", err)
	}
	req2.Header.Set("Last-Event-ID", strconv.FormatInt(firstID, 10))

	resp2, err := ts.Client().Do(req2)
	if err != nil {
		t.Fatalf("GET /api/stream reconnect: %v", err)
	}
	defer resp2.Body.Close()

	br2 := bufio.NewReader(resp2.Body)

	// Collect replayed events (we expect exactly 2: Task Two and Task Three).
	// Gather them via their ids.
	replayed := make(map[int64]bool)
	for i := 0; i < 2; i++ {
		ev := readSSEUntil(t, ctx2, br2, func(e sseEvent) bool {
			return e.kind == "task.created" && e.id > firstID
		})
		if ev.id <= firstID {
			t.Fatalf("replayed event id %d is not > firstID %d", ev.id, firstID)
		}
		replayed[ev.id] = true
	}

	if len(replayed) != 2 {
		t.Fatalf("expected 2 distinct replayed events, got %d", len(replayed))
	}

	// None of the replayed ids should be firstID (dedupe).
	if replayed[firstID] {
		t.Fatalf("first event id %d was re-sent on reconnect (no dedupe)", firstID)
	}

	cancel2()

	// All replayed ids must be strictly greater than firstID.
	for id := range replayed {
		if id <= firstID {
			t.Errorf("replayed id %d is not > firstID %d", id, firstID)
		}
	}
}
