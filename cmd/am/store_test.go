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
	if _, _, err := st.CreateProject("active", "Active", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("archproj", "Archived", ""); err != nil {
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
	allEvs, _, err := st.ListEvents(0, "", "", 500)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(allEvs) < 5 {
		t.Fatalf("want >=5 events, got %d", len(allEvs))
	}

	// before= the last event id: should return all but the last, newest-first.
	lastID := allEvs[len(allEvs)-1].ID
	got, err := st.ListEventsBefore(lastID, "", "", 100)
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
	gotAfterArchive, err := st.ListEventsBefore(lastID+1, "", "", 100)
	if err != nil {
		t.Fatalf("ListEventsBefore after archive: %v", err)
	}
	for _, e := range gotAfterArchive {
		if e.ProjectID == archPID {
			t.Errorf("ListEventsBefore(unfiltered) returned event from archived project_id=%d kind=%s", archPID, e.Kind)
		}
	}

	// Explicit project="archproj" should still return that project's events even archived.
	archEvs, err := st.ListEventsBefore(lastID+1, "archproj", "", 100)
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
	limited, err := st.ListEventsBefore(lastID+1, "", "", 2)
	if err != nil {
		t.Fatalf("ListEventsBefore limited: %v", err)
	}
	if len(limited) > 2 {
		t.Fatalf("expected <=2 events with limit=2, got %d", len(limited))
	}
}

func TestCreateProjectAndTaskHappyPath(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "   "}); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty title err = %v, want ErrValidation", err)
	}
}

func TestPatchTaskInvalidStatusValidation(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
			if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	_, _, err := st.CreateProject("testproj", "Test", "")
	if err != nil {
		t.Fatal(err)
	}

	// Default list excludes nothing (not archived yet)
	ps, err := st.ListProjects(false, "")
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
	ps, err = st.ListProjects(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("archived project should be hidden in default list, got %d", len(ps))
	}

	// All list includes it
	ps, err = st.ListProjects(true, "")
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
	ps, err = st.ListProjects(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("want 1 project after unarchive, got %d", len(ps))
	}
}

