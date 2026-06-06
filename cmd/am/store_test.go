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
