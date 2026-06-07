package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := openTestStore(t)
	ts := httptest.NewServer(NewServer(store).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// do issues a request against ts with optional headers, returning the response.
func do(t *testing.T, ts *httptest.Server, method, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestGetMissingTask404(t *testing.T) {
	ts := newTestServer(t)
	resp := do(t, ts, http.MethodGet, "/api/tasks/999", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET missing task = %d, want 404", resp.StatusCode)
	}
}

func TestCreateTaskEmptyTitle400(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "web")
	resp := do(t, ts, http.MethodPost, "/api/tasks",
		`{"project":"web","title":""}`, map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST empty title = %d, want 400", resp.StatusCode)
	}
}

func TestLostClaim409(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "web")
	id := mustCreateTask(t, ts, "web", "Claimable")

	// First claim by agent-a wins.
	r1 := do(t, ts, http.MethodPost, "/api/tasks/"+id+"/claim", "",
		map[string]string{"X-Agent": "agent-a"})
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first claim = %d, want 200", r1.StatusCode)
	}
	// Second claim by agent-b loses.
	r2 := do(t, ts, http.MethodPost, "/api/tasks/"+id+"/claim", "",
		map[string]string{"X-Agent": "agent-b"})
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusConflict {
		t.Fatalf("lost claim = %d, want 409", r2.StatusCode)
	}
}

func TestHostGuard(t *testing.T) {
	ts := newTestServer(t)
	cases := []struct {
		host    string
		want403 bool
	}{
		{"evil.com", true},
		{"evil.com:8787", true},
		{"127.0.0.1:8787", false},
		{"localhost:8787", false},
		{"127.0.0.1", false},
		{"localhost", false},
	}
	for _, c := range cases {
		// The Host header is set via req.Host (Go's client takes the Host from the
		// URL otherwise and ignores a Host entry in req.Header).
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/projects", nil)
		req.Host = c.host
		r, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("Host=%q: %v", c.host, err)
		}
		got403 := r.StatusCode == http.StatusForbidden
		r.Body.Close()
		if got403 != c.want403 {
			t.Fatalf("Host=%q status=%d want403=%v", c.host, r.StatusCode, c.want403)
		}
	}
}

func TestCSRFGuard(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "web")

	// Cross-site write → 403.
	r := do(t, ts, http.MethodPost, "/api/tasks",
		`{"project":"web","title":"x"}`,
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	gotCross := r.StatusCode
	r.Body.Close()
	if gotCross != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", gotCross)
	}

	// CLI-like write: no Origin / no Sec-Fetch headers → allowed (not 403).
	r = do(t, ts, http.MethodPost, "/api/tasks",
		`{"project":"web","title":"cli task"}`, nil)
	gotCLI := r.StatusCode
	r.Body.Close()
	if gotCLI == http.StatusForbidden {
		t.Fatalf("CLI-like POST = 403, want allowed")
	}

	// Same-origin browser write → allowed.
	r = do(t, ts, http.MethodPost, "/api/tasks",
		`{"project":"web","title":"same origin task"}`,
		map[string]string{"Sec-Fetch-Site": "same-origin"})
	gotSame := r.StatusCode
	r.Body.Close()
	if gotSame == http.StatusForbidden {
		t.Fatalf("same-origin POST = 403, want allowed")
	}

	// GET read is never blocked by csrf even with a cross-site hint.
	r = do(t, ts, http.MethodGet, "/api/events",
		"", map[string]string{"Sec-Fetch-Site": "cross-site"})
	gotGet := r.StatusCode
	r.Body.Close()
	if gotGet == http.StatusForbidden {
		t.Fatalf("cross-site GET = 403, want allowed (csrf must not block reads)")
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts := newTestServer(t)
	resp := do(t, ts, http.MethodGet, "/api/projects", "", nil)
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("missing Content-Security-Policy header")
	}
	if !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("CSP missing inline-style allowance: %q", csp)
	}
}

func TestListenAddrLoopback(t *testing.T) {
	for _, port := range []string{"8787", "8899", "0", ""} {
		if got := listenAddr(port); !strings.HasPrefix(got, "127.0.0.1:") {
			t.Fatalf("listenAddr(%q) = %q, want 127.0.0.1: prefix", port, got)
		}
	}
}

