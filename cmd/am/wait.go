package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// cmdWait blocks until a board condition is met, watching the SSE stream:
//
//	am wait <id> --done [--timeout D]                            until the task's status is done
//	am wait --ready [-p P] [-c CAT] [--meta KEY] [--timeout D]   until some ready task exists in scope
//
// Exactly these two conditions. Exit 0 when met (--done prints nothing;
// --ready prints one ready task id; --json prints the satisfying task JSON);
// exit 7 on timeout (default 10m); exit 3 if the task does not exist; exit 6
// if the server is unreachable; exit 9 on an invalid or revoked token. The wait is entirely client-side — the server
// is untouched (ADR-023); events only trigger a REST re-check, their payloads
// are never trusted as state.
func cmdWait(c *Client, a Args) {
	waitDone, waitReady, id := a.has("done"), a.has("ready"), a.at(0)
	metaKey := metaKeyArg(a, "wait") // >1 key or key=value → exit 5
	if waitDone == waitReady || (waitDone && id == "") || (waitReady && id != "") ||
		(waitDone && metaKey != "") { // --meta scopes --ready only
		usageErr("usage: am wait <id> --done | am wait --ready [-p P] [-c CAT] [--meta KEY]  [--timeout D]")
	}

	timeout := 10 * time.Minute
	if v := a.flag("timeout"); v != "" {
		d, err := parseWaitTimeout(v)
		if err != nil {
			fail(5, "wait: bad --timeout %q (Go duration like 5m, or seconds)", v)
		}
		timeout = d
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Client.http has a 10s Timeout that would kill a long SSE read; the wait
	// uses its own un-timed http.Client and is bounded by ctx instead. The
	// headers (X-Agent, then Bearer token or X-Agent-Scope) come from c.
	w := &waiter{c: c, hc: &http.Client{}, ctx: ctx}
	project := projectFor(a)
	category := categoryFor(a) // scopes --ready only; ignored under --done like -p

	// Cursor BEFORE the first condition check, so an event landing between
	// check and subscribe is replayed via ?since= (no check/subscribe race).
	cursor := w.cursor()

	var taskID int64 // resolved numeric id, for matching events under --done
	check := func() (*Task, bool) {
		if waitDone {
			t, met := w.checkDone(id)
			taskID = t.ID
			return t, met
		}
		return w.checkReady(project, category, metaKey)
	}
	if t, met := check(); met {
		waitPrint(a, t, waitReady)
		return
	}

	for { // (re)connect loop; ctx deadline bounds everything
		qs := url.Values{"since": {strconv.FormatInt(cursor, 10)}}
		// Project-scope the stream only for --ready: under --done the watched
		// task may live in a different project than AGENTMAN_PROJECT, and a
		// scoped stream would drop its events (the waiter would never re-check).
		// A category scope deliberately does NOT narrow the stream: /api/stream
		// does take ?category= (Phase R, ADR-028), but it resolves the category's
		// project set once at Subscribe, so a project created mid-wait would be
		// dropped for the life of the connection. The unscoped stream just triggers
		// the REST re-check, which IS category-scoped (the ADR-023 pattern: event
		// payloads are never trusted as state).
		if waitReady && project != "" {
			qs.Set("project", project)
		}
		// Each connection lives at most waitRecheckEvery, and the condition is
		// re-checked right after every (re)open. The stream is not lossless (the
		// hub drops events for a full subscriber, concurrent broadcasts can
		// arrive out of order, replay is capped), so a dropped event costs at
		// most one interval instead of the whole timeout.
		connCtx, connCancel := context.WithTimeout(ctx, waitRecheckEvery)
		resp := w.stream(connCtx, "/api/stream?"+qs.Encode())
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// Not a live SSE body (e.g. 500 streaming_unsupported): reading it as
			// SSE would hit EOF and reconnect in a tight loop until the timeout.
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			connCancel()
			fail(1, "wait: stream: %s", apiErr(data, "error "+strconv.Itoa(resp.StatusCode)))
		}
		if t, met := check(); met {
			resp.Body.Close()
			connCancel()
			waitPrint(a, t, waitReady)
			return
		}
		br := bufio.NewReader(resp.Body)
		pending := false // a relevant event whose re-check is deferred
		for {
			evID, data, err := readSSEFrame(br)
			if err != nil {
				break // disconnect (or deadline) → reconnect from cursor
			}
			if evID > cursor {
				cursor = evID
			}
			// Relevance gate: --done only re-checks on events for this task;
			// --ready re-checks on any event in scope (already project-filtered).
			if waitDone {
				var e struct {
					TaskID int64 `json:"task_id"`
				}
				if json.Unmarshal(data, &e) == nil && e.TaskID == taskID {
					pending = true
				}
			} else {
				pending = true
			}
			// Coalesce a burst: frames already buffered are read first, so k
			// queued events cost one re-check, not k. The deferred re-check
			// runs once the buffer drains, even if the last frame is irrelevant.
			if !pending || eventBuffered(br) {
				continue
			}
			pending = false
			if t, met := check(); met {
				resp.Body.Close()
				connCancel()
				waitPrint(a, t, waitReady)
				return
			}
		}
		resp.Body.Close()
		rotated := connCtx.Err() != nil // planned re-check cycle, not a drop
		connCancel()
		if ctx.Err() != nil {
			fail(7, "wait: timeout")
		}
		if !rotated { // the server closed the stream: back off before reconnecting
			select {
			case <-ctx.Done():
				fail(7, "wait: timeout")
			case <-time.After(waitReconnectDelay):
			}
		}
	}
}

