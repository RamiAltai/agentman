package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestListEventsBefore(t *testing.T) {
	st := openTestStore(t)

	// Create two projects, one active and one to be archived.
	if _, _, err := st.CreateProject("active", "Active"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("archproj", "Archived"); err != nil {
		t.Fatal(err)
	}

	// Create several tasks in "active" to generate events.
	var ids []int64
	for _, title := range []string{"t1", "t2", "t3", "t4"} {
		tk, _, err := st.CreateTask(CreateTaskInput{Project: "active", Title: title})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		ids = append(ids, tk.ID)
	}

	// Create a task in archproj.
	archTask, _, err := st.CreateTask(CreateTaskInput{Project: "archproj", Title: "arch-t1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = archTask

	// Collect all event IDs in ascending order.
	allEvs, _, err := st.ListEvents(0, "", 500)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(allEvs) < 5 {
		t.Fatalf("want >=5 events, got %d", len(allEvs))
	}

	// before= the last event id: should return all but the last, newest-first.
	lastID := allEvs[len(allEvs)-1].ID
	got, err := st.ListEventsBefore(lastID, "", 100)
	if err != nil {
		t.Fatalf("ListEventsBefore: %v", err)
	}
	if len(got) != len(allEvs)-1 {
		t.Fatalf("ListEventsBefore(%d) len=%d, want %d", lastID, len(got), len(allEvs)-1)
	}
	// Must be strictly descending.
	for i := 1; i < len(got); i++ {
		if got[i].ID >= got[i-1].ID {
			t.Fatalf("ids not descending: %d then %d", got[i-1].ID, got[i].ID)
		}
	}
	// All returned ids must be < lastID.
	for _, e := range got {
		if e.ID >= lastID {
			t.Fatalf("returned event id %d >= before %d", e.ID, lastID)
		}
	}

	// Archive archproj — its events must be excluded from unfiltered before-query.
	archPID, _ := st.projectID("archproj")
	if _, _, err := st.ArchiveProject("archproj", "tester"); err != nil {
		t.Fatal(err)
	}
	gotAfterArchive, err := st.ListEventsBefore(lastID+1, "", 100)
	if err != nil {
		t.Fatalf("ListEventsBefore after archive: %v", err)
	}
	for _, e := range gotAfterArchive {
		if e.ProjectID == archPID {
			t.Errorf("ListEventsBefore(unfiltered) returned event from archived project_id=%d kind=%s", archPID, e.Kind)
		}
	}

	// Explicit project="archproj" should still return that project's events even archived.
	archEvs, err := st.ListEventsBefore(lastID+1, "archproj", 100)
	if err != nil {
		t.Fatalf("ListEventsBefore(archproj): %v", err)
	}
	if len(archEvs) == 0 {
		t.Error("ListEventsBefore(archproj) returned no events; want archived project's events")
	}
	for _, e := range archEvs {
		if e.ProjectID != archPID {
			t.Errorf("ListEventsBefore(archproj) returned unexpected project_id=%d", e.ProjectID)
		}
	}

	// Limit is respected.
	limited, err := st.ListEventsBefore(lastID+1, "", 2)
	if err != nil {
		t.Fatalf("ListEventsBefore limited: %v", err)
	}
	if len(limited) > 2 {
		t.Fatalf("expected <=2 events with limit=2, got %d", len(limited))
	}
}

func TestCreateProjectAndTaskHappyPath(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, ev, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "Do a thing", Actor: "alice"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if ev == nil || ev.Kind != "task.created" {
		t.Fatalf("CreateTask event = %v, want kind task.created", ev)
	}
	if task.Title != "Do a thing" || task.Status != "todo" {
		t.Fatalf("CreateTask result = %+v", task)
	}
	got, err := st.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ID != task.ID || got.Title != "Do a thing" {
		t.Fatalf("GetTask result = %+v", got)
	}
	c, _, err := st.AddComment(task.ID, "alice", "a note")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if c.Body != "a note" {
		t.Fatalf("AddComment body = %q", c.Body)
	}
}

func TestCreateTaskEmptyTitleValidation(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "   "}); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty title err = %v, want ErrValidation", err)
	}
}

func TestPatchTaskInvalidStatusValidation(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "T"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := st.PatchTask(task.ID, map[string]any{"status": "bogus"}, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid status err = %v, want ErrValidation", err)
	}
}