func TestArchiveUnarchiveEndpoints(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "archivedemo")

	// Initially visible in default list
	r := do(t, ts, http.MethodGet, "/api/projects", "", nil)
	defer r.Body.Close()
	var ps []Project
	if err := json.NewDecoder(r.Body).Decode(&ps); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	found := false
	for _, p := range ps {
		if p.Slug == "archivedemo" {
			found = true
		}
	}
	if !found {
		t.Fatal("project should appear in default list before archive")
	}

	// Archive it
	r2 := do(t, ts, http.MethodPost, "/api/projects/archivedemo/archive", "", nil)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d, want 200", r2.StatusCode)
	}
	var archived Project
	if err := json.NewDecoder(r2.Body).Decode(&archived); err != nil {
		t.Fatalf("decode archived project: %v", err)
	}
	if archived.ArchivedAt == "" {
		t.Error("archived_at should be set after archive")
	}

	// No longer visible in default list
	r3 := do(t, ts, http.MethodGet, "/api/projects", "", nil)
	defer r3.Body.Close()
	var ps2 []Project
	if err := json.NewDecoder(r3.Body).Decode(&ps2); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	for _, p := range ps2 {
		if p.Slug == "archivedemo" {
			t.Fatal("archived project should not appear in default list")
		}
	}

	// Visible with ?archived=true
	r4 := do(t, ts, http.MethodGet, "/api/projects?archived=true", "", nil)
	defer r4.Body.Close()
	var ps3 []Project
	if err := json.NewDecoder(r4.Body).Decode(&ps3); err != nil {
		t.Fatalf("decode projects all: %v", err)
	}
	found = false
	for _, p := range ps3 {
		if p.Slug == "archivedemo" {
			found = true
		}
	}
	if !found {
		t.Fatal("archived project should appear in ?archived=true list")
	}

	// Unarchive it
	r5 := do(t, ts, http.MethodPost, "/api/projects/archivedemo/unarchive", "", nil)
	defer r5.Body.Close()
	if r5.StatusCode != http.StatusOK {
		t.Fatalf("unarchive = %d, want 200", r5.StatusCode)
	}
	var unarchived Project
	if err := json.NewDecoder(r5.Body).Decode(&unarchived); err != nil {
		t.Fatalf("decode unarchived project: %v", err)
	}
	if unarchived.ArchivedAt != "" {
		t.Error("archived_at should be empty after unarchive")
	}

	// Visible again in default list
	r6 := do(t, ts, http.MethodGet, "/api/projects", "", nil)
	defer r6.Body.Close()
	var ps4 []Project
	if err := json.NewDecoder(r6.Body).Decode(&ps4); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	found = false
	for _, p := range ps4 {
		if p.Slug == "archivedemo" {
			found = true
		}
	}
	if !found {
		t.Fatal("unarchived project should appear in default list again")
	}

	// 404 on missing project
	r7 := do(t, ts, http.MethodPost, "/api/projects/nosuchproject/archive", "", nil)
	defer r7.Body.Close()
	if r7.StatusCode != http.StatusNotFound {
		t.Fatalf("archive missing project = %d, want 404", r7.StatusCode)
	}
}

func TestCreateTaskIntoArchivedProject400(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "archproj")

	// Archive the project.
	r := do(t, ts, http.MethodPost, "/api/projects/archproj/archive", "", nil)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d, want 200", r.StatusCode)
	}

	// Attempt to create a task into the archived project — must get 400.
	resp := do(t, ts, http.MethodPost, "/api/tasks",
		`{"project":"archproj","title":"blocked task"}`,
		map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/tasks into archived project = %d, want 400", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body["error"] != "project_archived" {
		t.Fatalf("error body = %q, want project_archived", body["error"])
	}
}

func TestDeleteTaskEndpoint(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "delproj")
	id := mustCreateTask(t, ts, "delproj", "Delete me")

	// First DELETE → 200.
	r := do(t, ts, http.MethodDelete, "/api/tasks/"+id, "", nil)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("DELETE task = %d, want 200", r.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if body["status"] != "deleted" {
		t.Fatalf("delete body status = %q, want deleted", body["status"])
	}

	// GET after delete → 404.
	r2 := do(t, ts, http.MethodGet, "/api/tasks/"+id, "", nil)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("GET deleted task = %d, want 404", r2.StatusCode)
	}

	// Second DELETE → 404.
	r3 := do(t, ts, http.MethodDelete, "/api/tasks/"+id, "", nil)
	defer r3.Body.Close()
	if r3.StatusCode != http.StatusNotFound {
		t.Fatalf("re-DELETE task = %d, want 404", r3.StatusCode)
	}
}

