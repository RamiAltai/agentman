package main

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Sentinel errors mapped to HTTP status / CLI exit codes by callers.
var (
	ErrNotFound         = errors.New("not_found")
	ErrConflict         = errors.New("conflict")
	ErrValidation       = errors.New("validation")
	ErrProjectArchived  = errors.New("project_archived")
	ErrCategoryArchived = errors.New("category_archived")
	ErrOutOfScope       = errors.New("out_of_scope")
)

// ConflictError carries the current owner of a task that lost a claim race.
type ConflictError struct{ Assignee string }

func (e *ConflictError) Error() string { return "already_claimed" }

// BlockedError is returned when an operation (claim/patch) is prevented because
// the task has one or more incomplete prerequisites.
type BlockedError struct{ OpenPrereqs []int64 }

func (e *BlockedError) Error() string { return "blocked" }

// NotStaleError is returned when a steal-stale claim loses because the task's
// current claim is still fresh. Assignee names the current holder.
type NotStaleError struct{ Assignee string }

func (e *NotStaleError) Error() string { return "not_stale" }

var validStatus = map[string]bool{"todo": true, "doing": true, "blocked": true, "done": true}

// ---------- types ----------

// Scope is a client-asserted confinement boundary: a category slug, optionally
// narrowed to one project. The zero value means unscoped (the human, the
// dashboard) and passes every check. With no authentication this is accident
// prevention, not a security boundary (security.md's X-Agent caveat applies).
type Scope struct {
	Category string
	Project  string
}

func (s Scope) IsZero() bool { return s.Category == "" && s.Project == "" }

// String renders the wire/identity-file form: "cat" or "cat/proj".
func (s Scope) String() string {
	if s.Project == "" {
		return s.Category
	}
	return s.Category + "/" + s.Project
}

// parseScope parses "category" or "category/project". Input is trimmed and
// lowercased like slugs; empty or whitespace-bearing segments and more than
// one slash are rejected (wraps ErrValidation → HTTP 400 → CLI exit 5).
func parseScope(raw string) (Scope, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	parts := strings.Split(raw, "/")
	if len(parts) > 2 {
		return Scope{}, fmt.Errorf("%w: scope must be category[/project]", ErrValidation)
	}
	for _, seg := range parts {
		if seg == "" || strings.ContainsAny(seg, " \t") {
			return Scope{}, fmt.Errorf("%w: scope must be category[/project]", ErrValidation)
		}
	}
	sc := Scope{Category: parts[0]}
	if len(parts) == 2 {
		sc.Project = parts[1]
	}
	return sc, nil
}

// Category is the layer above projects (instance → category → project → task).
type Category struct {
	ID         int64  `json:"id"`
	UID        string `json:"uid"` // stable id, amc_<16 hex>, never changes
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	ArchivedAt string `json:"archived_at,omitempty"`
}

// CategoryStat is a Category augmented with the rollups the dashboard's
// category-home view needs: task counts (summed over the category's non-archived
// projects) and the agents active in the category within a recent window.
type CategoryStat struct {
	Category
	Counts       map[string]int `json:"counts"`
	ActiveAgents []string       `json:"active_agents"`
}

type Project struct {
	ID             int64          `json:"id"`
	UID            string         `json:"uid"` // stable id, amp_<16 hex>, never changes
	Slug           string         `json:"slug"`
	Name           string         `json:"name"`
	Category       string         `json:"category"` // category slug
	CreatedAt      string         `json:"created_at"`
	ArchivedAt     string         `json:"archived_at,omitempty"`
	VaultProjectID string         `json:"vault_project_id,omitempty"`
	VaultPath      string         `json:"vault_path,omitempty"`
	Counts         map[string]int `json:"counts,omitempty"`
}

// DepRef is a lightweight reference to a task used in dependency lists.
type DepRef struct {
	ID      int64  `json:"id"`
	Ref     int64  `json:"ref"`
	Project string `json:"project"`
	Title   string `json:"title"`
	Status  string `json:"status"`
}

type Task struct {
	ID           int64             `json:"id"`
	ProjectID    int64             `json:"project_id"`
	Project      string            `json:"project"` // slug
	Ref          int64             `json:"ref"`
	Title        string            `json:"title"`
	Body         string            `json:"body,omitempty"`
	Status       string            `json:"status"`
	Assignee     string            `json:"assignee"`
	CreatedBy    string            `json:"created_by,omitempty"`
	Priority     int               `json:"priority"`
	NComments    int               `json:"nc"`
	NPrereqs     int               `json:"nprereq,omitempty"`
	NOpenPrereqs int               `json:"nopen,omitempty"`
	CreatedAt    string            `json:"created_at"`
	UpdatedAt    string            `json:"updated_at"`
	ClaimedAt    string            `json:"claimed_at,omitempty"`
	Labels       []string          `json:"labels,omitempty"`
	Meta         map[string]string `json:"meta,omitempty"`
	Comments     []Comment         `json:"comments,omitempty"`
	Events       []Event           `json:"events,omitempty"`
	DependsOn    []DepRef          `json:"depends_on,omitempty"`
	Blocks       []DepRef          `json:"blocks,omitempty"`
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
	Category string // category slug; composes with Project and every other filter
	Status   string
	Assignee string
	Query    string // substring match on title OR body (LIKE, ASCII-case-insensitive)
	Label    string // tasks carrying this label (normalized before matching)
	MetaKey  string // tasks carrying this meta key (presence, not value; normalized)
	Limit    int
	Ready    bool          // todo tasks with no open prereqs
	Blocked  bool          // tasks with ≥1 open prereq
	Stale    time.Duration // >0: assigned, not-done tasks with no activity since the cutoff
}

