package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// cwfStoreTaskIDs lists GET /api/tasks<query> and returns the ids, sorted.
func cwfStoreTaskIDs(t *testing.T, ts *httptest.Server, query string, hdr map[string]string) []string {
	t.Helper()
	r := do(t, ts, http.MethodGet, "/api/tasks"+query, "", hdr)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tasks%s = %d, want 200", query, r.StatusCode)
	}
	var tasks []Task
	if err := json.NewDecoder(r.Body).Decode(&tasks); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	ids := []string{}
	for _, tk := range tasks {
		ids = append(ids, strconv.FormatInt(tk.ID, 10))
	}
	sort.Strings(ids)
	return ids
}

// cwfStoreGetTask fetches one task unscoped.
func cwfStoreGetTask(t *testing.T, ts *httptest.Server, id string) Task {
	t.Helper()
	r := do(t, ts, http.MethodGet, "/api/tasks/"+id, "", nil)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET task %s = %d, want 200", id, r.StatusCode)
	}
	var tk Task
	if err := json.NewDecoder(r.Body).Decode(&tk); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return tk
}

// FIND-0178: bad bodies on the dep/label/comment/create/patch handlers are a
// 400 (or 404 for an unknown prereq) and record no event; the string-ref form
// of depends_on works.
func TestCwfStoreRequestValidation(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	mustCreateProject(t, ts, "web")
	a := mustCreateTask(t, ts, "web", "a")
	b := mustCreateTask(t, ts, "web", "b")
	bRef := "web-" + strconv.FormatInt(cwfStoreGetTask(t, ts, b).Ref, 10)
	hdr := map[string]string{"Content-Type": "application/json"}
	deps, labels, comments := "/api/tasks/"+a+"/deps", "/api/tasks/"+a+"/labels", "/api/tasks/"+a+"/comments"

	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, deps, `{}`, http.StatusBadRequest},
		{http.MethodPost, deps, `{"depends_on":null}`, http.StatusBadRequest},
		{http.MethodPost, deps, `{"depends_on":[]}`, http.StatusBadRequest},
		{http.MethodPost, deps, `{"depends_on":{}}`, http.StatusBadRequest},
		{http.MethodPost, deps, `{"depends_on":true}`, http.StatusBadRequest},
		{http.MethodPost, deps, `{"depends_on":`, http.StatusBadRequest},
		{http.MethodPost, deps, `{"depends_on":"web-99"}`, http.StatusNotFound},
		{http.MethodPost, deps, `{"depends_on":"nope"}`, http.StatusNotFound},
		{http.MethodPost, deps, `{"depends_on":9999}`, http.StatusNotFound},
		{http.MethodPost, labels, `{"label":""}`, http.StatusBadRequest},
		{http.MethodPost, labels, `{}`, http.StatusBadRequest},
		{http.MethodPost, labels, `{"label":`, http.StatusBadRequest},
		{http.MethodPost, comments, `{"body":""}`, http.StatusBadRequest},
		{http.MethodPost, comments, `{"body":"   "}`, http.StatusBadRequest},
		{http.MethodPost, comments, `{"body":`, http.StatusBadRequest},
		{http.MethodPost, "/api/tasks", `{"project":"web"`, http.StatusBadRequest},
		{http.MethodPatch, "/api/tasks/" + a, `{"status":`, http.StatusBadRequest},
	} {
		before, _ := st.MaxEventID()
		if got := cwfStoreStatus(t, ts, c.method, c.path, c.body, hdr); got != c.want {
			t.Errorf("%s %s %s = %d, want %d", c.method, c.path, c.body, got, c.want)
		}
		if after, _ := st.MaxEventID(); after != before {
			t.Errorf("%s %s %s recorded an event (%d -> %d)", c.method, c.path, c.body, before, after)
		}
	}
	if tk := cwfStoreGetTask(t, ts, a); len(tk.DependsOn) != 0 || len(tk.Labels) != 0 || len(tk.Comments) != 0 {
		t.Fatalf("rejected requests changed task a: %+v", tk)
	}

	// The string-ref form agents send resolves to the task.
	if got := cwfStoreStatus(t, ts, http.MethodPost, deps, `{"depends_on":"`+bRef+`"}`, hdr); got != http.StatusOK {
		t.Fatalf("depends_on %q = %d, want 200", bRef, got)
	}
	if tk := cwfStoreGetTask(t, ts, a); len(tk.DependsOn) != 1 || strconv.FormatInt(tk.DependsOn[0].ID, 10) != b {
		t.Fatalf("after depends_on %q, task a depends on %+v, want task %s", bRef, tk.DependsOn, b)
	}
}