func TestDeleteProjectEndpoint(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "toberemoved")
	_ = mustCreateTask(t, ts, "toberemoved", "task inside")

	// DELETE project → 200.
	r := do(t, ts, http.MethodDelete, "/api/projects/toberemoved", "", nil)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("DELETE project = %d, want 200", r.StatusCode)
	}

	// Project no longer in list.
	r2 := do(t, ts, http.MethodGet, "/api/projects?archived=true", "", nil)
	defer r2.Body.Close()
	var ps []Project
	if err := json.NewDecoder(r2.Body).Decode(&ps); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	for _, p := range ps {
		if p.Slug == "toberemoved" {
			t.Fatal("deleted project still in list")
		}
	}

	// DELETE again → 404.
	r3 := do(t, ts, http.MethodDelete, "/api/projects/toberemoved", "", nil)
	defer r3.Body.Close()
	if r3.StatusCode != http.StatusNotFound {
		t.Fatalf("re-DELETE project = %d, want 404", r3.StatusCode)
	}
}

func TestDeleteCommentEndpoint(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "cmproj")
	id := mustCreateTask(t, ts, "cmproj", "Has comments")
	otherID := mustCreateTask(t, ts, "cmproj", "Other task")

	// Add a comment to the first task, capture its id.
	r := do(t, ts, http.MethodPost, "/api/tasks/"+id+"/comments",
		`{"body":"to be deleted"}`,
		map[string]string{"Content-Type": "application/json"})
	defer r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create comment = %d, want 201", r.StatusCode)
	}
	var cm Comment
	if err := json.NewDecoder(r.Body).Decode(&cm); err != nil {
		t.Fatalf("decode comment: %v", err)
	}
	cid := strconv.FormatInt(cm.ID, 10)

	// Wrong task id: the comment exists but does not belong to otherID → 404.
	rw := do(t, ts, http.MethodDelete, "/api/tasks/"+otherID+"/comments/"+cid, "", nil)
	defer rw.Body.Close()
	if rw.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE comment under wrong task = %d, want 404", rw.StatusCode)
	}

	// Non-existent comment id on the correct task → 404.
	rn := do(t, ts, http.MethodDelete, "/api/tasks/"+id+"/comments/999999", "", nil)
	defer rn.Body.Close()
	if rn.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE non-existent comment = %d, want 404", rn.StatusCode)
	}

	// Valid delete → 200 {"status":"deleted"}.
	rd := do(t, ts, http.MethodDelete, "/api/tasks/"+id+"/comments/"+cid, "", nil)
	defer rd.Body.Close()
	if rd.StatusCode != http.StatusOK {
		t.Fatalf("DELETE comment = %d, want 200", rd.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(rd.Body).Decode(&body); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if body["status"] != "deleted" {
		t.Fatalf("delete body status = %q, want deleted", body["status"])
	}

	// Task still exists; its comment is gone.
	rg := do(t, ts, http.MethodGet, "/api/tasks/"+id, "", nil)
	defer rg.Body.Close()
	if rg.StatusCode != http.StatusOK {
		t.Fatalf("GET task after comment delete = %d, want 200", rg.StatusCode)
	}
	var task Task
	if err := json.NewDecoder(rg.Body).Decode(&task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if len(task.Comments) != 0 {
		t.Fatalf("task still has %d comments, want 0", len(task.Comments))
	}

	// Re-delete the now-missing comment → 404.
	rr := do(t, ts, http.MethodDelete, "/api/tasks/"+id+"/comments/"+cid, "", nil)
	defer rr.Body.Close()
	if rr.StatusCode != http.StatusNotFound {
		t.Fatalf("re-DELETE comment = %d, want 404", rr.StatusCode)
	}
}

func TestEventsBeforeEndpoint(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "evproj")

	// Create tasks to generate events.
	for _, title := range []string{"a", "b", "c"} {
		mustCreateTask(t, ts, "evproj", title)
	}

	// Fetch all events via ?tail= to get their IDs.
	r := do(t, ts, http.MethodGet, "/api/events?tail=50", "", nil)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET events tail = %d, want 200", r.StatusCode)
	}
	var tailResp struct {
		Events []struct {
			ID int64 `json:"id"`
		} `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&tailResp); err != nil {
		t.Fatalf("decode tail response: %v", err)
	}
	if len(tailResp.Events) < 2 {
		t.Fatalf("expected >=2 events from tail, got %d", len(tailResp.Events))
	}
	// tailResp.Events is newest-first; the last entry is the oldest.
	oldestID := tailResp.Events[len(tailResp.Events)-1].ID
	newestID := tailResp.Events[0].ID

	// ?before=<newestID> should return events with id < newestID, newest-first.
	r2 := do(t, ts, http.MethodGet, "/api/events?before="+strconv.FormatInt(newestID, 10), "", nil)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("GET events before = %d, want 200", r2.StatusCode)
	}
	var beforeResp struct {
		Events []struct {
			ID int64 `json:"id"`
		} `json:"events"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&beforeResp); err != nil {
		t.Fatalf("decode before response: %v", err)
	}
	for _, e := range beforeResp.Events {
		if e.ID >= newestID {
			t.Errorf("?before=%d returned event id %d >= cutoff", newestID, e.ID)
		}
	}
	// Results must be newest-first (descending).
	for i := 1; i < len(beforeResp.Events); i++ {
		if beforeResp.Events[i].ID >= beforeResp.Events[i-1].ID {
			t.Fatalf("events not descending: %d then %d", beforeResp.Events[i-1].ID, beforeResp.Events[i].ID)
		}
	}

	// ?before=<oldestID> should return nothing (no events older than the first).
	r3 := do(t, ts, http.MethodGet, "/api/events?before="+strconv.FormatInt(oldestID, 10), "", nil)
	defer r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("GET events before oldest = %d, want 200", r3.StatusCode)
	}
	var emptyResp struct {
		Events []struct {
			ID int64 `json:"id"`
		} `json:"events"`
	}
	if err := json.NewDecoder(r3.Body).Decode(&emptyResp); err != nil {
		t.Fatalf("decode empty before response: %v", err)
	}
	if len(emptyResp.Events) != 0 {
		t.Fatalf("expected 0 events before oldest id, got %d", len(emptyResp.Events))
	}
}

