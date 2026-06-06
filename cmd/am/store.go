package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Sentinel errors mapped to HTTP status / CLI exit codes by callers.
var (
	ErrNotFound   = errors.New("not_found")
	ErrConflict   = errors.New("conflict")
	ErrValidation = errors.New("validation")
)

// ConflictError carries the current owner of a task that lost a claim race.
type ConflictError struct{ Assignee string }

func (e *ConflictError) Error() string { return "already_claimed" }

var validStatus = map[string]bool{"todo": true, "doing": true, "blocked": true, "done": true}

// ---------- types ----------

type Project struct {
	ID         int64          `json:"id"`
	Slug       string         `json:"slug"`
	Name       string         `json:"name"`
	CreatedAt  string         `json:"created_at"`
	ArchivedAt string         `json:"archived_at,omitempty"`
	Counts     map[string]int `json:"counts,omitempty"`
}

type Task struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	Project   string    `json:"project"` // slug
	Ref       int64     `json:"ref"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Status    string    `json:"status"`
	Assignee  string    `json:"assignee"`
	Priority  int       `json:"priority"`
	NComments int       `json:"nc"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
	Comments  []Comment `json:"comments,omitempty"`
	Events    []Event   `json:"events,omitempty"`
}

type Comment struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"task_id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type Event struct {
	ID        int64           `json:"id"`
	ProjectID int64           `json:"project_id"`
	TaskID    int64           `json:"task_id,omitempty"`
	Actor     string          `json:"actor"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
	CreatedAt string          `json:"created_at"`
}

type TaskFilter struct {
	Project  string
	Status   string
	Assignee string
	Limit    int
}

// ---------- store ----------

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	// All pragmas on the DSN so every (re)opened connection inherits them.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"+
		"&_pragma=foreign_keys(1)&_pragma=synchronous(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer => no SQLITE_BUSY, claims serialize
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := runMigrations(db, currentSchemaVersion, schemaMigrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// ---------- schema migrations ----------

// currentSchemaVersion is the version OpenStore migrates to. schema.sql seeds a
// fresh DB at version 1; runMigrations applies any steps with version > the DB's
// recorded version to reach this target.
const currentSchemaVersion = 2

// migration is one forward-only, idempotent step. apply runs inside the same tx
// that bumps meta.schema_version, so a step + its version bump commit atomically.
type migration struct {
	version int
	apply   func(*sql.Tx) error
}

// schemaMigrations is the ordered, forward-only migration list. The first shipped
// step is v2, which adds the projects.archived_at column for project archiving;
// further phases append steps like {version: N, apply: func(tx *sql.Tx) error { ... }}.
// Versions must be strictly increasing and start above 1 (the seed version).
var schemaMigrations = []migration{
	{version: 2, apply: func(tx *sql.Tx) error {
		_, err := tx.Exec("ALTER TABLE projects ADD COLUMN archived_at TEXT")
		return err
	}},
}

// readSchemaVersion returns the DB's recorded schema version, defaulting to 1 if
// the meta row is missing or unparseable (a fresh DB is implicitly at version 1).
func readSchemaVersion(db *sql.DB) (int, error) {
	var raw string
	err := db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&raw)
	if err == sql.ErrNoRows {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	v, perr := strconv.Atoi(strings.TrimSpace(raw))
	if perr != nil {
		return 1, nil
	}
	return v, nil
}

// runMigrations applies, in order, every step whose version exceeds the DB's
// current version, up to target. Each step's apply and the matching
// meta.schema_version bump run in one tx; on any error the tx is rolled back and
// the DB is left at the prior version. Re-running is a no-op (integer compare).
func runMigrations(db *sql.DB, target int, steps []migration) error {
	cur, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	for _, m := range steps {
		if m.version <= cur || m.version > target {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if err := m.apply(tx); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(
			"INSERT OR REPLACE INTO meta(key,value) VALUES('schema_version', ?)",
			strconv.Itoa(m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			tx.Rollback()
			return err
		}
		cur = m.version
	}
	return nil
}

func (s *Store) Close() error {
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// resolveTaskID accepts "13", "#13", or "web-3".
func (s *Store) resolveTaskID(ref string) (int64, error) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "#")
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return id, nil
	}
	i := strings.LastIndex(ref, "-")
	if i < 0 {
		return 0, ErrNotFound
	}
	n, err := strconv.ParseInt(ref[i+1:], 10, 64)
	if err != nil {
		return 0, ErrNotFound
	}
	var id int64
	err = s.db.QueryRow(
		"SELECT t.id FROM tasks t JOIN projects p ON p.id=t.project_id WHERE p.slug=? AND t.ref=?",
		ref[:i], n).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	return id, err
}

// ---------- projects ----------

func (s *Store) ListProjects(includeArchived bool) ([]Project, error) {
	q := `SELECT p.id, p.slug, p.name, p.created_at, COALESCE(p.archived_at,''),
	          COALESCE(SUM(t.status='todo'),0),
	          COALESCE(SUM(t.status='doing'),0),
	          COALESCE(SUM(t.status='blocked'),0),
	          COALESCE(SUM(t.status='done'),0)
	      FROM projects p LEFT JOIN tasks t ON t.project_id = p.id`
	if !includeArchived {
		q += " WHERE p.archived_at IS NULL"
	}
	q += " GROUP BY p.id ORDER BY p.id"
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		var todo, doing, blocked, done int
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.CreatedAt, &p.ArchivedAt,
			&todo, &doing, &blocked, &done); err != nil {
			return nil, err
		}
		p.Counts = map[string]int{"todo": todo, "doing": doing, "blocked": blocked, "done": done}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ArchiveProject soft-archives a project (sets archived_at). Idempotent.
func (s *Store) ArchiveProject(slug, actor string) (*Project, *Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var p Project
	var archivedAt string
	err = tx.QueryRow("SELECT id, slug, name, created_at, COALESCE(archived_at,'') FROM projects WHERE slug=?", slug).
		Scan(&p.ID, &p.Slug, &p.Name, &p.CreatedAt, &archivedAt)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if archivedAt != "" {
		// Already archived — idempotent success, no event
		p.ArchivedAt = archivedAt
		return &p, nil, tx.Commit()
	}
	if _, err := tx.Exec(
		"UPDATE projects SET archived_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", p.ID); err != nil {
		return nil, nil, err
	}
	ev, err := insertEvent(tx, p.ID, 0, actorOr(actor), "project.archived", map[string]any{"slug": slug})
	if err != nil {
		return nil, nil, err
	}
	if err := tx.QueryRow("SELECT COALESCE(archived_at,'') FROM projects WHERE id=?", p.ID).Scan(&p.ArchivedAt); err != nil {
		return nil, nil, err
	}
	return &p, ev, tx.Commit()
}

// UnarchiveProject restores a project (clears archived_at). Idempotent.
func (s *Store) UnarchiveProject(slug, actor string) (*Project, *Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var p Project
	var archivedAt string
	err = tx.QueryRow("SELECT id, slug, name, created_at, COALESCE(archived_at,'') FROM projects WHERE slug=?", slug).
		Scan(&p.ID, &p.Slug, &p.Name, &p.CreatedAt, &archivedAt)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if archivedAt == "" {
		// Not archived — idempotent success, no event
		return &p, nil, tx.Commit()
	}
	if _, err := tx.Exec("UPDATE projects SET archived_at=NULL WHERE id=?", p.ID); err != nil {
		return nil, nil, err
	}
	ev, err := insertEvent(tx, p.ID, 0, actorOr(actor), "project.unarchived", map[string]any{"slug": slug})
	if err != nil {
		return nil, nil, err
	}
	p.ArchivedAt = "" // explicitly clear; was scanned into a local var, not p.ArchivedAt
	return &p, ev, tx.Commit()
}

func (s *Store) CreateProject(slug, name string) (*Project, *Event, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" || strings.ContainsAny(slug, " /\t") {
		return nil, nil, ErrValidation
	}
	if name == "" {
		name = slug
	}
	var exists int
	s.db.QueryRow("SELECT 1 FROM projects WHERE slug=?", slug).Scan(&exists)
	if exists == 1 {
		return nil, nil, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec("INSERT INTO projects(slug,name) VALUES(?,?)", slug, name)
	if err != nil {
		return nil, nil, err
	}
	id, _ := res.LastInsertId()
	ev, err := insertEvent(tx, id, 0, "human", "project.created", map[string]any{"slug": slug})
	if err != nil {
		return nil, nil, err
	}
	var p Project
	if err := tx.QueryRow("SELECT id,slug,name,created_at FROM projects WHERE id=?", id).
		Scan(&p.ID, &p.Slug, &p.Name, &p.CreatedAt); err != nil {
		return nil, nil, err
	}
	return &p, ev, tx.Commit()
}

func (s *Store) projectID(slug string) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT id FROM projects WHERE slug=?", slug).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	return id, err
}

// ---------- tasks ----------

func (s *Store) ListTasks(f TaskFilter) ([]Task, error) {
	var where []string
	var args []any
	if f.Project != "" {
		where = append(where, "p.slug=?")
		args = append(args, f.Project)
	} else {
		// No explicit project: hide tasks belonging to archived projects from the
		// unfiltered board/list. An explicit project filter still returns that
		// project's tasks (so an archived project can still be inspected directly).
		where = append(where, "p.archived_at IS NULL")
	}
	if f.Status != "" {
		// allow comma list, e.g. "todo,doing"
		parts := strings.Split(f.Status, ",")
		ph := make([]string, len(parts))
		for i, st := range parts {
			ph[i] = "?"
			args = append(args, strings.TrimSpace(st))
		}
		where = append(where, "t.status IN ("+strings.Join(ph, ",")+")")
	}
	if f.Assignee != "" {
		where = append(where, "t.assignee=?")
		args = append(args, f.Assignee)
	}
	q := `SELECT t.id,t.ref,p.slug,t.title,t.status,COALESCE(t.assignee,''),t.priority,
	         t.created_at,t.updated_at,
	         (SELECT COUNT(*) FROM comments c WHERE c.task_id=t.id)
	       FROM tasks t JOIN projects p ON p.id=t.project_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY t.priority ASC, t.updated_at DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Ref, &t.Project, &t.Title, &t.Status, &t.Assignee,
			&t.Priority, &t.CreatedAt, &t.UpdatedAt, &t.NComments); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) getTaskTx(q queryer, id int64) (*Task, error) {
	var t Task
	var assignee sql.NullString
	err := q.QueryRow(`SELECT t.id,t.project_id,p.slug,t.ref,t.title,t.body,t.status,t.assignee,
	         t.priority,t.created_at,t.updated_at,
	         (SELECT COUNT(*) FROM comments c WHERE c.task_id=t.id)
	       FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=?`, id).
		Scan(&t.ID, &t.ProjectID, &t.Project, &t.Ref, &t.Title, &t.Body, &t.Status,
			&assignee, &t.Priority, &t.CreatedAt, &t.UpdatedAt, &t.NComments)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Assignee = assignee.String
	return &t, nil
}