func TestListTasksHidesArchivedProjectTasks(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("alpha", "Alpha", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("beta", "Beta", ""); err != nil {
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
	if _, _, err := st.CreateProject("alpha", "Alpha", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("beta", "Beta", ""); err != nil {
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
	recent, _, err := st.RecentEvents("", "", 50)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	for _, e := range recent {
		if e.ProjectID == alphaPID {
			t.Errorf("RecentEvents(\"\") returned event with archived project_id %d (kind=%s)", alphaPID, e.Kind)
		}
	}

	// Unfiltered ListEvents must NOT include alpha's events.
	all, _, err := st.ListEvents(0, "", "", 200)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, e := range all {
		if e.ProjectID == alphaPID {
			t.Errorf("ListEvents(0,\"\") returned event with archived project_id %d (kind=%s)", alphaPID, e.Kind)
		}
	}

	// Explicit project=alpha MUST still return alpha's events.
	alphaEvs, _, err := st.RecentEvents("alpha", "", 50)
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
	recentAfter, _, err := st.RecentEvents("", "", 50)
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
	if _, _, err := st.CreateProject("active", "Active", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("archived", "Archived", ""); err != nil {
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	evs, _, err := st.ListEvents(0, "web", "", 200)
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	if _, _, err := st.CreateProject("alpha", "Alpha", ""); err != nil {
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
	ps, err := st.ListProjects(true, "")
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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

	all, last, err := st.ListEvents(0, "", "", 0)
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
	rest, _, err := st.ListEvents(cursor, "", "", 0)
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
	recent, max, err := st.RecentEvents("", "", 0)
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
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
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
	if _, _, err := st.CreateProject("proj1", "P1", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("proj2", "P2", ""); err != nil {
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
	if _, _, err := st.CreateProject("gproj", "Graph Project", ""); err != nil {
		t.Fatal(err)
	}
	// Second project — its tasks must not appear in gproj's graph.
	if _, _, err := st.CreateProject("other", "Other", ""); err != nil {
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
	if _, _, err := st.CreateProject("lim", "Limits", ""); err != nil {
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

// ===================== am next (Phase L) tests =====================

// nextTaskMk creates a task with explicit priority/assignee for NextTask tests.
func nextTaskMk(t *testing.T, st *Store, project, title string, priority int, assignee string) int64 {
	t.Helper()
	tk, _, err := st.CreateTask(CreateTaskInput{
		Project: project, Title: title, Priority: priority, Assignee: assignee})
	if err != nil {
		t.Fatalf("CreateTask %s: %v", title, err)
	}
	return tk.ID
}

func TestNextTaskPicksHighestPriorityReady(t *testing.T) {
	st := openTestStore(t)
	for _, slug := range []string{"web", "attic"} {
		if _, _, err := st.CreateProject(slug, slug, ""); err != nil {
			t.Fatalf("CreateProject %s: %v", slug, err)
		}
	}

	// Decoys, all of which would beat the winner if they were eligible:
	// p0 but blocked by an open prereq.
	prereq := nextTaskMk(t, st, "web", "prereq", 3, "")
	blocked := nextTaskMk(t, st, "web", "blocked p0", 0, "")
	if _, err := st.AddDep(blocked, prereq, "alice"); err != nil {
		t.Fatalf("AddDep: %v", err)
	}
	// p0 but already assigned (still todo).
	nextTaskMk(t, st, "web", "assigned p0", 0, "someone-else")
	// p0 but done.
	doneID := nextTaskMk(t, st, "web", "done p0", 0, "")
	if _, _, err := st.PatchTask(doneID, map[string]any{"status": "done"}, "alice"); err != nil {
		t.Fatalf("PatchTask done: %v", err)
	}
	// p0 but in an archived project.
	nextTaskMk(t, st, "attic", "archived p0", 0, "")
	if _, _, err := st.ArchiveProject("attic", "alice"); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}
	// Eligible: the p1 winner and a p3 also-ran (the prereq task itself).
	winner := nextTaskMk(t, st, "web", "winner p1", 1, "")

	tk, ev, err := st.NextTask(NextFilter{}, "agent-x")
	if err != nil {
		t.Fatalf("NextTask: %v", err)
	}
	if tk.ID != winner {
		t.Fatalf("NextTask picked #%d (%s), want winner #%d", tk.ID, tk.Title, winner)
	}
	if tk.Status != "doing" || tk.Assignee != "agent-x" {
		t.Fatalf("task = %s/%s, want doing/agent-x", tk.Status, tk.Assignee)
	}
	if tk.ClaimedAt == "" {
		t.Fatal("claimed_at not set by NextTask")
	}
	if ev == nil || ev.Kind != "task.claimed" {
		t.Fatalf("event = %+v, want kind task.claimed", ev)
	}
	if !strings.Contains(string(ev.Data), `"assignee":[null,"agent-x"]`) {
		t.Fatalf("event data = %s, want assignee [null,agent-x]", ev.Data)
	}
	if !strings.Contains(string(ev.Data), `"status":"doing"`) {
		t.Fatalf("event data = %s, want status doing", ev.Data)
	}
}

func TestNextTaskFIFOWithinPriority(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	first := nextTaskMk(t, st, "web", "older", 2, "")
	nextTaskMk(t, st, "web", "newer", 2, "")

	tk, _, err := st.NextTask(NextFilter{}, "agent-x")
	if err != nil {
		t.Fatalf("NextTask: %v", err)
	}
	if tk.ID != first {
		t.Fatalf("NextTask picked #%d, want FIFO winner #%d (lower id)", tk.ID, first)
	}
}

func TestNextTaskProjectScoping(t *testing.T) {
	st := openTestStore(t)
	for _, slug := range []string{"web", "api"} {
		if _, _, err := st.CreateProject(slug, slug, ""); err != nil {
			t.Fatalf("CreateProject %s: %v", slug, err)
		}
	}
	apiTask := nextTaskMk(t, st, "api", "api urgent", 0, "")
	webTask := nextTaskMk(t, st, "web", "web slow", 3, "")

	// Project scope beats global priority order.
	tk, _, err := st.NextTask(NextFilter{Project: "web"}, "agent-x")
	if err != nil {
		t.Fatalf("NextTask(web): %v", err)
	}
	if tk.ID != webTask {
		t.Fatalf("NextTask(web) picked #%d, want #%d", tk.ID, webTask)
	}
	// No scope: best remaining across all projects.
	tk2, _, err := st.NextTask(NextFilter{}, "agent-y")
	if err != nil {
		t.Fatalf("NextTask(\"\"): %v", err)
	}
	if tk2.ID != apiTask {
		t.Fatalf("NextTask(\"\") picked #%d, want #%d", tk2.ID, apiTask)
	}
	// Bad slug → ErrNotFound.
	if _, _, err := st.NextTask(NextFilter{Project: "nosuch"}, "agent-z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("NextTask(nosuch) err = %v, want ErrNotFound", err)
	}
}

func TestNextTaskNoneReady(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// Only an assigned todo task — not a candidate.
	nextTaskMk(t, st, "web", "taken", 0, "someone-else")

	if _, _, err := st.NextTask(NextFilter{}, "agent-x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("NextTask err = %v, want ErrNotFound", err)
	}
	var n int
	st.db.QueryRow("SELECT COUNT(*) FROM events WHERE kind='task.claimed'").Scan(&n)
	if n != 0 {
		t.Fatalf("task.claimed event rows = %d, want 0 (no event on miss)", n)
	}
}

func TestNextTaskRaceDistinctWinners(t *testing.T) {
	// N callers, M>N ready tasks: everyone wins a DISTINCT task.
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	const n = 4
	for i := 0; i < n+2; i++ {
		nextTaskMk(t, st, "web", fmt.Sprintf("ready %d", i), 2, "")
	}

	type result struct {
		task *Task
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tk, _, err := st.NextTask(NextFilter{}, fmt.Sprintf("agent-%d", i))
			results[i] = result{task: tk, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[int64]bool{}
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("agent-%d: %v", i, r.err)
		}
		if seen[r.task.ID] {
			t.Fatalf("task #%d claimed twice", r.task.ID)
		}
		seen[r.task.ID] = true
	}

	// N callers, 1 ready task: exactly one winner, the rest ErrNotFound.
	st2 := openTestStore(t)
	if _, _, err := st2.CreateProject("web", "Web", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	nextTaskMk(t, st2, "web", "only one", 2, "")
	results2 := make([]result, n)
	var wg2 sync.WaitGroup
	start2 := make(chan struct{})
	for i := 0; i < n; i++ {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			<-start2
			tk, _, err := st2.NextTask(NextFilter{}, fmt.Sprintf("agent-%d", i))
			results2[i] = result{task: tk, err: err}
		}(i)
	}
	close(start2)
	wg2.Wait()

	winners, misses := 0, 0
	for i, r := range results2 {
		switch {
		case r.err == nil:
			winners++
		case errors.Is(r.err, ErrNotFound):
			misses++
		default:
			t.Fatalf("agent-%d: unexpected error %v", i, r.err)
		}
	}
	if winners != 1 || misses != n-1 {
		t.Fatalf("winners=%d misses=%d, want 1 and %d", winners, misses, n-1)
	}
}

func TestNextTaskEmptyAgentValidation(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.NextTask(NextFilter{}, ""); !errors.Is(err, ErrValidation) {
		t.Fatalf("NextTask empty agent err = %v, want ErrValidation", err)
	}
}

// ===================== Phase M: search (?q=) tests =====================

func TestListTasksQueryFilter(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("api", "API", ""); err != nil {
		t.Fatal(err)
	}
	titleHit, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "Fix login page"})
	if err != nil {
		t.Fatal(err)
	}
	bodyHit, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "Other thing", Body: "the login flow is broken"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "Unrelated"}); err != nil {
		t.Fatal(err)
	}
	apiHit, _, err := st.CreateTask(CreateTaskInput{Project: "api", Title: "login throttling"})
	if err != nil {
		t.Fatal(err)
	}

	ids := func(ts []Task) map[int64]bool {
		m := map[int64]bool{}
		for _, tk := range ts {
			m[tk.ID] = true
		}
		return m
	}

	// Title and body matches, across projects.
	got, err := st.ListTasks(TaskFilter{Query: "login"})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 3 || !ids(got)[titleHit.ID] || !ids(got)[bodyHit.ID] || !ids(got)[apiHit.ID] {
		t.Fatalf("Query login = %+v, want title+body+api hits", got)
	}

	// ASCII-case-insensitive (SQLite LIKE default).
	got, err = st.ListTasks(TaskFilter{Query: "LOGIN"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("Query LOGIN matched %d tasks, want 3 (case-insensitive)", len(got))
	}

	// No match → empty.
	got, err = st.ListTasks(TaskFilter{Query: "zebra"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Query zebra = %+v, want empty", got)
	}

	// Empty query → unfiltered.
	got, err = st.ListTasks(TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("empty Query returned %d tasks, want 4", len(got))
	}

	// Combines with Project and Status.
	got, err = st.ListTasks(TaskFilter{Query: "login", Project: "web", Status: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !ids(got)[titleHit.ID] || !ids(got)[bodyHit.ID] {
		t.Fatalf("Query+Project+Status = %+v, want the two web hits", got)
	}
}

func TestListTasksQueryEscapesLikeWildcards(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"100% done", "100 done", "a_b", "axb", `back\slash`} {
		if _, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: title}); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		query string
		want  []string
	}{
		{"100%", []string{"100% done"}},        // % is literal, not wildcard
		{"a_b", []string{"a_b"}},               // _ is literal, not single-char wildcard
		{`back\slash`, []string{`back\slash`}}, // backslash doesn't break ESCAPE
	}
	for _, c := range cases {
		got, err := st.ListTasks(TaskFilter{Query: c.query})
		if err != nil {
			t.Fatalf("Query %q: %v", c.query, err)
		}
		var titles []string
		for _, tk := range got {
			titles = append(titles, tk.Title)
		}
		if len(titles) != len(c.want) || (len(c.want) > 0 && titles[0] != c.want[0]) {
			t.Fatalf("Query %q = %v, want %v", c.query, titles, c.want)
		}
	}
}

// ===================== Phase M: label tests =====================

func TestAddRemoveLabel(t *testing.T) {
	st, t1, _, _ := setupDepFixture(t)

	// Add — normalized to lowercase, emits task.labeled.
	ev, err := st.AddLabel(t1, "Bug", "alice")
	if err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if ev == nil || ev.Kind != "task.labeled" {
		t.Fatalf("AddLabel event = %+v, want task.labeled", ev)
	}
	if !strings.Contains(string(ev.Data), `"label":"bug"`) {
		t.Fatalf("AddLabel event data = %s, want label bug", ev.Data)
	}

	// Duplicate add (any case) — idempotent, no event.
	ev2, err := st.AddLabel(t1, "bug", "alice")
	if err != nil {
		t.Fatalf("duplicate AddLabel: %v", err)
	}
	if ev2 != nil {
		t.Fatalf("duplicate AddLabel event = %+v, want nil", ev2)
	}

	// GetTask returns sorted labels.
	if _, err := st.AddLabel(t1, "api", "alice"); err != nil {
		t.Fatal(err)
	}
	tk, err := st.GetTask(t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(tk.Labels) != 2 || tk.Labels[0] != "api" || tk.Labels[1] != "bug" {
		t.Fatalf("GetTask labels = %v, want [api bug] sorted", tk.Labels)
	}

	// Remove — emits task.unlabeled.
	ev3, err := st.RemoveLabel(t1, "bug", "alice")
	if err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	if ev3 == nil || ev3.Kind != "task.unlabeled" {
		t.Fatalf("RemoveLabel event = %+v, want task.unlabeled", ev3)
	}
	if !strings.Contains(string(ev3.Data), `"label":"bug"`) {
		t.Fatalf("RemoveLabel event data = %s, want label bug", ev3.Data)
	}

	// Absent remove — idempotent, no event.
	ev4, err := st.RemoveLabel(t1, "bug", "alice")
	if err != nil {
		t.Fatalf("second RemoveLabel: %v", err)
	}
	if ev4 != nil {
		t.Fatalf("second RemoveLabel event = %+v, want nil", ev4)
	}

	// Missing task → ErrNotFound.
	if _, err := st.AddLabel(99999, "bug", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddLabel missing task err = %v, want ErrNotFound", err)
	}
}

func TestLabelValidation(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" = expect ErrValidation
	}{
		{"", ""},
		{strings.Repeat("a", 51), ""},
		{strings.Repeat("a", 50), strings.Repeat("a", 50)}, // boundary ok
		{"has space", ""},
		{"a,b", ""},
		{"a+b", ""},
		{"Bug", "bug"},           // uppercase normalized
		{"a.b_c-1", "a.b_c-1"},   // full allowed charset
		{"  padded  ", "padded"}, // trimmed
	}
	for _, c := range cases {
		got, err := normalizeLabel(c.in)
		if c.want == "" {
			if !errors.Is(err, ErrValidation) {
				t.Errorf("normalizeLabel(%q) err = %v, want ErrValidation", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeLabel(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestListTasksLabelFilter(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	if _, err := st.AddLabel(t1, "bug", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddLabel(t1, "api", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddLabel(t2, "docs", "alice"); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListTasks(TaskFilter{Label: "bug"})
	if err != nil {
		t.Fatalf("ListTasks label: %v", err)
	}
	if len(got) != 1 || got[0].ID != t1 {
		t.Fatalf("Label bug = %+v, want only t1", got)
	}
	// The list payload carries the task's labels, sorted.
	if len(got[0].Labels) != 2 || got[0].Labels[0] != "api" || got[0].Labels[1] != "bug" {
		t.Fatalf("list Labels = %v, want [api bug] sorted", got[0].Labels)
	}

	// Filter input is normalized (uppercase matches).
	got, err = st.ListTasks(TaskFilter{Label: "BUG"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != t1 {
		t.Fatalf("Label BUG = %+v, want only t1 (normalized)", got)
	}

	// Unknown label → empty.
	got, err = st.ListTasks(TaskFilter{Label: "ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Label ghost = %+v, want empty", got)
	}

	// Invalid label → ErrValidation.
	if _, err := st.ListTasks(TaskFilter{Label: "no spaces!"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid label err = %v, want ErrValidation", err)
	}
}

func TestAddLabelDoesNotBumpUpdatedAt(t *testing.T) {
	st, t1, _, _ := setupDepFixture(t)
	before, err := st.GetTask(t1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddLabel(t1, "bug", "alice"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if _, err := st.RemoveLabel(t1, "bug", "alice"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	after, err := st.GetTask(t1)
	if err != nil {
		t.Fatal(err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("updated_at changed %q → %q; labeling must not refresh a stale claim", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestDeleteTaskCascadesLabels(t *testing.T) {
	st, t1, _, _ := setupDepFixture(t)
	if _, err := st.AddLabel(t1, "bug", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteTask(t1, "alice"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	var count int
	st.db.QueryRow("SELECT COUNT(*) FROM task_labels WHERE task_id=?", t1).Scan(&count)
	if count != 0 {
		t.Fatalf("task_labels rows survived task deletion, count=%d", count)
	}
}

func TestTaskLabelsTableExistsOnReopenedDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first OpenStore: %v", err)
	}
	// Simulate a pre-Phase-M DB: drop the table, then reopen.
	if _, err := st.db.Exec("DROP TABLE task_labels"); err != nil {
		t.Fatalf("drop task_labels: %v", err)
	}
	st.Close()

	// Reopen — task_labels must come back (CREATE TABLE IF NOT EXISTS path),
	// with no migration step and no version bump.
	st2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("second OpenStore: %v", err)
	}
	defer st2.Close()

	var count int
	if err := st2.db.QueryRow("SELECT COUNT(*) FROM task_labels").Scan(&count); err != nil {
		t.Fatalf("task_labels table missing after reopen: %v", err)
	}
	v, err := readSchemaVersion(st2.db)
	if err != nil {
		t.Fatal(err)
	}
	if v != currentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d (labels need no migration)", v, currentSchemaVersion)
	}
}

// ===================== Phase O: categories, stable ids, vault binding =====================

func TestCreateCategory(t *testing.T) {
	st := openTestStore(t)

	// Slug is trimmed + lowercased; name defaults to slug.
	c, ev, err := st.CreateCategory("  Work ", "")
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	if c.Slug != "work" || c.Name != "work" {
		t.Fatalf("category = %+v, want slug/name work", c)
	}
	if !uidRe("amc_").MatchString(c.UID) {
		t.Fatalf("category uid = %q, want amc_<16 hex>", c.UID)
	}
	if ev == nil || ev.Kind != "category.created" {
		t.Fatalf("event = %+v, want kind category.created", ev)
	}
	if ev.ProjectID != 0 {
		t.Fatalf("category.created project_id = %d, want 0 (NULL)", ev.ProjectID)
	}

	// Duplicate slug → ErrConflict (case-insensitive via lowercasing).
	if _, _, err := st.CreateCategory("WORK", "Work"); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup category err = %v, want ErrConflict", err)
	}
	// Invalid slugs → ErrValidation.
	for _, bad := range []string{"", "  ", "has space", "has/slash", "has\ttab"} {
		if _, _, err := st.CreateCategory(bad, ""); !errors.Is(err, ErrValidation) {
			t.Errorf("CreateCategory(%q) err = %v, want ErrValidation", bad, err)
		}
	}

	// uids are distinct across categories.
	c2, _, err := st.CreateCategory("personal", "Personal")
	if err != nil {
		t.Fatalf("CreateCategory personal: %v", err)
	}
	if c2.UID == c.UID {
		t.Fatalf("uid collision: %q", c.UID)
	}

	// ListCategories: ordered by id, default category first (seeded by v4).
	cs, err := st.ListCategories(false)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	if len(cs) != 3 || cs[0].Slug != "general" || cs[1].Slug != "work" || cs[2].Slug != "personal" {
		t.Fatalf("ListCategories = %+v, want [general work personal]", cs)
	}
}

func TestArchiveUnarchiveCategory(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatal(err)
	}

	c, ev, err := st.ArchiveCategory("work", "tester")
	if err != nil {
		t.Fatalf("ArchiveCategory: %v", err)
	}
	if c.ArchivedAt == "" || ev == nil || ev.Kind != "category.archived" {
		t.Fatalf("archive = %+v ev=%+v, want archived_at set + category.archived", c, ev)
	}
	// Idempotent re-archive: success, no event.
	c2, ev2, err := st.ArchiveCategory("work", "tester")
	if err != nil || ev2 != nil || c2.ArchivedAt == "" {
		t.Fatalf("re-archive = %+v ev=%+v err=%v, want idempotent no-event", c2, ev2, err)
	}

	// Hidden from the default list, visible with includeArchived.
	cs, _ := st.ListCategories(false)
	for _, cat := range cs {
		if cat.Slug == "work" {
			t.Fatal("archived category in default ListCategories")
		}
	}
	all, _ := st.ListCategories(true)
	found := false
	for _, cat := range all {
		if cat.Slug == "work" && cat.ArchivedAt != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("archived category missing from ListCategories(true)")
	}

	u, ev3, err := st.UnarchiveCategory("work", "tester")
	if err != nil || ev3 == nil || ev3.Kind != "category.unarchived" || u.ArchivedAt != "" {
		t.Fatalf("unarchive = %+v ev=%+v err=%v, want cleared + category.unarchived", u, ev3, err)
	}
	// Idempotent re-unarchive: success, no event.
	if _, ev4, err := st.UnarchiveCategory("work", "tester"); err != nil || ev4 != nil {
		t.Fatalf("re-unarchive ev=%+v err=%v, want idempotent no-event", ev4, err)
	}

	// Unknown slug → ErrNotFound.
	if _, _, err := st.ArchiveCategory("nosuch", "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archive nosuch err = %v, want ErrNotFound", err)
	}
	if _, _, err := st.UnarchiveCategory("nosuch", "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unarchive nosuch err = %v, want ErrNotFound", err)
	}
}

func TestCreateProjectWithCategory(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatal(err)
	}

	// Explicit category resolves.
	p, _, err := st.CreateProject("pentest", "Pentest X", "work")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Category != "work" {
		t.Fatalf("project category = %q, want work", p.Category)
	}
	if !uidRe("amp_").MatchString(p.UID) {
		t.Fatalf("project uid = %q, want amp_<16 hex>", p.UID)
	}

	// Empty category defaults to general.
	pg, _, err := st.CreateProject("misc", "", "")
	if err != nil {
		t.Fatalf("CreateProject default category: %v", err)
	}
	if pg.Category != "general" {
		t.Fatalf("default category = %q, want general", pg.Category)
	}

	// Unknown category → ErrNotFound.
	if _, _, err := st.CreateProject("x", "", "nosuch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown category err = %v, want ErrNotFound", err)
	}

	// Archived category → ErrCategoryArchived.
	if _, _, err := st.ArchiveCategory("work", "t"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("y", "", "work"); !errors.Is(err, ErrCategoryArchived) {
		t.Fatalf("archived category err = %v, want ErrCategoryArchived", err)
	}

	// Slugs are globally unique across categories.
	if _, _, err := st.CreateProject("pentest", "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup slug across categories err = %v, want ErrConflict", err)
	}
}

func TestPatchProject(t *testing.T) {
	st := openTestStore(t)
	p, _, err := st.CreateProject("web", "Web", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("api", "API", ""); err != nil {
		t.Fatal(err)
	}
	tk, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "T"})
	if err != nil {
		t.Fatal(err)
	}

	// Slug rename: uid unchanged, project.patched delta, old slug gone.
	p2, ev, err := st.PatchProject("web", map[string]any{"slug": "frontend"}, "tester")
	if err != nil {
		t.Fatalf("PatchProject slug: %v", err)
	}
	if p2.Slug != "frontend" || p2.UID != p.UID {
		t.Fatalf("patched = slug %q uid %q, want frontend + unchanged uid %q", p2.Slug, p2.UID, p.UID)
	}
	if ev == nil || ev.Kind != "project.patched" || !strings.Contains(string(ev.Data), `"slug":["web","frontend"]`) {
		t.Fatalf("event = %+v, want project.patched with slug delta", ev)
	}
	if _, _, err := st.PatchProject("web", map[string]any{"name": "x"}, "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("patch old slug err = %v, want ErrNotFound", err)
	}
	// Task refs keep working via the new slug.
	if got, err := st.GetTask(tk.ID); err != nil || got.Project != "frontend" {
		t.Fatalf("task project after rename = %v err=%v, want frontend", got, err)
	}

	// Rename onto an existing slug → ErrConflict; invalid slug → ErrValidation.
	if _, _, err := st.PatchProject("frontend", map[string]any{"slug": "api"}, "t"); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup rename err = %v, want ErrConflict", err)
	}
	if _, _, err := st.PatchProject("frontend", map[string]any{"slug": "has space"}, "t"); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid rename err = %v, want ErrValidation", err)
	}
	if _, _, err := st.PatchProject("frontend", map[string]any{"name": " "}, "t"); !errors.Is(err, ErrValidation) {
		t.Fatalf("blank name err = %v, want ErrValidation", err)
	}

	// Vault fields: set, then clear with empty strings.
	p3, ev3, err := st.PatchProject("frontend",
		map[string]any{"vault_project_id": "p_123", "vault_path": "/vault/frontend"}, "tester")
	if err != nil || ev3 == nil {
		t.Fatalf("vault patch = %v ev=%v, want event", err, ev3)
	}
	if p3.VaultProjectID != "p_123" || p3.VaultPath != "/vault/frontend" {
		t.Fatalf("vault fields = %q/%q, want p_123 //vault/frontend", p3.VaultProjectID, p3.VaultPath)
	}
	p4, _, err := st.PatchProject("frontend", map[string]any{"vault_project_id": "", "vault_path": ""}, "tester")
	if err != nil || p4.VaultProjectID != "" || p4.VaultPath != "" {
		t.Fatalf("vault clear = %+v err=%v, want empty fields", p4, err)
	}

	// No-op patch: idempotent success, no event. uid/category are ignored keys.
	p5, ev5, err := st.PatchProject("frontend",
		map[string]any{"slug": "frontend", "uid": "amp_hacked", "category_id": 99}, "tester")
	if err != nil || ev5 != nil {
		t.Fatalf("no-op patch ev=%+v err=%v, want no event", ev5, err)
	}
	if p5.UID != p.UID {
		t.Fatalf("uid changed by patch: %q → %q", p.UID, p5.UID)
	}
}

func TestCategoryArchiveCascade(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("pentest", "", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("misc", "", ""); err != nil {
		t.Fatal(err)
	}
	tk, _, err := st.CreateTask(CreateTaskInput{Project: "pentest", Title: "hidden later"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ArchiveCategory("work", "tester"); err != nil {
		t.Fatal(err)
	}

	// ListProjects default hides the archived category's projects…
	ps, err := st.ListProjects(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Slug != "misc" {
		t.Fatalf("default ListProjects = %+v, want only misc", ps)
	}
	// …includeArchived shows them…
	all, err := st.ListProjects(true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListProjects(true) = %+v, want 2", all)
	}
	// …and an explicit category scope keeps them inspectable.
	scoped, err := st.ListProjects(false, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].Slug != "pentest" {
		t.Fatalf("ListProjects(false, work) = %+v, want pentest", scoped)
	}

	// ListTasks default hides the cascade; explicit category still returns.
	ts, err := st.ListTasks(TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range ts {
		if x.ID == tk.ID {
			t.Fatal("task under archived category leaked into default ListTasks")
		}
	}
	scopedTasks, err := st.ListTasks(TaskFilter{Category: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scopedTasks) != 1 || scopedTasks[0].ID != tk.ID {
		t.Fatalf("ListTasks(Category:work) = %+v, want the hidden task", scopedTasks)
	}

	// Default event feed hides events from projects in archived categories.
	evs, _, err := st.ListEvents(0, "", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := st.projectID("pentest")
	for _, e := range evs {
		if e.ProjectID == pid {
			t.Fatalf("default feed leaked event %s from archived-category project", e.Kind)
		}
	}
	// Explicit ?project= still returns them (unchanged branch).
	pevs, _, err := st.ListEvents(0, "pentest", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(pevs) == 0 {
		t.Fatal("explicit project feed returned nothing for archived-category project")
	}
}

func TestListTasksCategoryFilterComposes(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("wproj", "", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("gproj", "", ""); err != nil {
		t.Fatal(err)
	}
	w1, _, err := st.CreateTask(CreateTaskInput{Project: "wproj", Title: "fix login"})
	if err != nil {
		t.Fatal(err)
	}
	w2, _, err := st.CreateTask(CreateTaskInput{Project: "wproj", Title: "blocked one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDep(w2.ID, w1.ID, "t"); err != nil {
		t.Fatal(err)
	}
	g1, _, err := st.CreateTask(CreateTaskInput{Project: "gproj", Title: "fix login too"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddLabel(w1.ID, "bug", "t"); err != nil {
		t.Fatal(err)
	}

	ids := func(ts []Task) map[int64]bool {
		m := map[int64]bool{}
		for _, x := range ts {
			m[x.ID] = true
		}
		return m
	}

	// Category alone: only work tasks.
	got, err := st.ListTasks(TaskFilter{Category: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if m := ids(got); len(m) != 2 || !m[w1.ID] || !m[w2.ID] {
		t.Fatalf("Category=work = %+v, want w1+w2", got)
	}
	// + Status.
	got, _ = st.ListTasks(TaskFilter{Category: "work", Status: "todo"})
	if m := ids(got); len(m) != 2 {
		t.Fatalf("Category+Status = %+v, want 2", got)
	}
	// + Ready (w2 is blocked).
	got, _ = st.ListTasks(TaskFilter{Category: "work", Ready: true})
	if m := ids(got); len(m) != 1 || !m[w1.ID] {
		t.Fatalf("Category+Ready = %+v, want only w1", got)
	}
	// + Blocked.
	got, _ = st.ListTasks(TaskFilter{Category: "work", Blocked: true})
	if m := ids(got); len(m) != 1 || !m[w2.ID] {
		t.Fatalf("Category+Blocked = %+v, want only w2", got)
	}
	// + Label.
	got, _ = st.ListTasks(TaskFilter{Category: "work", Label: "bug"})
	if m := ids(got); len(m) != 1 || !m[w1.ID] {
		t.Fatalf("Category+Label = %+v, want only w1", got)
	}
	// + Query: "fix login" matches tasks in both categories; the filter narrows to work.
	got, _ = st.ListTasks(TaskFilter{Category: "work", Query: "fix login"})
	if m := ids(got); len(m) != 1 || !m[w1.ID] || m[g1.ID] {
		t.Fatalf("Category+Query = %+v, want only w1", got)
	}
	// + Project (consistent pair narrows; mismatched pair is empty).
	got, _ = st.ListTasks(TaskFilter{Category: "work", Project: "wproj"})
	if m := ids(got); len(m) != 2 {
		t.Fatalf("Category+Project = %+v, want 2", got)
	}
	got, _ = st.ListTasks(TaskFilter{Category: "work", Project: "gproj"})
	if len(got) != 0 {
		t.Fatalf("mismatched Category+Project = %+v, want empty", got)
	}
	// + Stale: claim w1 then backdate it.
	if _, _, err := st.ClaimTask(w1.ID, "agent-a"); err != nil {
		t.Fatal(err)
	}
	backdateTask(t, st, w1.ID)
	got, _ = st.ListTasks(TaskFilter{Category: "work", Stale: time.Hour})
	if m := ids(got); len(m) != 1 || !m[w1.ID] {
		t.Fatalf("Category+Stale = %+v, want only w1", got)
	}
	// Unknown category slug: no rows (it's a plain equality filter).
	got, _ = st.ListTasks(TaskFilter{Category: "nosuch"})
	if len(got) != 0 {
		t.Fatalf("Category=nosuch = %+v, want empty", got)
	}
}

func TestNextTaskCategoryScoping(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateCategory("personal", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("wproj", "", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("pproj", "", "personal"); err != nil {
		t.Fatal(err)
	}
	// Higher-priority ready task in WORK; the personal-scoped pick must skip it
	// (acceptance sketch 3).
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "wproj", Title: "urgent work", Priority: 0}); err != nil {
		t.Fatal(err)
	}
	p1, _, err := st.CreateTask(CreateTaskInput{Project: "pproj", Title: "personal one"})
	if err != nil {
		t.Fatal(err)
	}
	p2, _, err := st.CreateTask(CreateTaskInput{Project: "pproj", Title: "personal two"})
	if err != nil {
		t.Fatal(err)
	}

	tk, _, err := st.NextTask(NextFilter{Category: "personal"}, "agent-a")
	if err != nil {
		t.Fatalf("NextTask(personal): %v", err)
	}
	if tk.ID != p1.ID {
		t.Fatalf("NextTask(personal) picked #%d, want #%d (never the work task)", tk.ID, p1.ID)
	}
	// Sequential pickers get distinct in-scope tasks.
	tk2, _, err := st.NextTask(NextFilter{Category: "personal"}, "agent-b")
	if err != nil {
		t.Fatalf("second NextTask(personal): %v", err)
	}
	if tk2.ID != p2.ID {
		t.Fatalf("second pick = #%d, want #%d", tk2.ID, p2.ID)
	}
	// Scope drained → ErrNotFound even though work still has a ready task.
	if _, _, err := st.NextTask(NextFilter{Category: "personal"}, "agent-c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("drained scope err = %v, want ErrNotFound", err)
	}
	// Bad category slug → ErrNotFound.
	if _, _, err := st.NextTask(NextFilter{Category: "nosuch"}, "agent-d"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad category err = %v, want ErrNotFound", err)
	}

	// Archived category is excluded UNCONDITIONALLY (even unscoped).
	if _, _, err := st.ArchiveCategory("work", "t"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.NextTask(NextFilter{}, "agent-e"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unscoped next with only archived-category work err = %v, want ErrNotFound", err)
	}
}

func TestCreateTaskArchivedCategory(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("wproj", "", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ArchiveCategory("work", "t"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "wproj", Title: "nope"}); !errors.Is(err, ErrCategoryArchived) {
		t.Fatalf("create task under archived category err = %v, want ErrCategoryArchived", err)
	}
}

// ===================== Phase P: task meta =====================

func TestTaskMetaCRUD(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatal(err)
	}

	// Create with meta — rows land, task.created data carries them.
	tk, ev, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "carrier",
		Meta: map[string]string{"Auto": "packet-7", "owner": "alice"}})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if ev == nil || ev.Kind != "task.created" {
		t.Fatalf("event = %+v, want task.created", ev)
	}
	if !strings.Contains(string(ev.Data), `"meta"`) ||
		!strings.Contains(string(ev.Data), `"auto":"packet-7"`) {
		t.Fatalf("task.created data = %s, want meta with normalized auto key", ev.Data)
	}

	// GetTask returns the pairs (key normalized to lowercase).
	got, err := st.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meta) != 2 || got.Meta["auto"] != "packet-7" || got.Meta["owner"] != "alice" {
		t.Fatalf("GetTask meta = %v, want auto=packet-7 owner=alice", got.Meta)
	}

	// Patch upsert: overwrite one key, add another — one event.
	_, ev2, err := st.PatchTask(tk.ID, map[string]any{"meta": map[string]any{"auto": "packet-8", "stage": "review"}}, "alice")
	if err != nil {
		t.Fatalf("PatchTask meta: %v", err)
	}
	if ev2 == nil || ev2.Kind != "task.patched" {
		t.Fatalf("patch event = %+v, want task.patched", ev2)
	}
	if !strings.Contains(string(ev2.Data), `"auto":["packet-7","packet-8"]`) {
		t.Fatalf("patch delta = %s, want auto [packet-7,packet-8]", ev2.Data)
	}
	if !strings.Contains(string(ev2.Data), `"stage":[null,"review"]`) {
		t.Fatalf("patch delta = %s, want stage [null,review]", ev2.Data)
	}
	got, err = st.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meta) != 3 || got.Meta["auto"] != "packet-8" || got.Meta["stage"] != "review" {
		t.Fatalf("meta after upsert = %v", got.Meta)
	}

	// Removal via empty value, delta records [old, nil].
	_, ev3, err := st.PatchTask(tk.ID, map[string]any{"meta": map[string]any{"stage": ""}}, "alice")
	if err != nil {
		t.Fatalf("PatchTask remove: %v", err)
	}
	if ev3 == nil || !strings.Contains(string(ev3.Data), `"stage":["review",null]`) {
		t.Fatalf("remove delta = %+v, want stage [review,null]", ev3)
	}
	got, err = st.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Meta["stage"]; ok {
		t.Fatalf("meta after removal = %v, stage should be gone", got.Meta)
	}

	// Removing an absent key is a silent no-op (idempotent, no event).
	_, ev4, err := st.PatchTask(tk.ID, map[string]any{"meta": map[string]any{"ghost": ""}}, "alice")
	if err != nil {
		t.Fatalf("absent-key removal: %v", err)
	}
	if ev4 != nil {
		t.Fatalf("absent-key removal event = %+v, want nil", ev4)
	}
}

func TestTaskMetaValidation(t *testing.T) {
	// Key normalization shares the label rules but has its own error text.
	keyCases := []struct {
		in   string
		want string // "" = expect ErrValidation
	}{
		{"Auto", "auto"}, // uppercase normalized
		{"a b", ""},
		{"k=v", ""},
		{"", ""},
		{strings.Repeat("a", 51), ""},
		{strings.Repeat("a", 50), strings.Repeat("a", 50)}, // boundary ok
	}
	for _, c := range keyCases {
		got, err := normalizeMetaKey(c.in)
		if c.want == "" {
			if !errors.Is(err, ErrValidation) {
				t.Errorf("normalizeMetaKey(%q) err = %v, want ErrValidation", c.in, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("normalizeMetaKey(%q) = %q, %v, want %q", c.in, got, err, c.want)
		}
	}

	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatal(err)
	}
	// Oversized value at create.
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "t",
		Meta: map[string]string{"k": strings.Repeat("v", maxTitleLen+1)}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("501-byte meta value err = %v, want ErrValidation", err)
	}
	// Empty value at create (removal has no meaning there).
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "t",
		Meta: map[string]string{"k": ""}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty meta value at create err = %v, want ErrValidation", err)
	}
	// Two raw keys normalizing to the same key at create — nondeterministic
	// winner, so the whole request is rejected.
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "t",
		Meta: map[string]string{"Auto": "a", "auto": "b"}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("colliding meta keys at create err = %v, want ErrValidation", err)
	}

	tk, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "patchee"})
	if err != nil {
		t.Fatal(err)
	}
	// Non-string patch value.
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"meta": map[string]any{"k": 42.0}}, "a"); !errors.Is(err, ErrValidation) {
		t.Fatalf("non-string meta value err = %v, want ErrValidation", err)
	}
	// Non-object meta.
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"meta": "nope"}, "a"); !errors.Is(err, ErrValidation) {
		t.Fatalf("non-object meta err = %v, want ErrValidation", err)
	}
	// '=' in key via patch.
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"meta": map[string]any{"k=v": "x"}}, "a"); !errors.Is(err, ErrValidation) {
		t.Fatalf("'=' in key err = %v, want ErrValidation", err)
	}
	// Oversized value via patch.
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"meta": map[string]any{"k": strings.Repeat("v", maxTitleLen+1)}}, "a"); !errors.Is(err, ErrValidation) {
		t.Fatalf("501-byte patch value err = %v, want ErrValidation", err)
	}
	// Colliding raw keys via patch — rejected before any row is touched.
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"meta": map[string]any{"Auto": "a", "auto": "b"}}, "a"); !errors.Is(err, ErrValidation) {
		t.Fatalf("colliding meta keys via patch err = %v, want ErrValidation", err)
	}
	got, err := st.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Meta["auto"]; ok {
		t.Fatalf("meta after rejected collision = %v, want no auto key", got.Meta)
	}
}

func TestPatchTaskMetaAtomicOneEvent(t *testing.T) {
	st, t1, _, _ := setupDepFixture(t)

	// One call, two keys → exactly ONE task.patched carrying both.
	_, ev, err := st.PatchTask(t1, map[string]any{"meta": map[string]any{"auto": "x", "owner": "alice"}}, "alice")
	if err != nil {
		t.Fatalf("PatchTask: %v", err)
	}
	if ev == nil || ev.Kind != "task.patched" {
		t.Fatalf("event = %+v, want task.patched", ev)
	}
	if !strings.Contains(string(ev.Data), `"auto":[null,"x"]`) ||
		!strings.Contains(string(ev.Data), `"owner":[null,"alice"]`) {
		t.Fatalf("delta = %s, want both keys", ev.Data)
	}
	var n int
	st.db.QueryRow("SELECT COUNT(*) FROM events WHERE kind='task.patched' AND task_id=?", t1).Scan(&n)
	if n != 1 {
		t.Fatalf("task.patched events = %d, want 1", n)
	}

	// Second-key validation failure rolls back the first key too (all-or-nothing).
	st2, ta, _, _ := setupDepFixture(t)
	if _, _, err := st2.PatchTask(ta, map[string]any{"meta": map[string]any{"a1": "ok", "bad key": "y"}}, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid second key err = %v, want ErrValidation", err)
	}
	var rows int
	st2.db.QueryRow("SELECT COUNT(*) FROM task_meta WHERE task_id=?", ta).Scan(&rows)
	if rows != 0 {
		t.Fatalf("task_meta rows after failed patch = %d, want 0 (tx rollback)", rows)
	}
	st2.db.QueryRow("SELECT COUNT(*) FROM events WHERE kind='task.patched' AND task_id=?", ta).Scan(&rows)
	if rows != 0 {
		t.Fatalf("events after failed patch = %d, want 0", rows)
	}
}

func TestPatchTaskMetaNoOpNoEvent(t *testing.T) {
	st, t1, _, _ := setupDepFixture(t)
	if _, _, err := st.PatchTask(t1, map[string]any{"meta": map[string]any{"auto": "x"}}, "alice"); err != nil {
		t.Fatal(err)
	}
	// Same key, same value — idempotent success, no event.
	_, ev, err := st.PatchTask(t1, map[string]any{"meta": map[string]any{"auto": "x"}}, "alice")
	if err != nil {
		t.Fatalf("no-op patch: %v", err)
	}
	if ev != nil {
		t.Fatalf("no-op patch event = %+v, want nil", ev)
	}
}

func TestMetaOnlyPatchDoesNotBumpUpdatedAt(t *testing.T) {
	st, t1, _, _ := setupDepFixture(t)
	before, err := st.GetTask(t1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PatchTask(t1, map[string]any{"meta": map[string]any{"auto": "x"}}, "alice"); err != nil {
		t.Fatalf("meta patch: %v", err)
	}
	if _, _, err := st.PatchTask(t1, map[string]any{"meta": map[string]any{"auto": ""}}, "alice"); err != nil {
		t.Fatalf("meta removal: %v", err)
	}
	after, err := st.GetTask(t1)
	if err != nil {
		t.Fatal(err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("updated_at changed %q → %q; meta edits must not refresh a stale claim", before.UpdatedAt, after.UpdatedAt)
	}

	// A mixed patch (field + meta) still bumps. Sleep past the millisecond
	// resolution of strftime('%f') so the bump is observable.
	time.Sleep(10 * time.Millisecond)
	if _, _, err := st.PatchTask(t1, map[string]any{"priority": 1, "meta": map[string]any{"auto": "y"}}, "alice"); err != nil {
		t.Fatalf("mixed patch: %v", err)
	}
	mixed, err := st.GetTask(t1)
	if err != nil {
		t.Fatal(err)
	}
	if mixed.UpdatedAt == before.UpdatedAt {
		t.Fatal("mixed field+meta patch did not bump updated_at")
	}
}

// metaTaskMk creates a task carrying meta for the filter/next tests.
func metaTaskMk(t *testing.T, st *Store, project, title string, priority int, meta map[string]string) int64 {
	t.Helper()
	tk, _, err := st.CreateTask(CreateTaskInput{Project: project, Title: title, Priority: priority, Meta: meta})
	if err != nil {
		t.Fatalf("CreateTask %s: %v", title, err)
	}
	return tk.ID
}

func TestNextTaskMetaFilter(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("wproj", "", "work"); err != nil {
		t.Fatal(err)
	}

	// An ordinary task outranks every carrier — the meta scope must skip it.
	nextTaskMk(t, st, "web", "urgent plain", 0, "")
	auto := map[string]string{"auto": "1"}
	older := metaTaskMk(t, st, "web", "carrier p2 older", 2, auto)
	metaTaskMk(t, st, "web", "carrier p2 newer", 2, auto)
	urgent := metaTaskMk(t, st, "web", "carrier p1", 1, auto)
	// A blocked carrier (open prereq) is never picked, even at p0.
	prereq := nextTaskMk(t, st, "web", "prereq", 3, "")
	blocked := metaTaskMk(t, st, "web", "carrier p0 blocked", 0, auto)
	if _, err := st.AddDep(blocked, prereq, "alice"); err != nil {
		t.Fatal(err)
	}

	// Priority first among carriers…
	tk, _, err := st.NextTask(NextFilter{MetaKey: "auto"}, "agent-a")
	if err != nil {
		t.Fatalf("NextTask(meta auto): %v", err)
	}
	if tk.ID != urgent {
		t.Fatalf("picked #%d (%s), want carrier p1 #%d", tk.ID, tk.Title, urgent)
	}
	// …then FIFO within a priority.
	tk2, _, err := st.NextTask(NextFilter{MetaKey: "auto"}, "agent-b")
	if err != nil {
		t.Fatal(err)
	}
	if tk2.ID != older {
		t.Fatalf("picked #%d, want FIFO carrier #%d", tk2.ID, older)
	}

	// Composes with Category: the only carrier in "work" wins; "web"'s don't leak in.
	wTask := metaTaskMk(t, st, "wproj", "work carrier", 3, auto)
	tk3, _, err := st.NextTask(NextFilter{Category: "work", MetaKey: "auto"}, "agent-c")
	if err != nil {
		t.Fatal(err)
	}
	if tk3.ID != wTask {
		t.Fatalf("category+meta picked #%d, want #%d", tk3.ID, wTask)
	}

	// Drain the remaining unblocked carrier…
	if tk4, _, err := st.NextTask(NextFilter{MetaKey: "auto"}, "agent-d"); err != nil || tk4.Title != "carrier p2 newer" {
		t.Fatalf("drain pick = %+v, %v; want carrier p2 newer", tk4, err)
	}
	// …then only the blocked carrier is left → ErrNotFound (the plain ready
	// task never counts).
	if _, _, err := st.NextTask(NextFilter{MetaKey: "auto"}, "agent-d"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exhausted carriers err = %v, want ErrNotFound", err)
	}
	// Bad key → ErrValidation.
	if _, _, err := st.NextTask(NextFilter{MetaKey: "no spaces"}, "agent-e"); !errors.Is(err, ErrValidation) {
		t.Fatalf("bad meta key err = %v, want ErrValidation", err)
	}
}

func TestNextTaskMetaRaceDistinctWinners(t *testing.T) {
	// N callers race for M<N carrier tasks while ordinary ready tasks abound:
	// exactly M succeed, each with a DISTINCT carrier; the rest miss.
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatal(err)
	}
	const n, m = 4, 2
	for i := 0; i < n; i++ {
		nextTaskMk(t, st, "web", fmt.Sprintf("plain %d", i), 0, "") // decoys, better priority
	}
	carriers := map[int64]bool{}
	for i := 0; i < m; i++ {
		carriers[metaTaskMk(t, st, "web", fmt.Sprintf("carrier %d", i), 2, map[string]string{"auto": "1"})] = true
	}

	type result struct {
		task *Task
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tk, _, err := st.NextTask(NextFilter{MetaKey: "auto"}, fmt.Sprintf("agent-%d", i))
			results[i] = result{task: tk, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	winners, misses := 0, 0
	seen := map[int64]bool{}
	for i, r := range results {
		switch {
		case r.err == nil:
			winners++
			if seen[r.task.ID] {
				t.Fatalf("carrier #%d claimed twice", r.task.ID)
			}
			seen[r.task.ID] = true
			if !carriers[r.task.ID] {
				t.Fatalf("agent-%d won #%d (%s), which does not carry the key", i, r.task.ID, r.task.Title)
			}
		case errors.Is(r.err, ErrNotFound):
			misses++
		default:
			t.Fatalf("agent-%d: unexpected error %v", i, r.err)
		}
	}
	if winners != m || misses != n-m {
		t.Fatalf("winners=%d misses=%d, want %d and %d", winners, misses, m, n-m)
	}
}

func TestListTasksMetaKeyFilter(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("web", "Web", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("wproj", "", "work"); err != nil {
		t.Fatal(err)
	}

	// Presence, not value: two tasks, same key, different values — both match.
	a := metaTaskMk(t, st, "web", "a", 2, map[string]string{"auto": "one"})
	b := metaTaskMk(t, st, "web", "b", 2, map[string]string{"auto": "two"})
	plain := nextTaskMk(t, st, "web", "plain", 2, "")
	w := metaTaskMk(t, st, "wproj", "w", 2, map[string]string{"auto": "three"})

	got, err := st.ListTasks(TaskFilter{MetaKey: "auto"})
	if err != nil {
		t.Fatalf("ListTasks meta: %v", err)
	}
	ids := map[int64]bool{}
	for _, tk := range got {
		ids[tk.ID] = true
	}
	if len(got) != 3 || !ids[a] || !ids[b] || !ids[w] || ids[plain] {
		t.Fatalf("MetaKey auto = %v, want {a,b,w}", ids)
	}

	// Composes with Category.
	got, err = st.ListTasks(TaskFilter{MetaKey: "auto", Category: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != w {
		t.Fatalf("MetaKey+Category = %+v, want only w", got)
	}

	// Composes with Ready (block one carrier behind an open prereq).
	if _, err := st.AddDep(a, plain, "alice"); err != nil {
		t.Fatal(err)
	}
	got, err = st.ListTasks(TaskFilter{MetaKey: "auto", Ready: true, Project: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != b {
		t.Fatalf("MetaKey+Ready = %+v, want only b", got)
	}

	// Composes with Status.
	if _, _, err := st.PatchTask(b, map[string]any{"status": "done"}, "alice"); err != nil {
		t.Fatal(err)
	}
	got, err = st.ListTasks(TaskFilter{MetaKey: "auto", Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != b {
		t.Fatalf("MetaKey+Status = %+v, want only b", got)
	}

	// Filter input is normalized; invalid keys → ErrValidation.
	if got, err = st.ListTasks(TaskFilter{MetaKey: "AUTO"}); err != nil || len(got) != 3 {
		t.Fatalf("MetaKey AUTO = %d tasks, %v; want 3 (normalized)", len(got), err)
	}
	if _, err := st.ListTasks(TaskFilter{MetaKey: "no spaces"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid meta key err = %v, want ErrValidation", err)
	}
}

func TestListTasksReturnsMeta(t *testing.T) {
	st, t1, t2, _ := setupDepFixture(t)
	// Values with ',' and '=' must survive the stitch (the reason meta is NOT
	// fetched via GROUP_CONCAT like labels).
	gnarly := "a=b,c=d, trailing"
	if _, _, err := st.PatchTask(t1, map[string]any{"meta": map[string]any{"auto": gnarly, "owner": "alice"}}, "a"); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListTasks(TaskFilter{Project: "web"})
	if err != nil {
		t.Fatal(err)
	}
	var seen1, seen2 bool
	for _, tk := range got {
		switch tk.ID {
		case t1:
			seen1 = true
			if len(tk.Meta) != 2 || tk.Meta["auto"] != gnarly || tk.Meta["owner"] != "alice" {
				t.Fatalf("t1 meta = %v, want auto=%q owner=alice", tk.Meta, gnarly)
			}
		case t2:
			seen2 = true
			if len(tk.Meta) != 0 {
				t.Fatalf("t2 meta = %v, want empty", tk.Meta)
			}
		}
	}
	if !seen1 || !seen2 {
		t.Fatalf("list missing fixture tasks (seen1=%v seen2=%v)", seen1, seen2)
	}
}

func TestDeleteTaskCascadesMeta(t *testing.T) {
	st, t1, _, _ := setupDepFixture(t)
	if _, _, err := st.PatchTask(t1, map[string]any{"meta": map[string]any{"auto": "x"}}, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteTask(t1, "alice"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	var count int
	st.db.QueryRow("SELECT COUNT(*) FROM task_meta WHERE task_id=?", t1).Scan(&count)
	if count != 0 {
		t.Fatalf("task_meta rows survived task deletion, count=%d", count)
	}
}

func TestTaskMetaTableExistsOnReopenedDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first OpenStore: %v", err)
	}
	// Simulate a pre-Phase-P DB: drop the table, then reopen.
	if _, err := st.db.Exec("DROP TABLE task_meta"); err != nil {
		t.Fatalf("drop task_meta: %v", err)
	}
	st.Close()

	// Reopen — task_meta must come back (CREATE TABLE IF NOT EXISTS path),
	// with no migration step and no version bump.
	st2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("second OpenStore: %v", err)
	}
	defer st2.Close()

	var count int
	if err := st2.db.QueryRow("SELECT COUNT(*) FROM task_meta").Scan(&count); err != nil {
		t.Fatalf("task_meta table missing after reopen: %v", err)
	}
	v, err := readSchemaVersion(st2.db)
	if err != nil {
		t.Fatal(err)
	}
	if v != currentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d (meta needs no migration)", v, currentSchemaVersion)
	}
}

// ===================== Phase Q: scope plumbing =====================

func TestTaskScopeAndProjectCategory(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	if _, _, err := st.CreateProject("wproj", "W", "work"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tk, _, err := st.CreateTask(CreateTaskInput{Project: "wproj", Title: "scoped", Actor: "agent-a"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	cat, proj, createdBy, err := st.taskScope(tk.ID)
	if err != nil {
		t.Fatalf("taskScope: %v", err)
	}
	if cat != "work" || proj != "wproj" || createdBy != "agent-a" {
		t.Fatalf("taskScope = %q/%q/%q, want work/wproj/agent-a", cat, proj, createdBy)
	}
	if _, _, _, err := st.taskScope(99999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("taskScope(missing) err = %v, want ErrNotFound", err)
	}

	// CreateTask without an actor records the actorOr default.
	anon, _, err := st.CreateTask(CreateTaskInput{Project: "wproj", Title: "anon"})
	if err != nil {
		t.Fatalf("CreateTask anon: %v", err)
	}
	if _, _, createdBy, _ = st.taskScope(anon.ID); createdBy != "anon" {
		t.Fatalf("created_by without actor = %q, want anon", createdBy)
	}
	// GetTask surfaces created_by.
	got, err := st.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.CreatedBy != "agent-a" {
		t.Fatalf("GetTask CreatedBy = %q, want agent-a", got.CreatedBy)
	}

	if cat, err := st.projectCategory("wproj"); err != nil || cat != "work" {
		t.Fatalf("projectCategory(wproj) = %q, %v; want work", cat, err)
	}
	if _, err := st.projectCategory("nosuch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("projectCategory(nosuch) err = %v, want ErrNotFound", err)
	}
}

// Scoped extension of TestNextTaskRaceDistinctWinners: N workers all asking
// for {Category: personal, MetaKey: auto} must win DISTINCT tasks that are
// both in-category and meta-carrying, never an out-of-scope or keyless decoy
// — the scope rides inside the atomic pick+claim.
func TestNextTaskRaceScopedCategoryMeta(t *testing.T) {
	st := openTestStore(t)
	for _, c := range []string{"personal", "work"} {
		if _, _, err := st.CreateCategory(c, ""); err != nil {
			t.Fatalf("CreateCategory %s: %v", c, err)
		}
	}
	if _, _, err := st.CreateProject("pproj", "P", "personal"); err != nil {
		t.Fatalf("CreateProject pproj: %v", err)
	}
	if _, _, err := st.CreateProject("wproj", "W", "work"); err != nil {
		t.Fatalf("CreateProject wproj: %v", err)
	}

	const n = 4
	// Decoys that would all win on priority if scope/meta leaked out of the
	// candidate predicate: out-of-scope carriers and in-scope keyless tasks.
	for i := 0; i < n; i++ {
		if _, _, err := st.CreateTask(CreateTaskInput{Project: "wproj", Title: fmt.Sprintf("work carrier %d", i),
			Priority: 0, Meta: map[string]string{"auto": "x"}}); err != nil {
			t.Fatalf("CreateTask work carrier: %v", err)
		}
		nextTaskMk(t, st, "pproj", fmt.Sprintf("personal keyless %d", i), 0, "")
	}
	carriers := map[int64]bool{}
	for i := 0; i < n+2; i++ {
		tk, _, err := st.CreateTask(CreateTaskInput{Project: "pproj", Title: fmt.Sprintf("personal carrier %d", i),
			Priority: 2, Meta: map[string]string{"auto": "x"}})
		if err != nil {
			t.Fatalf("CreateTask personal carrier: %v", err)
		}
		carriers[tk.ID] = true
	}

	type result struct {
		task *Task
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tk, _, err := st.NextTask(NextFilter{Category: "personal", MetaKey: "auto"}, fmt.Sprintf("agent-%d", i))
			results[i] = result{task: tk, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[int64]bool{}
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("agent-%d: %v", i, r.err)
		}
		if !carriers[r.task.ID] {
			t.Fatalf("agent-%d won #%d (%s) — outside the scoped carrier set", i, r.task.ID, r.task.Title)
		}
		if seen[r.task.ID] {
			t.Fatalf("task #%d claimed twice", r.task.ID)
		}
		seen[r.task.ID] = true
	}
}

// TestListCategoriesCounts covers the stats folded into ListCategoriesWithStats:
// task counts summed across a category's non-archived projects, and the distinct
// non-human agents active within the window.
func TestListCategoriesCounts(t *testing.T) {
	st := openTestStore(t)

	if _, _, err := st.CreateCategory("work", "Work"); err != nil {
		t.Fatalf("CreateCategory work: %v", err)
	}
	// Two projects under "work"; one will be archived. mustCreateProject lands in
	// "general", so create explicitly under the named category here.
	for _, slug := range []string{"alpha", "beta"} {
		if _, _, err := st.CreateProject(slug, slug, "work"); err != nil {
			t.Fatalf("CreateProject %s: %v", slug, err)
		}
	}

	// alpha: 2 todo, 1 doing, 1 done. beta (to be archived): 3 todo.
	// Actor "human" on setup so the only non-human actors in the active-agents
	// query are the ones the assertions deliberately introduce below.
	mk := func(project, title, status string) int64 {
		tk, _, err := st.CreateTask(CreateTaskInput{Project: project, Title: title, Actor: "human"})
		if err != nil {
			t.Fatalf("CreateTask %s/%s: %v", project, title, err)
		}
		if status != "todo" {
			if _, _, err := st.PatchTask(tk.ID, map[string]any{"status": status}, "human"); err != nil {
				t.Fatalf("PatchTask status %s: %v", status, err)
			}
		}
		return tk.ID
	}
	mk("alpha", "a1", "todo")
	mk("alpha", "a2", "todo")
	mk("alpha", "a3", "doing")
	mk("alpha", "a4", "done")
	mk("beta", "b1", "todo")
	mk("beta", "b2", "todo")
	mk("beta", "b3", "todo")

	// Archive beta — its tasks must drop out of the work category's counts.
	if _, _, err := st.ArchiveProject("beta", "human"); err != nil {
		t.Fatalf("ArchiveProject beta: %v", err)
	}

	cats, err := st.ListCategoriesWithStats(false, 30*time.Minute)
	if err != nil {
		t.Fatalf("ListCategoriesWithStats: %v", err)
	}
	var work *CategoryStat
	for i := range cats {
		if cats[i].Slug == "work" {
			work = &cats[i]
		}
	}
	if work == nil {
		t.Fatalf("work category not found in %+v", cats)
	}
	// Only alpha contributes (beta archived): 2 todo, 1 doing, 0 blocked, 1 done.
	want := map[string]int{"todo": 2, "doing": 1, "blocked": 0, "done": 1}
	for k, v := range want {
		if work.Counts[k] != v {
			t.Fatalf("work counts[%s] = %d, want %d (counts=%+v)", k, work.Counts[k], v, work.Counts)
		}
	}

	// Active agents: a comment by a non-human agent within the window counts;
	// a comment whose event is backdated past the window does not; human never.
	freshTask := mk("alpha", "fresh", "todo")
	if _, _, err := st.AddComment(freshTask, "robo-fresh", "still here"); err != nil {
		t.Fatalf("AddComment robo-fresh: %v", err)
	}
	if _, _, err := st.AddComment(freshTask, "human", "human comment"); err != nil {
		t.Fatalf("AddComment human: %v", err)
	}
	// robo-old's only event is backdated outside the window.
	oldEv, _, err := st.AddComment(freshTask, "robo-old", "long ago")
	if err != nil {
		t.Fatalf("AddComment robo-old: %v", err)
	}
	_ = oldEv
	if _, err := st.db.Exec(
		"UPDATE events SET created_at=? WHERE actor='robo-old' AND task_id=?",
		"2020-01-01T00:00:00.000Z", freshTask); err != nil {
		t.Fatalf("backdate robo-old event: %v", err)
	}

	cats, err = st.ListCategoriesWithStats(false, 30*time.Minute)
	if err != nil {
		t.Fatalf("ListCategoriesWithStats (2): %v", err)
	}
	work = nil
	for i := range cats {
		if cats[i].Slug == "work" {
			work = &cats[i]
		}
	}
	if work == nil {
		t.Fatalf("work category not found (2)")
	}
	if len(work.ActiveAgents) != 1 || work.ActiveAgents[0] != "robo-fresh" {
		t.Fatalf("active_agents = %v, want [robo-fresh] (human excluded, robo-old aged out)", work.ActiveAgents)
	}
}
