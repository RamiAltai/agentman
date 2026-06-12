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
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	store       *Store
	hub         *Hub
	logRequests bool
}

func NewServer(store *Store) *Server {
	return &Server{store: store, hub: NewHub()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
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

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("archived") == "true"
	ps, err := s.store.ListProjects(includeArchived)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handleArchiveProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p, ev, err := s.store.ArchiveProject(slug, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev != nil {
		s.hub.Broadcast(ev)
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUnarchiveProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p, ev, err := s.store.UnarchiveProject(slug, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev != nil {
		s.hub.Broadcast(ev)
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var in struct{ Slug, Name string }
	if err := decode(r, &in); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	p, ev, err := s.store.CreateProject(in.Slug, in.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast(ev)
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := TaskFilter{
		Project:  q.Get("project"),
		Status:   q.Get("status"),
		Assignee: q.Get("assignee"),
		Label:    q.Get("label"), // store validates/normalizes
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
	ts, err := s.store.ListTasks(f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) handleAddDep(w http.ResponseWriter, r *http.Request) {
	taskID, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
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
	ev, err := s.store.AddDep(taskID, depID, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev != nil {
		s.hub.Broadcast(ev)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRemoveDep(w http.ResponseWriter, r *http.Request) {
	taskID, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	depID, err := s.store.resolveTaskID(r.PathValue("depId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	ev, err := s.store.RemoveDep(taskID, depID, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev != nil {
		s.hub.Broadcast(ev)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAddLabel(w http.ResponseWriter, r *http.Request) {
	taskID, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Label string `json:"label"`
	}
	if err := decode(r, &in); err != nil || in.Label == "" {
		writeErr(w, ErrValidation)
		return
	}
	ev, err := s.store.AddLabel(taskID, in.Label, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev != nil {
		s.hub.Broadcast(ev)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRemoveLabel(w http.ResponseWriter, r *http.Request) {
	taskID, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	ev, err := s.store.RemoveLabel(taskID, r.PathValue("label"), actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev != nil {
		s.hub.Broadcast(ev)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project  string `json:"project"`
		Title    string `json:"title"`
		Body     string `json:"body"`
		Assignee string `json:"assignee"`
		Priority *int   `json:"priority"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	pr := 2
	if in.Priority != nil {
		pr = *in.Priority
	}
	t, ev, err := s.store.CreateTask(CreateTaskInput{
		Project: in.Project, Title: in.Title, Body: in.Body,
		Priority: pr, Assignee: in.Assignee, Actor: actorOf(r),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast(ev)
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
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
	id, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var patch map[string]any
	if err := decode(r, &patch); err != nil {
		writeErr(w, ErrValidation)
		return
	}
	t, ev, err := s.store.PatchTask(id, patch, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast(ev)
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	agent := actorOf(r)
	var in struct {
		Assignee   string `json:"assignee"`
		StealStale string `json:"steal_stale"`
	}
	_ = decode(r, &in)
	if in.Assignee != "" {
		agent = in.Assignee
	}
	var t *Task
	var ev *Event
	if in.StealStale != "" {
		d, perr := time.ParseDuration(in.StealStale)
		if perr != nil || d <= 0 {
			writeErr(w, ErrValidation)
			return
		}
		t, ev, err = s.store.StealStaleClaim(id, agent, d)
	} else {
		t, ev, err = s.store.ClaimTask(id, agent)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev != nil { // idempotent re-claim returns no event
		s.hub.Broadcast(ev)
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleNext(w http.ResponseWriter, r *http.Request) {
	agent := actorOf(r)
	var in struct {
		Project  string `json:"project"`
		Assignee string `json:"assignee"`
	}
	_ = decode(r, &in)
	if in.Assignee != "" {
		agent = in.Assignee
	}
	t, ev, err := s.store.NextTask(in.Project, agent)
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev != nil { // symmetry with handleClaim; NextTask always emits on success
		s.hub.Broadcast(ev)
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleComment(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
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
	c, ev, err := s.store.AddComment(id, actorOf(r), in.Body)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast(ev)
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	ev, err := s.store.DeleteTask(id, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast(ev)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	taskID, err := s.store.resolveTaskID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil {
		writeErr(w, ErrNotFound)
		return
	}
	ev, err := s.store.DeleteComment(taskID, cid, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast(ev)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	ev, err := s.store.DeleteProject(slug, actorOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast(ev)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleProjectGraph(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	data, err := s.store.ProjectGraph(slug)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if t := q.Get("tail"); t != "" { // newest-first, for the dashboard feed bootstrap
		evs, max, err := s.store.RecentEvents(q.Get("project"), atoiDefault(t, 40))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": evs, "last_id": max})
		return
	}
	if b := q.Get("before"); b != "" { // newest-first, paging older events
		evs, err := s.store.ListEventsBefore(atoi64Default(b, 0), q.Get("project"), atoiDefault(q.Get("limit"), 40))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": evs})
		return
	}
	since := atoi64Default(q.Get("since"), 0)
	evs, last, err := s.store.ListEvents(since, q.Get("project"), atoiDefault(q.Get("limit"), 0))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs, "last_id": last})
}

// handleStream is the SSE endpoint that drives the live dashboard.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// JSON like every other error path — the dashboard's api() parses JSON.
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming_unsupported"})
		return
	}
	q := r.URL.Query()
	var pid int64
	if p := q.Get("project"); p != "" {
		if id, err := s.store.projectID(p); err == nil {
			pid = id
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	// Subscribe FIRST so no event is lost between snapshot and live stream.
	sub := s.hub.Subscribe(pid)
	defer s.hub.Unsubscribe(sub)

	// Resume point: Last-Event-ID header (set by EventSource on reconnect) or ?since=.
	since := atoi64Default(r.Header.Get("Last-Event-ID"), atoi64Default(q.Get("since"), 0))
	lastSent := since
	maxAtSub, _ := s.store.MaxEventID()

	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	// Replay the gap [since, maxAtSub] from the durable log.
	if since < maxAtSub {
		if evs, _, err := s.store.ListEvents(since, q.Get("project"), 500); err == nil {
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
		}
	}

	ping := time.NewTicker(15 * time.Second)
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

// ---------- helpers ----------

func actorOf(r *http.Request) string {
	if a := r.Header.Get("X-Agent"); a != "" {
		return a
	}
	return "human"
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
	case errors.Is(err, ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "validation"})
	case errors.Is(err, ErrProjectArchived):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_archived"})
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
