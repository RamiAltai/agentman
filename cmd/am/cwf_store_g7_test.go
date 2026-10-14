package main

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// cwfStoreSnapshot renders every row a rejected mutation could touch (tasks,
// task_deps, the newest event id) so a test can assert nothing changed.
func cwfStoreSnapshot(t *testing.T, st *Store) string {
	t.Helper()
	var b strings.Builder
	maxID, err := st.MaxEventID()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "events<=%d;", maxID)
	for _, q := range []string{
		"SELECT id||':'||status||':'||COALESCE(assignee,'')||':'||COALESCE(claimed_at,'')||':'||updated_at FROM tasks ORDER BY id",
		"SELECT task_id||'->'||depends_on_id FROM task_deps ORDER BY task_id, depends_on_id",
	} {
		rows, err := st.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			b.WriteString(s + ";")
		}
		rows.Close()
	}
	return b.String()
}

func cwfStoreAssertUnchanged(t *testing.T, st *Store, before, what string) {
	t.Helper()
	if after := cwfStoreSnapshot(t, st); after != before {
		t.Fatalf("%s left side effects:\nbefore %s\nafter  %s", what, before, after)
	}
}

// FIND-0256: staleCutoff emits exactly SQLite's strftime('%Y-%m-%dT%H:%M:%fZ')
// layout, and the stale filter splits a 1h window between 59m and 61m of
// inactivity on the same day.
func TestCwfStoreStaleCutoffFormat(t *testing.T) {
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	for _, d := range []time.Duration{0, time.Hour, 36 * time.Hour} {
		if got := staleCutoff(d); !re.MatchString(got) {
			t.Fatalf("staleCutoff(%v) = %q, want strftime layout", d, got)
		}
	}
	// Sorts consistently with SQLite's own now, taken in between.
	st := openTestStore(t)
	cut := staleCutoff(0)
	var now string
	if err := st.db.QueryRow("SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now')").Scan(&now); err != nil {
		t.Fatal(err)
	}
	if later := staleCutoff(0); !(cut <= now && now <= later) {
		t.Fatalf("staleCutoff(0) %q / SQLite now %q / staleCutoff(0) %q out of order", cut, now, later)
	}

	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for name, age := range map[string]time.Duration{"59m": 59 * time.Minute, "61m": 61 * time.Minute} {
		tk := cwfStoreNewTask(t, st, "web", name)
		if _, _, err := st.ClaimTask(tk.ID, "agent-"+name); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age).UTC().Format("2006-01-02T15:04:05.000Z")
		if _, err := st.db.Exec("UPDATE tasks SET updated_at=? WHERE id=?", at, tk.ID); err != nil {
			t.Fatal(err)
		}
		ids[name] = tk.ID
	}
	stale, err := st.ListTasks(TaskFilter{Stale: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].ID != ids["61m"] {
		t.Fatalf("stale(1h) = %+v, want only the 61m task %d", stale, ids["61m"])
	}
	if _, _, err := st.StealStaleClaim(ids["59m"], "thief", time.Hour); err == nil {
		t.Fatal("stole a claim 59m old with a 1h window")
	}
	if tk, _, err := st.StealStaleClaim(ids["61m"], "thief", time.Hour); err != nil || tk.Assignee != "thief" {
		t.Fatalf("steal of 61m-old claim = %+v, %v; want thief", tk, err)
	}
}

