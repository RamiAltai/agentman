package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	store *Store
	hub   *Hub
}

func NewServer(store *Store) *Server {
	return &Server{store: store, hub: NewHub()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
	mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /api/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.handlePatchTask)
	mux.HandleFunc("POST /api/tasks/{id}/claim", s.handleClaim)
	mux.HandleFunc("POST /api/tasks/{id}/comments", s.handleComment)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/stream", s.handleStream)

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
}

// ---------- handlers ----------

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListProjects()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ps)
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
		Limit:    atoiDefault(q.Get("limit"), 0),
	}
	ts, err := s.store.ListTasks(f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
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
		Assignee string `json:"assignee"`
	}
	_ = decode(r, &in)
	if in.Assignee != "" {
		agent = in.Assignee
	}
	t, ev, err := s.store.ClaimTask(id, agent)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast(ev)
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
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
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
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	case errors.Is(err, ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "validation"})
	case errors.Is(err, ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