// GetTask returns a task with comments and recent events.
func (s *Store) GetTask(id int64) (*Task, error) {
	t, err := s.getTaskTx(s.db, id)
	if err != nil {
		return nil, err
	}
	crows, err := s.db.Query("SELECT id,task_id,author,body,created_at FROM comments WHERE task_id=? ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	t.Comments = []Comment{}
	for crows.Next() {
		var c Comment
		if err := crows.Scan(&c.ID, &c.TaskID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		t.Comments = append(t.Comments, c)
	}
	erows, err := s.db.Query(`SELECT id,COALESCE(project_id,0),COALESCE(task_id,0),actor,kind,data,created_at
	       FROM events WHERE task_id=? ORDER BY id DESC LIMIT 50`, id)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	t.Events = []Event{}
	for erows.Next() {
		var e Event
		var data string
		if err := erows.Scan(&e.ID, &e.ProjectID, &e.TaskID, &e.Actor, &e.Kind, &data, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(data)
		t.Events = append(t.Events, e)
	}
	return t, nil
}

type CreateTaskInput struct {
	Project  string
	Title    string
	Body     string
	Priority int
	Assignee string
	Actor    string
}

func (s *Store) CreateTask(in CreateTaskInput) (*Task, *Event, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, nil, ErrValidation
	}
	pid, err := s.projectID(in.Project)
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var ref int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(ref),0)+1 FROM tasks WHERE project_id=?", pid).Scan(&ref); err != nil {
		return nil, nil, err
	}
	res, err := tx.Exec(`INSERT INTO tasks(project_id,ref,title,body,priority,assignee)
	         VALUES(?,?,?,?,?,?)`, pid, ref, in.Title, in.Body, in.Priority, nullStr(in.Assignee))
	if err != nil {
		return nil, nil, err
	}
	id, _ := res.LastInsertId()
	ev, err := insertEvent(tx, pid, id, actorOr(in.Actor), "task.created",
		map[string]any{"title": in.Title})
	if err != nil {
		return nil, nil, err
	}
	t, err := s.getTaskTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	return t, ev, tx.Commit()
}

