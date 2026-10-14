package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// FIND-0211: a token scoped to personal is held to its scope on the read
// handlers and on the label/dep/comment/project mutation handlers, exactly like
// the X-Agent-Scope header (TestScopeReads, TestScopeMutationSweep). Bad
// tokens on every route are TestCwfStoreRoutesRejectBadToken.
func TestCwfStoreTokenScopeReadsAndMutations(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	workID, personalID := scopedBoard(t, ts)
	workID2 := mustCreateTask(t, ts, "wproj", "work prereq")
	unscoped := map[string]string{"Content-Type": "application/json"}
	for _, c := range []struct{ path, body string }{
		{"/api/tasks/" + workID + "/deps", `{"depends_on":` + workID2 + `}`},
		{"/api/tasks/" + workID + "/labels", `{"label":"seed"}`},
	} {
		if got := cwfStoreStatus(t, ts, http.MethodPost, c.path, c.body, unscoped); got != http.StatusOK {
			t.Fatalf("seed POST %s = %d, want 200", c.path, got)
		}
	}
	r := do(t, ts, http.MethodPost, "/api/tasks/"+workID+"/comments", `{"body":"seed"}`, unscoped)
	var cm Comment
	json.NewDecoder(r.Body).Decode(&cm)
	r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("seed comment = %d, want 201", r.StatusCode)
	}

	hdr := bearer("agent-p", mintToken(t, ts, "personal"))
	maxBefore, _ := st.MaxEventID()
	work := "/api/tasks/" + workID
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, work, ""},
		{http.MethodGet, "/api/tasks?category=work", ""},
		{http.MethodGet, "/api/tasks?project=wproj", ""},
		{http.MethodGet, "/api/projects/wproj/graph", ""},
		{http.MethodPost, work + "/labels", `{"label":"bug"}`},
		{http.MethodDelete, work + "/labels/seed", ""},
		{http.MethodPost, work + "/deps", `{"depends_on":` + personalID + `}`},
		{http.MethodDelete, work + "/deps/" + workID2, ""},
		{http.MethodDelete, work + "/comments/" + strconv.FormatInt(cm.ID, 10), ""},
		{http.MethodPatch, "/api/projects/wproj", `{"name":"pwned"}`},
		{http.MethodPost, "/api/projects/wproj/archive", ""},
		{http.MethodPost, "/api/projects/wproj/unarchive", ""},
		{http.MethodDelete, "/api/projects/wproj", ""},
	} {
		if got := cwfStoreStatus(t, ts, c.method, c.path, c.body, hdr); got != http.StatusForbidden {
			t.Errorf("personal token: %s %s = %d, want 403", c.method, c.path, got)
		}
	}
	// The unfiltered list narrows to the token's scope; its own task is readable.
	if got := cwfStoreTaskIDs(t, ts, "", hdr); strings.Join(got, ",") != personalID {
		t.Errorf("personal token: unfiltered list = %v, want [%s]", got, personalID)
	}
	if got := cwfStoreStatus(t, ts, http.MethodGet, "/api/tasks/"+personalID, "", hdr); got != http.StatusOK {
		t.Errorf("personal token: GET own task = %d, want 200", got)
	}
	// None of the denied requests changed anything.
	if maxAfter, _ := st.MaxEventID(); maxAfter != maxBefore {
		t.Errorf("denied requests wrote events %d..%d", maxBefore+1, maxAfter)
	}
	w := cwfStoreGetTask(t, ts, workID)
	if strings.Join(w.Labels, ",") != "seed" || len(w.Comments) != 1 || len(w.DependsOn) != 1 {
		t.Errorf("work task after denied requests = %+v, want the seeded label, comment and dep", w)
	}
}