// ===================== dependency endpoint tests =====================

func TestAddDepEndpoint(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "depproj")
	id1 := mustCreateTask(t, ts, "depproj", "Prereq task")
	id2 := mustCreateTask(t, ts, "depproj", "Dependent task")

	// POST /api/tasks/<id2>/deps with {depends_on: <id1_num>}
	r := do(t, ts, http.MethodPost, "/api/tasks/"+id2+"/deps",
		`{"depends_on":`+id1+`}`,
		map[string]string{"Content-Type": "application/json"})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("POST deps = %d, want 200", r.StatusCode)
	}

	// GET task2 should now show depends_on.
	rg := do(t, ts, http.MethodGet, "/api/tasks/"+id2, "", nil)
	defer rg.Body.Close()
	var task Task
	if err := json.NewDecoder(rg.Body).Decode(&task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if len(task.DependsOn) != 1 {
		t.Fatalf("task.DependsOn = %v, want 1 entry", task.DependsOn)
	}
}

func TestAddDepCycleEndpoint(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "cyc")
	id1 := mustCreateTask(t, ts, "cyc", "T1")
	id2 := mustCreateTask(t, ts, "cyc", "T2")

	// id2 depends on id1
	r1 := do(t, ts, http.MethodPost, "/api/tasks/"+id2+"/deps",
		`{"depends_on":`+id1+`}`,
		map[string]string{"Content-Type": "application/json"})
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first dep = %d, want 200", r1.StatusCode)
	}

	// id1 depends on id2 → cycle → 400
	r2 := do(t, ts, http.MethodPost, "/api/tasks/"+id1+"/deps",
		`{"depends_on":`+id2+`}`,
		map[string]string{"Content-Type": "application/json"})
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Fatalf("cycle dep = %d, want 400", r2.StatusCode)
	}
}