// waitRecheckEvery bounds one stream connection (then reconnect + REST
// re-check); waitReconnectDelay is the pause after the server ends a stream.
// Variables so tests can shorten them.
var (
	waitRecheckEvery   = 30 * time.Second
	waitReconnectDelay = time.Second
)

// parseWaitTimeout accepts a Go duration ("5m") or a bare integer of seconds
// ("300"). Non-positive or unparseable values error.
func parseWaitTimeout(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("non-positive timeout %q", s)
		}
		if int64(n) > math.MaxInt64/int64(time.Second) {
			return 0, fmt.Errorf("timeout %q overflows", s)
		}
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("non-positive timeout %q", s)
	}
	return d, nil
}

// waitPrint emits the success output: --json the satisfying task, --ready the
// ready task's id, --done nothing (terse stdout convention).
func waitPrint(a Args, t *Task, ready bool) {
	if a.has("json") && t != nil {
		printJSON(t)
		return
	}
	if ready && t != nil {
		fmt.Println(t.ID)
	}
}

// waiter wraps the streaming-capable HTTP plumbing for cmdWait.
type waiter struct {
	c   *Client // identity + credential headers (Client.newRequest); scope the REST re-checks
	hc  *http.Client
	ctx context.Context
}

// get performs a ctx-bounded GET; transport errors map to exit 7 (deadline)
// or exit 6 (server down), like the rest of the CLI.
func (w *waiter) get(path string) (int, []byte) {
	resp := w.request(w.ctx, path)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// stream opens a long-lived GET (SSE) bounded by ctx (a child of w.ctx); the
// caller closes the body.
func (w *waiter) stream(ctx context.Context, path string) *http.Response {
	return w.request(ctx, path)
}

func (w *waiter) request(ctx context.Context, path string) *http.Response {
	resp, err := w.hc.Do(w.c.newRequest(ctx, http.MethodGet, path, nil))
	if err != nil {
		if w.ctx.Err() != nil {
			fail(7, "wait: timeout")
		}
		transportFail(w.c.base, http.MethodGet, err)
	}
	// A bad credential hard-fails on any request (stream included), so a
	// revoked token can neither pass a re-check nor spin the reconnect loop.
	if resp.StatusCode == http.StatusUnauthorized {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fail(9, "wait: %s", apiErr(data, "invalid or revoked token"))
	}
	return resp
}

// cursor fetches the current max event id (the SSE resume point).
func (w *waiter) cursor() int64 {
	st, data := w.get("/api/events?tail=1")
	if st < 200 || st >= 300 {
		fail(1, "wait: %s", apiErr(data, "error "+strconv.Itoa(st)))
	}
	var out struct {
		LastID int64 `json:"last_id"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		fail(1, "wait: bad response from server: %v", err)
	}
	return out.LastID
}

// checkDone re-evaluates the --done condition via REST (never via event payloads).
func (w *waiter) checkDone(id string) (*Task, bool) {
	st, data := w.get("/api/tasks/" + url.PathEscape(id))
	switch {
	case st == 404:
		fail(3, "wait: #%s not found", id)
	case st == 403:
		fail(8, "wait: #%s %s", id, apiErr(data, "out of scope"))
	case st < 200 || st >= 300:
		fail(1, "wait: %s", apiErr(data, "error "+strconv.Itoa(st)))
	}
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		fail(1, "wait: bad response from server: %v", err)
	}
	return &t, t.Status == "done"
}

// checkReady re-evaluates the --ready condition via REST. Like the category
// scope, --meta narrows only this re-check, never the SSE stream (ADR-023:
// event payloads are triggers, not state).
func (w *waiter) checkReady(project, category, metaKey string) (*Task, bool) {
	qs := url.Values{"ready": {"true"}, "limit": {"1"}}
	if project != "" {
		qs.Set("project", project)
	}
	if category != "" {
		qs.Set("category", category)
	}
	if metaKey != "" {
		qs.Set("meta_key", metaKey)
	}
	st, data := w.get("/api/tasks?" + qs.Encode())
	if st == 403 { // explicit -p/-c outside the agent's scope
		fail(8, "wait: %s", apiErr(data, "out of scope"))
	}
	if st < 200 || st >= 300 {
		fail(1, "wait: %s", apiErr(data, "error "+strconv.Itoa(st)))
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		fail(1, "wait: bad response from server: %v", err)
	}
	if len(tasks) == 0 {
		return nil, false
	}
	return &tasks[0], true
}

// eventBuffered reports whether br already holds a complete event frame, so
// reading it cannot block. A false negative only costs an extra re-check.
func eventBuffered(br *bufio.Reader) bool {
	b, _ := br.Peek(br.Buffered())
	i := bytes.Index(b, []byte("data: "))
	return i >= 0 && bytes.Contains(b[i:], []byte("\n\n"))
}

// readSSEFrame reads one SSE event (id + data, terminated by a blank line),
// skipping ": ping" comment lines and the "retry:" preamble. Returns the
// read error on disconnect.
func readSSEFrame(br *bufio.Reader) (int64, []byte, error) {
	var id int64
	var data []byte
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return 0, nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, ":"): // heartbeat comment — skip
		case strings.HasPrefix(line, "id: "):
			id, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
		case strings.HasPrefix(line, "data: "):
			data = []byte(strings.TrimPrefix(line, "data: "))
		case line == "":
			if len(data) > 0 {
				return id, data, nil
			}
			id, data = 0, nil // retry:/empty frame — keep reading
		}
	}
}