// FIND-0179: a scoped project create without a category is checked against
// the store's default ("general").
func TestCwfStoreScopedCreateProjectDefaultCategory(t *testing.T) {
	ts := newTestServer(t)
	scopedBoard(t, ts)

	r := do(t, ts, http.MethodPost, "/api/projects", `{"slug":"gproj"}`, scoped("agent-g", "general"))
	defer r.Body.Close()
	var p Project
	json.NewDecoder(r.Body).Decode(&p)
	if r.StatusCode != http.StatusCreated || p.Category != "general" {
		t.Fatalf("general-scoped create without category = %d %+v, want 201 in general", r.StatusCode, p)
	}

	if got := cwfStoreStatus(t, ts, http.MethodPost, "/api/projects", `{"slug":"pdefault"}`, scoped("agent-p", "personal")); got != http.StatusForbidden {
		t.Fatalf("personal-scoped create without category = %d, want 403", got)
	}
	r2 := do(t, ts, http.MethodGet, "/api/projects", "", nil)
	b, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if strings.Contains(string(b), "pdefault") {
		t.Fatalf("denied create still made the project: %s", b)
	}
}

// FIND-0180: ?blocked, ?assignee and ?limit reach the store filter.
func TestCwfStoreListTasksQueryWiring(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "web")
	prereq := mustCreateTask(t, ts, "web", "prereq")
	blocked := mustCreateTask(t, ts, "web", "blocked")
	assigned := mustCreateTask(t, ts, "web", "assigned")
	hdr := map[string]string{"Content-Type": "application/json"}
	if got := cwfStoreStatus(t, ts, http.MethodPost, "/api/tasks/"+blocked+"/deps", `{"depends_on":`+prereq+`}`, hdr); got != http.StatusOK {
		t.Fatalf("add dep = %d", got)
	}
	if got := cwfStoreStatus(t, ts, http.MethodPatch, "/api/tasks/"+assigned, `{"assignee":"agent-x"}`, hdr); got != http.StatusOK {
		t.Fatalf("assign = %d", got)
	}

	for _, c := range []struct {
		query string
		want  []string
	}{
		{"?blocked=true", []string{blocked}},
		{"?assignee=agent-x", []string{assigned}},
	} {
		if got := cwfStoreTaskIDs(t, ts, c.query, nil); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("GET /api/tasks%s = %v, want %v", c.query, got, c.want)
		}
	}
	if got := cwfStoreTaskIDs(t, ts, "?limit=1", nil); len(got) != 1 {
		t.Errorf("GET /api/tasks?limit=1 = %v, want one task", got)
	}
}

