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

func TestBatchDedupSerializationAndDurableResults(t *testing.T) {
	var mu sync.Mutex
	jobs := map[string]api.Job{}
	for i, host := range []string{"owner/repo/a", "owner/repo/b", "owner/repo/a"} {
		id := string(rune('a' + i))
		jobs[id] = api.Job{ID: id, Repository: "owner/repo", Host: host, Created: time.Now().Add(time.Duration(i) * time.Second)}
	}
	failResults := true
	submitted := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer edge-token" {
			t.Error("Traefik token missing")
		}
		if r.URL.Query().Get("repository") != "owner/repo" {
			t.Error("repository missing")
		}
		if r.Method == "GET" {
			batch := []api.Job{}
			for _, j := range jobs {
				batch = append(batch, j)
			}
			json.NewEncoder(w).Encode(batch)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/deployer/jobs/")
		submitted[id]++
		if failResults {
			http.Error(w, "hub offline", 503)
			return
		}
		delete(jobs, id)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	c, e := api.NewClient(server.URL, "edge-token")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "queue.json")
	running := map[string]int{}
	executed := map[string]int{}
	maxTotal := 0
	total := 0
	var execMu sync.Mutex
	execute := func(ctx context.Context, j api.Job) api.Result {
		execMu.Lock()
		running[j.Host]++
		executed[j.ID]++
		total++
		if total > maxTotal {
			maxTotal = total
		}
		if running[j.Host] > 1 {
			t.Error("concurrent jobs on same host")
		}
		execMu.Unlock()
		time.Sleep(20 * time.Millisecond)
		execMu.Lock()
		total--
		running[j.Host]--
		execMu.Unlock()
		return api.Result{Outcome: "staged", Detail: "test"}
	}
	runner, e := Open(c, "owner/repo", path, execute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Open(c, "owner/repo", path, execute); e == nil {
		t.Fatal("second process queue lock accepted")
	}
	if e = runner.Cycle(t.Context(), 2); e == nil {
		t.Fatal("result outage should be reported")
	}
	runner.Close()
	runner, e = Open(c, "owner/repo", path, execute)
	if e != nil {
		t.Fatal(e)
	}
	defer runner.Close()
	mu.Lock()
	failResults = false
	mu.Unlock()
	for range 3 {
		if e = runner.Cycle(t.Context(), 2); e != nil {
			t.Fatal(e)
		}
	}
	for id, n := range executed {
		if n != 1 {
			t.Fatalf("job %s executed %d times", id, n)
		}
	}
	if len(executed) != 3 {
		t.Fatalf("batch incomplete: %v", executed)
	}
	if maxTotal < 2 {
		t.Fatal("different hosts did not execute concurrently")
	}
	if len(runner.Entries) != 0 {
		t.Fatal("acknowledged jobs retained in queue", runner.Entries)
	}
}
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
	if e = r.Cycle(t.Context(), 1); e != nil {
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
	if err = r.Cycle(t.Context(), 1); err == nil {
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

func TestResultSentWhileOtherHostStillRunning(t *testing.T) {
	fastReported := make(chan struct{})
	releaseSlow := make(chan struct{})
	var mu sync.Mutex
	jobs := map[string]api.Job{"fast": {ID: "fast", Host: "a/b/fast", Repository: "a/b"}, "slow": {ID: "slow", Host: "a/b/slow", Repository: "a/b"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "GET" {
			batch := []api.Job{}
			for _, j := range jobs {
				batch = append(batch, j)
			}
			json.NewEncoder(w).Encode(batch)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/deployer/jobs/")
		delete(jobs, id)
		if id == "fast" {
			close(fastReported)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client, _ := api.NewClient(server.URL, "")
	runner, err := Open(client, "a/b", filepath.Join(t.TempDir(), "queue"), func(ctx context.Context, j api.Job) api.Result {
		if j.ID == "slow" {
			<-releaseSlow
		}
		return api.Result{Outcome: "staged"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	done := make(chan error, 1)
	go func() { done <- runner.Cycle(t.Context(), 2) }()
	select {
	case <-fastReported:
	case <-time.After(3 * time.Second):
		close(releaseSlow)
		<-done
		t.Fatal("fast result waited for slow host")
	}
	close(releaseSlow)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