// PatchTask applies allowed field changes and records a single event.
func (s *Store) PatchTask(id int64, patch map[string]any, actor string) (*Task, *Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	cur, err := s.getTaskTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	var sets []string
	var args []any
	delta := map[string]any{}
	statusChanged, assignChanged := false, false

	if v, ok := patch["status"]; ok {
		st, _ := v.(string)
		if !validStatus[st] {
			return nil, nil, ErrValidation
		}
		if st != cur.Status {
			sets = append(sets, "status=?")
			args = append(args, st)
			delta["status"] = []any{cur.Status, st}
			statusChanged = true
		}
	}
	if v, ok := patch["assignee"]; ok {
		as, _ := v.(string)
		if as != cur.Assignee {
			sets = append(sets, "assignee=?")
			args = append(args, nullStr(as))
			delta["assignee"] = []any{nullable(cur.Assignee), nullable(as)}
			assignChanged = true
		}
	}
	if v, ok := patch["title"]; ok {
		ti, _ := v.(string)
		if strings.TrimSpace(ti) == "" {
			return nil, nil, ErrValidation
		}
		if ti != cur.Title {
			sets = append(sets, "title=?")
			args = append(args, ti)
			delta["title"] = []any{cur.Title, ti}
		}
	}
	if v, ok := patch["body"]; ok {
		bo, _ := v.(string)
		if bo != cur.Body {
			sets = append(sets, "body=?")
			args = append(args, bo)
			delta["body"] = true
		}
	}
	if v, ok := patch["priority"]; ok {
		pr := toInt(v, cur.Priority)
		if pr != cur.Priority {
			sets = append(sets, "priority=?")
			args = append(args, pr)
			delta["priority"] = []any{cur.Priority, pr}
		}
	}

	if len(sets) == 0 { // no-op: idempotent success, no event
		return cur, nil, tx.Commit()
	}
	sets = append(sets, "updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')")
	args = append(args, id)
	if _, err := tx.Exec("UPDATE tasks SET "+strings.Join(sets, ",")+" WHERE id=?", args...); err != nil {
		return nil, nil, err
	}
	kind := "task.patched"
	if statusChanged {
		kind = "task.status"
	} else if assignChanged {
		kind = "task.assign"
	}
	ev, err := insertEvent(tx, cur.ProjectID, id, actorOr(actor), kind, delta)
	if err != nil {
		return nil, nil, err
	}
	t, err := s.getTaskTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	return t, ev, tx.Commit()
}

