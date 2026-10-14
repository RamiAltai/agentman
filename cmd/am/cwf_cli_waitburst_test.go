package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// FIND-0341: a burst of events delivered together triggers one --ready
// re-check, not one per event.
func TestCwfCliWaitReadyCoalescesBurst(t *testing.T) {
	var checks atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/events"):
			w.Write([]byte(`{"last_id":0}`))
		case r.URL.Path == "/api/tasks":
			checks.Add(1)
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			var b strings.Builder
			for i := 1; i <= 20; i++ {
				fmt.Fprintf(&b, "id: %d\ndata: {\"id\":%d,\"task_id\":1}\n\n", i, i)
			}
			w.Write([]byte(b.String()))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}))
	t.Cleanup(stub.Close)

	c := newWaitClient(stub.URL)
	code := captureExit(t, func() { cmdWait(c, parse([]string{"--ready", "--timeout", "1s"})) })
	if code != 7 {
		t.Fatalf("exit = %d, want 7 (timeout)", code)
	}
	// initial check + post-open check + one for the burst
	if n := checks.Load(); n != 3 {
		t.Fatalf("ready re-checks = %d, want 3 (burst of 20 coalesced)", n)
	}
}

// FIND-0341: under --done, a relevant event followed in the same burst by an
// event for another task must still trigger the deferred re-check.
func TestCwfCliWaitDoneRelevantThenIrrelevant(t *testing.T) {
	var gets atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/events"):
			w.Write([]byte(`{"last_id":0}`))
		case r.URL.Path == "/api/tasks/5":
			status := "open"
			if gets.Add(1) >= 3 {
				status = "done"
			}
			fmt.Fprintf(w, `{"id":5,"status":%q}`, status)
		case r.URL.Path == "/api/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("id: 1\ndata: {\"id\":1,\"task_id\":5}\n\nid: 2\ndata: {\"id\":2,\"task_id\":9}\n\n"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}))
	t.Cleanup(stub.Close)

	c := newWaitClient(stub.URL)
	code := captureExit(t, func() { cmdWait(c, parse([]string{"--done", "5", "--timeout", "2s"})) })
	if code != -1 {
		t.Fatalf("exit = %d, want normal return (task done after the relevant event)", code)
	}
	if n := gets.Load(); n != 3 {
		t.Fatalf("task GETs = %d, want 3", n)
	}
}
