package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// sseEvent holds the parsed fields of a single SSE event.
type sseEvent struct {
	id   int64
	data string
	kind string // decoded from data JSON "kind" field
}

// readSSEUntil reads events (blank-line terminated, with a non-empty "data:"
// line) from br until until(ev) returns true, and returns that event. Events
// that do not match are consumed and discarded, so a later call on the same
// reader cannot see them. Fails the test when ctx is done first.
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

// dialStream opens an SSE connection to path with the extra request headers
// hdr and fails the test unless it answers 200. It does not consume the
// retry: preamble; reconnect tests read replayed frames straight away.
func dialStream(t *testing.T, ctx context.Context, ts *httptest.Server, path string, hdr map[string]string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest %s: %v", path, err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("stream %s status = %d, want 200", path, resp.StatusCode)
	}
	return resp, bufio.NewReader(resp.Body)
}

// openStream opens an SSE connection and waits for the retry: preamble, proving
// the subscription is live before the caller triggers events.
func openStream(t *testing.T, ctx context.Context, ts *httptest.Server, path string) (*http.Response, *bufio.Reader) {
	t.Helper()
	resp, br := dialStream(t, ctx, ts, path, nil)
	waitForRetry(t, ctx, br)
	return resp, br
}

func TestSSEDeliversLiveEvent(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "sseproj")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, br := openStream(t, ctx, ts, "/api/stream")
	defer resp.Body.Close()

	// Now create a task — this should produce a task.created event.
	mustCreateTask(t, ts, "sseproj", "SSE Live Task")

	// Without a cursor the stream replays the log from the start, which here is
	// only sseproj's project.created; the next frame must be the live event.
	ev := readSSEUntil(t, ctx, br, func(e sseEvent) bool { return e.kind != "project.created" })
	if ev.kind != "task.created" || ev.id <= 0 || !strings.Contains(ev.data, "SSE Live Task") {
		t.Fatalf("first frame = id %d %q %s, want the SSE Live Task task.created", ev.id, ev.kind, ev.data)
	}
}

func TestSSEReplayOnReconnect(t *testing.T) {
	ts, store := newTestServerWithStore(t)
	mustCreateProject(t, ts, "replayproj")

	// --- First connection ---
	ctx1, cancel1 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel1()

	resp1, br1 := openStream(t, ctx1, ts, "/api/stream")

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

	resp2, br2 := dialStream(t, ctx2, ts, "/api/stream",
		map[string]string{"Last-Event-ID": strconv.FormatInt(firstID, 10)})
	defer resp2.Body.Close()

	// Every frame up to the newest event, unfiltered: exactly Task Two and
	// Task Three, in order — a re-sent firstID or any extra frame fails.
	maxID, err := store.MaxEventID()
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}
	ids := cwfStoreCountUntil(t, ctx2, br2, maxID)
	if len(ids) != 2 || ids[0] <= firstID || ids[1] != maxID {
		t.Fatalf("replayed ids %v after cursor %d, want the 2 gap events ending at %d", ids, firstID, maxID)
	}

	// Nothing else trails the replay: the next frame is the next live event.
	mustCreateTask(t, ts, "replayproj", "Task Four")
	if ev := readSSEUntil(t, ctx2, br2, func(sseEvent) bool { return true }); ev.id != maxID+1 || ev.kind != "task.created" {
		t.Fatalf("first frame after replay = id %d %q, want the live task.created %d", ev.id, ev.kind, maxID+1)
	}
}

// TestSSECategoryScopedStream verifies /api/stream?category= delivers only the
// scoped category's project events, lets the project.created carve-out through
// regardless of category, and keeps a second subscriber on the other category
// isolated.
func TestSSECategoryScopedStream(t *testing.T) {
	ts := newTestServer(t)
	mustCreateCategory(t, ts, "acat")
	mustCreateCategory(t, ts, "bcat")
	mustCreateProjectIn(t, ts, "aproj", "acat")
	mustCreateProjectIn(t, ts, "bproj", "bcat")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Subscriber A scoped to acat; subscriber B scoped to bcat.
	respA, brA := openStream(t, ctx, ts, "/api/stream?category=acat")
	defer respA.Body.Close()
	respB, brB := openStream(t, ctx, ts, "/api/stream?category=bcat")
	defer respB.Body.Close()

	// A task in bproj must NOT reach A, but a task in aproj must; the second
	// bproj task, created after A's, makes B's side discriminating too.
	mustCreateTask(t, ts, "bproj", "B Task")
	mustCreateTask(t, ts, "aproj", "A Task")
	mustCreateTask(t, ts, "bproj", "B Task 2")

	// A should see the aproj task.created (and never the bproj one — the bproj
	// event has a lower id, so if A wrongly delivered it, this read would catch
	// the wrong title first).
	ev := readSSEUntil(t, ctx, brA, func(e sseEvent) bool { return e.kind == "task.created" })
	if !strings.Contains(ev.data, "A Task") {
		t.Fatalf("subscriber A first task.created = %q, want the aproj task", ev.data)
	}

	// B should see its own two tasks back to back, never A's (which has an id
	// between them, so a leak would arrive second).
	for _, want := range []string{`"B Task"`, `"B Task 2"`} {
		evB := readSSEUntil(t, ctx, brB, func(e sseEvent) bool { return e.kind == "task.created" })
		if !strings.Contains(evB.data, want) {
			t.Fatalf("subscriber B task.created = %q, want %s", evB.data, want)
		}
	}

	// project.created carve-out: a new project in bcat still reaches A (so a new
	// tab can appear live even on a category-scoped dashboard). Wait for
	// bproj2's event specifically; A's own aproj2 arrives first.
	mustCreateProjectIn(t, ts, "aproj2", "acat")
	mustCreateProjectIn(t, ts, "bproj2", "bcat")
	readSSEUntil(t, ctx, brA, func(e sseEvent) bool {
		return e.kind == "project.created" && strings.Contains(e.data, `"slug":"bproj2"`)
	})
}