// ClaimTask atomically assigns an unclaimed, non-done task to agent.
// Returns (task, event, nil) on win; (task, nil, nil) if agent already owns it
// (idempotent); (nil,nil,*ConflictError) if owned by someone else; ErrNotFound.
func (s *Store) ClaimTask(id int64, agent string) (*Task, *Event, error) {
	if agent == "" {
		return nil, nil, ErrValidation
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var newStatus string
	var pid int64
	err = tx.QueryRow(`
		UPDATE tasks
		   SET assignee=?,
		       status=CASE WHEN status='todo' THEN 'doing' ELSE status END,
		       updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id=? AND assignee IS NULL AND status!='done'
		RETURNING project_id, status`, agent, id).Scan(&pid, &newStatus)
	if err == sql.ErrNoRows {
		// Lost: figure out why.
		cur, gerr := s.getTaskTx(tx, id)
		if gerr != nil {
			return nil, nil, gerr // ErrNotFound or real error
		}
		if cur.Assignee == agent {
			return cur, nil, tx.Commit() // idempotent re-claim
		}
		return nil, nil, &ConflictError{Assignee: orDash(cur.Assignee, cur.Status)}
	}
	if err != nil {
		return nil, nil, err
	}
	ev, err := insertEvent(tx, pid, id, agent, "task.claimed",
		map[string]any{"assignee": []any{nil, agent}, "status": newStatus})
	if err != nil {
		return nil, nil, err
	}
	t, err := s.getTaskTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	return t, ev, tx.Commit()
}

func (s *Store) AddComment(id int64, author, body string) (*Comment, *Event, error) {
	if strings.TrimSpace(body) == "" {
		return nil, nil, ErrValidation
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	cur, err := s.getTaskTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	res, err := tx.Exec("INSERT INTO comments(task_id,author,body) VALUES(?,?,?)", id, actorOr(author), body)
	if err != nil {
		return nil, nil, err
	}
	cid, _ := res.LastInsertId()
	tx.Exec("UPDATE tasks SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", id)
	ev, err := insertEvent(tx, cur.ProjectID, id, actorOr(author), "comment.added",
		map[string]any{"comment_id": cid})
	if err != nil {
		return nil, nil, err
	}
	var c Comment
	if err := tx.QueryRow("SELECT id,task_id,author,body,created_at FROM comments WHERE id=?", cid).
		Scan(&c.ID, &c.TaskID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
		return nil, nil, err
	}
	return &c, ev, tx.Commit()
}

// ---------- events / feed ----------

func (s *Store) ListEvents(since int64, project string, limit int) ([]Event, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var args []any
	q := `SELECT id,COALESCE(project_id,0),COALESCE(task_id,0),actor,kind,data,created_at
	      FROM events WHERE id>?`
	args = append(args, since)
	if project != "" {
		pid, err := s.projectID(project)
		if err != nil {
			return nil, since, err
		}
		q += " AND project_id=?"
		args = append(args, pid)
	}
	q += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()
	out := []Event{}
	last := since
	for rows.Next() {
		var e Event
		var data string
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.TaskID, &e.Actor, &e.Kind, &data, &e.CreatedAt); err != nil {
			return nil, since, err
		}
		e.Data = json.RawMessage(data)
		out = append(out, e)
		last = e.ID
	}
	return out, last, rows.Err()
}

func (s *Store) MaxEventID() (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT COALESCE(MAX(id),0) FROM events").Scan(&id)
	return id, err
}

// RecentEvents returns the newest events first (for the dashboard feed bootstrap)
// along with the max event id, which the client uses as its SSE cursor.
func (s *Store) RecentEvents(project string, limit int) ([]Event, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	var args []any
	q := `SELECT id,COALESCE(project_id,0),COALESCE(task_id,0),actor,kind,data,created_at FROM events`
	if project != "" {
		pid, err := s.projectID(project)
		if err != nil {
			return nil, 0, err
		}
		q += " WHERE project_id=?"
		args = append(args, pid)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Event{}
	var max int64
	for rows.Next() {
		var e Event
		var data string
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.TaskID, &e.Actor, &e.Kind, &data, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		e.Data = json.RawMessage(data)
		if e.ID > max {
			max = e.ID
		}
		out = append(out, e)
	}
	return out, max, rows.Err()
}

// ---------- helpers ----------

// queryer is satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func insertEvent(tx *sql.Tx, projectID, taskID int64, actor, kind string, data any) (*Event, error) {
	b, _ := json.Marshal(data)
	res, err := tx.Exec("INSERT INTO events(project_id,task_id,actor,kind,data) VALUES(?,?,?,?,?)",
		nullableID(projectID), nullableID(taskID), actor, kind, string(b))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	var e Event
	var d string
	if err := tx.QueryRow(`SELECT id,COALESCE(project_id,0),COALESCE(task_id,0),actor,kind,data,created_at
	       FROM events WHERE id=?`, id).
		Scan(&e.ID, &e.ProjectID, &e.TaskID, &e.Actor, &e.Kind, &d, &e.CreatedAt); err != nil {
		return nil, err
	}
	e.Data = json.RawMessage(d)
	return &e, nil
}

func nullableID(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func actorOr(a string) string {
	if a == "" {
		return "anon"
	}
	return a
}

func toInt(v any, def int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}

// orDash returns the assignee, or if empty (e.g. a done task) the status, so a
// claim conflict message is still informative.
func orDash(assignee, status string) string {
	if assignee != "" {
		return assignee
	}
	return status
}