func TestClaimRaceExactlyOneWinner(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "Race me"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	agents := []string{"agent-a", "agent-b"}
	type result struct {
		agent string
		task  *Task
		err   error
	}
	results := make([]result, len(agents))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, ag := range agents {
		wg.Add(1)
		go func(i int, ag string) {
			defer wg.Done()
			<-start
			tk, _, err := st.ClaimTask(task.ID, ag)
			results[i] = result{agent: ag, task: tk, err: err}
		}(i, ag)
	}
	close(start)
	wg.Wait()

	winners, conflicts := 0, 0
	var winner string
	for _, r := range results {
		if r.err == nil {
			winners++
			winner = r.agent
			if r.task == nil {
				t.Fatalf("winner %s returned nil task", r.agent)
			}
			continue
		}
		var ce *ConflictError
		if errors.As(r.err, &ce) {
			conflicts++
		} else {
			t.Fatalf("unexpected error for %s: %v", r.agent, r.err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d, want 1 and 1", winners, conflicts)
	}
	final, err := st.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if final.Assignee != winner {
		t.Fatalf("final owner = %q, want winner %q", final.Assignee, winner)
	}
	if final.Status != "doing" {
		t.Fatalf("final status = %q, want doing", final.Status)
	}
}

// ===================== stale-claim recovery tests =====================

// staleISO is a backdated updated_at value, in the exact strftime('%Y-%m-%dT%H:%M:%fZ')
// format the store writes, far enough in the past to be stale for any test duration.
const staleISO = "2026-01-01T00:00:00.000Z"

// backdateTask rewinds a task's updated_at directly (same-package test seam).
func backdateTask(t *testing.T, st *Store, id int64) {
	t.Helper()
	if _, err := st.db.Exec("UPDATE tasks SET updated_at=? WHERE id=?", staleISO, id); err != nil {
		t.Fatalf("backdate task %d: %v", id, err)
	}
}

func TestStealStaleClaim(t *testing.T) {
	newTask := func(t *testing.T, st *Store, title string) int64 {
		t.Helper()
		tk, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: title})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return tk.ID
	}
	mustClaim := func(t *testing.T, st *Store, id int64, agent string) {
		t.Helper()
		if _, _, err := st.ClaimTask(id, agent); err != nil {
			t.Fatalf("ClaimTask(%d, %s): %v", id, agent, err)
		}
	}

	cases := []struct {
		name  string
		setup func(t *testing.T, st *Store) int64 // returns the id to steal
		agent string
		dur   time.Duration
		check func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error)
	}{
		{
			name: "stale claim is stolen",
			setup: func(t *testing.T, st *Store) int64 {
				id := newTask(t, st, "stale")
				mustClaim(t, st, id, "agent-old")
				backdateTask(t, st, id)
				return id
			},
			agent: "agent-new", dur: time.Hour,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				if err != nil {
					t.Fatalf("steal: %v", err)
				}
				if tk.Assignee != "agent-new" {
					t.Fatalf("assignee = %q, want agent-new", tk.Assignee)
				}
				if tk.ClaimedAt == "" || tk.ClaimedAt <= staleISO {
					t.Fatalf("claimed_at = %q, want bumped past %s", tk.ClaimedAt, staleISO)
				}
				if tk.UpdatedAt <= staleISO {
					t.Fatalf("updated_at = %q, want bumped past %s", tk.UpdatedAt, staleISO)
				}
				if ev == nil || ev.Kind != "task.reclaimed" {
					t.Fatalf("event = %+v, want kind task.reclaimed", ev)
				}
				if !strings.Contains(string(ev.Data), "agent-old") {
					t.Fatalf("event data %s does not name previous assignee", ev.Data)
				}
				var n int
				st.db.QueryRow("SELECT COUNT(*) FROM events WHERE kind='task.reclaimed' AND task_id=?", id).Scan(&n)
				if n != 1 {
					t.Fatalf("task.reclaimed event rows = %d, want 1", n)
				}
			},
		},
		{
			name: "fresh claim is not stealable",
			setup: func(t *testing.T, st *Store) int64 {
				id := newTask(t, st, "fresh")
				mustClaim(t, st, id, "agent-old")
				return id
			},
			agent: "agent-new", dur: time.Hour,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				var nse *NotStaleError
				if !errors.As(err, &nse) {
					t.Fatalf("err = %v, want *NotStaleError", err)
				}
				if nse.Assignee != "agent-old" {
					t.Fatalf("NotStaleError.Assignee = %q, want agent-old", nse.Assignee)
				}
				final, _ := st.GetTask(id)
				if final.Assignee != "agent-old" {
					t.Fatalf("assignee after lost steal = %q, want agent-old", final.Assignee)
				}
			},
		},
		{
			name: "unclaimed degrades to normal claim",
			setup: func(t *testing.T, st *Store) int64 {
				return newTask(t, st, "unclaimed")
			},
			agent: "agent-new", dur: time.Hour,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				if err != nil {
					t.Fatalf("steal on unclaimed: %v", err)
				}
				if tk.Assignee != "agent-new" || tk.Status != "doing" {
					t.Fatalf("task = %s/%s, want agent-new/doing", tk.Assignee, tk.Status)
				}
				if ev == nil || ev.Kind != "task.claimed" {
					t.Fatalf("event = %+v, want kind task.claimed", ev)
				}
			},
		},
		{
			name: "done task conflicts",
			setup: func(t *testing.T, st *Store) int64 {
				id := newTask(t, st, "done")
				mustClaim(t, st, id, "agent-old")
				if _, _, err := st.PatchTask(id, map[string]any{"status": "done"}, "agent-old"); err != nil {
					t.Fatalf("PatchTask done: %v", err)
				}
				backdateTask(t, st, id)
				return id
			},
			agent: "agent-new", dur: time.Hour,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				var ce *ConflictError
				if !errors.As(err, &ce) {
					t.Fatalf("err = %v, want *ConflictError", err)
				}
			},
		},
		{
			name: "own claim is idempotent",
			setup: func(t *testing.T, st *Store) int64 {
				id := newTask(t, st, "mine")
				mustClaim(t, st, id, "agent-new")
				return id
			},
			agent: "agent-new", dur: time.Hour,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				if err != nil {
					t.Fatalf("idempotent steal: %v", err)
				}
				if tk == nil || tk.Assignee != "agent-new" {
					t.Fatalf("task = %+v, want owned by agent-new", tk)
				}
				if ev != nil {
					t.Fatalf("event = %+v, want nil (idempotent)", ev)
				}
			},
		},
		{
			name:  "missing id",
			setup: func(t *testing.T, st *Store) int64 { return 99999 },
			agent: "agent-new", dur: time.Hour,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
			},
		},
		{
			name:  "empty agent",
			setup: func(t *testing.T, st *Store) int64 { return newTask(t, st, "noagent") },
			agent: "", dur: time.Hour,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("err = %v, want ErrValidation", err)
				}
			},
		},
		{
			name:  "zero duration",
			setup: func(t *testing.T, st *Store) int64 { return newTask(t, st, "zerodur") },
			agent: "agent-new", dur: 0,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("err = %v, want ErrValidation", err)
				}
			},
		},
		{
			name:  "negative duration",
			setup: func(t *testing.T, st *Store) int64 { return newTask(t, st, "negdur") },
			agent: "agent-new", dur: -time.Hour,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("err = %v, want ErrValidation", err)
				}
			},
		},
		{
			name: "open prereq blocks the steal",
			setup: func(t *testing.T, st *Store) int64 {
				prereq := newTask(t, st, "prereq")
				dep := newTask(t, st, "dependent")
				if _, err := st.AddDep(dep, prereq, "alice"); err != nil {
					t.Fatalf("AddDep: %v", err)
				}
				return dep
			},
			agent: "agent-new", dur: time.Hour,
			check: func(t *testing.T, st *Store, id int64, tk *Task, ev *Event, err error) {
				var be *BlockedError
				if !errors.As(err, &be) {
					t.Fatalf("err = %v, want *BlockedError", err)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := openTestStore(t)
			if _, _, err := st.CreateProject("web", "Web"); err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			id := c.setup(t, st)
			tk, ev, err := st.StealStaleClaim(id, c.agent, c.dur)
			c.check(t, st, id, tk, ev, err)
		})
	}
}

