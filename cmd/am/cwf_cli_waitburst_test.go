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
