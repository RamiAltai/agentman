package main

import (
	"errors"
	"testing"
)

// FIND-0279: pins the project side of the shared archive helper (the category
// side is TestArchiveUnarchiveCategory): event kinds, the event's project id
// (the project for project events, none for category events), idempotent
// no-event repeats and ErrNotFound.
func TestCwfStoreProjectArchiveEvents(t *testing.T) {
	st := openTestStore(t)
	p, _, err := st.CreateProject("web", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name     string
		fn       func(string, string) (*Project, *Event, error)
		kind     string // "" = idempotent, no event
		archived bool
	}{
		{"archive", st.ArchiveProject, "project.archived", true},
		{"re-archive", st.ArchiveProject, "", true},
		{"unarchive", st.UnarchiveProject, "project.unarchived", false},
		{"re-unarchive", st.UnarchiveProject, "", false},
	} {
		got, ev, err := step.fn("web", "tester")
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got.ID != p.ID || (got.ArchivedAt != "") != step.archived {
			t.Fatalf("%s: project = %+v, want id %d archived=%v", step.name, got, p.ID, step.archived)
		}
		if step.kind == "" {
			if ev != nil {
				t.Fatalf("%s: event %+v, want none", step.name, ev)
			}
			continue
		}
		if ev == nil || ev.Kind != step.kind || ev.ProjectID != p.ID || ev.Actor != "tester" {
			t.Fatalf("%s: event %+v, want %s on project %d by tester", step.name, ev, step.kind, p.ID)
		}
	}
	if _, _, err := st.UnarchiveProject("nosuch", "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unarchive unknown project err = %v, want ErrNotFound", err)
	}

	if _, _, err := st.CreateCategory("work", ""); err != nil {
		t.Fatal(err)
	}
	if _, ev, err := st.ArchiveCategory("work", "tester"); err != nil || ev == nil || ev.ProjectID != 0 {
		t.Fatalf("category archive event = %+v err=%v, want a category-level event (project 0)", ev, err)
	}
}

// FIND-0231: ListEvents (SSE replay), ListEventsBefore (paging) and
// RecentEvents (bootstrap) agree on which events exist under every filter,
// archived projects and categories included.
func TestCwfStoreEventFeedsAgree(t *testing.T) {
	st := openTestStore(t)
	for _, c := range []string{"acat", "bcat"} {
		if _, _, err := st.CreateCategory(c, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range [][2]string{{"live", "acat"}, {"shelved", "acat"}, {"other", "bcat"}} {
		if _, _, err := st.CreateProject(p[0], p[0], p[1]); err != nil {
			t.Fatal(err)
		}
		cwfStoreNewTask(t, st, p[0], "t-"+p[0])
	}
	if _, _, err := st.ArchiveProject("shelved", "tester"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ArchiveCategory("bcat", "tester"); err != nil {
		t.Fatal(err)
	}
	maxID, err := st.MaxEventID()
	if err != nil {
		t.Fatal(err)
	}
	ids := func(evs []Event) map[int64]bool {
		m := map[int64]bool{}
		for _, e := range evs {
			m[e.ID] = true
		}
		return m
	}
	for _, f := range []struct{ project, category string }{
		{"", ""}, {"", "acat"}, {"", "bcat"}, {"shelved", ""}, {"other", ""}, {"live", "acat"},
	} {
		replay, _, err := st.ListEvents(0, f.project, f.category, 500)
		if err != nil {
			t.Fatal(err)
		}
		paged, err := st.ListEventsBefore(maxID+1, f.project, f.category, 200)
		if err != nil {
			t.Fatal(err)
		}
		recent, _, err := st.RecentEvents(f.project, f.category, 200)
		if err != nil {
			t.Fatal(err)
		}
		a, b, c := ids(replay), ids(paged), ids(recent)
		if len(a) == 0 || len(a) != len(b) || len(a) != len(c) {
			t.Fatalf("%+v: replay %d, paged %d, recent %d events; want the same non-empty set", f, len(a), len(b), len(c))
		}
		for id := range a {
			if !b[id] || !c[id] {
				t.Fatalf("%+v: event %d in ListEvents but not in ListEventsBefore (%v) or RecentEvents (%v)", f, id, b[id], c[id])
			}
		}
	}
}