func TestStealRaceExactlyOneWinner(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "Steal me"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := st.ClaimTask(task.ID, "dead-agent"); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	backdateTask(t, st, task.ID)

	agents := []string{"stealer-a", "stealer-b", "stealer-c", "stealer-d"}
	type result struct {
		agent string
		task  *Task
		err   error
	}
	results := make([]result, len(agents))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, ag := range agents {
		wg.Add(1)
		go func(i int, ag string) {
			defer wg.Done()
			<-start
			tk, _, err := st.StealStaleClaim(task.ID, ag, time.Hour)
			results[i] = result{agent: ag, task: tk, err: err}
		}(i, ag)
	}
	close(start)
	wg.Wait()

	winners, notStale := 0, 0
	var winner string
	for _, r := range results {
		if r.err == nil {
			winners++
			winner = r.agent
			if r.task == nil {
				t.Fatalf("winner %s returned nil task", r.agent)
			}
			continue
		}
		var nse *NotStaleError
		if errors.As(r.err, &nse) {
			notStale++
		} else {
			t.Fatalf("unexpected error for %s: %v", r.agent, r.err)
		}
	}
	if winners != 1 || notStale != len(agents)-1 {
		t.Fatalf("winners=%d notStale=%d, want 1 and %d", winners, notStale, len(agents)-1)
	}
	final, err := st.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if final.Assignee != winner {
		t.Fatalf("final owner = %q, want winner %q", final.Assignee, winner)
	}
	var n int
	st.db.QueryRow("SELECT COUNT(*) FROM events WHERE kind='task.reclaimed' AND task_id=?", task.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("task.reclaimed event rows = %d, want exactly 1", n)
	}
}

func TestListTasksStaleFilter(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	mk := func(title string) int64 {
		t.Helper()
		tk, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: title})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		return tk.ID
	}

	// Stale: claimed (doing+assigned) then backdated.
	stale := mk("stale doing")
	if _, _, err := st.ClaimTask(stale, "agent-a"); err != nil {
		t.Fatal(err)
	}
	backdateTask(t, st, stale)

	// Fresh: claimed just now.
	fresh := mk("fresh doing")
	if _, _, err := st.ClaimTask(fresh, "agent-b"); err != nil {
		t.Fatal(err)
	}

	// Unassigned doing, backdated: not stale (nobody holds it).
	unassigned := mk("unassigned doing")
	if _, _, err := st.PatchTask(unassigned, map[string]any{"status": "doing"}, "alice"); err != nil {
		t.Fatal(err)
	}
	backdateTask(t, st, unassigned)

	// Assigned done, backdated: not stale (finished work is never stale).
	done := mk("assigned done")
	if _, _, err := st.ClaimTask(done, "agent-c"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PatchTask(done, map[string]any{"status": "done"}, "agent-c"); err != nil {
		t.Fatal(err)
	}
	backdateTask(t, st, done)

	// Backdated but then commented: the comment bumps updated_at → no longer stale.
	revived := mk("commented doing")
	if _, _, err := st.ClaimTask(revived, "agent-d"); err != nil {
		t.Fatal(err)
	}
	backdateTask(t, st, revived)
	if _, _, err := st.AddComment(revived, "agent-d", "still on it"); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListTasks(TaskFilter{Project: "web", Stale: time.Hour, Status: "todo,doing,blocked,done"})
	if err != nil {
		t.Fatalf("ListTasks stale: %v", err)
	}
	if len(got) != 1 || got[0].ID != stale {
		t.Fatalf("stale filter returned %+v, want only task %d", got, stale)
	}
}

func TestClaimSetsClaimedAt(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "claim me"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ClaimedAt != "" {
		t.Fatalf("claimed_at before claim = %q, want empty", task.ClaimedAt)
	}
	claimed, _, err := st.ClaimTask(task.ID, "agent-a")
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed.ClaimedAt == "" {
		t.Fatal("claimed_at not set by ClaimTask")
	}
}

func TestDropClearsClaimedAt(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "drop me"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := st.ClaimTask(task.ID, "agent-a"); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	// am drop = PATCH {assignee:"", status:"todo"}.
	dropped, _, err := st.PatchTask(task.ID, map[string]any{"assignee": "", "status": "todo"}, "agent-a")
	if err != nil {
		t.Fatalf("PatchTask drop: %v", err)
	}
	if dropped.ClaimedAt != "" {
		t.Fatalf("claimed_at after drop = %q, want cleared", dropped.ClaimedAt)
	}
	// Reassigning via PATCH sets it again.
	reassigned, _, err := st.PatchTask(task.ID, map[string]any{"assignee": "agent-b"}, "alice")
	if err != nil {
		t.Fatalf("PatchTask assign: %v", err)
	}
	if reassigned.ClaimedAt == "" {
		t.Fatal("claimed_at not set on PATCH reassign")
	}
}

