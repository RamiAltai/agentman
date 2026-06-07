package main

import (
	"errors"
	"sync"
	"testing"
)

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