// FIND-0184: token mint/revoke failure paths answer 400/404 and leave the
// token list unchanged; a repeat revoke is a 404.
func TestCwfStoreTokenAdminErrors(t *testing.T) {
	ts := newTestServer(t)
	scopedBoard(t, ts)
	mustCreateCategory(t, ts, "old")
	if got := cwfStoreStatus(t, ts, http.MethodPost, "/api/categories/old/archive", "", nil); got != http.StatusOK {
		t.Fatalf("archive category = %d", got)
	}
	mintToken(t, ts, "work")
	listTokens := func() string {
		r := do(t, ts, http.MethodGet, "/api/tokens", "", nil)
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/tokens = %d", r.StatusCode)
		}
		b, _ := io.ReadAll(r.Body)
		return string(b)
	}
	baseline := listTokens()
	var toks []Token
	if err := json.Unmarshal([]byte(baseline), &toks); err != nil || len(toks) != 1 {
		t.Fatalf("token list = %s (%v), want one token", baseline, err)
	}

	hdr := map[string]string{"Content-Type": "application/json"}
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"scope":`, http.StatusBadRequest},
		{`{"scope":""}`, http.StatusBadRequest},
		{`{"scope":"a/b/c"}`, http.StatusBadRequest},
		{`{"scope":"nosuchcat"}`, http.StatusNotFound},
		{`{"scope":"old"}`, http.StatusBadRequest},
		{`{"scope":"work/nosuch"}`, http.StatusNotFound},
		{`{"scope":"work/pproj"}`, http.StatusBadRequest}, // pproj lives in personal
	} {
		if got := cwfStoreStatus(t, ts, http.MethodPost, "/api/tokens", c.body, hdr); got != c.want {
			t.Errorf("POST /api/tokens %s = %d, want %d", c.body, got, c.want)
		}
	}
	if got := listTokens(); got != baseline {
		t.Fatalf("failed mints changed the token list:\n%s\nwant\n%s", got, baseline)
	}

	if got := cwfStoreStatus(t, ts, http.MethodPost, "/api/tokens/tk_nope/revoke", "", nil); got != http.StatusNotFound {
		t.Errorf("revoke unknown token = %d, want 404", got)
	}
	revoke := "/api/tokens/" + toks[0].ID + "/revoke"
	if got := cwfStoreStatus(t, ts, http.MethodPost, revoke, "", nil); got != http.StatusOK {
		t.Fatalf("revoke = %d, want 200", got)
	}
	afterRevoke := listTokens()
	if got := cwfStoreStatus(t, ts, http.MethodPost, revoke, "", nil); got != http.StatusNotFound {
		t.Errorf("repeat revoke = %d, want 404", got)
	}
	if got := listTokens(); got != afterRevoke {
		t.Errorf("repeat revoke changed the token list:\n%s\nwant\n%s", got, afterRevoke)
	}
}

// FIND-0184: a store failure on GET /api/tokens is a 500, not an empty list.
func TestCwfStoreListTokensStoreError(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	st.Close()
	if got := cwfStoreStatus(t, ts, http.MethodGet, "/api/tokens", "", nil); got != http.StatusInternalServerError {
		t.Fatalf("GET /api/tokens on a closed store = %d, want 500", got)
	}
}

// FIND-0208: the proposals carve-out does not extend to next, and an unknown
// explicit project under a scope is a 403 on next and on lists.
func TestCwfStoreNarrowScopeContract(t *testing.T) {
	ts := newTestServer(t)
	scopedBoard(t, ts)
	mustCreateProposals(t, ts)
	propID := mustCreateTask(t, ts, "proposals", "a proposal")
	hdr := scoped("agent-p", "personal")

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/tasks/next", `{"project":"proposals"}`},
		{http.MethodPost, "/api/tasks/next", `{"project":"nosuch"}`},
		{http.MethodGet, "/api/tasks?project=nosuch", ""},
	} {
		if got := cwfStoreStatus(t, ts, c.method, c.path, c.body, hdr); got != http.StatusForbidden {
			t.Errorf("personal scope %s %s %s = %d, want 403", c.method, c.path, c.body, got)
		}
	}
	if tk := cwfStoreGetTask(t, ts, propID); tk.Status != "todo" || tk.Assignee != "" {
		t.Fatalf("proposal after denied next = %s/%q, want todo/unassigned", tk.Status, tk.Assignee)
	}
}