func TestArchiveUnarchiveProject(t *testing.T) {
	st := openTestStore(t)
	// Create a project
	_, _, err := st.CreateProject("testproj", "Test")
	if err != nil {
		t.Fatal(err)
	}

	// Default list excludes nothing (not archived yet)
	ps, err := st.ListProjects(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("want 1 project, got %d", len(ps))
	}

	// Archive it
	p, ev, err := st.ArchiveProject("testproj", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if p.ArchivedAt == "" {
		t.Error("ArchivedAt should be set after archive")
	}
	if ev == nil {
		t.Error("expected event on first archive")
	}

	// Default list now excludes it
	ps, err = st.ListProjects(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("archived project should be hidden in default list, got %d", len(ps))
	}

	// All list includes it
	ps, err = st.ListProjects(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("archived project should appear with includeArchived=true, got %d", len(ps))
	}

	// Idempotent re-archive (no event)
	_, ev2, err := st.ArchiveProject("testproj", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if ev2 != nil {
		t.Error("expected no event on idempotent re-archive")
	}

	// Unarchive
	p2, ev3, err := st.UnarchiveProject("testproj", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if p2.ArchivedAt != "" {
		t.Error("ArchivedAt should be empty after unarchive")
	}
	if ev3 == nil {
		t.Error("expected event on unarchive")
	}

	// Default list includes it again
	ps, err = st.ListProjects(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("want 1 project after unarchive, got %d", len(ps))
	}
}

func TestListTasksHidesArchivedProjectTasks(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("alpha", "Alpha"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("beta", "Beta"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "alpha", Title: "a1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "beta", Title: "b1"}); err != nil {
		t.Fatal(err)
	}

	// Before archiving: unfiltered list returns both projects' tasks.
	all, err := st.ListTasks(TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered list before archive: want 2 tasks, got %d", len(all))
	}

	if _, _, err := st.ArchiveProject("alpha", "tester"); err != nil {
		t.Fatal(err)
	}

	// After archiving alpha: unfiltered list must hide alpha's task (the reported bug).
	all, err = st.ListTasks(TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Project != "beta" {
		t.Fatalf("unfiltered list after archive: want only beta's task, got %+v", all)
	}

	// Explicit project filter still returns the archived project's tasks (inspection escape hatch).
	alpha, err := st.ListTasks(TaskFilter{Project: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(alpha) != 1 || alpha[0].Title != "a1" {
		t.Fatalf("explicit ?project=alpha after archive: want a1, got %+v", alpha)
	}

	// Unarchiving restores it to the unfiltered list.
	if _, _, err := st.UnarchiveProject("alpha", "tester"); err != nil {
		t.Fatal(err)
	}
	all, err = st.ListTasks(TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered list after unarchive: want 2 tasks, got %d", len(all))
	}
}

func TestFeedHidesArchivedProjectEvents(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("alpha", "Alpha"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("beta", "Beta"); err != nil {
		t.Fatal(err)
	}
	// Create tasks — generates task.created events for each project.
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "alpha", Title: "alpha task"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "beta", Title: "beta task"}); err != nil {
		t.Fatal(err)
	}

	// Resolve alpha's project id for assertion checks.
	alphaPID, err := st.projectID("alpha")
	if err != nil {
		t.Fatal(err)
	}

	// Archive alpha.
	if _, _, err := st.ArchiveProject("alpha", "tester"); err != nil {
		t.Fatal(err)
	}

	// Unfiltered RecentEvents must NOT include any event whose project_id is alpha's.
	recent, _, err := st.RecentEvents("", 50)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	for _, e := range recent {
		if e.ProjectID == alphaPID {
			t.Errorf("RecentEvents(\"\") returned event with archived project_id %d (kind=%s)", alphaPID, e.Kind)
		}
	}

	// Unfiltered ListEvents must NOT include alpha's events.
	all, _, err := st.ListEvents(0, "", 200)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, e := range all {
		if e.ProjectID == alphaPID {
			t.Errorf("ListEvents(0,\"\") returned event with archived project_id %d (kind=%s)", alphaPID, e.Kind)
		}
	}

	// Explicit project=alpha MUST still return alpha's events.
	alphaEvs, _, err := st.RecentEvents("alpha", 50)
	if err != nil {
		t.Fatalf("RecentEvents(alpha): %v", err)
	}
	if len(alphaEvs) == 0 {
		t.Error("RecentEvents(\"alpha\") returned no events for archived project; want alpha's events")
	}
	for _, e := range alphaEvs {
		if e.ProjectID != alphaPID {
			t.Errorf("RecentEvents(\"alpha\") returned event with unexpected project_id %d", e.ProjectID)
		}
	}

	// Unarchive alpha — its events must reappear in the unfiltered feed.
	if _, _, err := st.UnarchiveProject("alpha", "tester"); err != nil {
		t.Fatal(err)
	}
	recentAfter, _, err := st.RecentEvents("", 50)
	if err != nil {
		t.Fatalf("RecentEvents after unarchive: %v", err)
	}
	found := false
	for _, e := range recentAfter {
		if e.ProjectID == alphaPID {
			found = true
			break
		}
	}
	if !found {
		t.Error("RecentEvents(\"\") after unarchive should contain alpha's events again")
	}
}

func TestCreateTaskRejectsArchivedProject(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("active", "Active"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("archived", "Archived"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ArchiveProject("archived", "tester"); err != nil {
		t.Fatal(err)
	}

	// Creating into an archived project must return ErrProjectArchived.
	_, _, err := st.CreateTask(CreateTaskInput{Project: "archived", Title: "should fail"})
	if !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("CreateTask into archived project: got %v, want ErrProjectArchived", err)
	}

	// Creating into an active project must still succeed.
	task, ev, err := st.CreateTask(CreateTaskInput{Project: "active", Title: "should work"})
	if err != nil {
		t.Fatalf("CreateTask into active project: %v", err)
	}
	if task == nil || ev == nil {
		t.Fatal("CreateTask into active project returned nil task or event")
	}
}

func TestDeleteTaskCascadesComments(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "to delete"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	c, _, err := st.AddComment(task.ID, "alice", "a comment")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	_ = c

	ev, err := st.DeleteTask(task.ID, "alice")
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if ev == nil || ev.Kind != "task.deleted" {
		t.Fatalf("DeleteTask event = %v, want kind task.deleted", ev)
	}

	// Task must be gone.
	if _, err := st.GetTask(task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask after delete = %v, want ErrNotFound", err)
	}

	// Comment must also be gone (cascade).
	var count int
	st.db.QueryRow("SELECT COUNT(*) FROM comments WHERE task_id=?", task.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("comments after task delete = %d, want 0", count)
	}

	// task.deleted event must exist.
	evs, _, err := st.ListEvents(0, "web", 200)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "task.deleted" && e.TaskID == task.ID {
			found = true
		}
	}
	if !found {
		t.Error("task.deleted event not found in event log")
	}
}

func TestDeleteTaskNotFound(t *testing.T) {
	st := openTestStore(t)
	_, err := st.DeleteTask(99999, "alice")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteTask non-existent = %v, want ErrNotFound", err)
	}
}

func TestDeleteCommentRemovesOnlyComment(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "task with comments"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	c1, _, err := st.AddComment(task.ID, "alice", "first")
	if err != nil {
		t.Fatalf("AddComment 1: %v", err)
	}
	c2, _, err := st.AddComment(task.ID, "bob", "second")
	if err != nil {
		t.Fatalf("AddComment 2: %v", err)
	}

	ev, err := st.DeleteComment(task.ID, c1.ID, "alice")
	if err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	if ev == nil || ev.Kind != "comment.deleted" {
		t.Fatalf("DeleteComment event = %v, want kind comment.deleted", ev)
	}

	// First comment gone, second still present.
	var count int
	st.db.QueryRow("SELECT COUNT(*) FROM comments WHERE id=?", c1.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("comment c1 still exists after delete")
	}
	st.db.QueryRow("SELECT COUNT(*) FROM comments WHERE id=?", c2.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("comment c2 should still exist, count=%d", count)
	}

	// Task itself is still present.
	if _, err := st.GetTask(task.ID); err != nil {
		t.Fatalf("task should still exist: %v", err)
	}

	// Wrong task id → ErrNotFound.
	_, err = st.DeleteComment(task.ID+999, c2.ID, "bob")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteComment wrong taskID = %v, want ErrNotFound", err)
	}
}

func TestDeleteProjectCascades(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("alpha", "Alpha"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, _, err := st.CreateTask(CreateTaskInput{Project: "alpha", Title: "child task"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := st.AddComment(task.ID, "alice", "note"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	ev, err := st.DeleteProject("alpha", "alice")
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if ev == nil || ev.Kind != "project.deleted" {
		t.Fatalf("DeleteProject event = %v, want kind project.deleted", ev)
	}

	// Project gone.
	ps, err := st.ListProjects(true)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range ps {
		if p.Slug == "alpha" {
			t.Fatal("project alpha still in list after DeleteProject")
		}
	}

	// Task gone (cascade).
	if _, err := st.GetTask(task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("task after project delete = %v, want ErrNotFound", err)
	}

	// Comments gone (cascade).
	var count int
	st.db.QueryRow("SELECT COUNT(*) FROM comments WHERE task_id=?", task.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("comments after project delete = %d, want 0", count)
	}

	// project.deleted event persists in event log (events are NOT deleted).
	// Use direct DB query since ListEvents filters by archived projects.
	var evCount int
	st.db.QueryRow("SELECT COUNT(*) FROM events WHERE kind='project.deleted'").Scan(&evCount)
	if evCount == 0 {
		t.Error("project.deleted event not found in event log")
	}

	// ErrNotFound on re-delete.
	_, err = st.DeleteProject("alpha", "alice")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteProject re-delete = %v, want ErrNotFound", err)
	}
}

func TestEventsCursorStrictlyIncreasing(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "T"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := st.ClaimTask(task.ID, "agent-a"); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if _, _, err := st.AddComment(task.ID, "agent-a", "hi"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if _, _, err := st.PatchTask(task.ID, map[string]any{"status": "done"}, "agent-a"); err != nil {
		t.Fatalf("PatchTask: %v", err)
	}

	all, last, err := st.ListEvents(0, "", 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) < 4 { // project.created, task.created, task.claimed, comment.added, task.status
		t.Fatalf("expected >=4 events, got %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("event ids not strictly increasing: %d then %d", all[i-1].ID, all[i].ID)
		}
	}
	if last != all[len(all)-1].ID {
		t.Fatalf("ListEvents last = %d, want %d", last, all[len(all)-1].ID)
	}

	// since-cursor: events after the first should exclude it.
	cursor := all[0].ID
	rest, _, err := st.ListEvents(cursor, "", 0)
	if err != nil {
		t.Fatalf("ListEvents(since): %v", err)
	}
	if len(rest) != len(all)-1 {
		t.Fatalf("ListEvents(since=%d) len = %d, want %d", cursor, len(rest), len(all)-1)
	}
	for _, e := range rest {
		if e.ID <= cursor {
			t.Fatalf("ListEvents(since=%d) returned id %d <= cursor", cursor, e.ID)
		}
	}

	// RecentEvents returns newest first and the same max id.
	recent, max, err := st.RecentEvents("", 0)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if max != last {
		t.Fatalf("RecentEvents max = %d, want %d", max, last)
	}
	if len(recent) > 1 && recent[0].ID <= recent[1].ID {
		t.Fatalf("RecentEvents not newest-first: %d then %d", recent[0].ID, recent[1].ID)
	}
}

// ===================== dependency tests =====================

func setupDepFixture(t *testing.T) (*Store, int64, int64, int64) {
	t.Helper()
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web"); err != nil {
		t.Fatal(err)
	}
	t1, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "Task 1"})
	if err != nil {
		t.Fatal(err)
	}
	t2, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "Task 2"})
	if err != nil {
		t.Fatal(err)
	}
	t3, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "Task 3"})
	if err != nil {
		t.Fatal(err)
	}
	return st, t1.ID, t2.ID, t3.ID
}

func TestAddDepHappyPath(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)

	ev, err := st.AddDep(t2, t1, "alice") // t2 depends on t1
	if err != nil {
		t.Fatalf("AddDep: %v", err)
	}
	if ev == nil {
		t.Fatal("AddDep: expected event, got nil")
	}
	if ev.Kind != "task.dep_added" {
		t.Fatalf("AddDep event kind = %q, want task.dep_added", ev.Kind)
	}

	// GetTask should show the dependency.
	task2, err := st.GetTask(t2)
	if err != nil {
		t.Fatal(err)
	}
	if len(task2.DependsOn) != 1 || task2.DependsOn[0].ID != t1 {
		t.Fatalf("DependsOn = %+v, want [{ID:%d}]", task2.DependsOn, t1)
	}

	// GetTask on t1 should show it blocks t2.
	task1, err := st.GetTask(t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(task1.Blocks) != 1 || task1.Blocks[0].ID != t2 {
		t.Fatalf("Blocks = %+v, want [{ID:%d}]", task1.Blocks, t2)
	}
}

func TestAddDepSelfRejected(t *testing.T) {
	st, t1, _, _ := setupDepFixture(t)
	_, err := st.AddDep(t1, t1, "alice")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("self-dep err = %v, want ErrValidation", err)
	}
}