// FIND-0257: insertEvent refuses unmarshalable data and surfaces an Exec
// failure, writing no row; a caller whose insertEvent fails rolls back the
// whole mutation.
func TestCwfStoreInsertEventFailures(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	before := cwfStoreSnapshot(t, st)
	for _, data := range []any{math.NaN(), math.Inf(1), make(chan int), map[string]any{"f": func() {}}} {
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if ev, err := insertEvent(tx, 0, 0, "a", "x.test", data); err == nil || ev != nil ||
			!strings.Contains(err.Error(), "marshal event data") {
			t.Fatalf("insertEvent(%T) = %+v, %v; want a marshal error", data, ev, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	cwfStoreAssertUnchanged(t, st, before, "insertEvent with bad data")

	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if ev, err := insertEvent(tx, 0, 0, "a", "x.test", map[string]any{}); err == nil || ev != nil {
		t.Fatalf("insertEvent on a finished tx = %+v, %v; want an error", ev, err)
	}

	if _, err := st.db.Exec(`CREATE TRIGGER cwf_no_events BEFORE INSERT ON events
	       BEGIN SELECT RAISE(ABORT, 'events down'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "lost"}); err == nil {
		t.Fatal("CreateTask succeeded although its event insert failed")
	}
	cwfStoreAssertUnchanged(t, st, before, "CreateTask with a failing event insert")
}

// FIND-0258: ListEventsBefore and RecentEvents clamp limit (<=0 or >200 → 40)
// and reject an unknown project or category with ErrNotFound (404 over HTTP).
func TestCwfStoreEventsLimitClampAndUnknownFilters(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 45; i++ {
		cwfStoreNewTask(t, st, "web", fmt.Sprintf("t%d", i))
	}
	maxID, _ := st.MaxEventID() // 46 events: project.created + 45 task.created
	for _, c := range []struct{ limit, want int }{{0, 40}, {-1, 40}, {2, 2}, {200, 45}, {201, 40}} {
		before, err := st.ListEventsBefore(maxID, "", "", c.limit)
		if err != nil || len(before) != c.want || before[0].ID != maxID-1 {
			t.Fatalf("ListEventsBefore(limit=%d) = %d events, %v; want %d starting at %d", c.limit, len(before), err, c.want, maxID-1)
		}
		recent, _, err := st.RecentEvents("", "", c.limit)
		want := c.want
		if c.limit == 200 {
			want = 46
		}
		if err != nil || len(recent) != want || recent[0].ID != maxID {
			t.Fatalf("RecentEvents(limit=%d) = %d events, %v; want %d starting at %d", c.limit, len(recent), err, want, maxID)
		}
	}

	for _, f := range []struct{ project, category string }{{"nosuch", ""}, {"", "nosuch"}} {
		if _, err := st.ListEventsBefore(maxID, f.project, f.category, 10); !errors.Is(err, ErrNotFound) {
			t.Errorf("ListEventsBefore(%+v) err = %v, want ErrNotFound", f, err)
		}
		if _, _, err := st.RecentEvents(f.project, f.category, 10); !errors.Is(err, ErrNotFound) {
			t.Errorf("RecentEvents(%+v) err = %v, want ErrNotFound", f, err)
		}
	}
	for _, path := range []string{
		"/api/events?project=nosuch&before=100", "/api/events?category=nosuch&before=100",
		"/api/events?project=nosuch&tail=10", "/api/events?category=nosuch&tail=10",
	} {
		if got := cwfStoreStatus(t, ts, http.MethodGet, path, "", nil); got != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, got)
		}
	}
}

// FIND-0259: ProjectGraph returns only the requested project's edges, keeps
// done tasks as nodes, still serves an archived project, and returns empty
// (non-nil) lists for an empty project; every edge endpoint is a node.
func TestCwfStoreProjectGraphScope(t *testing.T) {
	st := openTestStore(t)
	for _, slug := range []string{"gproj", "other", "empty"} {
		if _, _, err := st.CreateProject(slug, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	a := cwfStoreNewTask(t, st, "gproj", "A")
	b := cwfStoreNewTask(t, st, "gproj", "B")
	x := cwfStoreNewTask(t, st, "other", "X")
	y := cwfStoreNewTask(t, st, "other", "Y")
	if _, err := st.AddDep(b.ID, a.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDep(y.ID, x.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PatchTask(a.ID, map[string]any{"status": "done"}, "alice"); err != nil {
		t.Fatal(err)
	}

	check := func(phase string) {
		t.Helper()
		g, err := st.ProjectGraph("gproj")
		if err != nil {
			t.Fatalf("%s: ProjectGraph: %v", phase, err)
		}
		nodes := map[int64]string{}
		for _, n := range g.Nodes {
			nodes[n.ID] = n.Status
		}
		if len(nodes) != 2 || nodes[a.ID] != "done" || nodes[b.ID] != "todo" {
			t.Fatalf("%s: nodes = %v, want done A %d and todo B %d", phase, nodes, a.ID, b.ID)
		}
		if len(g.Edges) != 1 || g.Edges[0] != (GraphEdge{From: a.ID, To: b.ID}) {
			t.Fatalf("%s: edges = %+v, want only A->B (other project's X->Y excluded)", phase, g.Edges)
		}
		for _, e := range g.Edges {
			if _, ok := nodes[e.From]; !ok {
				t.Fatalf("%s: edge %+v endpoint %d missing from nodes", phase, e, e.From)
			}
			if _, ok := nodes[e.To]; !ok {
				t.Fatalf("%s: edge %+v endpoint %d missing from nodes", phase, e, e.To)
			}
		}
	}
	check("active")
	if _, _, err := st.ArchiveProject("gproj", "alice"); err != nil {
		t.Fatal(err)
	}
	check("archived")

	g, err := st.ProjectGraph("empty")
	if err != nil || g.Nodes == nil || g.Edges == nil || len(g.Nodes)+len(g.Edges) != 0 {
		t.Fatalf("empty project graph = %+v, %v; want empty non-nil nodes and edges", g, err)
	}
}

// FIND-0300: AddDep with an unknown task or prerequisite id is ErrNotFound and
// writes nothing.
func TestCwfStoreAddDepMissingTask(t *testing.T) {
	st, t1, _, _ := setupDepFixture(t)
	before := cwfStoreSnapshot(t, st)
	for _, pair := range [][2]int64{{t1, 99999}, {99999, t1}} {
		if _, err := st.AddDep(pair[0], pair[1], "alice"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("AddDep(%d, %d) err = %v, want ErrNotFound", pair[0], pair[1], err)
		}
	}
	cwfStoreAssertUnchanged(t, st, before, "AddDep with a missing id")
}
