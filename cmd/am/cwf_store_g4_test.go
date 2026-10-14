package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FIND-0121: a full buffer never blocks Broadcast (exactly cap(ch) events are
// kept), and Unsubscribe closes the channel, is idempotent, and detaches the
// subscriber so a later Broadcast cannot send on the closed channel.
func TestCwfStoreHubFullBufferAndUnsubscribe(t *testing.T) {
	h := NewHub()
	sub := h.Subscribe(subFilter{})
	done := make(chan struct{})
	go func() {
		for i := 1; i <= cap(sub.ch)+10; i++ {
			h.Broadcast(&Event{ID: int64(i), ProjectID: 1, Kind: "task.created"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Broadcast blocked on a subscriber whose buffer is full")
	}
	if len(sub.ch) != 64 || cap(sub.ch) != 64 {
		t.Fatalf("buffered %d of cap %d, want 64 of 64", len(sub.ch), cap(sub.ch))
	}
	h.Unsubscribe(sub)
	h.Unsubscribe(sub) // second call must not panic (double close)
	h.Broadcast(&Event{ID: 999, ProjectID: 1, Kind: "task.created"})
	n := 0
	for e := range sub.ch { // terminates only if the channel is closed
		n++
		if e.ID != int64(n) {
			t.Fatalf("buffered event %d has id %d; want the oldest kept in order", n, e.ID)
		}
	}
	if n != 64 {
		t.Fatalf("drained %d events after Unsubscribe, want 64", n)
	}
}

func cwfStoreStatus(t *testing.T, ts *httptest.Server, method, path, body string, hdr map[string]string) int {
	t.Helper()
	r := do(t, ts, method, path, body, hdr)
	r.Body.Close()
	return r.StatusCode
}

// FIND-0182: a category/project scope (header or token) is confined to its own
// project on lists, next, project mutations and graph reads; an unknown
// explicit slug under a category scope is a 403 on lists and a 404 on
// project mutations.
func TestCwfStoreProjectScopeConfinement(t *testing.T) {
	ts := newTestServer(t)
	mustCreateCategory(t, ts, "work")
	mustCreateProjectIn(t, ts, "api", "work")
	mustCreateProjectIn(t, ts, "web", "work")
	mustCreateTask(t, ts, "api", "api task")
	mustCreateTask(t, ts, "web", "web task")
	tok := mintToken(t, ts, "work/api")

	for name, hdr := range map[string]map[string]string{
		"header": scoped("agent-a", "work/api"),
		"token":  bearer("agent-a", tok),
	} {
		for _, c := range []struct {
			method, path, body string
			want               int
		}{
			{http.MethodGet, "/api/tasks?project=web", "", http.StatusForbidden},
			{http.MethodGet, "/api/tasks?project=api", "", http.StatusOK},
			{http.MethodGet, "/api/projects/web/graph", "", http.StatusForbidden},
			{http.MethodGet, "/api/projects/api/graph", "", http.StatusOK},
			{http.MethodPatch, "/api/projects/web", `{"name":"x"}`, http.StatusForbidden},
			{http.MethodPost, "/api/projects/web/archive", "", http.StatusForbidden},
			{http.MethodPost, "/api/projects/web/unarchive", "", http.StatusForbidden},
			{http.MethodDelete, "/api/projects/web", "", http.StatusForbidden},
			{http.MethodPost, "/api/tasks/next", `{"project":"web"}`, http.StatusForbidden},
			{http.MethodPatch, "/api/projects/api", `{"name":"API"}`, http.StatusOK},
		} {
			if got := cwfStoreStatus(t, ts, c.method, c.path, c.body, hdr); got != c.want {
				t.Errorf("%s: %s %s = %d, want %d", name, c.method, c.path, got, c.want)
			}
		}
	}
	// next with the scope's own project hands out its task (once).
	r := do(t, ts, http.MethodPost, "/api/tasks/next", `{"project":"api"}`, scoped("agent-a", "work/api"))
	var got Task
	json.NewDecoder(r.Body).Decode(&got)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || got.Project != "api" {
		t.Fatalf("next in own project = %d %+v, want 200 with the api task", r.StatusCode, got)
	}
	// web survived the denied mutations.
	if c := cwfStoreStatus(t, ts, http.MethodGet, "/api/projects/web/graph", "", nil); c != http.StatusOK {
		t.Fatalf("web graph after denied mutations = %d, want 200", c)
	}

	cat := scoped("agent-w", "work")
	if c := cwfStoreStatus(t, ts, http.MethodGet, "/api/tasks?project=nonexistent", "", cat); c != http.StatusForbidden {
		t.Errorf("category scope, list of unknown project = %d, want 403", c)
	}
	if c := cwfStoreStatus(t, ts, http.MethodPatch, "/api/projects/nonexistent", `{"name":"x"}`, cat); c != http.StatusNotFound {
		t.Errorf("category scope, patch of unknown project = %d, want 404", c)
	}
}

// FIND-0183: Last-Event-ID wins over ?since= (EventSource reconnects keep the
// page's original ?since= in the URL).
func TestCwfStoreStreamLastEventIDWins(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	mustCreateProject(t, ts, "cur")
	mustCreateTask(t, ts, "cur", "a")
	mustCreateTask(t, ts, "cur", "b")
	maxID, _ := st.MaxEventID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/stream?since=0", nil)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(maxID-1, 10))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ev := readSSEUntil(t, ctx, bufio.NewReader(resp.Body), func(sseEvent) bool { return true }); ev.id != maxID {
		t.Fatalf("first replayed id = %d, want %d (Last-Event-ID ignored?)", ev.id, maxID)
	}
}

// FIND-0183: an unknown ?category= falls back to the unfiltered stream for the
// replay too (the documented choice in handleStream; /api/events 404s instead).
func TestCwfStoreStreamUnknownCategoryReplays(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	mustCreateProject(t, ts, "real")
	mustCreateTask(t, ts, "real", "t")
	maxID, _ := st.MaxEventID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, br := openStream(t, ctx, ts, "/api/stream?since=0&category=nope")
	defer resp.Body.Close()
	if ids := cwfStoreCountUntil(t, ctx, br, maxID); len(ids) != int(maxID) {
		t.Fatalf("replayed ids %v, want 1..%d", ids, maxID)
	}
}

// FIND-0183: a live event at or below the replay cursor (the overlap between
// Subscribe and the replay) is dropped, not delivered twice.
func TestCwfStoreStreamDedupesReplayedLive(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	mustCreateProject(t, ts, "dup")
	mustCreateTask(t, ts, "dup", "a")
	maxID, _ := st.MaxEventID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, br := openStream(t, ctx, ts, "/api/stream?since=0")
	defer resp.Body.Close()
	cwfStoreCountUntil(t, ctx, br, maxID)
	srv.hub.Broadcast(&Event{ID: 1, Kind: "project.created", Data: []byte("{}")}) // already replayed
	mustCreateTask(t, ts, "dup", "b")
	if ev := readSSEUntil(t, ctx, br, func(sseEvent) bool { return true }); ev.id != maxID+1 {
		t.Fatalf("first live event id = %d, want %d (replayed id re-sent?)", ev.id, maxID+1)
	}
}

type cwfStoreFailWriter struct{ hdr http.Header }

func (w cwfStoreFailWriter) Header() http.Header       { return w.hdr }
func (w cwfStoreFailWriter) WriteHeader(int)           {}
func (w cwfStoreFailWriter) Flush()                    {}
func (w cwfStoreFailWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// cwfStoreSubscriber waits for the hub's single subscriber to appear.
func cwfStoreSubscriber(t *testing.T, h *Hub) *subscriber {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		h.mu.RLock()
		for s := range h.subs {
			h.mu.RUnlock()
			return s
		}
		h.mu.RUnlock()
	}
	t.Fatal("handleStream never subscribed")
	return nil
}

// FIND-0183: handleStream ends the response (instead of looping until the
// request context ends) on a failed replay write, a failed live write, a
// failed ping, and a closed subscriber channel. The request context here is
// never cancelled, so a missing return hangs and the test times out.
func TestCwfStoreStreamExitPaths(t *testing.T) {
	defer func(d time.Duration) { streamPingInterval = d }(streamPingInterval)
	for _, c := range []struct {
		name  string
		w     http.ResponseWriter
		since string
		ping  time.Duration
		poke  func(*Server, *subscriber, int64)
	}{
		{name: "replay write fails", w: cwfStoreFailWriter{http.Header{}}, since: "0", ping: time.Hour},
		{name: "live write fails", w: cwfStoreFailWriter{http.Header{}}, ping: time.Hour,
			poke: func(s *Server, _ *subscriber, max int64) {
				s.hub.Broadcast(&Event{ID: max + 1, Kind: "project.created", Data: []byte("{}")})
			}},
		{name: "ping write fails", w: cwfStoreFailWriter{http.Header{}}, ping: 5 * time.Millisecond},
		{name: "channel closed", w: httptest.NewRecorder(), ping: time.Hour,
			poke: func(s *Server, sub *subscriber, _ int64) { s.hub.Unsubscribe(sub) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			streamPingInterval = c.ping
			st := openTestStore(t)
			if _, _, err := st.CreateProject("p", "", ""); err != nil {
				t.Fatal(err)
			}
			maxID, _ := st.MaxEventID()
			since := c.since
			if since == "" {
				since = strconv.FormatInt(maxID, 10) // nothing to replay
			}
			srv := NewServer(st)
			req := httptest.NewRequest(http.MethodGet, "/api/stream?since="+since, nil)
			done := make(chan struct{})
			go func() { srv.handleStream(c.w, req); close(done) }()
			if c.poke != nil {
				c.poke(srv, cwfStoreSubscriber(t, srv.hub), maxID)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("handleStream did not return")
			}
		})
	}
}

// FIND-0243: resolveTaskID accepts a global id, "#id" and "<slug>-<ref>"
// (slugs may contain dashes); anything else is ErrNotFound.
func TestCwfStoreResolveTaskID(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	for _, p := range []string{"web", "my-proj"} {
		if _, _, err := st.CreateProject(p, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	web1 := cwfStoreNewTask(t, st, "web", "w1")
	web2 := cwfStoreNewTask(t, st, "web", "w2")
	cwfStoreNewTask(t, st, "my-proj", "m1")
	cwfStoreNewTask(t, st, "my-proj", "m2")
	my3 := cwfStoreNewTask(t, st, "my-proj", "m3")
	if web2.Ref != 2 || my3.Ref != 3 {
		t.Fatalf("refs = %d, %d; want 2, 3", web2.Ref, my3.Ref)
	}
	sid := func(id int64) string { return strconv.FormatInt(id, 10) }
	for in, want := range map[string]int64{
		sid(my3.ID):        my3.ID,
		"#" + sid(web1.ID): web1.ID,
		" web-2 ":          web2.ID,
		"my-proj-3":        my3.ID,
	} {
		if got, err := st.resolveTaskID(in); err != nil || got != want {
			t.Errorf("resolveTaskID(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	// web-3 exists only as my-proj-3: refs are per project.
	for _, in := range []string{"web-999", "web-3", "nosuch-1", "web-x", "web", ""} {
		if got, err := st.resolveTaskID(in); !errors.Is(err, ErrNotFound) {
			t.Errorf("resolveTaskID(%q) = %d, %v; want ErrNotFound", in, got, err)
		}
	}
	r := do(t, ts, http.MethodGet, "/api/tasks/web-2", "", nil)
	var got Task
	json.NewDecoder(r.Body).Decode(&got)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || got.ID != web2.ID {
		t.Fatalf("GET /api/tasks/web-2 = %d id %d, want 200 id %d", r.StatusCode, got.ID, web2.ID)
	}
}

// FIND-0250: ClaimTask's validation, not-found, idempotent re-claim (one
// task.claimed event in total) and done-task conflict branches.
func TestCwfStoreClaimTaskEdges(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	tk := cwfStoreNewTask(t, st, "web", "t")
	if _, _, err := st.ClaimTask(tk.ID, ""); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty agent err = %v, want ErrValidation", err)
	}
	if _, _, err := st.ClaimTask(99999, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task err = %v, want ErrNotFound", err)
	}
	if _, ev, err := st.ClaimTask(tk.ID, "a"); err != nil || ev == nil {
		t.Fatalf("first claim = %v, %v; want an event", ev, err)
	}
	if got, ev, err := st.ClaimTask(tk.ID, "a"); err != nil || ev != nil || got.Assignee != "a" {
		t.Fatalf("re-claim = %+v, %v, %v; want task, no event, nil", got, ev, err)
	}
	evs, _, err := st.ListEvents(0, "web", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	claimed := 0
	for _, e := range evs {
		if e.Kind == "task.claimed" {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("task.claimed events = %d, want 1", claimed)
	}
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"status": "done"}, "a"); err != nil {
		t.Fatal(err)
	}
	var ce *ConflictError
	if _, _, err := st.ClaimTask(tk.ID, "b"); !errors.As(err, &ce) {
		t.Fatalf("claim of a done task by another agent err = %v, want ConflictError", err)
	}
}

// FIND-0157: runMigrations stops at target even when later steps are listed.
func TestCwfStoreMigrationTargetCap(t *testing.T) {
	st := openTestStore(t)
	base := currentSchemaVersion
	var applied []int
	var steps []migration
	for i := 1; i <= 3; i++ {
		v := base + i
		steps = append(steps, migration{version: v, apply: func(*sql.Tx) error {
			applied = append(applied, v)
			return nil
		}})
	}
	if err := runMigrations(st.db, base+2, steps); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || applied[1] != base+2 {
		t.Fatalf("applied = %v, want [%d %d]", applied, base+1, base+2)
	}
	if v, _ := readSchemaVersion(st.db); v != base+2 {
		t.Fatalf("schema_version = %d, want %d", v, base+2)
	}
}

// FIND-0157: a missing or unparseable schema_version reads as 1.
func TestCwfStoreReadSchemaVersionFallbacks(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.db.Exec("UPDATE meta SET value='abc' WHERE key='schema_version'"); err != nil {
		t.Fatal(err)
	}
	if v, err := readSchemaVersion(st.db); err != nil || v != 1 {
		t.Fatalf("unparseable version = %d, %v; want 1", v, err)
	}
	if _, err := st.db.Exec("DELETE FROM meta WHERE key='schema_version'"); err != nil {
		t.Fatal(err)
	}
	if v, err := readSchemaVersion(st.db); err != nil || v != 1 {
		t.Fatalf("missing version = %d, %v; want 1", v, err)
	}
}

// FIND-0176: claim and next with a body assignee claim on behalf of that
// agent (it is the assignee and the event actor). Under a scope the scope
// check runs on the task first; delegating to another agent name is allowed
// (pinned as current behavior: scope limits WHICH tasks, not for WHOM).
func TestCwfStoreClaimNextAssignee(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	workID, personalID := scopedBoard(t, ts)
	asA := map[string]string{"X-Agent": "agent-a", "Content-Type": "application/json"}

	claimAs := func(id string, hdr map[string]string) (int, Task) {
		r := do(t, ts, http.MethodPost, "/api/tasks/"+id+"/claim", `{"assignee":"agent-b"}`, hdr)
		defer r.Body.Close()
		var tk Task
		json.NewDecoder(r.Body).Decode(&tk)
		return r.StatusCode, tk
	}
	if code, tk := claimAs(workID, asA); code != http.StatusOK || tk.Assignee != "agent-b" {
		t.Fatalf("delegated claim = %d assignee %q, want 200 agent-b", code, tk.Assignee)
	}
	evs, _, _ := st.ListEvents(0, "wproj", "", 0)
	last := evs[len(evs)-1]
	if last.Kind != "task.claimed" || last.Actor != "agent-b" || !strings.Contains(string(last.Data), `[null,"agent-b"]`) {
		t.Fatalf("claim event = %s by %s %s, want task.claimed by agent-b with assignee [null,agent-b]", last.Kind, last.Actor, last.Data)
	}

	// Scoped: out-of-scope task is still refused, in-scope delegation works.
	ps := scoped("agent-p", "personal")
	if code, _ := claimAs(workID, ps); code != http.StatusForbidden {
		t.Fatalf("scoped delegated claim of work task = %d, want 403", code)
	}
	mustCreateTask(t, ts, "pproj", "second personal")
	if code, tk := claimAs(personalID, ps); code != http.StatusOK || tk.Assignee != "agent-b" {
		t.Fatalf("scoped delegated claim = %d assignee %q, want 200 agent-b", code, tk.Assignee)
	}

	// next: the picked task goes to the named assignee, inside the scope.
	r := do(t, ts, http.MethodPost, "/api/tasks/next", `{"assignee":"agent-c"}`, ps)
	var tk Task
	json.NewDecoder(r.Body).Decode(&tk)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || tk.Assignee != "agent-c" || tk.Project != "pproj" {
		t.Fatalf("scoped delegated next = %d %s/%q, want 200 pproj/agent-c", r.StatusCode, tk.Project, tk.Assignee)
	}
	mustCreateTask(t, ts, "wproj", "second work")
	r = do(t, ts, http.MethodPost, "/api/tasks/next", `{"assignee":"agent-d"}`, asA)
	json.NewDecoder(r.Body).Decode(&tk)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || tk.Assignee != "agent-d" {
		t.Fatalf("delegated next = %d assignee %q, want 200 agent-d", r.StatusCode, tk.Assignee)
	}
}