func TestAddDepCrossProjectRejected(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("proj1", "P1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("proj2", "P2"); err != nil {
		t.Fatal(err)
	}
	ta, _, _ := st.CreateTask(CreateTaskInput{Project: "proj1", Title: "A"})
	tb, _, _ := st.CreateTask(CreateTaskInput{Project: "proj2", Title: "B"})
	_, err := st.AddDep(ta.ID, tb.ID, "alice")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("cross-project dep err = %v, want ErrValidation", err)
	}
}

func TestAddDepCycleRejected(t *testing.T) {
	st, t1, t2, t3 := setupDepFixture(t)
	// t2 depends on t1
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatalf("AddDep(t2->t1): %v", err)
	}
	// t3 depends on t2
	if _, err := st.AddDep(t3, t2, "alice"); err != nil {
		t.Fatalf("AddDep(t3->t2): %v", err)
	}
	// t1 depends on t3 would form a cycle: t1->t3->t2->t1
	_, err := st.AddDep(t1, t3, "alice")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("cycle dep err = %v, want ErrValidation", err)
	}
}

func TestAddDepDirectCycleRejected(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatalf("AddDep(t2->t1): %v", err)
	}
	// t1 depends on t2 would form a direct cycle
	_, err := st.AddDep(t1, t2, "alice")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("direct cycle err = %v, want ErrValidation", err)
	}
}

