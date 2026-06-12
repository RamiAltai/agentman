package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// cmdWait blocks until a board condition is met, watching the SSE stream:
//
//	am wait <id> --done [--timeout D]      until the task's status is done
//	am wait --ready [-p P] [--timeout D]   until some ready task exists
//
// Exactly these two conditions. Exit 0 when met (--done prints nothing;
// --ready prints one ready task id; --json prints the satisfying task JSON);
// exit 7 on timeout (default 10m); exit 3 if the task does not exist; exit 6
// if the server is unreachable. The wait is entirely client-side — the server
// is untouched (ADR-023); events only trigger a REST re-check, their payloads
// are never trusted as state.
func cmdWait(c *Client, a Args) {
	waitDone, waitReady, id := a.has("done"), a.has("ready"), a.at(0)
	if waitDone == waitReady || (waitDone && id == "") || (waitReady && id != "") {
		fail(1, "usage: am wait <id> --done | am wait --ready [-p P]  [--timeout D]")
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
	// uses its own un-timed http.Client and is bounded by ctx instead.
	w := &waiter{base: c.base, agent: c.agent, hc: &http.Client{}, ctx: ctx}
	project := projectFor(a)

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
		return w.checkReady(project)
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
		if waitReady && project != "" {
			qs.Set("project", project)
		}
		resp := w.stream("/api/stream?" + qs.Encode())
		br := bufio.NewReader(resp.Body)
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
				if json.Unmarshal(data, &e) != nil || e.TaskID != taskID {
					continue
				}
			}
			if t, met := check(); met {
				resp.Body.Close()
				waitPrint(a, t, waitReady)
				return
			}
		}
		resp.Body.Close()
		if ctx.Err() != nil {
			fail(7, "wait: timeout")
		}
	}
}

// parseWaitTimeout accepts a Go duration ("5m") or a bare integer of seconds
// ("300"). Non-positive or unparseable values error.
func parseWaitTimeout(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("non-positive timeout %q", s)
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
	base  string
	agent string
	hc    *http.Client
	ctx   context.Context
}

// get performs a ctx-bounded GET; transport errors map to exit 7 (deadline)
// or exit 6 (server down), like the rest of the CLI.
func (w *waiter) get(path string) (int, []byte) {
	resp := w.request(path)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// stream opens a long-lived GET (SSE); the caller closes the body.
func (w *waiter) stream(path string) *http.Response { return w.request(path) }

func (w *waiter) request(path string) *http.Response {
	req, err := http.NewRequestWithContext(w.ctx, http.MethodGet, w.base+path, nil)
	if err != nil {
		fail(1, "agentman: %v", err)
	}
	if w.agent != "" {
		req.Header.Set("X-Agent", w.agent)
	}
	resp, err := w.hc.Do(req)
	if err != nil {
		if w.ctx.Err() != nil {
			fail(7, "wait: timeout")
		}
		fail(6, "agentman: cannot reach server at %s (is `am serve` running?)", w.base)
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
	json.Unmarshal(data, &out)
	return out.LastID
}

// checkDone re-evaluates the --done condition via REST (never via event payloads).
func (w *waiter) checkDone(id string) (*Task, bool) {
	st, data := w.get("/api/tasks/" + url.PathEscape(id))
	switch {
	case st == 404:
		fail(3, "wait: #%s not found", id)
	case st < 200 || st >= 300:
		fail(1, "wait: %s", apiErr(data, "error "+strconv.Itoa(st)))
	}
	var t Task
	json.Unmarshal(data, &t)
	return &t, t.Status == "done"
}

// checkReady re-evaluates the --ready condition via REST.
func (w *waiter) checkReady(project string) (*Task, bool) {
	qs := url.Values{"ready": {"true"}, "limit": {"1"}}
	if project != "" {
		qs.Set("project", project)
	}
	st, data := w.get("/api/tasks?" + qs.Encode())
	if st < 200 || st >= 300 {
		fail(1, "wait: %s", apiErr(data, "error "+strconv.Itoa(st)))
	}
	var tasks []Task
	json.Unmarshal(data, &tasks)
	if len(tasks) == 0 {
		return nil, false
	}
	return &tasks[0], true
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
