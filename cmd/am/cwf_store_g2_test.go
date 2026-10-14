package main

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FIND-0234: an agent creating a project is the event's actor.
func TestCwfStoreProjectCreatedActor(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	r := do(t, ts, http.MethodPost, "/api/projects", `{"slug":"agentproj"}`,
		map[string]string{"X-Agent": "agent-x", "X-Agent-Scope": "general"})
	r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d, want 201", r.StatusCode)
	}
	evs, _, err := st.ListEvents(0, "agentproj", "", 0)
	if err != nil || len(evs) != 1 || evs[0].Kind != "project.created" {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	if evs[0].Actor != "agent-x" {
		t.Fatalf("project.created actor = %q, want agent-x", evs[0].Actor)
	}
}

// FIND-0235: create applies PatchProject's slug and name rules.
func TestCwfStoreCreateProjectLimits(t *testing.T) {
	st := openTestStore(t)
	long := strings.Repeat("a", maxTitleLen+1)
	for _, c := range []struct{ slug, name string }{
		{long, ""},
		{"okslug", long},
		{"okslug", "   "},
	} {
		if _, _, err := st.CreateProject(c.slug, c.name, ""); !errors.Is(err, ErrValidation) {
			t.Errorf("CreateProject(len %d, len %d) err = %v, want ErrValidation", len(c.slug), len(c.name), err)
		}
	}
	if _, _, err := st.CreateProject(strings.Repeat("a", maxTitleLen), "", ""); err != nil {
		t.Fatalf("slug of exactly maxTitleLen: %v", err)
	}
}

// FIND-0236: project slugs are lowercased on create and rename, so a scope
// naming the project (parseScope lowercases) can reach it.
func TestCwfStoreProjectSlugLowercased(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	p, _, err := st.CreateProject("Web", "", "")
	if err != nil || p.Slug != "web" {
		t.Fatalf("CreateProject(Web) = %+v, %v; want slug web", p, err)
	}
	if _, _, err := st.CreateProject("WEB", "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("case-variant duplicate err = %v, want ErrConflict", err)
	}
	r := do(t, ts, http.MethodPatch, "/api/projects/web", `{"name":"renamed"}`,
		map[string]string{"X-Agent-Scope": "general/Web"})
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("patch under scope general/Web = %d, want 200", r.StatusCode)
	}
	mintToken(t, ts, "general/Web") // 404 before the fix
	p, _, err = st.PatchProject("web", map[string]any{"slug": "Site"}, "human")
	if err != nil || p.Slug != "site" {
		t.Fatalf("rename to Site = %+v, %v; want slug site", p, err)
	}
}