func TestAddDepIdempotent(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	ev1, err := st.AddDep(t2, t1, "alice")
	if err != nil {
		t.Fatalf("first AddDep: %v", err)
	}
	if ev1 == nil {
		t.Fatal("first AddDep: want event")
	}
	// Second add of the same edge must be a no-op (nil event).
	ev2, err := st.AddDep(t2, t1, "alice")
	if err != nil {
		t.Fatalf("second AddDep: %v", err)
	}
	if ev2 != nil {
		t.Fatal("second AddDep: want nil event (idempotent)")
	}
}

func TestRemoveDep(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatalf("AddDep: %v", err)
	}
	ev, err := st.RemoveDep(t2, t1, "alice")
	if err != nil {
		t.Fatalf("RemoveDep: %v", err)
	}
	if ev == nil || ev.Kind != "task.dep_removed" {
		t.Fatalf("RemoveDep event = %v, want task.dep_removed", ev)
	}
	// Edge should be gone.
	task2, _ := st.GetTask(t2)
	if len(task2.DependsOn) != 0 {
		t.Fatalf("DependsOn after remove = %+v, want empty", task2.DependsOn)
	}
	// Idempotent remove — no error, nil event.
	ev2, err := st.RemoveDep(t2, t1, "alice")
	if err != nil {
		t.Fatalf("second RemoveDep: %v", err)
	}
	if ev2 != nil {
		t.Fatal("second RemoveDep: want nil event")
	}
}