// TestSSECategoryReconnectReplay verifies a category-scoped reconnect replays
// only that category's gap events (not the other category's).
func TestSSECategoryReconnectReplay(t *testing.T) {
	ts, store := newTestServerWithStore(t)
	mustCreateCategory(t, ts, "acat")
	mustCreateCategory(t, ts, "bcat")
	mustCreateProjectIn(t, ts, "aproj", "acat")
	mustCreateProjectIn(t, ts, "bproj", "bcat")

	// Capture a cursor, then create one task in each category during the "gap".
	maxBefore, err := store.MaxEventID()
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}
	mustCreateTask(t, ts, "bproj", "B Gap")
	mustCreateTask(t, ts, "aproj", "A Gap")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, br := dialStream(t, ctx, ts, "/api/stream?category=acat&since="+strconv.FormatInt(maxBefore, 10), nil)
	defer resp.Body.Close()

	// The first replayed task.created must be A's "A Gap" (B's "B Gap" has a lower
	// event id, so if it were wrongly replayed it would arrive first).
	ev := readSSEUntil(t, ctx, br, func(e sseEvent) bool { return e.kind == "task.created" })
	if !strings.Contains(ev.data, "A Gap") {
		t.Fatalf("first replayed task.created = %q, want the aproj task (bproj must be excluded)", ev.data)
	}
}

// TestSSEStaleCursorResets: a Last-Event-ID beyond the log's end (DB replaced
// by import, or emptied by prune, while a client kept its cursor) must not
// dedupe away the live events that follow.
func TestSSEStaleCursorResets(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "stale")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, br := dialStream(t, ctx, ts, "/api/stream", map[string]string{"Last-Event-ID": "999999"})
	defer resp.Body.Close()
	waitForRetry(t, ctx, br)
	// The stale cursor is announced (so the dashboard reloads) instead of
	// replaying the whole log.
	for _, want := range []string{"", "event: reset", "data: {}"} {
		line, err := br.ReadString('\n')
		if got := strings.TrimRight(line, "\r\n"); err != nil || got != want {
			t.Fatalf("after retry: line %q (err %v), want %q", got, err, want)
		}
	}

	mustCreateTask(t, ts, "stale", "after reset")
	ev := readSSEUntil(t, ctx, br, func(e sseEvent) bool { return e.kind == "task.created" })
	if ev.id <= 0 {
		t.Fatalf("task.created id = %d", ev.id)
	}
}

// blockingWriter stalls the first Write until release is closed, standing in
// for a client whose TCP window is full; it records everything written.
type blockingWriter struct {
	hdr     http.Header
	release chan struct{}
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	buf     strings.Builder
}

func (b *blockingWriter) Header() http.Header { return b.hdr }
func (b *blockingWriter) WriteHeader(int)     {}
func (b *blockingWriter) Flush()              {}
func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.release })
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// TestSSELaggedSubscriberDisconnects: when the hub drops an event because the
// handler is stalled, handleStream ends the response without writing the
// buffered events, so the client reconnects from its last id and the replay
// covers the dropped one (instead of skipping it forever).
func TestSSELaggedSubscriberDisconnects(t *testing.T) {
	srv := NewServer(openTestStore(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil).WithContext(ctx)
	w := &blockingWriter{hdr: http.Header{}, release: make(chan struct{}), started: make(chan struct{})}
	done := make(chan struct{})
	go func() { srv.handleStream(w, req); close(done) }()

	<-w.started // subscribed, now stuck writing the retry preamble
	for i := 1; i <= 65; i++ {
		srv.hub.Broadcast(&Event{ID: int64(i), ProjectID: 1, Kind: "task.created", Data: []byte("{}")})
	}
	close(w.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleStream kept a lagged subscriber open")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.Contains(w.buf.String(), "id: ") {
		t.Fatalf("lagged stream wrote events past the hole:\n%s", w.buf.String())
	}
}
