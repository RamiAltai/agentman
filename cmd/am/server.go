package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	store       *Store
	hub         *Hub
	logRequests bool
	// proposals names the carve-out project (default meta/proposals): task
	// creation there — and commenting on one's OWN tasks there — is allowed
	// from any scope, so scoped agents can always file proposals (R4).
	proposals Scope
	// commitMu spans each mutation's commit and its Broadcast (see publish).
	commitMu sync.Mutex
}

func NewServer(store *Store) *Server {
	return &Server{store: store, hub: NewHub(), proposals: Scope{Category: "meta", Project: "proposals"}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/categories", s.handleListCategories)
	mux.HandleFunc("POST /api/categories", s.handleCreateCategory)
	mux.HandleFunc("POST /api/categories/{slug}/archive", s.handleArchiveCategory)
	mux.HandleFunc("POST /api/categories/{slug}/unarchive", s.handleUnarchiveCategory)
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
	mux.HandleFunc("PATCH /api/projects/{slug}", s.handlePatchProject)
	mux.HandleFunc("POST /api/projects/{slug}/archive", s.handleArchiveProject)
	mux.HandleFunc("POST /api/projects/{slug}/unarchive", s.handleUnarchiveProject)
	mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /api/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.handlePatchTask)
	mux.HandleFunc("POST /api/tasks/next", s.handleNext)
	mux.HandleFunc("POST /api/tasks/{id}/claim", s.handleClaim)
	mux.HandleFunc("POST /api/tasks/{id}/comments", s.handleComment)
	mux.HandleFunc("POST /api/tasks/{id}/deps", s.handleAddDep)
	mux.HandleFunc("DELETE /api/tasks/{id}/deps/{depId}", s.handleRemoveDep)
	mux.HandleFunc("POST /api/tasks/{id}/labels", s.handleAddLabel)
	mux.HandleFunc("DELETE /api/tasks/{id}/labels/{label}", s.handleRemoveLabel)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDeleteTask)
	mux.HandleFunc("DELETE /api/tasks/{id}/comments/{cid}", s.handleDeleteComment)
	mux.HandleFunc("DELETE /api/projects/{slug}", s.handleDeleteProject)
	mux.HandleFunc("GET /api/projects/{slug}/graph", s.handleProjectGraph)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("POST /api/tokens", s.handleCreateToken)
	mux.HandleFunc("GET /api/tokens", s.handleListTokens)
	mux.HandleFunc("POST /api/tokens/{id}/revoke", s.handleRevokeToken)

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// Loopback browser hardening. securityHeaders is outermost so its headers
	// (and nosniff) also apply to the 403s emitted by hostGuard/csrfGuard.
	// Order of checks: host first (cheapest reject), then CSRF, then the mux.
	handler := http.Handler(securityHeaders(hostGuard(csrfGuard(mux))))
	if s.logRequests {
		handler = requestLogger(handler)
	}
	return handler
}

// ---------- security middleware ----------

// hostGuard blocks DNS-rebinding by requiring a loopback Host header. The CLI and
// the dashboard's EventSource/fetch both send 127.0.0.1:port or localhost:port,
// so they pass; a rebound attacker host (e.g. evil.com) is rejected. Applies to
// every method, including GET/SSE.
func hostGuard(next http.Handler) http.Handler {
	allowed := map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true, "": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !allowed[host] {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden_host"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrfGuard blocks cross-origin browser writes while allowing the header-less CLI
// and same-origin dashboard. It only inspects state-changing methods; GET/HEAD/
// OPTIONS (incl. SSE) pass untouched. A request is rejected if Sec-Fetch-Site is
// present and cross-origin, or if an Origin header's host differs from Host.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross_origin"})
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross_origin"})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets nosniff and a CSP that still permits the dashboard's
// external /app.js + /app.css, inline style attributes (set via el(...,{style:…})),
// same-origin fetch/EventSource, and data: emoji/img. style-src 'unsafe-inline'
// is required for the inline style attributes; dropping it breaks board styling.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; " +
		"img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code written
// by the handler. It also implements http.Flusher so SSE connections (which do
// w.(http.Flusher).Flush()) continue to work when logging is enabled.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Flush delegates to the underlying writer if it supports http.Flusher.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requestLogger is middleware that logs method, path, status, latency, and
// actor for every request. It is only installed when s.logRequests is true.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s %s", r.Method, r.URL.Path, rec.status, time.Since(start), actorOf(r))
	})
}