func TestGetTaskDependsOnAndBlocks(t *testing.T) {
	st, t1, t2, t3 := setupDepFixture(t)
	// t2 depends on t1; t3 depends on t1
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDep(t3, t1, "alice"); err != nil {
		t.Fatal(err)
	}

	task1, err := st.GetTask(t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(task1.DependsOn) != 0 {
		t.Fatalf("t1.DependsOn = %v, want empty", task1.DependsOn)
	}
	if len(task1.Blocks) != 2 {
		t.Fatalf("t1.Blocks = %v, want 2 items", task1.Blocks)
	}

	task2, err := st.GetTask(t2)
	if err != nil {
		t.Fatal(err)
	}
	if len(task2.DependsOn) != 1 || task2.DependsOn[0].ID != t1 {
		t.Fatalf("t2.DependsOn = %v, want [t1]", task2.DependsOn)
	}
}

func TestDeleteTaskCascadesDeps(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatal(err)
	}

	// Delete t1 (the prereq). The edge must be removed via FK cascade.
	if _, err := st.DeleteTask(t1, "alice"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	var count int
	st.db.QueryRow("SELECT COUNT(*) FROM task_deps WHERE depends_on_id=?", t1).Scan(&count)
	if count != 0 {
		t.Fatalf("task_deps row survived deletion of prereq, count=%d", count)
	}

	// t2 should now have no deps.
	task2, _ := st.GetTask(t2)
	if len(task2.DependsOn) != 0 {
		t.Fatalf("t2.DependsOn after prereq delete = %v, want empty", task2.DependsOn)
	}
}

func TestListTasksDepCounts(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatal(err)
	}

	tasks, err := st.ListTasks(TaskFilter{Project: "web"})
	if err != nil {
		t.Fatal(err)
	}
	m := map[int64]Task{}
	for _, tk := range tasks {
		m[tk.ID] = tk
	}
	if m[t1].NPrereqs != 0 || m[t1].NOpenPrereqs != 0 {
		t.Fatalf("t1 counts = nprereq=%d nopen=%d, want 0,0", m[t1].NPrereqs, m[t1].NOpenPrereqs)
	}
	if m[t2].NPrereqs != 1 {
		t.Fatalf("t2.NPrereqs = %d, want 1", m[t2].NPrereqs)
	}
	if m[t2].NOpenPrereqs != 1 {
		t.Fatalf("t2.NOpenPrereqs = %d, want 1 (t1 is todo)", m[t2].NOpenPrereqs)
	}

	// Mark t1 done → t2's NOpenPrereqs should drop to 0.
	if _, _, err := st.PatchTask(t1, map[string]any{"status": "done"}, "alice"); err != nil {
		t.Fatal(err)
	}
	tasks2, _ := st.ListTasks(TaskFilter{Project: "web"})
	m2 := map[int64]Task{}
	for _, tk := range tasks2 {
		m2[tk.ID] = tk
	}
	if m2[t2].NOpenPrereqs != 0 {
		t.Fatalf("t2.NOpenPrereqs after t1 done = %d, want 0", m2[t2].NOpenPrereqs)
	}
}

func TestListTasksReadyFilter(t *testing.T) {
	st, t1, t2, t3 := setupDepFixture(t)
	// t2 depends on t1 (open prereq → not ready)
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatal(err)
	}
	// t3 has no deps → ready

	ready, err := st.ListTasks(TaskFilter{Project: "web", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, tk := range ready {
		ids[tk.ID] = true
	}
	// t1 and t3 have no open prereqs and are todo → ready
	if !ids[t1] {
		t.Error("t1 should appear in ready list (no deps)")
	}
	if !ids[t3] {
		t.Error("t3 should appear in ready list (no deps)")
	}
	// t2 has open prereq → not ready
	if ids[t2] {
		t.Error("t2 should NOT appear in ready list (has open prereq)")
	}
}

func TestListTasksBlockedFilter(t *testing.T) {
	st, t1, t2, t3 := setupDepFixture(t)
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatal(err)
	}

	blocked, err := st.ListTasks(TaskFilter{Project: "web", Blocked: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, tk := range blocked {
		ids[tk.ID] = true
	}
	if !ids[t2] {
		t.Error("t2 should appear in blocked list (has open prereq)")
	}
	if ids[t1] || ids[t3] {
		t.Error("t1/t3 should NOT appear in blocked list")
	}
}

func TestClaimBlockedByOpenPrereq(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatal(err)
	}

	// Claim t2 while t1 is todo → must be blocked.
	_, _, err := st.ClaimTask(t2, "agent-a")
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("ClaimTask with open prereq: got %v, want *BlockedError", err)
	}
	if len(be.OpenPrereqs) == 0 {
		t.Fatal("BlockedError.OpenPrereqs should be non-empty")
	}
}

func TestClaimUnblockedAfterPrereqDone(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatal(err)
	}
	// Mark t1 done.
	if _, _, err := st.PatchTask(t1, map[string]any{"status": "done"}, "alice"); err != nil {
		t.Fatal(err)
	}
	// Now claim t2 — should succeed.
	task, ev, err := st.ClaimTask(t2, "agent-a")
	if err != nil {
		t.Fatalf("ClaimTask after prereq done: %v", err)
	}
	if task == nil || ev == nil {
		t.Fatal("want task+event on successful claim")
	}
}

func TestPatchTaskBlockedByOpenPrereq(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	if _, err := st.AddDep(t2, t1, "alice"); err != nil {
		t.Fatal(err)
	}

	// Cannot patch t2 to doing while t1 is todo.
	_, _, err := st.PatchTask(t2, map[string]any{"status": "doing"}, "alice")
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("PatchTask(doing) with open prereq: got %v, want *BlockedError", err)
	}

	// Cannot patch t2 to done while t1 is todo.
	_, _, err = st.PatchTask(t2, map[string]any{"status": "done"}, "alice")
	if !errors.As(err, &be) {
		t.Fatalf("PatchTask(done) with open prereq: got %v, want *BlockedError", err)
	}

	// Allowed to patch title even with open prereq.
	_, _, err = st.PatchTask(t2, map[string]any{"title": "New Title"}, "alice")
	if err != nil {
		t.Fatalf("PatchTask(title) should succeed: %v", err)
	}

	// Allowed to patch to blocked/todo even with open prereq.
	_, _, err = st.PatchTask(t2, map[string]any{"status": "blocked"}, "alice")
	if err != nil {
		t.Fatalf("PatchTask(blocked) should succeed: %v", err)
	}

	// Mark t1 done → now patch t2 to doing should work.
	if _, _, err := st.PatchTask(t1, map[string]any{"status": "done"}, "alice"); err != nil {
		t.Fatal(err)
	}
	_, _, err = st.PatchTask(t2, map[string]any{"status": "doing"}, "alice")
	if err != nil {
		t.Fatalf("PatchTask(doing) after prereq done: %v", err)
	}
}

func TestTaskDepsTableExistsOnReopenedDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first OpenStore: %v", err)
	}
	st.Close()

	// Reopen — task_deps must exist (IF NOT EXISTS path).
	st2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("second OpenStore: %v", err)
	}
	defer st2.Close()

	var count int
	if err := st2.db.QueryRow("SELECT COUNT(*) FROM task_deps").Scan(&count); err != nil {
		t.Fatalf("task_deps table missing after reopen: %v", err)
	}
}