// FIND-0262: a non-string vault value is a validation error, not a clear.
func TestCwfStorePatchProjectVaultTypes(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("vp", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PatchProject("vp", map[string]any{"vault_project_id": "V1", "vault_path": "/p"}, "human"); err != nil {
		t.Fatal(err)
	}
	for _, patch := range []map[string]any{
		{"vault_path": float64(123)},
		{"vault_project_id": true},
		{"vault_path": map[string]any{}},
	} {
		if _, _, err := st.PatchProject("vp", patch, "human"); !errors.Is(err, ErrValidation) {
			t.Errorf("PatchProject(%v) err = %v, want ErrValidation", patch, err)
		}
	}
	p, _, err := st.PatchProject("vp", map[string]any{"vault_path": nil}, "human")
	if err != nil || p.VaultPath != "" || p.VaultProjectID != "V1" {
		t.Fatalf("null clear = %+v, %v; want vault_path cleared, id kept", p, err)
	}
}

// FIND-0238: a pre-assigned task has claimed_at, and task.created names the assignee.
func TestCwfStoreCreateTaskPreassigned(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("pa", "", ""); err != nil {
		t.Fatal(err)
	}
	tk, ev, err := st.CreateTask(CreateTaskInput{Project: "pa", Title: "x", Priority: 2, Assignee: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if tk.ClaimedAt == "" || tk.Status != "todo" {
		t.Fatalf("pre-assigned task claimed_at=%q status=%q; want set, todo", tk.ClaimedAt, tk.Status)
	}
	if !strings.Contains(string(ev.Data), `"assignee":"bob"`) {
		t.Fatalf("task.created data = %s, want assignee", ev.Data)
	}
	tk, ev, err = st.CreateTask(CreateTaskInput{Project: "pa", Title: "y", Priority: 2})
	if err != nil {
		t.Fatal(err)
	}
	if tk.ClaimedAt != "" || strings.Contains(string(ev.Data), "assignee") {
		t.Fatalf("unassigned task claimed_at=%q data=%s; want neither", tk.ClaimedAt, ev.Data)
	}
}

// FIND-0192: the project graph lists every task; the meta lookup must not
// bind one variable per task in a single statement (SQLite caps that at 32766).
func TestCwfStoreProjectGraphLargeProject(t *testing.T) {
	st := openTestStore(t)
	p, _, err := st.CreateProject("big", "", "")
	if err != nil {
		t.Fatal(err)
	}
	const n = 33000
	if _, err := st.db.Exec(`WITH RECURSIVE k(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM k WHERE i<?)
	    INSERT INTO tasks(project_id,ref,title) SELECT ?, i, 't' FROM k`, n, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO task_meta(task_id,key,value)
	    SELECT id,'k',CAST(ref AS TEXT) FROM tasks WHERE ref IN (1, 501, ?)`, n); err != nil {
		t.Fatal(err)
	}
	g, err := st.ProjectGraph("big")
	if err != nil {
		t.Fatalf("ProjectGraph: %v", err)
	}
	if len(g.Nodes) != n {
		t.Fatalf("nodes = %d, want %d", len(g.Nodes), n)
	}
	withMeta := 0
	for _, nd := range g.Nodes {
		if nd.Meta["k"] != "" {
			if nd.Meta["k"] != strconv.FormatInt(nd.Ref, 10) {
				t.Fatalf("task ref %d got meta %q", nd.Ref, nd.Meta["k"])
			}
			withMeta++
		}
	}
	if withMeta != 3 {
		t.Fatalf("tasks with meta = %d, want 3", withMeta)
	}
}

func cwfStorePlan(t *testing.T, st *Store, q string, args ...any) string {
	t.Helper()
	rows, err := st.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return strings.Join(out, "\n")
}

// FIND-0271 / FIND-0272: next and project-filtered event replay are index-served.
func TestCwfStoreHotQueryPlans(t *testing.T) {
	st := openTestStore(t)
	// NextTask's candidate subselect (unscoped `am next`).
	next := cwfStorePlan(t, st, `SELECT t.id FROM tasks t JOIN projects p ON p.id=t.project_id
	                           JOIN categories c ON c.id=p.category_id
	   WHERE t.status='todo' AND t.assignee IS NULL AND p.archived_at IS NULL
	     AND c.archived_at IS NULL
	     AND NOT EXISTS (SELECT 1 FROM task_deps d JOIN tasks pt ON pt.id=d.depends_on_id
	                     WHERE d.task_id=t.id AND pt.status!='done')
	   ORDER BY t.priority ASC, t.id ASC LIMIT 1`)
	if !strings.Contains(next, "idx_tasks_next") || strings.Contains(next, "TEMP B-TREE") {
		t.Errorf("next plan does not walk idx_tasks_next:\n%s", next)
	}
	// ListEvents with a project filter.
	evs := cwfStorePlan(t, st, `SELECT events.id FROM events LEFT JOIN projects p ON p.id = events.project_id
	      LEFT JOIN categories c ON c.id = p.category_id
	      WHERE events.id>? AND events.project_id=? ORDER BY events.id ASC LIMIT ?`, 0, 1, 500)
	if !strings.Contains(evs, "idx_events_project") {
		t.Errorf("project-filtered ListEvents plan does not use idx_events_project:\n%s", evs)
	}
	// The assignee filter keeps an index after idx_tasks_assignee became partial.
	if a := cwfStorePlan(t, st, "SELECT id FROM tasks t WHERE t.assignee=?", "x"); !strings.Contains(a, "idx_tasks_assignee") {
		t.Errorf("assignee filter plan:\n%s", a)
	}
}

func cwfStoreCountUntil(t *testing.T, ctx context.Context, br *bufio.Reader, lastID int64) []int64 {
	t.Helper()
	var ids []int64
	readSSEUntil(t, ctx, br, func(e sseEvent) bool {
		ids = append(ids, e.id)
		return e.id >= lastID
	})
	return ids
}

// FIND-0457: a reconnect gap larger than one ListEvents page is replayed in full.
func TestCwfStoreStreamReplaysLargeGap(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	if _, _, err := st.CreateProject("gap", "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1100; i++ {
		if _, _, err := st.CreateTask(CreateTaskInput{Project: "gap", Title: "t", Priority: 2}); err != nil {
			t.Fatal(err)
		}
	}
	maxID, _ := st.MaxEventID()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, br := openStream(t, ctx, ts, "/api/stream?since=0")
	defer resp.Body.Close()
	ids := cwfStoreCountUntil(t, ctx, br, maxID)
	if int64(len(ids)) != maxID {
		t.Fatalf("replayed %d events, want %d", len(ids), maxID)
	}
	for i := range ids {
		if ids[i] != int64(i+1) {
			t.Fatalf("replay[%d] = id %d, want %d", i, ids[i], i+1)
		}
	}
}

// FIND-0455: an unknown ?project= is dropped for the replay too, matching the
// unfiltered live stream it already got (the replay used to be skipped).
func TestCwfStoreStreamUnknownProjectReplays(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	if _, _, err := st.CreateProject("real", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "real", Title: "t", Priority: 2}); err != nil {
		t.Fatal(err)
	}
	maxID, _ := st.MaxEventID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, br := openStream(t, ctx, ts, "/api/stream?since=0&project=gone")
	defer resp.Body.Close()
	if ids := cwfStoreCountUntil(t, ctx, br, maxID); len(ids) != int(maxID) {
		t.Fatalf("replayed ids %v, want 1..%d", ids, maxID)
	}
}

// FIND-0458: publish holds the commit-to-broadcast window, so a mutation that
// commits later cannot broadcast first even when the earlier one stalls
// between its commit and its Broadcast.
func TestCwfStorePublishOrdersBroadcasts(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	if _, _, err := st.CreateProject("race", "", ""); err != nil {
		t.Fatal(err)
	}
	sub := srv.hub.Subscribe(subFilter{})
	defer srv.hub.Unsubscribe(sub)
	create := func() (*Event, error) {
		_, ev, err := st.CreateTask(CreateTaskInput{Project: "race", Title: "t", Priority: 2})
		return ev, err
	}
	committed := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- srv.publish(func() (*Event, error) {
			ev, err := create()
			close(committed)
			time.Sleep(50 * time.Millisecond) // stall between commit and broadcast
			return ev, err
		})
	}()
	<-committed
	if err := srv.publish(create); err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	a, b := <-sub.ch, <-sub.ch
	if a.ID >= b.ID {
		t.Fatalf("broadcast order %d then %d, want ascending ids", a.ID, b.ID)
	}
}

// FIND-0193: every route in Handler() that is not an intentionally unscoped
// read resolves the caller's scope, so a bad bearer token is a 401 there. New
// routes are picked up from server.go, so one that forgets scopeOf fails here.
func TestCwfStoreRoutesRejectBadToken(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(routes) < 20 {
		t.Fatalf("found %d routes in server.go; the pattern no longer matches Handler()", len(routes))
	}
	unscopedReads := map[string]bool{ // no identity scope by design (dashboard reads)
		"GET /api/categories": true,
		"GET /api/projects":   true,
		"GET /api/events":     true,
		"GET /api/stream":     true,
	}
	ts, st := newTestServerWithStore(t)
	mustCreateProject(t, ts, "routes")
	id := mustCreateTask(t, ts, "routes", "a")
	dep := mustCreateTask(t, ts, "routes", "b")
	// FIND-0181: a revoked token is as dead as a bogus one on every route.
	revoked, tok, err := st.CreateToken(Scope{Category: "general"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RevokeToken(tok.ID); err != nil {
		t.Fatal(err)
	}
	maxBefore, _ := st.MaxEventID()
	for _, bad := range []string{"amt_bogus", revoked} {
		for _, m := range routes {
			method, pattern := m[1], m[2]
			if unscopedReads[method+" "+pattern] {
				continue
			}
			path := strings.NewReplacer("{slug}", "routes", "{depId}", dep, "{label}", "x", "{cid}", "1").Replace(pattern)
			if strings.HasPrefix(path, "/api/tokens/") {
				path = strings.Replace(path, "{id}", tok.ID, 1)
			}
			path = strings.Replace(path, "{id}", id, 1)
			r := do(t, ts, method, path, "{}", map[string]string{"Authorization": "Bearer " + bad})
			r.Body.Close()
			if r.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s with token %.8s… = %d, want 401 (scope not resolved?)", method, path, bad, r.StatusCode)
			}
		}
	}
	// No rejected request changed anything: no event was written, and the
	// task and project the deletes targeted are still there.
	if maxAfter, _ := st.MaxEventID(); maxAfter != maxBefore {
		t.Errorf("rejected requests wrote events %d..%d", maxBefore+1, maxAfter)
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	if _, err := st.GetTask(n); err != nil {
		t.Errorf("task %s after rejected requests: %v", id, err)
	}
	if _, err := st.projectID("routes"); err != nil {
		t.Errorf("project after rejected requests: %v", err)
	}
	if toks, err := st.ListTokens(); err != nil || len(toks) != 1 {
		t.Errorf("tokens after rejected requests = %d, %v; want only the revoked one", len(toks), err)
	}
}
