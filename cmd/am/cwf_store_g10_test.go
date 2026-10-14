package main

import (
	"fmt"
	"sync"
	"testing"
)

// cwfStoreNewProject creates project slug (name = slug) in the default
// category, failing the test on error. Task-level counterpart: cwfStoreNewTask.
func cwfStoreNewProject(t *testing.T, st *Store, slug string) {
	t.Helper()
	if _, _, err := st.CreateProject(slug, "", ""); err != nil {
		t.Fatalf("CreateProject(%s): %v", slug, err)
	}
}

// cwfStoreWebTasks returns a fresh store holding project "web" (default
// category) and three todo tasks, "Task 1".."Task 3", with no deps, labels or
// meta. Dependency and meta tests both start from it.
func cwfStoreWebTasks(t *testing.T) (*Store, int64, int64, int64) {
	t.Helper()
	st := openTestStore(t)
	cwfStoreNewProject(t, st, "web")
	t1 := cwfStoreNewTask(t, st, "web", "Task 1")
	t2 := cwfStoreNewTask(t, st, "web", "Task 2")
	t3 := cwfStoreNewTask(t, st, "web", "Task 3")
	return st, t1.ID, t2.ID, t3.ID
}

// cwfStoreNextResult is one racer's NextTask outcome.
type cwfStoreNextResult struct {
	task *Task
	err  error
}

// cwfStoreRaceNext releases n goroutines at once, each calling
// st.NextTask(f, "agent-<i>"), and returns their outcomes indexed by i.
func cwfStoreRaceNext(t *testing.T, st *Store, n int, f NextFilter) []cwfStoreNextResult {
	t.Helper()
	results := make([]cwfStoreNextResult, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tk, _, err := st.NextTask(f, fmt.Sprintf("agent-%d", i))
			results[i] = cwfStoreNextResult{task: tk, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	return results
}