// Suppress unused import warning for fmt (used in TestAddDepCycleRejected).
var _ = fmt.Sprintf

// ===================== ProjectGraph tests =====================

func TestProjectGraph(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("gproj", "Graph Project"); err != nil {
		t.Fatal(err)
	}
	// Second project — its tasks must not appear in gproj's graph.
	if _, _, err := st.CreateProject("other", "Other"); err != nil {
		t.Fatal(err)
	}

	ta, _, err := st.CreateTask(CreateTaskInput{Project: "gproj", Title: "Task A"})
	if err != nil {
		t.Fatal(err)
	}
	tb, _, err := st.CreateTask(CreateTaskInput{Project: "gproj", Title: "Task B"})
	if err != nil {
		t.Fatal(err)
	}
	tc, _, err := st.CreateTask(CreateTaskInput{Project: "gproj", Title: "Task C"})
	if err != nil {
		t.Fatal(err)
	}
	// Task in other project — must not leak into gproj graph.
	tOther, _, err := st.CreateTask(CreateTaskInput{Project: "other", Title: "Other Task"})
	if err != nil {
		t.Fatal(err)
	}
	_ = tOther

	// Chain: B depends on A; C depends on B.
	if _, err := st.AddDep(tb.ID, ta.ID, "alice"); err != nil {
		t.Fatalf("AddDep(B->A): %v", err)
	}
	if _, err := st.AddDep(tc.ID, tb.ID, "alice"); err != nil {
		t.Fatalf("AddDep(C->B): %v", err)
	}

	data, err := st.ProjectGraph("gproj")
	if err != nil {
		t.Fatalf("ProjectGraph: %v", err)
	}

	// Node count must match the project's task count.
	if len(data.Nodes) != 3 {
		t.Fatalf("ProjectGraph nodes = %d, want 3", len(data.Nodes))
	}
	// All nodes must belong to gproj.
	for _, n := range data.Nodes {
		if n.Project != "gproj" {
			t.Errorf("ProjectGraph returned node from project %q, want gproj", n.Project)
		}
	}

	// Edge count.
	if len(data.Edges) != 2 {
		t.Fatalf("ProjectGraph edges = %d, want 2", len(data.Edges))
	}

	// Verify direction: From = prereq, To = dependent.
	edgeMap := map[int64]int64{}
	for _, e := range data.Edges {
		edgeMap[e.From] = e.To
	}
	// B depends on A → edge From=A.ID, To=B.ID
	if edgeMap[ta.ID] != tb.ID {
		t.Errorf("expected edge A->B (from=%d, to=%d), got to=%d", ta.ID, tb.ID, edgeMap[ta.ID])
	}
	// C depends on B → edge From=B.ID, To=C.ID
	if edgeMap[tb.ID] != tc.ID {
		t.Errorf("expected edge B->C (from=%d, to=%d), got to=%d", tb.ID, tc.ID, edgeMap[tb.ID])
	}
}

func TestProjectGraphMissingProject(t *testing.T) {
	st := openTestStore(t)
	_, err := st.ProjectGraph("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ProjectGraph missing slug: got %v, want ErrNotFound", err)
	}
}

// TestInputLimits: oversized titles/bodies/comments and out-of-range priorities
// are rejected with ErrValidation on both create and patch paths.
func TestInputLimits(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("lim", "Limits"); err != nil {
		t.Fatal(err)
	}
	longTitle := strings.Repeat("x", maxTitleLen+1)
	longBody := strings.Repeat("x", maxBodyLen+1)

	// Create rejections.
	createCases := []struct {
		name string
		in   CreateTaskInput
	}{
		{"long title", CreateTaskInput{Project: "lim", Title: longTitle}},
		{"long body", CreateTaskInput{Project: "lim", Title: "ok", Body: longBody}},
		{"priority too high", CreateTaskInput{Project: "lim", Title: "ok", Priority: maxPriority + 1}},
		{"priority negative", CreateTaskInput{Project: "lim", Title: "ok", Priority: -1}},
	}
	for _, c := range createCases {
		if _, _, err := st.CreateTask(c.in); !errors.Is(err, ErrValidation) {
			t.Errorf("CreateTask %s: err = %v, want ErrValidation", c.name, err)
		}
	}

	// Boundary values are accepted.
	task, _, err := st.CreateTask(CreateTaskInput{
		Project: "lim", Title: strings.Repeat("t", maxTitleLen),
		Body: strings.Repeat("b", maxBodyLen), Priority: maxPriority,
	})
	if err != nil {
		t.Fatalf("CreateTask at limits: %v", err)
	}

	// Patch rejections. Priority arrives as float64 over JSON, so test that form.
	patchCases := []struct {
		name  string
		patch map[string]any
	}{
		{"long title", map[string]any{"title": longTitle}},
		{"long body", map[string]any{"body": longBody}},
		{"priority too high", map[string]any{"priority": float64(maxPriority + 1)}},
		{"priority negative", map[string]any{"priority": float64(-1)}},
	}
	for _, c := range patchCases {
		if _, _, err := st.PatchTask(task.ID, c.patch, "alice"); !errors.Is(err, ErrValidation) {
			t.Errorf("PatchTask %s: err = %v, want ErrValidation", c.name, err)
		}
	}

	// Comment rejection + boundary accept.
	if _, _, err := st.AddComment(task.ID, "alice", longBody); !errors.Is(err, ErrValidation) {
		t.Errorf("AddComment long body: err = %v, want ErrValidation", err)
	}
	if _, _, err := st.AddComment(task.ID, "alice", strings.Repeat("c", maxBodyLen)); err != nil {
		t.Errorf("AddComment at limit: %v", err)
	}
}
