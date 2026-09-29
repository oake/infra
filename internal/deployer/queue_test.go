package deployer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oake/infra/internal/api"
)

func TestInterruptedJobBecomesAmbiguous(t *testing.T) {
	c, _ := api.NewClient("http://127.0.0.1:1", "")
	path := filepath.Join(t.TempDir(), "queue.json")
	r, e := Open(c, "owner/repo", path, nil)
	if e != nil {
		t.Fatal(e)
	}
	r.Entries["job"] = Entry{Job: api.Job{ID: "job", Host: "owner/repo/a", Repository: "owner/repo"}, Phase: "running"}
	if e = r.save(); e != nil {
		t.Fatal(e)
	}
	r.Close()
	r, e = Open(c, "owner/repo", path, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if r.Entries["job"].Result.Outcome != "ambiguous" {
		t.Fatal("interrupted job would blindly rerun")
	}
}
func TestSupersededBeforeStarting(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			json.NewEncoder(w).Encode([]api.Job{{ID: "job", Host: "owner/repo/a", Repository: "owner/repo"}})
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()
	c, _ := api.NewClient(server.URL, "")
	r, e := Open(c, "owner/repo", filepath.Join(t.TempDir(), "queue.json"), func(context.Context, api.Job) api.Result { t.Error("superseded job executed"); return api.Result{} })
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if e = r.Sync(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e = r.execute(t.Context(), "job"); e != nil {
		t.Fatal(e)
	}
	if _, exists := r.Entries["job"]; exists {
		t.Fatal("superseded queue entry not removed")
	}
}

func TestRejectsOtherRepositoryJobsAndJournal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]api.Job{{ID: "other", Host: "other/repo/builder", Repository: "other/repo"}})
	}))
	defer server.Close()
	c, _ := api.NewClient(server.URL, "")
	path := filepath.Join(t.TempDir(), "queue.json")
	execute := func(context.Context, api.Job) api.Result {
		t.Fatal("executed a job for another repository")
		return api.Result{}
	}
	r, err := Open(c, "owner/repo", path, execute)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Sync(t.Context()); err == nil {
		t.Fatal("cross-repository batch accepted")
	}
	if len(r.Entries) != 0 {
		t.Fatal("cross-repository job entered journal")
	}
	r.Entries["own"] = Entry{Job: api.Job{ID: "own", Repository: "owner/repo", Host: "owner/repo/builder"}, Phase: "queued"}
	if err = r.save(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if other, err := Open(c, "other/repo", path, execute); err == nil {
		other.Close()
		t.Fatal("journal reused for another repository")
	}
}

type runnerHub struct {
	mu          sync.Mutex
	jobs        map[string]api.Job
	failResults bool
	reported    chan string
}

func newRunnerHub(t *testing.T) (*runnerHub, *api.Client) {
	t.Helper()
	h := &runnerHub{jobs: map[string]api.Job{}, reported: make(chan string, 100)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer token" || r.URL.Query().Get("repository") != "a/b" {
			t.Error("missing authentication or repository")
		}
		if r.Method == "GET" {
			jobs := []api.Job{}
			for _, j := range h.jobs {
				jobs = append(jobs, j)
			}
			_ = json.NewEncoder(w).Encode(jobs)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/deployer/jobs/")
		if h.failResults {
			http.Error(w, "offline", 503)
			return
		}
		delete(h.jobs, id)
		h.reported <- id
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	c, err := api.NewClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	return h, c
}
func (h *runnerHub) add(id, host string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jobs[id] = api.Job{ID: id, Host: "a/b/" + host, Repository: "a/b", Created: time.Now()}
}
func receive(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler stalled")
		return ""
	}
}
func startRunner(t *testing.T, c *api.Client, interval time.Duration, concurrency int, execute Execute) (*Runner, func()) {
	t.Helper()
	r, err := Open(c, "a/b", filepath.Join(t.TempDir(), "queue.json"), execute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, interval, concurrency) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(3 * time.Second):
				t.Error("runner did not stop")
			}
			r.Close()
		})
	}
	t.Cleanup(stop)
	return r, stop
}