// ---------- handlers ----------

func (s *Server) handleListCategories(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("archived") == "true"
	// Stats (task counts + recently-active agents, 30-min window) are always
	// folded into the payload — the dashboard's category-home view needs them and
	// there is no scope enforcement here (the dashboard is unscoped: a human sees
	// everything; category view is a query-param choice, not an identity scope).
	cs, err := s.store.ListCategoriesWithStats(includeArchived, 30*time.Minute)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	if !s.unscopedGuard(w, r) { // the category layer is above every scope
		return
	}
	var in struct{ Slug, Name string }
	if err := decode(r, &in); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	var c *Category
	if err := s.publish(func() (ev *Event, err error) {
		c, ev, err = s.store.CreateCategory(in.Slug, in.Name)
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleArchiveCategory(w http.ResponseWriter, r *http.Request) {
	if !s.unscopedGuard(w, r) {
		return
	}
	var c *Category
	if err := s.publish(func() (ev *Event, err error) {
		c, ev, err = s.store.ArchiveCategory(r.PathValue("slug"), actorOf(r))
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleUnarchiveCategory(w http.ResponseWriter, r *http.Request) {
	if !s.unscopedGuard(w, r) {
		return
	}
	var c *Category
	if err := s.publish(func() (ev *Event, err error) {
		c, ev, err = s.store.UnarchiveCategory(r.PathValue("slug"), actorOf(r))
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	includeArchived := q.Get("archived") == "true"
	ps, err := s.store.ListProjects(includeArchived, q.Get("category"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !s.mutableProject(w, r, slug) {
		return
	}
	var patch map[string]any
	if err := decode(r, &patch); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	var p *Project
	if err := s.publish(func() (ev *Event, err error) {
		p, ev, err = s.store.PatchProject(slug, patch, actorOf(r))
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleArchiveProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !s.mutableProject(w, r, slug) {
		return
	}
	var p *Project
	if err := s.publish(func() (ev *Event, err error) {
		p, ev, err = s.store.ArchiveProject(slug, actorOf(r))
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUnarchiveProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !s.mutableProject(w, r, slug) {
		return
	}
	var p *Project
	if err := s.publish(func() (ev *Event, err error) {
		p, ev, err = s.store.UnarchiveProject(slug, actorOf(r))
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct{ Slug, Name, Category string }
	if err := decode(r, &in); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	// A category-scoped agent may create projects inside its OWN category; a
	// project-scoped one may not create projects at all. Compare against the
	// effective category (empty defaults to "general" in the store).
	if !sc.IsZero() {
		cat := in.Category
		if cat == "" {
			cat = "general"
		}
		if sc.Project != "" || cat != sc.Category {
			writeErr(w, denyScope(r, sc))
			return
		}
	}
	// Empty category defaults to "general" in the store — keeps the dashboard's
	// existing {slug,name} POST working unchanged.
	var p *Project
	if err := s.publish(func() (ev *Event, err error) {
		p, ev, err = s.store.CreateProjectAs(in.Slug, in.Name, in.Category, actorOf(r))
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// maxTaskListLimit caps GET /api/tasks. It equals the largest page any
// first-party client asks for (the dashboard's limit=500; the CLI sends 50).
const maxTaskListLimit = 500

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := TaskFilter{
		Project:  q.Get("project"),
		Category: q.Get("category"),
		Status:   q.Get("status"),
		Assignee: q.Get("assignee"),
		Label:    q.Get("label"),    // store validates/normalizes
		MetaKey:  q.Get("meta_key"), // store validates/normalizes
		Limit:    atoiDefault(q.Get("limit"), 0),
		Ready:    q.Get("ready") == "true",
		Blocked:  q.Get("blocked") == "true",
	}
	if v := q.Get("q"); v != "" {
		if len(v) > maxTitleLen { // cap search input like titles (bounds LIKE work)
			writeErr(w, ErrValidation)
			return
		}
		f.Query = v
	}
	if v := q.Get("stale"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			writeErr(w, ErrValidation)
			return
		}
		f.Stale = d
	}
	sc, err := s.scopeOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Silent narrowing for unfiltered lists, loud 403 for explicit
	// out-of-scope filters; ?project=<proposals> stays readable. This is also
	// what scopes `am wait --ready`'s REST re-check.
	if f.Project, f.Category, err = s.narrowScope(r, sc, f.Project, f.Category, true); err != nil {
		writeErr(w, err)
		return
	}
	// Absent, non-positive or oversized limits all mean the cap: the whole
	// result is built and encoded in memory on the single DB connection.
	if f.Limit <= 0 || f.Limit > maxTaskListLimit {
		f.Limit = maxTaskListLimit
	}
	ts, err := s.store.ListTasks(f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) handleAddDep(w http.ResponseWriter, r *http.Request) {
	// One check on the dependent task suffices: the store's same-project rule
	// already forces the prereq into the same (in-scope) project.
	taskID, ok := s.mutableTask(w, r)
	if !ok {
		return
	}
	var in struct {
		DependsOn any `json:"depends_on"` // accept string ref or number
	}
	if err := decode(r, &in); err != nil || in.DependsOn == nil {
		writeErr(w, ErrValidation)
		return
	}
	// Resolve depends_on — may be a numeric id or a string ref like "web-3".
	var depIDStr string
	switch v := in.DependsOn.(type) {
	case float64:
		depIDStr = strconv.FormatInt(int64(v), 10)
	case string:
		depIDStr = v
	default:
		writeErr(w, ErrValidation)
		return
	}
	depID, err := s.store.resolveTaskID(depIDStr)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.publish(func() (*Event, error) { return s.store.AddDep(taskID, depID, actorOf(r)) }); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRemoveDep(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.mutableTask(w, r)
	if !ok {
		return
	}
	depID, err := s.store.resolveTaskID(r.PathValue("depId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.publish(func() (*Event, error) { return s.store.RemoveDep(taskID, depID, actorOf(r)) }); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAddLabel(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.mutableTask(w, r)
	if !ok {
		return
	}
	var in struct {
		Label string `json:"label"`
	}
	if err := decode(r, &in); err != nil || in.Label == "" {
		writeErr(w, ErrValidation)
		return
	}
	if err := s.publish(func() (*Event, error) { return s.store.AddLabel(taskID, in.Label, actorOf(r)) }); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRemoveLabel(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.mutableTask(w, r)
	if !ok {
		return
	}
	if err := s.publish(func() (*Event, error) {
		return s.store.RemoveLabel(taskID, r.PathValue("label"), actorOf(r))
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project  string            `json:"project"`
		Title    string            `json:"title"`
		Body     string            `json:"body"`
		Assignee string            `json:"assignee"`
		Priority *int              `json:"priority"`
		Meta     map[string]string `json:"meta"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	sc, err := s.scopeOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.checkCreate(r, sc, in.Project); err != nil {
		writeErr(w, err)
		return
	}
	pr := 2
	if in.Priority != nil {
		pr = *in.Priority
	}
	var t *Task
	if err := s.publish(func() (ev *Event, err error) {
		t, ev, err = s.store.CreateTask(CreateTaskInput{
			Project: in.Project, Title: in.Title, Body: in.Body,
			Priority: pr, Assignee: in.Assignee, Actor: actorOf(r), Meta: in.Meta,
		})
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	sc, err := s.scopeOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.checkTaskRead(r, sc, id); err != nil {
		writeErr(w, err)
		return
	}
	t, err := s.store.GetTask(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handlePatchTask(w http.ResponseWriter, r *http.Request) {
	// Covers status/assign/edit/drop AND each id of a bulk status/assign —
	// the CLI loops per id, so partial-failure semantics fall out for free.
	id, ok := s.mutableTask(w, r)
	if !ok {
		return
	}
	var patch map[string]any
	if err := decode(r, &patch); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	var t *Task
	if err := s.publish(func() (ev *Event, err error) {
		t, ev, err = s.store.PatchTask(id, patch, actorOf(r))
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	// One check covers both claim AND steal_stale — stealing is scope-checked
	// exactly like a claim (R4).
	id, ok := s.mutableTask(w, r)
	if !ok {
		return
	}
	agent := actorOf(r)
	var in struct {
		Assignee   string `json:"assignee"`
		StealStale string `json:"steal_stale"`
	}
	if err := decode(r, &in); err != nil { // an empty body is fine; malformed JSON is not
		writeErr(w, ErrValidation)
		return
	}
	if in.Assignee != "" {
		agent = in.Assignee
	}
	var stale time.Duration
	if in.StealStale != "" {
		d, perr := time.ParseDuration(in.StealStale)
		if perr != nil || d <= 0 {
			writeErr(w, ErrValidation)
			return
		}
		stale = d
	}
	var t *Task
	if err := s.publish(func() (ev *Event, err error) { // idempotent re-claim returns no event
		if stale > 0 {
			t, ev, err = s.store.StealStaleClaim(id, agent, stale)
		} else {
			t, ev, err = s.store.ClaimTask(id, agent)
		}
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleNext(w http.ResponseWriter, r *http.Request) {
	agent := actorOf(r)
	var in struct {
		Project  string `json:"project"`
		Category string `json:"category"`
		Assignee string `json:"assignee"`
		MetaKey  string `json:"meta_key"`
	}
	if err := decode(r, &in); err != nil { // an empty body is fine; malformed JSON is not
		writeErr(w, ErrValidation)
		return
	}
	if in.Assignee != "" {
		agent = in.Assignee
	}
	sc, err := s.scopeOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Merging the scope into the filter BEFORE the store call puts it inside
	// the atomic pick+claim — a scoped agent can never be handed an
	// out-of-scope task. allowProposals=false: next never picks up proposals.
	proj, cat, err := s.narrowScope(r, sc, in.Project, in.Category, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var t *Task
	if err := s.publish(func() (ev *Event, err error) {
		t, ev, err = s.store.NextTaskContext(r.Context(), NextFilter{Project: proj, Category: cat, MetaKey: in.MetaKey}, agent)
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleComment(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	sc, err := s.scopeOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.checkComment(r, sc, id); err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	var c *Comment
	if err := s.publish(func() (ev *Event, err error) {
		c, ev, err = s.store.AddComment(id, actorOf(r), in.Body)
		return
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mutableTask(w, r)
	if !ok {
		return
	}
	if err := s.publish(func() (*Event, error) { return s.store.DeleteTask(id, actorOf(r)) }); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.mutableTask(w, r)
	if !ok {
		return
	}
	cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil {
		writeErr(w, ErrNotFound)
		return
	}
	if err := s.publish(func() (*Event, error) { return s.store.DeleteComment(taskID, cid, actorOf(r)) }); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	sc, err := s.scopeOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.checkProjectMut(r, sc, slug); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.publish(func() (*Event, error) { return s.store.DeleteProject(slug, sc.Category, actorOf(r)) }); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleProjectGraph(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	sc, err := s.scopeOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.checkProjectRead(r, sc, slug); err != nil {
		writeErr(w, err)
		return
	}
	data, err := s.store.ProjectGraph(slug)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// ?category= scopes the feed to one category's projects; it EXCLUDES that
	// category's own (NULL-project) events on purpose (see store.ListEvents).
	cat := q.Get("category")
	if t := q.Get("tail"); t != "" { // newest-first, for the dashboard feed bootstrap
		evs, max, err := s.store.RecentEvents(q.Get("project"), cat, atoiDefault(t, 40))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": evs, "last_id": max})
		return
	}
	if b := q.Get("before"); b != "" { // newest-first, paging older events
		evs, err := s.store.ListEventsBefore(atoi64Default(b, 0), q.Get("project"), cat, atoiDefault(q.Get("limit"), 40))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": evs})
		return
	}
	since := atoi64Default(q.Get("since"), 0)
	evs, last, err := s.store.ListEvents(since, q.Get("project"), cat, atoiDefault(q.Get("limit"), 0))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs, "last_id": last})
}

// handleStream is the SSE endpoint that drives the live dashboard.
// streamPingInterval spaces the keep-alive comments on an idle stream; a var
// so tests can shorten it.
var streamPingInterval = 15 * time.Second

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// JSON like every other error path — the dashboard's api() parses JSON.
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming_unsupported"})
		return
	}
	q := r.URL.Query()
	// An unknown ?project= or ?category= is dropped for BOTH the live filter
	// and the replay, so the two agree: the subscriber simply sees the
	// unfiltered stream (am wait re-checks via REST on every event).
	proj := q.Get("project")
	var pid int64
	if proj != "" {
		if id, err := s.store.projectID(proj); err == nil {
			pid = id
		} else {
			proj = ""
		}
	}
	// ?category= scopes the stream to one category's projects. Resolve the
	// project-id set ONCE here so Broadcast stays a pure in-memory check.
	cat := q.Get("category")
	var cid int64
	var projectIDs map[int64]bool
	if cat != "" {
		if id, err := s.store.categoryID(cat); err == nil {
			cid = id
			if set, err := s.store.ProjectIDsInCategory(id); err == nil {
				projectIDs = set
			}
		} else {
			cat = "" // unknown category: fall back to unfiltered
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	// Subscribe FIRST so no event is lost between snapshot and live stream.
	sub := s.hub.Subscribe(subFilter{projectID: pid, categoryID: cid, projectIDs: projectIDs})
	defer s.hub.Unsubscribe(sub)

	// Resume point: Last-Event-ID header (set by EventSource on reconnect) or ?since=.
	since := atoi64Default(r.Header.Get("Last-Event-ID"), atoi64Default(q.Get("since"), 0))
	maxAtSub, _ := s.store.MaxEventID()
	// A cursor beyond the log's end belongs to another history (the DB was
	// replaced by `am db import`, or emptied by `am db prune`): keeping it
	// would dedupe away every live event until ids climb past it. Replay
	// from the start instead.
	if since > maxAtSub {
		since = 0
	}
	lastSent := since

	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	// Replay the gap (since, maxAtSub] from the durable log, page by page:
	// live delivery only covers events broadcast after Subscribe, so a gap
	// event skipped here would never reach this client. A failed page ends
	// the response; the client reconnects from lastSent and retries it.
	const replayPage = 500
	for lastSent < maxAtSub {
		evs, _, err := s.store.ListEvents(lastSent, proj, cat, replayPage)
		if err != nil {
			log.Printf("agentman: stream replay: %v", err)
			return
		}
		for i := range evs {
			if evs[i].ID > maxAtSub {
				break
			}
			if writeEvent(w, &evs[i]) != nil {
				return
			}
			lastSent = evs[i].ID
		}
		flusher.Flush()
		if len(evs) < replayPage || evs[len(evs)-1].ID > maxAtSub {
			break
		}
	}

	ping := time.NewTicker(streamPingInterval)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case e, ok := <-sub.ch:
			if !ok {
				return
			}
			// The hub dropped an event for this subscriber (buffer full, so
			// sub.ch is never empty here). Writing anything later would move
			// the client's cursor past the hole; ending the response instead
			// makes it reconnect from lastSent, which replays the gap.
			select {
			case <-sub.lagged:
				return
			default:
			}
			if e.ID <= lastSent { // dedupe overlap with replay
				continue
			}
			if writeEvent(w, e) != nil {
				return
			}
			lastSent = e.ID
			flusher.Flush()
		}
	}
}

func writeEvent(w io.Writer, e *Event) error {
	b, _ := json.Marshal(e)
	_, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.ID, b)
	return err
}

// ---------- token admin (Phase S) ----------

// unscopedGuard admits only a fully unscoped caller (the human at the CLI /
// dashboard). It gates the category layer (above every scope) and the
// mint-requires-unscoped boundary crux: the three token-admin endpoints are
// refused for ANY request that carries a scope — whether asserted by an
// X-Agent-Scope header or derived from a (valid) bearer token — so a confined
// agent can never forge a token for another scope. A bad bearer token still
// 401s here (scopeOf surfaces ErrInvalidToken) rather than being mistaken for
// "unscoped". Returns ok=false after writing the error response.
func (s *Server) unscopedGuard(w http.ResponseWriter, r *http.Request) (ok bool) {
	sc, err := s.scopeOf(r)
	if err != nil {
		writeErr(w, err) // bad/revoked token → 401
		return false
	}
	if !sc.IsZero() { // any scope (header or token) is refused — 403 → exit 8
		writeErr(w, denyScope(r, sc))
		return false
	}
	return true
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	if !s.unscopedGuard(w, r) {
		return
	}
	var in struct {
		Scope string `json:"scope"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	sc, err := parseScope(in.Scope)
	if err != nil {
		writeErr(w, err)
		return
	}
	plaintext, tok, err := s.store.CreateToken(sc)
	if err != nil {
		writeErr(w, err)
		return
	}
	// The plaintext token rides this one response and never appears again
	// (not in ls, not in the DB row, not in any log).
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         tok.ID,
		"scope":      tok.Scope().String(),
		"token":      plaintext,
		"created_at": tok.CreatedAt,
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	if !s.unscopedGuard(w, r) {
		return
	}
	toks, err := s.store.ListTokens()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toks)
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if !s.unscopedGuard(w, r) {
		return
	}
	tok, err := s.store.RevokeToken(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tok)
}

// ---------- helpers ----------

// publish runs one store mutation and broadcasts its event under commitMu,
// so events reach the hub in commit (= id) order. Without it two handlers
// could commit N and N+1 but broadcast N+1 first, and the id high-water
// dedupe in handleStream (and the dashboard's cursor) would drop N for good.
// The lock covers only the DB call and the non-blocking Broadcast, never
// client I/O; writes already serialize on the single DB connection.
func (s *Server) publish(mutate func() (*Event, error)) error {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	ev, err := mutate()
	if err != nil {
		return err
	}
	s.hub.Broadcast(ev) // nil-safe: no-op mutations return no event
	return nil
}

func actorOf(r *http.Request) string {
	if a := r.Header.Get("X-Agent"); a != "" {
		return a
	}
	return "human"
}

// ---------- scope enforcement (Phase Q) ----------

// scopeOf is the SOLE reader of request scope — no handler reads the
// Authorization header or X-Agent-Scope directly. Precedence (Phase S):
//   - A bearer token WINS: its server-side bound scope is authoritative and any
//     X-Agent-Scope header is ignored. An unknown/revoked token — or an
//     Authorization header that carries no Bearer token at all (empty value,
//     other scheme) — is ErrInvalidToken (→ 401 → exit 9), NEVER a silent
//     fallthrough to the header or zero scope.
//   - With no token, the X-Agent-Scope header is the scope: absent is the zero
//     (unscoped) Scope; a malformed one is a 400.
func (s *Server) scopeOf(r *http.Request) (Scope, error) {
	if r.Header.Get("Authorization") != "" {
		tok := bearerToken(r)
		if tok == "" {
			return Scope{}, ErrInvalidToken
		}
		return s.store.ResolveToken(tok)
	}
	raw := r.Header.Get("X-Agent-Scope")
	if raw == "" {
		return Scope{}, nil
	}
	return parseScope(raw)
}

// bearerToken extracts the token from an "Authorization: Bearer <tok>" header,
// or "" when absent. The scheme match is case-insensitive (per RFC 7235); the
// token value is returned verbatim.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// scopeAllows reports whether a task in (cat, proj) falls inside sc.
// The zero scope allows everything.
func scopeAllows(sc Scope, cat, proj string) bool {
	if sc.IsZero() {
		return true
	}
	return sc.Category == cat && (sc.Project == "" || sc.Project == proj)
}

// denyScope logs an out_of_scope rejection (log-only — deliberately no event
// kind in Phase Q) and returns ErrOutOfScope (→ 403 → CLI exit 8).
func denyScope(r *http.Request, sc Scope) error {
	log.Printf("agentman: out_of_scope: actor=%s scope=%s %s %s", actorOf(r), sc, r.Method, r.URL.Path)
	return ErrOutOfScope
}

// checkTaskMut gates every mutation of an existing task (claim/steal, patch,
// delete, deps, labels, comment deletion). It reads outside the mutation's
// tx, which relies on task→project and project→category immutability AND on
// task ids never being reused (AUTOINCREMENT, schema v6) — see the PatchTask
// scope note.
func (s *Server) checkTaskMut(r *http.Request, sc Scope, taskID int64) error {
	if sc.IsZero() {
		return nil
	}
	cat, proj, _, err := s.store.taskScope(taskID)
	if err != nil {
		return err // ErrNotFound stays a 404
	}
	if scopeAllows(sc, cat, proj) {
		return nil
	}
	return denyScope(r, sc)
}

// mutableTask resolves the {id} path value and runs scopeOf + checkTaskMut,
// the shared preamble of every handler that mutates an existing task.
// Returns ok=false after writing the error response.
func (s *Server) mutableTask(w http.ResponseWriter, r *http.Request) (id int64, ok bool) {
	id, err := s.store.resolveTaskID(r.PathValue("id"))
	if err == nil {
		var sc Scope
		if sc, err = s.scopeOf(r); err == nil {
			err = s.checkTaskMut(r, sc, id)
		}
	}
	if err != nil {
		writeErr(w, err)
		return 0, false
	}
	return id, true
}

// mutableProject runs scopeOf + checkProjectMut for patch/archive/unarchive.
// Returns false after writing the error response.
func (s *Server) mutableProject(w http.ResponseWriter, r *http.Request, slug string) bool {
	sc, err := s.scopeOf(r)
	if err == nil {
		err = s.checkProjectMut(r, sc, slug)
	}
	if err != nil {
		writeErr(w, err)
		return false
	}
	return true
}

// checkTaskRead gates single-task reads: in scope, or in the proposals
// project (readable by all — proposals are meant to be seen).
func (s *Server) checkTaskRead(r *http.Request, sc Scope, taskID int64) error {
	if sc.IsZero() {
		return nil
	}
	cat, proj, _, err := s.store.taskScope(taskID)
	if err != nil {
		return err
	}
	if scopeAllows(sc, cat, proj) || s.isProposalsPair(cat, proj) {
		return nil
	}
	return denyScope(r, sc)
}

// checkComment allows commenting in scope, plus the carve-out: a scoped agent
// may comment on tasks it CREATED in the proposals project (follow-ups on its
// own proposals). created_by may be empty for pre-v5 tasks with pruned
// events — those never match, the safe direction.
func (s *Server) checkComment(r *http.Request, sc Scope, taskID int64) error {
	if sc.IsZero() {
		return nil
	}
	cat, proj, createdBy, err := s.store.taskScope(taskID)
	if err != nil {
		return err
	}
	if scopeAllows(sc, cat, proj) {
		return nil
	}
	if s.isProposalsPair(cat, proj) && createdBy != "" && createdBy == actorOf(r) {
		return nil
	}
	return denyScope(r, sc)
}

// isProposals reports whether slug currently designates the carve-out
// project: the slug must match AND the project's actual category must be the
// designated one. Slugs are globally unique, so a same-named project in
// another category is NOT the carve-out — without the category check a
// scoped agent could squat the slug inside its own scope and capture every
// other agent's proposals. A missing project still counts (ErrNotFound
// passes through as true): the gate stays open and the store 404s, keeping
// the carve-out inert rather than special-cased. Every site that consults
// the carve-out goes through here or, when it already holds a task's
// (category, project) pair, through isProposalsPair.
func (s *Server) isProposals(slug string) (bool, error) {
	if slug != s.proposals.Project {
		return false, nil
	}
	cat, err := s.store.projectCategory(slug)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return s.isProposalsPair(cat, slug), nil
}

// isProposalsPair reports whether (cat, proj) is the designated carve-out
// project.
func (s *Server) isProposalsPair(cat, proj string) bool {
	return cat == s.proposals.Category && proj == s.proposals.Project
}

// projectInScope reports whether project slug lies inside the non-zero scope
// sc: a project scope must name slug, and the project's actual category must
// be sc.Category. The category is checked for project scopes too: slugs are
// freed by delete, so a same-named project recreated in another category must
// not inherit a token minted for the old one (scopeAllows compares the pair).
// Store errors, ErrNotFound for an unknown slug included, pass through.
func (s *Server) projectInScope(sc Scope, slug string) (bool, error) {
	if sc.Project != "" && slug != sc.Project {
		return false, nil
	}
	cat, err := s.store.projectCategory(slug)
	if err != nil {
		return false, err
	}
	return cat == sc.Category, nil
}

// checkCreate gates task creation: the target project must be in scope, OR be
// the proposals project (the carve-out works from any scope). A category
// mismatch on the designated slug falls through to the normal scope rules;
// if the project does not exist the store 404s, leaving the carve-out inert
// rather than special-cased.
func (s *Server) checkCreate(r *http.Request, sc Scope, projectSlug string) error {
	if sc.IsZero() {
		return nil
	}
	if ok, err := s.isProposals(projectSlug); err != nil {
		return err
	} else if ok {
		return nil
	}
	// An unknown slug stays a 404, matching unscoped create.
	return s.checkProjectMut(r, sc, projectSlug)
}

// checkProjectRead gates project-level reads (graph): in scope or the
// proposals project (the (category, project) pair — a same-slug project in
// another category falls through to the normal rules).
func (s *Server) checkProjectRead(r *http.Request, sc Scope, slug string) error {
	if sc.IsZero() {
		return nil
	}
	if ok, err := s.isProposals(slug); err != nil {
		return err
	} else if ok {
		return nil
	}
	return s.checkProjectMut(r, sc, slug)
}

// checkProjectMut gates project mutations (patch/archive/unarchive/delete):
// the project itself must be in scope — no proposals carve-out (proposing is
// creating tasks, never reshaping the project). Unlike task ids, slugs are
// reused after a delete, so this read can race a delete+recreate; delete (the
// irreversible one) re-checks the category inside its own tx.
func (s *Server) checkProjectMut(r *http.Request, sc Scope, slug string) error {
	if sc.IsZero() {
		return nil
	}
	ok, err := s.projectInScope(sc, slug)
	if err != nil {
		return err // unknown slug stays a 404
	}
	if !ok {
		return denyScope(r, sc)
	}
	return nil
}

// narrowScope merges the caller's scope into explicit list/next filters.
// Explicit values that contradict the scope are rejected loudly
// (ErrOutOfScope); absent ones are filled in from the scope (silent
// narrowing, so an unfiltered `am ls` just shows the agent its world).
// allowProposals additionally accepts an explicit ?project=<proposals>
// (reads, the pair-checked carve-out); next passes false — the carve-out
// does not extend to next (an agent whose scope already covers the
// proposals project still picks them via plain in-scope matching).
func (s *Server) narrowScope(r *http.Request, sc Scope, proj, cat string, allowProposals bool) (string, string, error) {
	if sc.IsZero() {
		return proj, cat, nil
	}
	if cat != "" && cat != sc.Category {
		return "", "", denyScope(r, sc)
	}
	if proj != "" {
		if allowProposals {
			ok, err := s.isProposals(proj)
			if err != nil {
				return "", "", err
			}
			if ok {
				return proj, cat, nil
			}
		}
		ok, err := s.projectInScope(sc, proj)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", "", err
		}
		if !ok { // an unknown explicit slug is not provably in scope either
			return "", "", denyScope(r, sc)
		}
		// Hand the scope's category to the store as well, so its own query
		// re-checks the pair (the project could be deleted and its slug
		// recreated elsewhere between this read and the store call).
		return proj, sc.Category, nil
	}
	return sc.Project, sc.Category, nil
}

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
	if errors.Is(err, io.EOF) {
		return nil // empty body is allowed
	}
	return err
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ce *ConflictError
	if errors.As(err, &ce) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "already_claimed", "assignee": ce.Assignee})
		return
	}
	var be *BlockedError
	if errors.As(err, &be) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "blocked", "open_prereqs": be.OpenPrereqs})
		return
	}
	var nse *NotStaleError
	if errors.As(err, &nse) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "not_stale", "assignee": nse.Assignee})
		return
	}
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	case errors.Is(err, ErrInvalidToken):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	case errors.Is(err, ErrOutOfScope):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "out_of_scope"})
	case errors.Is(err, ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "validation"})
	case errors.Is(err, ErrProjectArchived):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_archived"})
	case errors.Is(err, ErrCategoryArchived):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "category_archived"})
	case errors.Is(err, ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
	default:
		log.Printf("agentman: internal error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
	}
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func atoi64Default(s string, def int64) int64 {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return def
}