func TestRemoveDepEndpoint(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "rmproj")
	id1 := mustCreateTask(t, ts, "rmproj", "Prereq")
	id2 := mustCreateTask(t, ts, "rmproj", "Dep task")

	// Add the dep.
	r := do(t, ts, http.MethodPost, "/api/tasks/"+id2+"/deps",
		`{"depends_on":`+id1+`}`,
		map[string]string{"Content-Type": "application/json"})
	r.Body.Close()

	// Remove it.
	rd := do(t, ts, http.MethodDelete, "/api/tasks/"+id2+"/deps/"+id1, "", nil)
	defer rd.Body.Close()
	if rd.StatusCode != http.StatusOK {
		t.Fatalf("DELETE dep = %d, want 200", rd.StatusCode)
	}

	// Verify it's gone.
	rg := do(t, ts, http.MethodGet, "/api/tasks/"+id2, "", nil)
	defer rg.Body.Close()
	var task Task
	if err := json.NewDecoder(rg.Body).Decode(&task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if len(task.DependsOn) != 0 {
		t.Fatalf("DependsOn after remove = %v, want empty", task.DependsOn)
	}
}

func TestClaimBlockedEndpoint(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "blkproj")
	id1 := mustCreateTask(t, ts, "blkproj", "Prereq")
	id2 := mustCreateTask(t, ts, "blkproj", "Blocked task")

	// Add dep: id2 depends on id1.
	r := do(t, ts, http.MethodPost, "/api/tasks/"+id2+"/deps",
		`{"depends_on":`+id1+`}`,
		map[string]string{"Content-Type": "application/json"})
	r.Body.Close()

	// Attempt to claim id2 → 409 blocked.
	rc := do(t, ts, http.MethodPost, "/api/tasks/"+id2+"/claim", "",
		map[string]string{"X-Agent": "agent-x"})
	defer rc.Body.Close()
	if rc.StatusCode != http.StatusConflict {
		t.Fatalf("claim blocked task = %d, want 409", rc.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(rc.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "blocked" {
		t.Fatalf("error = %q, want blocked", body["error"])
	}
	if _, ok := body["open_prereqs"]; !ok {
		t.Fatal("response missing open_prereqs field")
	}
}

// ---------- helpers ----------

func mustCreateProject(t *testing.T, ts *httptest.Server, slug string) {
	t.Helper()
	resp := do(t, ts, http.MethodPost, "/api/projects",
		`{"slug":"`+slug+`","name":"`+slug+`"}`,
		map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project %q = %d, want 201", slug, resp.StatusCode)
	}
}

func mustCreateTask(t *testing.T, ts *httptest.Server, project, title string) string {
	t.Helper()
	resp := do(t, ts, http.MethodPost, "/api/tasks",
		`{"project":"`+project+`","title":"`+title+`"}`,
		map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task = %d, want 201", resp.StatusCode)
	}
	var tk struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tk); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return strconv.FormatInt(tk.ID, 10)
}

// ---------- D1: writeErr hides internal detail ----------

func TestWriteErrHidesInternalDetail(t *testing.T) {
	secret := "secret SQL detail /Users/x/agentman.db"

	// Default branch: unknown error → 500 with generic body, no secret leaked.
	rec := httptest.NewRecorder()
	writeErr(rec, errors.New(secret))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != `{"error":"internal"}` {
		t.Fatalf("body = %q, want {\"error\":\"internal\"}", body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("response body leaked secret: %q", body)
	}

	// Sentinel case: ErrNotFound must still map to 404 / not_found.
	rec2 := httptest.NewRecorder()
	writeErr(rec2, ErrNotFound)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("ErrNotFound status = %d, want 404", rec2.Code)
	}
	body2 := strings.TrimSpace(rec2.Body.String())
	if body2 != `{"error":"not_found"}` {
		t.Fatalf("ErrNotFound body = %q, want {\"error\":\"not_found\"}", body2)
	}
}

// ---------- D2: requestLogger middleware ----------

func TestRequestLoggerPassesThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("hello"))
	})
	handler := requestLogger(inner)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
	if got := rec.Body.String(); got != "hello" {
		t.Fatalf("body = %q, want hello", got)
	}
}

func TestRequestLoggerPreservesFlusher(t *testing.T) {
	store := openTestStore(t)
	srv := NewServer(store)
	srv.logRequests = true
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (streaming unsupported if 500)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// Read lines until we see "retry:" (proving SSE started) or an error.
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 4096))
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "retry:") {
			found = true
			break
		}
	}
	cancel() // stop the SSE stream
	if !found {
		t.Fatal("never received retry: line from SSE stream — flusher may not be preserved")
	}
}