func TestRunFourSlotsAndImmediateRefill(t *testing.T) {
	h, c := newRunnerHub(t)
	gates := map[string]chan struct{}{}
	for i, id := range []string{"one", "two", "three", "four", "five"} {
		h.add(id, id)
		job := h.jobs[id]
		job.Created = time.Unix(int64(i), 0)
		h.jobs[id] = job
		gates[id] = make(chan struct{})
	}
	started := make(chan string, 10)
	_, _ = startRunner(t, c, time.Hour, 4, func(ctx context.Context, j api.Job) api.Result {
		started <- j.ID
		select {
		case <-gates[j.ID]:
		case <-ctx.Done():
		}
		return api.Result{Outcome: "staged"}
	})
	first := map[string]bool{}
	for range 4 {
		first[receive(t, started)] = true
	}
	if first["five"] {
		t.Fatalf("queue order not respected: %v", first)
	}
	select {
	case id := <-started:
		t.Fatalf("exceeded four slots: %s", id)
	case <-time.After(30 * time.Millisecond):
	}
	close(gates["one"])
	if id := receive(t, h.reported); id != "one" {
		t.Fatal(id)
	}
	if id := receive(t, started); id != "five" {
		t.Fatal("slot not refilled immediately", id)
	}
	// Other initial jobs are deliberately still running.
}

func TestRunPollsNewJobsWhileHostBusyAndSerializesHost(t *testing.T) {
	h, c := newRunnerHub(t)
	h.add("old", "same")
	release := make(chan struct{})
	started := make(chan string, 10)
	_, _ = startRunner(t, c, 10*time.Millisecond, 4, func(ctx context.Context, j api.Job) api.Result {
		started <- j.ID
		if j.ID == "old" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return api.Result{Outcome: "staged"}
	})
	if receive(t, started) != "old" {
		t.Fatal("old job did not start")
	}
	h.mu.Lock()
	delete(h.jobs, "old")
	h.mu.Unlock()
	h.add("replacement", "same")
	h.add("new-host", "other")
	if id := receive(t, started); id != "new-host" {
		t.Fatal("overlapping work on busy host", id)
	}
	if id := receive(t, h.reported); id != "new-host" {
		t.Fatal(id)
	}
	select {
	case id := <-started:
		t.Fatal("second job started on busy host", id)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if id := receive(t, started); id != "replacement" {
		t.Fatal(id)
	}
}

func TestRunDurableResultRetryWithoutReexecution(t *testing.T) {
	h, c := newRunnerHub(t)
	h.add("job", "host")
	h.failResults = true
	executed := make(chan string, 10)
	r, stop := startRunner(t, c, 10*time.Millisecond, 4, func(ctx context.Context, j api.Job) api.Result {
		executed <- j.ID
		return api.Result{Outcome: "staged"}
	})
	if _, err := Open(c, "a/b", r.Path, r.Execute); err == nil {
		t.Fatal("second runner acquired queue lock")
	}
	receive(t, executed)
	// Wait for the result to be journaled before restarting the process.
	deadline := time.After(3 * time.Second)
	for {
		r.mu.Lock()
		entry := r.Entries["job"]
		r.mu.Unlock()
		if entry.Phase == "result" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("result not persisted")
		case <-time.After(time.Millisecond):
		}
	}
	stop()
	restored, err := Open(c, "a/b", r.Path, func(context.Context, api.Job) api.Result {
		t.Error("durable result executed again")
		return api.Result{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	h.mu.Lock()
	h.failResults = false
	h.mu.Unlock()
	if err = restored.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if receive(t, h.reported) != "job" {
		t.Fatal("wrong result")
	}
	if len(restored.Entries) != 0 {
		t.Fatal("acknowledged job retained")
	}
	select {
	case <-executed:
		t.Fatal("job ran twice")
	default:
	}
}