// NextFilter scopes NextTask (the atomic pick+claim). Each field composes with
// the others; Phase Q adds more dimensions here rather than widening the
// signature again.
type NextFilter struct {
	Project  string
	Category string
	MetaKey  string // only tasks carrying this meta key (presence, not value)
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
	// Refuse a DB written by a newer am: migrations only run forward, so an
	// older binary would otherwise operate on (and corrupt) too-new data.
	// Mirrors the ceiling validateImportCandidate applies to import snapshots.
	if v, err := readSchemaVersion(db); err != nil {
		db.Close()
		return nil, err
	} else if v > currentSchemaVersion {
		db.Close()
		return nil, fmt.Errorf("database schema_version %d is newer than supported %d — upgrade am", v, currentSchemaVersion)
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
const currentSchemaVersion = 5

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
	{version: 3, apply: func(tx *sql.Tx) error {
		_, err := tx.Exec("ALTER TABLE tasks ADD COLUMN claimed_at TEXT")
		return err
	}},
	// v4: category layer + stable ids + vault binding. The categories table
	// itself comes from schema.sql (CREATE TABLE IF NOT EXISTS runs before
	// migrations on both fresh and existing DBs); this step extends projects,
	// seeds the default category, and backfills.
	{version: 4, apply: func(tx *sql.Tx) error {
		// category_id is added WITHOUT NOT NULL: SQLite's ADD COLUMN cannot add
		// a NOT NULL column unless it has a non-NULL constant default, which is
		// wrong for an FK. The NOT NULL invariant is enforced by the app instead
		// (CreateProject always sets it; the UPDATE below backfills old rows).
		// UNIQUE is likewise not allowed in ADD COLUMN, hence the uid index.
		for _, q := range []string{
			"ALTER TABLE projects ADD COLUMN category_id INTEGER REFERENCES categories(id)",
			"ALTER TABLE projects ADD COLUMN uid TEXT",
			"ALTER TABLE projects ADD COLUMN vault_project_id TEXT",
			"ALTER TABLE projects ADD COLUMN vault_path TEXT",
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_uid ON projects(uid)",
			"CREATE INDEX IF NOT EXISTS idx_projects_category ON projects(category_id)",
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		// Default category, created unconditionally so fresh installs have one.
		if _, err := tx.Exec(
			"INSERT INTO categories(uid,slug,name) SELECT ?, 'general', 'General' "+
				"WHERE NOT EXISTS (SELECT 1 FROM categories WHERE slug='general')",
			newUID("amc_")); err != nil {
			return err
		}
		if _, err := tx.Exec(
			"UPDATE projects SET category_id=(SELECT id FROM categories WHERE slug='general') WHERE category_id IS NULL"); err != nil {
			return err
		}
		// Backfill stable ids row by row — each project needs a distinct uid.
		rows, err := tx.Query("SELECT id FROM projects WHERE uid IS NULL")
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec("UPDATE projects SET uid=? WHERE id=?", newUID("amp_"), id); err != nil {
				return err
			}
		}
		return nil
	}},
	// v5: tasks.created_by — who created the task, for the Phase Q proposals
	// carve-out (a scoped agent may comment on its OWN proposal tickets).
	// Backfill is best-effort from the durable event log: the LATEST
	// task.created event's actor is the creator — latest, not first, because
	// tasks.id is a reusable rowid (no AUTOINCREMENT) and DeleteTask leaves a
	// deleted task's events behind, so an id's oldest creation event may
	// belong to a deleted predecessor; the newest one is always the current
	// incarnation's. Tasks whose events were pruned (`am db prune`) stay
	// NULL — they simply never match the own-proposal comment rule, which is
	// the safe direction.
	{version: 5, apply: func(tx *sql.Tx) error {
		if _, err := tx.Exec("ALTER TABLE tasks ADD COLUMN created_by TEXT"); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE tasks SET created_by=(
		       SELECT e.actor FROM events e
		       WHERE e.task_id=tasks.id AND e.kind='task.created'
		       ORDER BY e.id DESC LIMIT 1)
		     WHERE created_by IS NULL`)
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

// ---------- categories ----------

// CreateCategory creates a category. The slug is trimmed and lowercased (it is
// the canonical handle the `?category=` filters compare against); name defaults
// to the slug.
func (s *Store) CreateCategory(slug, name string) (*Category, *Event, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" || strings.ContainsAny(slug, " /\t") {
		return nil, nil, ErrValidation
	}
	if name == "" {
		name = slug
	}
	var exists int
	s.db.QueryRow("SELECT 1 FROM categories WHERE slug=?", slug).Scan(&exists)
	if exists == 1 {
		return nil, nil, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var id int64
	for attempt := 0; ; attempt++ {
		res, err := tx.Exec("INSERT INTO categories(uid,slug,name) VALUES(?,?,?)", newUID("amc_"), slug, name)
		if isUniqueErr(err, "categories.uid") && attempt < 2 {
			continue // astronomically unlikely uid collision — retry with a fresh uid
		}
		if err != nil {
			return nil, nil, err
		}
		id, _ = res.LastInsertId()
		break
	}
	// Category events carry no project_id (projectID 0 → NULL in insertEvent).
	ev, err := insertEvent(tx, 0, 0, "human", "category.created", map[string]any{"slug": slug})
	if err != nil {
		return nil, nil, err
	}
	var c Category
	if err := tx.QueryRow("SELECT id,uid,slug,name,created_at FROM categories WHERE id=?", id).
		Scan(&c.ID, &c.UID, &c.Slug, &c.Name, &c.CreatedAt); err != nil {
		return nil, nil, err
	}
	return &c, ev, tx.Commit()
}

func (s *Store) ListCategories(includeArchived bool) ([]Category, error) {
	q := "SELECT id, uid, slug, name, created_at, COALESCE(archived_at,'') FROM categories"
	if !includeArchived {
		q += " WHERE archived_at IS NULL"
	}
	q += " ORDER BY id"
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.UID, &c.Slug, &c.Name, &c.CreatedAt, &c.ArchivedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListCategoriesWithStats lists categories (honoring includeArchived) augmented
// with task counts and recently-active agents. Counts sum each category's tasks
// across its NON-archived projects (an archived project's tasks are excluded
// even when includeArchived shows the category). Active agents are the distinct
// non-human actors whose recent events touched a task in the category within
// activeWindow. Two queries, merged in Go by category id.
func (s *Store) ListCategoriesWithStats(includeArchived bool, activeWindow time.Duration) ([]CategoryStat, error) {
	cats, err := s.ListCategories(includeArchived)
	if err != nil {
		return nil, err
	}
	out := make([]CategoryStat, 0, len(cats))
	byID := make(map[int64]*CategoryStat, len(cats))
	for i := range cats {
		out = append(out, CategoryStat{
			Category:     cats[i],
			Counts:       map[string]int{"todo": 0, "doing": 0, "blocked": 0, "done": 0},
			ActiveAgents: []string{},
		})
	}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}

	// Counts: only non-archived projects contribute tasks.
	crows, err := s.db.Query(`SELECT c.id,
	         COALESCE(SUM(t.status='todo'),0),
	         COALESCE(SUM(t.status='doing'),0),
	         COALESCE(SUM(t.status='blocked'),0),
	         COALESCE(SUM(t.status='done'),0)
	      FROM categories c
	      LEFT JOIN projects p ON p.category_id = c.id AND p.archived_at IS NULL
	      LEFT JOIN tasks t ON t.project_id = p.id
	      GROUP BY c.id`)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var cid int64
		var todo, doing, blocked, done int
		if err := crows.Scan(&cid, &todo, &doing, &blocked, &done); err != nil {
			return nil, err
		}
		if cs := byID[cid]; cs != nil {
			cs.Counts = map[string]int{"todo": todo, "doing": doing, "blocked": blocked, "done": done}
		}
	}
	if err := crows.Err(); err != nil {
		return nil, err
	}

	// Active agents: distinct non-human actors on task-bearing events within the
	// window, keyed by category. task_id IS NOT NULL ensures comment.added counts
	// (commenting is activity) while category-level events (project/category
	// admin) do not. Ordered for stable output.
	cutoff := time.Now().Add(-activeWindow).UTC().Format("2006-01-02T15:04:05.000Z")
	arows, err := s.db.Query(`SELECT DISTINCT c.id, events.actor
	      FROM events
	      JOIN projects p ON p.id = events.project_id
	      JOIN categories c ON c.id = p.category_id
	      WHERE events.task_id IS NOT NULL
	        AND events.actor != 'human'
	        AND events.created_at > ?
	      ORDER BY c.id, events.actor`, cutoff)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var cid int64
		var actor string
		if err := arows.Scan(&cid, &actor); err != nil {
			return nil, err
		}
		if cs := byID[cid]; cs != nil {
			cs.ActiveAgents = append(cs.ActiveAgents, actor)
		}
	}
	return out, arows.Err()
}

// ProjectIDsInCategory returns the set of project ids belonging to a category,
// by category id. Used by the hub to resolve a category subscription into a
// project-id set once at Subscribe time (no per-event DB hits).
func (s *Store) ProjectIDsInCategory(cid int64) (map[int64]bool, error) {
	rows, err := s.db.Query("SELECT id FROM projects WHERE category_id=?", cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ArchiveCategory soft-archives a category (sets archived_at). Idempotent.
func (s *Store) ArchiveCategory(slug, actor string) (*Category, *Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var c Category
	var archivedAt string
	err = tx.QueryRow("SELECT id, uid, slug, name, created_at, COALESCE(archived_at,'') FROM categories WHERE slug=?", slug).
		Scan(&c.ID, &c.UID, &c.Slug, &c.Name, &c.CreatedAt, &archivedAt)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if archivedAt != "" {
		// Already archived — idempotent success, no event
		c.ArchivedAt = archivedAt
		return &c, nil, tx.Commit()
	}
	if _, err := tx.Exec(
		"UPDATE categories SET archived_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", c.ID); err != nil {
		return nil, nil, err
	}
	ev, err := insertEvent(tx, 0, 0, actorOr(actor), "category.archived", map[string]any{"slug": slug})
	if err != nil {
		return nil, nil, err
	}
	if err := tx.QueryRow("SELECT COALESCE(archived_at,'') FROM categories WHERE id=?", c.ID).Scan(&c.ArchivedAt); err != nil {
		return nil, nil, err
	}
	return &c, ev, tx.Commit()
}

// UnarchiveCategory restores a category (clears archived_at). Idempotent.
func (s *Store) UnarchiveCategory(slug, actor string) (*Category, *Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var c Category
	var archivedAt string
	err = tx.QueryRow("SELECT id, uid, slug, name, created_at, COALESCE(archived_at,'') FROM categories WHERE slug=?", slug).
		Scan(&c.ID, &c.UID, &c.Slug, &c.Name, &c.CreatedAt, &archivedAt)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if archivedAt == "" {
		// Not archived — idempotent success, no event
		return &c, nil, tx.Commit()
	}
	if _, err := tx.Exec("UPDATE categories SET archived_at=NULL WHERE id=?", c.ID); err != nil {
		return nil, nil, err
	}
	ev, err := insertEvent(tx, 0, 0, actorOr(actor), "category.unarchived", map[string]any{"slug": slug})
	if err != nil {
		return nil, nil, err
	}
	c.ArchivedAt = "" // explicitly clear; was scanned into a local var, not c.ArchivedAt
	return &c, ev, tx.Commit()
}

func (s *Store) categoryID(slug string) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT id FROM categories WHERE slug=?", slug).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	return id, err
}

// ---------- projects ----------

// ListProjects lists projects with task counts. By default a project is hidden
// when it OR its category is archived (cascade); an explicit category scope
// drops the category-archived condition so an archived category's projects stay
// inspectable (mirroring the ListTasks explicit-project rule).
func (s *Store) ListProjects(includeArchived bool, category string) ([]Project, error) {
	q := `SELECT p.id, p.uid, p.slug, p.name, c.slug, p.created_at, COALESCE(p.archived_at,''),
	          COALESCE(p.vault_project_id,''), COALESCE(p.vault_path,''),
	          COALESCE(SUM(t.status='todo'),0),
	          COALESCE(SUM(t.status='doing'),0),
	          COALESCE(SUM(t.status='blocked'),0),
	          COALESCE(SUM(t.status='done'),0)
	      FROM projects p JOIN categories c ON c.id=p.category_id
	      LEFT JOIN tasks t ON t.project_id = p.id`
	var where []string
	var args []any
	if category != "" {
		where = append(where, "c.slug=?")
		args = append(args, category)
		if !includeArchived {
			where = append(where, "p.archived_at IS NULL")
		}
	} else if !includeArchived {
		where = append(where, "p.archived_at IS NULL AND c.archived_at IS NULL")
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " GROUP BY p.id ORDER BY p.id"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		var todo, doing, blocked, done int
		if err := rows.Scan(&p.ID, &p.UID, &p.Slug, &p.Name, &p.Category, &p.CreatedAt, &p.ArchivedAt,
			&p.VaultProjectID, &p.VaultPath, &todo, &doing, &blocked, &done); err != nil {
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

	var id int64
	var archivedAt string
	err = tx.QueryRow("SELECT id, COALESCE(archived_at,'') FROM projects WHERE slug=?", slug).
		Scan(&id, &archivedAt)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if archivedAt != "" {
		// Already archived — idempotent success, no event
		p, err := getProjectTx(tx, id)
		if err != nil {
			return nil, nil, err
		}
		return p, nil, tx.Commit()
	}
	if _, err := tx.Exec(
		"UPDATE projects SET archived_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", id); err != nil {
		return nil, nil, err
	}
	ev, err := insertEvent(tx, id, 0, actorOr(actor), "project.archived", map[string]any{"slug": slug})
	if err != nil {
		return nil, nil, err
	}
	p, err := getProjectTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	return p, ev, tx.Commit()
}

// UnarchiveProject restores a project (clears archived_at). Idempotent.
func (s *Store) UnarchiveProject(slug, actor string) (*Project, *Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var id int64
	var archivedAt string
	err = tx.QueryRow("SELECT id, COALESCE(archived_at,'') FROM projects WHERE slug=?", slug).
		Scan(&id, &archivedAt)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if archivedAt == "" {
		// Not archived — idempotent success, no event
		p, err := getProjectTx(tx, id)
		if err != nil {
			return nil, nil, err
		}
		return p, nil, tx.Commit()
	}
	if _, err := tx.Exec("UPDATE projects SET archived_at=NULL WHERE id=?", id); err != nil {
		return nil, nil, err
	}
	ev, err := insertEvent(tx, id, 0, actorOr(actor), "project.unarchived", map[string]any{"slug": slug})
	if err != nil {
		return nil, nil, err
	}
	p, err := getProjectTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	return p, ev, tx.Commit()
}

// CreateProject creates a project inside a category (empty category defaults
// to "general"). Project slugs are globally unique — a slug names exactly one
// project regardless of category, so existing task refs like "web-3" stay
// unambiguous. Returns ErrNotFound for an unknown category and
// ErrCategoryArchived for an archived one.
func (s *Store) CreateProject(slug, name, category string) (*Project, *Event, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" || strings.ContainsAny(slug, " /\t") {
		return nil, nil, ErrValidation
	}
	if name == "" {
		name = slug
	}
	if category == "" {
		category = "general"
	}
	cid, err := s.categoryID(category)
	if err != nil {
		return nil, nil, err // ErrNotFound for a bad slug
	}
	var catArchived sql.NullString
	if err := s.db.QueryRow("SELECT archived_at FROM categories WHERE id=?", cid).Scan(&catArchived); err != nil {
		return nil, nil, err
	}
	if catArchived.Valid && catArchived.String != "" {
		return nil, nil, ErrCategoryArchived
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
	var id int64
	for attempt := 0; ; attempt++ {
		res, err := tx.Exec("INSERT INTO projects(slug,name,category_id,uid) VALUES(?,?,?,?)",
			slug, name, cid, newUID("amp_"))
		if isUniqueErr(err, "projects.uid") && attempt < 2 {
			continue // astronomically unlikely uid collision — retry with a fresh uid
		}
		if err != nil {
			return nil, nil, err
		}
		id, _ = res.LastInsertId()
		break
	}
	ev, err := insertEvent(tx, id, 0, "human", "project.created", map[string]any{"slug": slug})
	if err != nil {
		return nil, nil, err
	}
	p, err := getProjectTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	return p, ev, tx.Commit()
}

// getProjectTx loads one extended project row (no counts) by id.
func getProjectTx(q queryer, id int64) (*Project, error) {
	var p Project
	err := q.QueryRow(`SELECT p.id, p.uid, p.slug, p.name, c.slug, p.created_at,
	         COALESCE(p.archived_at,''), COALESCE(p.vault_project_id,''), COALESCE(p.vault_path,'')
	       FROM projects p JOIN categories c ON c.id=p.category_id WHERE p.id=?`, id).
		Scan(&p.ID, &p.UID, &p.Slug, &p.Name, &p.Category, &p.CreatedAt,
			&p.ArchivedAt, &p.VaultProjectID, &p.VaultPath)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PatchProject applies allowed field changes to a project and records a single
// project.patched event (modeled on PatchTask). Allowed keys: slug, name,
// vault_project_id, vault_path. uid and category_id are deliberately NOT
// patchable — the uid is the immutable correlation key and category moves are
// out of scope. Unknown keys are ignored; a no-op patch is idempotent success
// with no event.
//
// Scope note: the handler's scope pre-check runs OUTSIDE this transaction,
// which is sound only because category_id is not patchable (a project never
// moves between categories) — if a move feature ships, the check must move
// in-tx.
func (s *Store) PatchProject(slug string, patch map[string]any, actor string) (*Project, *Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow("SELECT id FROM projects WHERE slug=?", slug).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	cur, err := getProjectTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	var sets []string
	var args []any
	delta := map[string]any{}

	if v, ok := patch["slug"]; ok {
		ns, _ := v.(string)
		ns = strings.TrimSpace(ns)
		if ns == "" || strings.ContainsAny(ns, " /\t") || len(ns) > maxTitleLen {
			return nil, nil, ErrValidation
		}
		if ns != cur.Slug {
			var exists int
			tx.QueryRow("SELECT 1 FROM projects WHERE slug=? AND id!=?", ns, id).Scan(&exists)
			if exists == 1 {
				return nil, nil, ErrConflict
			}
			sets = append(sets, "slug=?")
			args = append(args, ns)
			delta["slug"] = []any{cur.Slug, ns}
		}
	}
	if v, ok := patch["name"]; ok {
		nm, _ := v.(string)
		if strings.TrimSpace(nm) == "" || len(nm) > maxTitleLen {
			return nil, nil, ErrValidation
		}
		if nm != cur.Name {
			sets = append(sets, "name=?")
			args = append(args, nm)
			delta["name"] = []any{cur.Name, nm}
		}
	}
	if v, ok := patch["vault_project_id"]; ok {
		vid, _ := v.(string)
		if len(vid) > maxTitleLen {
			return nil, nil, ErrValidation
		}
		if vid != cur.VaultProjectID {
			sets = append(sets, "vault_project_id=?")
			args = append(args, nullStr(vid))
			delta["vault_project_id"] = []any{nullable(cur.VaultProjectID), nullable(vid)}
		}
	}
	if v, ok := patch["vault_path"]; ok {
		vp, _ := v.(string)
		if len(vp) > maxTitleLen {
			return nil, nil, ErrValidation
		}
		if vp != cur.VaultPath {
			sets = append(sets, "vault_path=?")
			args = append(args, nullStr(vp))
			delta["vault_path"] = []any{nullable(cur.VaultPath), nullable(vp)}
		}
	}

	if len(sets) == 0 { // no-op: idempotent success, no event
		return cur, nil, tx.Commit()
	}
	args = append(args, id)
	if _, err := tx.Exec("UPDATE projects SET "+strings.Join(sets, ",")+" WHERE id=?", args...); err != nil {
		return nil, nil, err
	}
	ev, err := insertEvent(tx, id, 0, actorOr(actor), "project.patched", delta)
	if err != nil {
		return nil, nil, err
	}
	p, err := getProjectTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	return p, ev, tx.Commit()
}

func (s *Store) projectID(slug string) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT id FROM projects WHERE slug=?", slug).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	return id, err
}

// projectCategory returns the category slug a project belongs to.
// ErrNotFound for an unknown project slug.
func (s *Store) projectCategory(slug string) (string, error) {
	var cat string
	err := s.db.QueryRow(
		"SELECT c.slug FROM projects p JOIN categories c ON c.id=p.category_id WHERE p.slug=?",
		slug).Scan(&cat)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return cat, err
}

// taskScope returns the category slug, project slug, and creator of a task in
// one SELECT — everything the server's scope checks need. created_by is empty
// for pre-v5 tasks whose events were pruned before the backfill.
// ErrNotFound for an unknown task id.
func (s *Store) taskScope(id int64) (category, project, createdBy string, err error) {
	err = s.db.QueryRow(`SELECT c.slug, p.slug, COALESCE(t.created_by,'')
	       FROM tasks t JOIN projects p ON p.id=t.project_id
	       JOIN categories c ON c.id=p.category_id WHERE t.id=?`, id).
		Scan(&category, &project, &createdBy)
	if err == sql.ErrNoRows {
		return "", "", "", ErrNotFound
	}
	return category, project, createdBy, err
}

// ---------- tasks ----------

func (s *Store) ListTasks(f TaskFilter) ([]Task, error) {
	var where []string
	var args []any
	if f.Project != "" {
		where = append(where, "p.slug=?")
		args = append(args, f.Project)
	} else if f.Category != "" {
		// Explicit category, no project: still hide archived projects, but keep
		// the category itself inspectable even when archived (same rule as the
		// explicit-project case below).
		where = append(where, "p.archived_at IS NULL")
	} else {
		// No explicit scope: hide tasks belonging to archived projects OR
		// archived categories (cascade) from the unfiltered board/list. An
		// explicit project filter still returns that project's tasks (so an
		// archived project can still be inspected directly).
		where = append(where, "p.archived_at IS NULL AND c.archived_at IS NULL")
	}
	if f.Category != "" {
		where = append(where, "c.slug=?")
		args = append(args, f.Category)
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
	if f.Query != "" {
		where = append(where, `(t.title LIKE ? ESCAPE '\' OR t.body LIKE ? ESCAPE '\')`)
		pat := "%" + likeEscape(f.Query) + "%"
		args = append(args, pat, pat)
	}
	if f.Label != "" {
		l, err := normalizeLabel(f.Label)
		if err != nil {
			return nil, err
		}
		where = append(where, "EXISTS (SELECT 1 FROM task_labels tl WHERE tl.task_id=t.id AND tl.label=?)")
		args = append(args, l)
	}
	if f.MetaKey != "" {
		k, err := normalizeMetaKey(f.MetaKey)
		if err != nil {
			return nil, err
		}
		where = append(where, "EXISTS (SELECT 1 FROM task_meta m WHERE m.task_id=t.id AND m.key=?)")
		args = append(args, k)
	}
	// open-prereq subquery reused for both Blocked and Ready filters.
	const openPrereqExpr = `EXISTS (SELECT 1 FROM task_deps d JOIN tasks pt ON pt.id=d.depends_on_id WHERE d.task_id=t.id AND pt.status!='done')`
	if f.Blocked {
		where = append(where, openPrereqExpr)
	}
	if f.Ready {
		where = append(where, "t.status='todo' AND NOT "+openPrereqExpr)
	}
	if f.Stale > 0 {
		// Stale = assigned, not done, and no activity (updated_at) since the cutoff.
		where = append(where, "t.assignee IS NOT NULL AND t.status!='done' AND t.updated_at < ?")
		args = append(args, staleCutoff(f.Stale))
	}
	q := `SELECT t.id,t.ref,p.slug,t.title,t.status,COALESCE(t.assignee,''),t.priority,
	         t.created_at,t.updated_at,COALESCE(t.claimed_at,''),
	         (SELECT COUNT(*) FROM comments c WHERE c.task_id=t.id),
	         COALESCE((SELECT COUNT(*) FROM task_deps d WHERE d.task_id=t.id),0),
	         COALESCE((SELECT COUNT(*) FROM task_deps d JOIN tasks pt ON pt.id=d.depends_on_id WHERE d.task_id=t.id AND pt.status!='done'),0),
	         COALESCE((SELECT GROUP_CONCAT(label) FROM task_labels tl WHERE tl.task_id=t.id),'')
	       FROM tasks t JOIN projects p ON p.id=t.project_id JOIN categories c ON c.id=p.category_id`
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
		var labelsCSV string
		if err := rows.Scan(&t.ID, &t.Ref, &t.Project, &t.Title, &t.Status, &t.Assignee,
			&t.Priority, &t.CreatedAt, &t.UpdatedAt, &t.ClaimedAt, &t.NComments, &t.NPrereqs, &t.NOpenPrereqs, &labelsCSV); err != nil {
			return nil, err
		}
		if labelsCSV != "" {
			// Sort in Go — GROUP_CONCAT order is not guaranteed. The label charset
			// excludes ',', so splitting on it is safe.
			t.Labels = strings.Split(labelsCSV, ",")
			sort.Strings(t.Labels)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Stitch meta with one follow-up SELECT. Values are opaque (may contain
	// ',' or '='), so the GROUP_CONCAT trick used for labels is unsafe here.
	if len(out) > 0 {
		idx := make(map[int64]int, len(out))
		ph := make([]string, len(out))
		margs := make([]any, len(out))
		for i := range out {
			idx[out[i].ID] = i
			ph[i] = "?"
			margs[i] = out[i].ID
		}
		mrows, err := s.db.Query(
			"SELECT task_id, key, value FROM task_meta WHERE task_id IN ("+strings.Join(ph, ",")+")", margs...)
		if err != nil {
			return nil, err
		}
		defer mrows.Close()
		for mrows.Next() {
			var tid int64
			var k, v string
			if err := mrows.Scan(&tid, &k, &v); err != nil {
				return nil, err
			}
			i := idx[tid]
			if out[i].Meta == nil {
				out[i].Meta = map[string]string{}
			}
			out[i].Meta[k] = v
		}
		if err := mrows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) getTaskTx(q queryer, id int64) (*Task, error) {
	var t Task
	var assignee sql.NullString
	err := q.QueryRow(`SELECT t.id,t.project_id,p.slug,t.ref,t.title,t.body,t.status,t.assignee,
	         COALESCE(t.created_by,''),t.priority,t.created_at,t.updated_at,COALESCE(t.claimed_at,''),
	         (SELECT COUNT(*) FROM comments c WHERE c.task_id=t.id)
	       FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=?`, id).
		Scan(&t.ID, &t.ProjectID, &t.Project, &t.Ref, &t.Title, &t.Body, &t.Status,
			&assignee, &t.CreatedBy, &t.Priority, &t.CreatedAt, &t.UpdatedAt, &t.ClaimedAt, &t.NComments)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Assignee = assignee.String
	return &t, nil
}

// GetTask returns a task with comments, recent events, and dependency refs.
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

	// Populate DependsOn (prerequisites of this task).
	drows, err := s.db.Query(`SELECT t.id,t.ref,p.slug,t.title,t.status
	       FROM task_deps d JOIN tasks t ON t.id=d.depends_on_id JOIN projects p ON p.id=t.project_id
	       WHERE d.task_id=? ORDER BY t.id`, id)
	if err != nil {
		return nil, err
	}
	defer drows.Close()
	t.DependsOn = []DepRef{}
	for drows.Next() {
		var dr DepRef
		if err := drows.Scan(&dr.ID, &dr.Ref, &dr.Project, &dr.Title, &dr.Status); err != nil {
			return nil, err
		}
		t.DependsOn = append(t.DependsOn, dr)
	}
	if err := drows.Err(); err != nil {
		return nil, err
	}

	// Populate Blocks (tasks that depend on this task).
	brows, err := s.db.Query(`SELECT t.id,t.ref,p.slug,t.title,t.status
	       FROM task_deps d JOIN tasks t ON t.id=d.task_id JOIN projects p ON p.id=t.project_id
	       WHERE d.depends_on_id=? ORDER BY t.id`, id)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	t.Blocks = []DepRef{}
	for brows.Next() {
		var dr DepRef
		if err := brows.Scan(&dr.ID, &dr.Ref, &dr.Project, &dr.Title, &dr.Status); err != nil {
			return nil, err
		}
		t.Blocks = append(t.Blocks, dr)
	}
	if err := brows.Err(); err != nil {
		return nil, err
	}

	// Populate Labels (sorted for stable output).
	lrows, err := s.db.Query("SELECT label FROM task_labels WHERE task_id=? ORDER BY label", id)
	if err != nil {
		return nil, err
	}
	defer lrows.Close()
	for lrows.Next() {
		var l string
		if err := lrows.Scan(&l); err != nil {
			return nil, err
		}
		t.Labels = append(t.Labels, l)
	}
	if err := lrows.Err(); err != nil {
		return nil, err
	}

	// Populate Meta (the map is unordered; JSON output sorts keys on marshal).
	mrows, err := s.db.Query("SELECT key, value FROM task_meta WHERE task_id=? ORDER BY key", id)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var k, v string
		if err := mrows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if t.Meta == nil {
			t.Meta = map[string]string{}
		}
		t.Meta[k] = v
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}

	// Also populate the terse counts for the full task view.
	if err := s.db.QueryRow(`SELECT
	       COALESCE((SELECT COUNT(*) FROM task_deps d WHERE d.task_id=?),0),
	       COALESCE((SELECT COUNT(*) FROM task_deps d JOIN tasks pt ON pt.id=d.depends_on_id WHERE d.task_id=? AND pt.status!='done'),0)`,
		id, id).Scan(&t.NPrereqs, &t.NOpenPrereqs); err != nil {
		return nil, err
	}

	return t, nil
}

// Input size limits. A runaway agent should not be able to insert megabyte
// titles that render into every board card and SSE event. Exceeding a limit
// (or an out-of-range priority) maps to ErrValidation → HTTP 400 → CLI exit 5.
const (
	maxTitleLen = 500     // bytes
	maxBodyLen  = 1 << 16 // 64 KiB; also the comment-body cap
	maxLabelLen = 50      // bytes
	minPriority = 0
	maxPriority = 3
)

// labelRe is the allowed label charset. It excludes ',' (so GROUP_CONCAT output
// splits safely) and '+'/space (so CLI +add/-remove tokens stay unambiguous).
var labelRe = regexp.MustCompile(`^[a-z0-9._-]+$`)

// normalizeLabel trims, lowercases, and validates a label. Lowercasing happens
// at this boundary so the `?label=` equality filter is predictable (SQL `=` is
// case-sensitive even though LIKE is not).
func normalizeLabel(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 1 || len(s) > maxLabelLen || !labelRe.MatchString(s) {
		return "", fmt.Errorf("%w: label must be 1-50 chars of a-z 0-9 . _ -", ErrValidation)
	}
	return s, nil
}

// normalizeMetaKey trims, lowercases, and validates a meta key. Keys are the
// filterable unit, so they reuse the label rules (labelRe, maxLabelLen) — the
// charset excludes '=' and space, keeping the CLI's key=value tokens
// unambiguous. Values stay opaque (any bytes, capped at maxTitleLen).
func normalizeMetaKey(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 1 || len(s) > maxLabelLen || !labelRe.MatchString(s) {
		return "", fmt.Errorf("%w: meta key must be 1-50 chars of a-z 0-9 . _ -", ErrValidation)
	}
	return s, nil
}

type CreateTaskInput struct {
	Project  string
	Title    string
	Body     string
	Priority int
	Assignee string
	Actor    string
	Meta     map[string]string // initial key→value pairs; empty values are invalid here
}

func (s *Store) CreateTask(in CreateTaskInput) (*Task, *Event, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, nil, ErrValidation
	}
	if len(in.Title) > maxTitleLen || len(in.Body) > maxBodyLen ||
		in.Priority < minPriority || in.Priority > maxPriority {
		return nil, nil, ErrValidation
	}
	// Validate every meta pair up front (all-or-nothing): keys normalized like
	// labels; values opaque but non-empty (removal has no meaning at create)
	// and capped like titles (they render onto board cards and SSE payloads).
	meta := make(map[string]string, len(in.Meta))
	for k, v := range in.Meta {
		nk, err := normalizeMetaKey(k)
		if err != nil {
			return nil, nil, err
		}
		if v == "" || len(v) > maxTitleLen {
			return nil, nil, fmt.Errorf("%w: meta value must be 1-%d bytes", ErrValidation, maxTitleLen)
		}
		// Two raw keys collapsing to one normalized key would make the winner
		// map-iteration nondeterministic — reject instead.
		if _, dup := meta[nk]; dup {
			return nil, nil, fmt.Errorf("%w: duplicate meta key after normalization: %q", ErrValidation, nk)
		}
		meta[nk] = v
	}
	pid, err := s.projectID(in.Project)
	if err != nil {
		return nil, nil, err
	}
	var archivedAt sql.NullString
	if err := s.db.QueryRow("SELECT archived_at FROM projects WHERE id=?", pid).Scan(&archivedAt); err != nil {
		return nil, nil, err
	}
	if archivedAt.Valid && archivedAt.String != "" {
		return nil, nil, ErrProjectArchived
	}
	// Archived-category cascade: a hidden board must not accept new tasks either.
	var catArchived sql.NullString
	if err := s.db.QueryRow(
		"SELECT c.archived_at FROM projects p JOIN categories c ON c.id=p.category_id WHERE p.id=?", pid).
		Scan(&catArchived); err != nil {
		return nil, nil, err
	}
	if catArchived.Valid && catArchived.String != "" {
		return nil, nil, ErrCategoryArchived
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
	res, err := tx.Exec(`INSERT INTO tasks(project_id,ref,title,body,priority,assignee,created_by)
	         VALUES(?,?,?,?,?,?,?)`, pid, ref, in.Title, in.Body, in.Priority, nullStr(in.Assignee),
		actorOr(in.Actor))
	if err != nil {
		return nil, nil, err
	}
	id, _ := res.LastInsertId()
	evData := map[string]any{"title": in.Title}
	if len(meta) > 0 {
		keys := make([]string, 0, len(meta))
		for k := range meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, err := tx.Exec("INSERT INTO task_meta(task_id,key,value) VALUES(?,?,?)", id, k, meta[k]); err != nil {
				return nil, nil, err
			}
		}
		evData["meta"] = meta
	}
	ev, err := insertEvent(tx, pid, id, actorOr(in.Actor), "task.created", evData)
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
//
// Scope note: the handler's scope pre-check (checkTaskMut) runs OUTSIDE this
// transaction. That is sound only because a task's project (and a project's
// category) is immutable today — if a task/project move feature ever ships,
// the scope check must move inside this tx.
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
			// Hard-block: cannot move to doing/done if open prereqs exist.
			if st == "doing" || st == "done" {
				openIDs, err := hasOpenPrereqs(tx, id)
				if err != nil {
					return nil, nil, err
				}
				if len(openIDs) > 0 {
					return nil, nil, &BlockedError{OpenPrereqs: openIDs}
				}
			}
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
			// Keep claimed_at in step with the assignee: set on (re)assign, clear
			// on unassign (so `am drop` resets it).
			if as != "" {
				sets = append(sets, "claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')")
			} else {
				sets = append(sets, "claimed_at=NULL")
			}
			delta["assignee"] = []any{nullable(cur.Assignee), nullable(as)}
			assignChanged = true
		}
	}
	if v, ok := patch["title"]; ok {
		ti, _ := v.(string)
		if strings.TrimSpace(ti) == "" || len(ti) > maxTitleLen {
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
		if len(bo) > maxBodyLen {
			return nil, nil, ErrValidation
		}
		if bo != cur.Body {
			sets = append(sets, "body=?")
			args = append(args, bo)
			delta["body"] = true
		}
	}
	if v, ok := patch["priority"]; ok {
		pr := toInt(v, cur.Priority)
		if pr < minPriority || pr > maxPriority {
			return nil, nil, ErrValidation
		}
		if pr != cur.Priority {
			sets = append(sets, "priority=?")
			args = append(args, pr)
			delta["priority"] = []any{cur.Priority, pr}
		}
	}
	metaChanged := false
	if v, ok := patch["meta"]; ok {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("%w: meta must be an object of string values", ErrValidation)
		}
		meta := make(map[string]string, len(mm))
		for k, raw := range mm {
			sv, ok := raw.(string)
			if !ok {
				return nil, nil, fmt.Errorf("%w: meta values must be strings", ErrValidation)
			}
			meta[k] = sv
		}
		var metaDelta map[string]any
		metaDelta, metaChanged, err = applyMetaTx(tx, id, meta)
		if err != nil {
			return nil, nil, err
		}
		if metaChanged {
			delta["meta"] = metaDelta
		}
	}

	if len(sets) == 0 && !metaChanged { // no-op: idempotent success, no event
		return cur, nil, tx.Commit()
	}
	// Meta-only patches deliberately skip the updated_at bump: meta is metadata
	// like labels, and refreshing the activity timestamp would keep a stale
	// claim alive (the AddLabel/dep-edge precedent, ADR-024).
	if len(sets) > 0 {
		sets = append(sets, "updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')")
		args = append(args, id)
		if _, err := tx.Exec("UPDATE tasks SET "+strings.Join(sets, ",")+" WHERE id=?", args...); err != nil {
			return nil, nil, err
		}
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

// hasOpenPrereqs returns the IDs of any prerequisite tasks that are not yet done.
// It is called within an existing transaction via the queryer interface.
func hasOpenPrereqs(q queryer, taskID int64) ([]int64, error) {
	type txQuerier interface {
		Query(query string, args ...any) (*sql.Rows, error)
	}
	// queryer only exposes QueryRow; we need Query here. Accept *sql.Tx or *sql.DB
	// by type-asserting to the broader interface.
	qr, ok := q.(txQuerier)
	if !ok {
		// Fail loud rather than silently skipping the hard-block check (which would
		// let a blocked task be claimed/started). All current callers pass *sql.Tx/*sql.DB.
		return nil, fmt.Errorf("hasOpenPrereqs: queryer %T does not support Query", q)
	}
	rows, err := qr.Query(`SELECT d.depends_on_id FROM task_deps d
	       JOIN tasks pt ON pt.id=d.depends_on_id
	       WHERE d.task_id=? AND pt.status!='done'`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimTask atomically assigns an unclaimed, non-done task to agent.
// Returns (task, event, nil) on win; (task, nil, nil) if agent already owns it
// (idempotent); (nil,nil,*ConflictError) if owned by someone else; ErrNotFound.
// Returns *BlockedError if the task has open prerequisites.
func (s *Store) ClaimTask(id int64, agent string) (*Task, *Event, error) {
	if agent == "" {
		return nil, nil, ErrValidation
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	// Hard-block: check prerequisites before attempting the claim.
	openIDs, err := hasOpenPrereqs(tx, id)
	if err != nil {
		return nil, nil, err
	}
	if len(openIDs) > 0 {
		return nil, nil, &BlockedError{OpenPrereqs: openIDs}
	}

	var newStatus string
	var pid int64
	err = tx.QueryRow(`
		UPDATE tasks
		   SET assignee=?,
		       claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
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

// StealStaleClaim atomically takes over a task whose current claim has gone
// stale — no activity (updated_at) for at least staleFor. An unclaimed task
// degrades to a normal claim. Returns (task, event, nil) on win; (task, nil,
// nil) if agent already owns it (idempotent); (nil,nil,*NotStaleError) if held
// by someone else and still fresh; (nil,nil,*ConflictError) if done; ErrNotFound.
// Returns *BlockedError if the task has open prerequisites.
func (s *Store) StealStaleClaim(id int64, agent string, staleFor time.Duration) (*Task, *Event, error) {
	if agent == "" || staleFor <= 0 {
		return nil, nil, ErrValidation
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	// Hard-block: check prerequisites before attempting the takeover.
	openIDs, err := hasOpenPrereqs(tx, id)
	if err != nil {
		return nil, nil, err
	}
	if len(openIDs) > 0 {
		return nil, nil, &BlockedError{OpenPrereqs: openIDs}
	}

	// Prior state, read in the same tx, so the event can name the previous holder.
	cur, err := s.getTaskTx(tx, id)
	if err != nil {
		return nil, nil, err // ErrNotFound or real error
	}
	if cur.Assignee == agent {
		return cur, nil, tx.Commit() // idempotent re-claim
	}

	// Conditional UPDATE is the atomicity guarantee: only one stealer can match
	// the stale predicate; the row is then fresh, so concurrent stealers miss.
	var newStatus string
	var pid int64
	err = tx.QueryRow(`
		UPDATE tasks
		   SET assignee=?,
		       claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		       status=CASE WHEN status='todo' THEN 'doing' ELSE status END,
		       updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id=? AND status!='done' AND (assignee IS NULL OR updated_at < ?)
		RETURNING project_id, status`, agent, id, staleCutoff(staleFor)).Scan(&pid, &newStatus)
	if err == sql.ErrNoRows {
		if cur.Status == "done" {
			return nil, nil, &ConflictError{Assignee: orDash(cur.Assignee, cur.Status)}
		}
		return nil, nil, &NotStaleError{Assignee: cur.Assignee}
	}
	if err != nil {
		return nil, nil, err
	}
	var ev *Event
	if cur.Assignee != "" {
		ev, err = insertEvent(tx, pid, id, agent, "task.reclaimed",
			map[string]any{"assignee": []any{cur.Assignee, agent}, "status": newStatus,
				"stale_for": staleFor.String()})
	} else {
		// Unclaimed: degrade to a normal claim (same payload shape as ClaimTask).
		ev, err = insertEvent(tx, pid, id, agent, "task.claimed",
			map[string]any{"assignee": []any{nil, agent}, "status": newStatus})
	}
	if err != nil {
		return nil, nil, err
	}
	t, err := s.getTaskTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	return t, ev, tx.Commit()
}

// NextTask atomically picks and claims the best ready task: status todo,
// unassigned, no open prerequisites, in a non-archived project AND non-archived
// category — optionally scoped by f (project/category slug, meta key). Returns
// ErrNotFound when nothing qualifies (or a slug does not exist). Tasks
// pre-assigned to the caller are deliberately skipped (candidates require
// assignee IS NULL) — claim those with `am claim`.
//
// Scope contract (Phase Q): a scoped caller's X-Agent-Scope is merged into f
// by the handler (narrowScope) BEFORE this runs, so the agent's scope is part
// of the candidate predicate inside the atomic pick+claim — a scoped agent can
// never be handed an out-of-scope task, even racing unscoped callers.
func (s *Store) NextTask(f NextFilter, agent string) (*Task, *Event, error) {
	if agent == "" {
		return nil, nil, ErrValidation
	}
	scope := ""
	args := []any{agent}
	if f.Project != "" {
		pid, err := s.projectID(f.Project)
		if err != nil {
			return nil, nil, err // ErrNotFound for a bad slug
		}
		scope += " AND t.project_id=?"
		args = append(args, pid)
	}
	if f.Category != "" {
		cid, err := s.categoryID(f.Category)
		if err != nil {
			return nil, nil, err // ErrNotFound for a bad slug
		}
		scope += " AND p.category_id=?"
		args = append(args, cid)
	}
	if f.MetaKey != "" {
		k, err := normalizeMetaKey(f.MetaKey)
		if err != nil {
			return nil, nil, err
		}
		// Must stay textually equivalent to ListTasks' MetaKey predicate — the
		// wait/next invariant: a task that releases `am wait --ready --meta K`
		// must be pickable by `am next --meta K`. Phase Q extends scope here.
		scope += " AND EXISTS (SELECT 1 FROM task_meta m WHERE m.task_id=t.id AND m.key=?)"
		args = append(args, k)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	// Pick + claim in ONE conditional UPDATE — the project's race primitive
	// (ADR-022/ADR-023); serialized by SetMaxOpenConns(1), so concurrent callers
	// get distinct tasks. The NOT EXISTS open-prereq predicate matches ListTasks'
	// Ready filter exactly. Ordering: priority ASC (0 = most urgent, matching
	// ListTasks), then id ASC — a FIFO tiebreak, deliberately NOT the
	// updated_at DESC display order of `am ls`: a pickup queue drains oldest-first.
	var id, pid int64
	err = tx.QueryRow(`
		UPDATE tasks
		   SET assignee=?,
		       claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		       status='doing',
		       updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = (
		   SELECT t.id FROM tasks t JOIN projects p ON p.id=t.project_id
		                           JOIN categories c ON c.id=p.category_id
		   WHERE t.status='todo' AND t.assignee IS NULL AND p.archived_at IS NULL
		     AND c.archived_at IS NULL`+scope+`
		     AND NOT EXISTS (SELECT 1 FROM task_deps d JOIN tasks pt ON pt.id=d.depends_on_id
		                     WHERE d.task_id=t.id AND pt.status!='done')
		   ORDER BY t.priority ASC, t.id ASC LIMIT 1)
		RETURNING id, project_id`, args...).Scan(&id, &pid)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	// Same event kind + payload shape as ClaimTask — a next pickup IS a claim.
	ev, err := insertEvent(tx, pid, id, agent, "task.claimed",
		map[string]any{"assignee": []any{nil, agent}, "status": "doing"})
	if err != nil {
		return nil, nil, err
	}
	t, err := s.getTaskTx(tx, id)
	if err != nil {
		return nil, nil, err
	}
	return t, ev, tx.Commit()
}

// DeleteTask hard-deletes a task (and its comments via FK cascade) and records
// a task.deleted event in the same transaction. Returns ErrNotFound if the task
// does not exist.
func (s *Store) DeleteTask(id int64, actor string) (*Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	t, err := s.getTaskTx(tx, id)
	if err != nil {
		return nil, err
	}
	ev, err := insertEvent(tx, t.ProjectID, id, actorOr(actor), "task.deleted",
		map[string]any{"title": t.Title})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM tasks WHERE id=?", id); err != nil {
		return nil, err
	}
	return ev, tx.Commit()
}

// DeleteComment hard-deletes a single comment that belongs to taskID. Bumps
// the task's updated_at and records a comment.deleted event. Returns ErrNotFound
// if the comment does not exist or does not belong to taskID.
func (s *Store) DeleteComment(taskID, commentID int64, actor string) (*Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Verify the comment exists and belongs to this task.
	var dummy int64
	err = tx.QueryRow("SELECT id FROM comments WHERE id=? AND task_id=?", commentID, taskID).Scan(&dummy)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// Need the task's project_id for the event.
	t, err := s.getTaskTx(tx, taskID)
	if err != nil {
		return nil, err
	}

	ev, err := insertEvent(tx, t.ProjectID, taskID, actorOr(actor), "comment.deleted",
		map[string]any{"comment_id": commentID})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM comments WHERE id=?", commentID); err != nil {
		return nil, err
	}
	tx.Exec("UPDATE tasks SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", taskID)
	return ev, tx.Commit()
}

// DeleteProject hard-deletes a project (and its tasks+comments via FK cascade)
// and records a project.deleted event. Returns ErrNotFound if the slug does not exist.
func (s *Store) DeleteProject(slug, actor string) (*Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var projectID int64
	err = tx.QueryRow("SELECT id FROM projects WHERE slug=?", slug).Scan(&projectID)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	ev, err := insertEvent(tx, projectID, 0, actorOr(actor), "project.deleted",
		map[string]any{"slug": slug})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM projects WHERE id=?", projectID); err != nil {
		return nil, err
	}
	return ev, tx.Commit()
}

// ---------- dependencies ----------

// AddDep records a dependency edge: taskID depends on dependsOnID.
// Rejects self-deps, cross-project deps, and cycles. Idempotent (duplicate → nil,nil).
func (s *Store) AddDep(taskID, dependsOnID int64, actor string) (*Event, error) {
	if taskID == dependsOnID {
		return nil, fmt.Errorf("%w: a task cannot depend on itself", ErrValidation)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Load both tasks to validate existence and same-project constraint.
	task, err := s.getTaskTx(tx, taskID)
	if err != nil {
		return nil, err
	}
	prereq, err := s.getTaskTx(tx, dependsOnID)
	if err != nil {
		return nil, err
	}
	if task.ProjectID != prereq.ProjectID {
		return nil, fmt.Errorf("%w: dependencies must be within the same project", ErrValidation)
	}

	// Cycle check: adding taskID→dependsOnID creates a cycle iff taskID is
	// reachable by walking depends_on edges forward from dependsOnID.
	cycle, err := wouldCycle(tx, taskID, dependsOnID)
	if err != nil {
		return nil, err
	}
	if cycle {
		return nil, fmt.Errorf("%w: would create a dependency cycle", ErrValidation)
	}

	res, err := tx.Exec("INSERT OR IGNORE INTO task_deps(task_id,depends_on_id) VALUES(?,?)", taskID, dependsOnID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Edge already existed — idempotent success.
		return nil, tx.Commit()
	}
	ev, err := insertEvent(tx, task.ProjectID, taskID, actorOr(actor), "task.dep_added",
		map[string]any{"depends_on": dependsOnID})
	if err != nil {
		return nil, err
	}
	return ev, tx.Commit()
}

// RemoveDep removes a dependency edge. No-op if the edge does not exist.
func (s *Store) RemoveDep(taskID, dependsOnID int64, actor string) (*Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec("DELETE FROM task_deps WHERE task_id=? AND depends_on_id=?", taskID, dependsOnID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, tx.Commit() // already gone — idempotent
	}
	// Need project_id for the event.
	task, err := s.getTaskTx(tx, taskID)
	if err != nil {
		return nil, err
	}
	ev, err := insertEvent(tx, task.ProjectID, taskID, actorOr(actor), "task.dep_removed",
		map[string]any{"depends_on": dependsOnID})
	if err != nil {
		return nil, err
	}
	return ev, tx.Commit()
}

// ---------- labels ----------

// AddLabel attaches a label to a task. The label is normalized (trimmed,
// lowercased) before insertion. Idempotent (already present → nil,nil).
// Deliberately does NOT bump updated_at — labeling is metadata, and refreshing
// the task's activity timestamp would keep a stale claim alive (same precedent
// as dep edges).
func (s *Store) AddLabel(taskID int64, label, actor string) (*Event, error) {
	l, err := normalizeLabel(label)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	task, err := s.getTaskTx(tx, taskID)
	if err != nil {
		return nil, err // ErrNotFound or real error
	}
	res, err := tx.Exec("INSERT OR IGNORE INTO task_labels(task_id,label) VALUES(?,?)", taskID, l)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Label already present — idempotent success.
		return nil, tx.Commit()
	}
	ev, err := insertEvent(tx, task.ProjectID, taskID, actorOr(actor), "task.labeled",
		map[string]any{"label": l})
	if err != nil {
		return nil, err
	}
	return ev, tx.Commit()
}

// RemoveLabel detaches a label from a task. No-op if the label is not present.
func (s *Store) RemoveLabel(taskID int64, label, actor string) (*Event, error) {
	l, err := normalizeLabel(label)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec("DELETE FROM task_labels WHERE task_id=? AND label=?", taskID, l)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, tx.Commit() // already absent — idempotent
	}
	// Need project_id for the event.
	task, err := s.getTaskTx(tx, taskID)
	if err != nil {
		return nil, err
	}
	ev, err := insertEvent(tx, task.ProjectID, taskID, actorOr(actor), "task.unlabeled",
		map[string]any{"label": l})
	if err != nil {
		return nil, err
	}
	return ev, tx.Commit()
}

// ---------- meta ----------

// applyMetaTx upserts/deletes task_meta rows for taskID inside tx, walking
// keys in sorted order so event payloads and failure points are deterministic.
// An empty value removes the key (absent key = silent no-op); any validation
// error aborts the caller's tx, so a multi-key patch is all-or-nothing. The
// returned delta maps key → [old, new] (nil = absent) for keys that changed.
func applyMetaTx(tx *sql.Tx, taskID int64, meta map[string]string) (map[string]any, bool, error) {
	// Normalize every key up front and reject collisions (two raw keys → one
	// normalized key): applying both would pick a map-iteration-order winner
	// and record a just-written value as "old" in the delta.
	norm := make(map[string]string, len(meta))
	for rawK, v := range meta {
		k, err := normalizeMetaKey(rawK)
		if err != nil {
			return nil, false, err
		}
		if _, dup := norm[k]; dup {
			return nil, false, fmt.Errorf("%w: duplicate meta key after normalization: %q", ErrValidation, k)
		}
		norm[k] = v
	}
	keys := make([]string, 0, len(norm))
	for k := range norm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	delta := map[string]any{}
	for _, k := range keys {
		var old sql.NullString
		err := tx.QueryRow("SELECT value FROM task_meta WHERE task_id=? AND key=?", taskID, k).Scan(&old)
		if err != nil && err != sql.ErrNoRows {
			return nil, false, err
		}
		v := norm[k]
		if v == "" { // empty value = remove the key
			if !old.Valid {
				continue // already absent — silent no-op
			}
			if _, err := tx.Exec("DELETE FROM task_meta WHERE task_id=? AND key=?", taskID, k); err != nil {
				return nil, false, err
			}
			delta[k] = []any{old.String, nil}
			continue
		}
		// Values are opaque but capped like titles (board cards, SSE payloads).
		if len(v) > maxTitleLen {
			return nil, false, fmt.Errorf("%w: meta value must be 1-%d bytes", ErrValidation, maxTitleLen)
		}
		if old.Valid && old.String == v {
			continue // unchanged
		}
		if _, err := tx.Exec(`INSERT INTO task_meta(task_id,key,value) VALUES(?,?,?)
		       ON CONFLICT(task_id,key) DO UPDATE SET value=excluded.value`, taskID, k, v); err != nil {
			return nil, false, err
		}
		delta[k] = []any{nullable(old.String), v}
	}
	return delta, len(delta) > 0, nil
}

// wouldCycle reports whether adding an edge taskID→dependsOnID would introduce
// a cycle. It does so by checking if taskID is reachable from dependsOnID via
// existing depends_on edges (recursive BFS over task_deps).
func wouldCycle(tx *sql.Tx, taskID, dependsOnID int64) (bool, error) {
	// Use a recursive CTE to walk the graph from dependsOnID forward.
	rows, err := tx.Query(`
		WITH RECURSIVE reach(id) AS (
		  SELECT ? AS id
		  UNION
		  SELECT d.depends_on_id FROM task_deps d JOIN reach r ON r.id=d.task_id
		)
		SELECT 1 FROM reach WHERE id=? LIMIT 1`, dependsOnID, taskID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := rows.Next()
	return found, rows.Err()
}

func (s *Store) AddComment(id int64, author, body string) (*Comment, *Event, error) {
	if strings.TrimSpace(body) == "" || len(body) > maxBodyLen {
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

func (s *Store) ListEvents(since int64, project, category string, limit int) ([]Event, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var args []any
	q := `SELECT events.id,COALESCE(events.project_id,0),COALESCE(events.task_id,0),events.actor,events.kind,events.data,events.created_at
	      FROM events LEFT JOIN projects p ON p.id = events.project_id
	      LEFT JOIN categories c ON c.id = p.category_id
	      WHERE events.id>?`
	args = append(args, since)
	if project != "" {
		pid, err := s.projectID(project)
		if err != nil {
			return nil, since, err
		}
		q += " AND events.project_id=?"
		args = append(args, pid)
	}
	if category != "" {
		cid, err := s.categoryID(category)
		if err != nil {
			return nil, since, err
		}
		// c.id=? matches only events whose project lives in the category. This
		// intentionally EXCLUDES category-level events (NULL project_id) — they
		// belong to the All/overview feed, not a single category's drill-down.
		q += " AND c.id=?"
		args = append(args, cid)
	}
	if project == "" && category == "" {
		q += " AND (events.project_id IS NULL OR (p.archived_at IS NULL AND c.archived_at IS NULL))"
	}
	q += " ORDER BY events.id ASC LIMIT ?"
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

// ListEventsBefore returns events with id < before, newest-first, up to limit.
// It mirrors the archived-project filter used by ListEvents and RecentEvents.
// Default limit 40, cap 200.
func (s *Store) ListEventsBefore(before int64, project, category string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	var args []any
	q := `SELECT events.id,COALESCE(events.project_id,0),COALESCE(events.task_id,0),events.actor,events.kind,events.data,events.created_at
	      FROM events LEFT JOIN projects p ON p.id = events.project_id
	      LEFT JOIN categories c ON c.id = p.category_id
	      WHERE events.id<?`
	args = append(args, before)
	if project != "" {
		pid, err := s.projectID(project)
		if err != nil {
			return nil, err
		}
		q += " AND events.project_id=?"
		args = append(args, pid)
	}
	if category != "" {
		cid, err := s.categoryID(category)
		if err != nil {
			return nil, err
		}
		// c.id=? excludes category-level (NULL project_id) events on purpose; see
		// ListEvents.
		q += " AND c.id=?"
		args = append(args, cid)
	}
	if project == "" && category == "" {
		q += " AND (events.project_id IS NULL OR (p.archived_at IS NULL AND c.archived_at IS NULL))"
	}
	q += " ORDER BY events.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var data string
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.TaskID, &e.Actor, &e.Kind, &data, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(data)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) MaxEventID() (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT COALESCE(MAX(id),0) FROM events").Scan(&id)
	return id, err
}

// RecentEvents returns the newest events first (for the dashboard feed bootstrap)
// along with the max event id, which the client uses as its SSE cursor.
func (s *Store) RecentEvents(project, category string, limit int) ([]Event, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	var args []any
	var where []string
	q := `SELECT events.id,COALESCE(events.project_id,0),COALESCE(events.task_id,0),events.actor,events.kind,events.data,events.created_at
	      FROM events LEFT JOIN projects p ON p.id = events.project_id
	      LEFT JOIN categories c ON c.id = p.category_id`
	if project != "" {
		pid, err := s.projectID(project)
		if err != nil {
			return nil, 0, err
		}
		where = append(where, "events.project_id=?")
		args = append(args, pid)
	}
	if category != "" {
		cid, err := s.categoryID(category)
		if err != nil {
			return nil, 0, err
		}
		// c.id=? excludes category-level (NULL project_id) events on purpose; see
		// ListEvents.
		where = append(where, "c.id=?")
		args = append(args, cid)
	}
	if project == "" && category == "" {
		where = append(where, "(events.project_id IS NULL OR (p.archived_at IS NULL AND c.archived_at IS NULL))")
	}
	q += " WHERE " + strings.Join(where, " AND ")
	q += " ORDER BY events.id DESC LIMIT ?"
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

// ---------- project graph ----------

// GraphEdge is one directed edge in the project dependency graph.
// From is the prerequisite task id; To is the dependent task id.
// (direction = "unblocks": From must complete before To can start)
type GraphEdge struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// ProjectGraphData is the payload returned by GET /api/projects/{slug}/graph.
type ProjectGraphData struct {
	Nodes []Task      `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// ProjectGraph returns all tasks (nodes) and dependency edges for the project
// identified by slug. Edges are oriented prereq → dependent (the "unblocks"
// direction). Returns ErrNotFound when the slug does not exist.
func (s *Store) ProjectGraph(slug string) (*ProjectGraphData, error) {
	pid, err := s.projectID(slug)
	if err != nil {
		return nil, err // ErrNotFound propagated
	}

	nodes, err := s.ListTasks(TaskFilter{Project: slug})
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(
		`SELECT d.depends_on_id, d.task_id
		       FROM task_deps d
		       JOIN tasks t ON t.id = d.task_id
		       WHERE t.project_id = ?`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	edges := []GraphEdge{}
	for rows.Next() {
		var e GraphEdge
		if err := rows.Scan(&e.From, &e.To); err != nil {
			return nil, err
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &ProjectGraphData{Nodes: nodes, Edges: edges}, nil
}

// ---------- helpers ----------

// queryer is satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// newUID returns a stable identifier: prefix + 16 lowercase hex chars (8 bytes
// of crypto/rand). Used for category ("amc_") and project ("amp_") uids — the
// immutable correlation keys that survive slug renames. crypto/rand.Read never
// fails (it always fills the buffer or aborts the program).
func newUID(prefix string) string {
	var b [8]byte
	rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// isUniqueErr reports whether err is a SQLite UNIQUE-constraint failure on the
// named column (e.g. "projects.uid"). Insert callers use it to retry with a
// fresh uid on the astronomically unlikely 64-bit collision.
func isUniqueErr(err error, col string) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: "+col)
}

func insertEvent(tx *sql.Tx, projectID, taskID int64, actor, kind string, data any) (*Event, error) {
	b, err := json.Marshal(data)
	if err != nil {
		// The events table is the durable replay cursor — never write a silently
		// corrupted/empty payload.
		return nil, fmt.Errorf("marshal event data: %w", err)
	}
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

// staleCutoff returns the ISO-8601 UTC timestamp staleFor ago, formatted to
// match SQLite's strftime('%Y-%m-%dT%H:%M:%fZ','now') exactly (3-digit
// fractional seconds), so the lexicographic comparison against stored
// updated_at values is correct. Changing the format silently breaks it.
func staleCutoff(d time.Duration) string {
	return time.Now().UTC().Add(-d).Format("2006-01-02T15:04:05.000Z")
}

// likeEscape escapes the SQLite LIKE wildcards % and _ (and the escape char \
// itself) so a search query matches them literally; the LIKE clause must use
// ESCAPE '\'. Note: SQLite LIKE is ASCII-case-insensitive by default — that is
// the documented behavior of `?q=` / `am ls --grep`. Unicode case folding is
// deliberately not applied (fine at personal-board scale).
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// orDash returns the assignee, or if empty (e.g. a done task) the status, so a
// claim conflict message is still informative.
func orDash(assignee, status string) string {
	if assignee != "" {
		return assignee
	}
	return status
}