// FIND-0247: ListTasks' Assignee filter returns only that assignee's tasks and
// composes with Status.
func TestCwfStoreListTasksAssignee(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, c := range []struct{ title, assignee string }{
		{"alice-todo", "alice"}, {"alice-doing", "alice"}, {"bob", "bob"}, {"nobody", ""},
	} {
		tk, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: c.title, Assignee: c.assignee})
		if err != nil {
			t.Fatal(err)
		}
		ids[c.title] = tk.ID
	}
	if _, _, err := st.PatchTask(ids["alice-doing"], map[string]any{"status": "doing"}, "alice"); err != nil {
		t.Fatal(err)
	}
	titles := func(f TaskFilter) string {
		t.Helper()
		tasks, err := st.ListTasks(f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tk := range tasks {
			out = append(out, tk.Title)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		f    TaskFilter
		want string
	}{
		{TaskFilter{Assignee: "alice"}, "alice-doing,alice-todo"},
		{TaskFilter{Assignee: "bob"}, "bob"},
		{TaskFilter{Assignee: "carol"}, ""},
		{TaskFilter{Assignee: "alice", Status: "doing"}, "alice-doing"},
		{TaskFilter{Assignee: "bob", Status: "doing"}, ""},
	} {
		if got := titles(c.f); got != c.want {
			t.Errorf("ListTasks(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}

// FIND-0248: refs count from 1 per project, independently, and (ADR-015,
// still in force for refs; only ids stopped being reused) deleting the
// highest-numbered task frees its ref for the next task in that project.
func TestCwfStoreTaskRefSequence(t *testing.T) {
	st := openTestStore(t)
	for _, p := range []string{"web", "api"} {
		if _, _, err := st.CreateProject(p, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	w1 := cwfStoreNewTask(t, st, "web", "w1")
	a1 := cwfStoreNewTask(t, st, "api", "a1")
	w2 := cwfStoreNewTask(t, st, "web", "w2")
	a2 := cwfStoreNewTask(t, st, "api", "a2")
	if w1.Ref != 1 || w2.Ref != 2 || a1.Ref != 1 || a2.Ref != 2 {
		t.Fatalf("refs web %d,%d api %d,%d; want 1,2 in each project", w1.Ref, w2.Ref, a1.Ref, a2.Ref)
	}
	if _, err := st.DeleteTask(w2.ID, "human"); err != nil {
		t.Fatal(err)
	}
	w3 := cwfStoreNewTask(t, st, "web", "w3")
	if w3.Ref != 2 || w3.ID == w2.ID {
		t.Fatalf("after deleting web-2: new task ref %d id %d (old id %d); want ref 2 reused with a fresh id", w3.Ref, w3.ID, w2.ID)
	}
	if got, err := st.resolveTaskID("web-2"); err != nil || got != w3.ID {
		t.Fatalf("web-2 resolves to %d, %v; want the new task %d", got, err, w3.ID)
	}
	// Deleting a lower ref leaves a hole; numbering continues after the max.
	if _, err := st.DeleteTask(w1.ID, "human"); err != nil {
		t.Fatal(err)
	}
	if w4 := cwfStoreNewTask(t, st, "web", "w4"); w4.Ref != 3 {
		t.Fatalf("after deleting web-1: new ref %d, want 3", w4.Ref)
	}
}

// FIND-0249: CreateProject rejects malformed slugs, CreateToken rejects a
// category-less scope, and UnarchiveProject is ErrNotFound for an unknown slug
// and an event-free no-op for a project that is not archived.
func TestCwfStoreProjectTokenGuards(t *testing.T) {
	st := openTestStore(t)
	for _, slug := range []string{"", "   ", "a b", "a/b", "a\tb"} {
		if _, _, err := st.CreateProject(slug, "name", ""); !errors.Is(err, ErrValidation) {
			t.Errorf("CreateProject(%q) err = %v, want ErrValidation", slug, err)
		}
	}
	if ps, err := st.ListProjects(true, ""); err != nil || len(ps) != 0 {
		t.Fatalf("projects after rejected creates = %d, %v; want none", len(ps), err)
	}
	if _, _, err := st.CreateToken(Scope{}); !errors.Is(err, ErrValidation) {
		t.Errorf("CreateToken(Scope{}) err = %v, want ErrValidation", err)
	}
	if _, _, err := st.CreateToken(Scope{Project: "web"}); !errors.Is(err, ErrValidation) {
		t.Errorf("CreateToken(project only) err = %v, want ErrValidation", err)
	}
	if toks, err := st.ListTokens(); err != nil || len(toks) != 0 {
		t.Fatalf("tokens after rejected creates = %d, %v; want none", len(toks), err)
	}

	if _, _, err := st.UnarchiveProject("nosuch", "human"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UnarchiveProject(nosuch) err = %v, want ErrNotFound", err)
	}
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	maxBefore, _ := st.MaxEventID()
	p, ev, err := st.UnarchiveProject("web", "human")
	if err != nil || ev != nil || p == nil || p.Slug != "web" || p.ArchivedAt != "" {
		t.Fatalf("UnarchiveProject(never archived) = %+v, %+v, %v; want the project and no event", p, ev, err)
	}
	if maxAfter, _ := st.MaxEventID(); maxAfter != maxBefore {
		t.Fatalf("no-op unarchive wrote event %d", maxAfter)
	}
}

// FIND-0251: a body patch persists and emits task.patched with delta
// {"body":true}; patching the same body again is a no-op without an event.
func TestCwfStorePatchTaskBody(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	tk := cwfStoreNewTask(t, st, "web", "t")
	got, ev, err := st.PatchTask(tk.ID, map[string]any{"body": "new body"}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "new body" {
		t.Fatalf("returned body = %q, want new body", got.Body)
	}
	if ev == nil || ev.Kind != "task.patched" || string(ev.Data) != `{"body":true}` {
		t.Fatalf("event = %+v, want task.patched {\"body\":true}", ev)
	}
	if re, err := st.GetTask(tk.ID); err != nil || re.Body != "new body" {
		t.Fatalf("stored body = %+v, %v; want new body", re, err)
	}
	if _, ev, err := st.PatchTask(tk.ID, map[string]any{"body": "new body"}, "alice"); err != nil || ev != nil {
		t.Fatalf("same-body patch = %+v, %v; want no event", ev, err)
	}
	// Clearing the body is a change too.
	if got, ev, err := st.PatchTask(tk.ID, map[string]any{"body": ""}, "alice"); err != nil || ev == nil || got.Body != "" {
		t.Fatalf("clear body = %+v, %+v, %v; want empty body and an event", got, ev, err)
	}
}

// FIND-0254: ListEvents pages by cursor (last is the final returned id, the
// next page starts right after it), keeps limits up to 500, maps <=0 and >500
// to 200, and returns since itself as last on an empty page.
func TestCwfStoreListEventsPaging(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 204; i++ { // 205 events with project.created
		cwfStoreNewTask(t, st, "web", "t"+strconv.Itoa(i))
	}
	maxID, _ := st.MaxEventID()

	page1, last1, err := st.ListEvents(0, "", "", 2)
	if err != nil || len(page1) != 2 || last1 != page1[1].ID {
		t.Fatalf("page 1 = %+v, last %d, %v; want 2 events, last = the second id", page1, last1, err)
	}
	page2, last2, err := st.ListEvents(last1, "", "", 2)
	if err != nil || len(page2) != 2 || page2[0].ID != page1[1].ID+1 || last2 != page2[1].ID {
		t.Fatalf("page 2 after %d = %+v, last %d, %v; want the next 2 events", last1, page2, last2, err)
	}

	for _, c := range []struct{ limit, want int }{{0, 200}, {-1, 200}, {500, 205}, {501, 200}} {
		evs, last, err := st.ListEvents(0, "", "", c.limit)
		if err != nil || len(evs) != c.want || last != evs[c.want-1].ID {
			t.Errorf("ListEvents(limit %d) = %d events, last %d, %v; want %d", c.limit, len(evs), last, err, c.want)
		}
	}
	if evs, last, err := st.ListEvents(maxID, "", "", 0); err != nil || len(evs) != 0 || last != maxID {
		t.Fatalf("ListEvents(since max) = %d events, last %d, %v; want none, last %d", len(evs), last, err, maxID)
	}
}
