package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/oake/infra/internal/api"
)

type Entry struct {
	Job    api.Job     `json:"job"`
	Phase  string      `json:"phase"`
	Result *api.Result `json:"result,omitempty"`
}
type Execute func(context.Context, api.Job) api.Result
type Runner struct {
	Client     *api.Client
	Repository string
	Path       string
	Execute    Execute
	mu         sync.Mutex
	Entries    map[string]Entry
	lock       *os.File
}

func Open(client *api.Client, repository, path string, execute Execute) (*Runner, error) {
	if !api.Repository.MatchString(repository) {
		return nil, errors.New("repository must be owner/repo")
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	lock, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, errors.New("another runner is using this queue")
	}
	r := &Runner{Client: client, Repository: repository, Path: path, Execute: execute, Entries: map[string]Entry{}, lock: lock}
	b, e := os.ReadFile(path)
	if e == nil {
		e = json.Unmarshal(b, &r.Entries)
	}
	if e != nil && !os.IsNotExist(e) {
		r.Close()
		return nil, e
	}
	for id, v := range r.Entries {
		if v.Job.Repository != repository {
			r.Close()
			return nil, errors.New("queue contains jobs for another repository; use a separate queue file")
		}
		if v.Phase == "running" {
			v.Phase = "result"
			v.Result = &api.Result{Outcome: "ambiguous", Detail: "Runner restarted during deployment. Inspect the host before retrying.", Finished: time.Now().UTC()}
			r.Entries[id] = v
		}
	}
	if e = r.save(); e != nil {
		r.Close()
		return nil, e
	}
	return r, nil
}
func (r *Runner) Close() {
	if r.lock != nil {
		_ = syscall.Flock(int(r.lock.Fd()), syscall.LOCK_UN)
		_ = r.lock.Close()
	}
}
func (r *Runner) save() error {
	b, e := json.MarshalIndent(r.Entries, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(r.Path), ".queue-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), r.Path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(r.Path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (r *Runner) jobs(ctx context.Context) ([]api.Job, error) {
	var jobs []api.Job
	e := r.Client.Do(ctx, "GET", "/api/deployer/jobs?repository="+url.QueryEscape(r.Repository), nil, &jobs)
	for _, j := range jobs {
		if j.Repository != r.Repository {
			return nil, errors.New("hub returned a job for another repository")
		}
	}
	return jobs, e
}

// deliver acknowledges one durable result without waiting for other hosts.
func (r *Runner) deliver(ctx context.Context, id string, result api.Result) error {
	if err := r.Client.Do(ctx, "POST", "/api/deployer/jobs/"+url.PathEscape(id)+"?repository="+url.QueryEscape(r.Repository), result, nil); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.Entries[id]
	entry.Phase = "done"
	r.Entries[id] = entry
	return r.save()
}

// Sync resends durable results before refreshing the complete outstanding batch.
func (r *Runner) Sync(ctx context.Context) error {
	r.mu.Lock()
	pending := map[string]api.Result{}
	for id, e := range r.Entries {
		if e.Phase == "result" {
			pending[id] = *e.Result
		}
	}
	r.mu.Unlock()
	for id, result := range pending {
		if e := r.deliver(ctx, id, result); e != nil {
			return e
		}
	}
	jobs, e := r.jobs(ctx)
	if e != nil {
		return e
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	present := map[string]bool{}
	for _, j := range jobs {
		present[j.ID] = true
		if _, ok := r.Entries[j.ID]; !ok {
			r.Entries[j.ID] = Entry{Job: j, Phase: "queued"}
		}
	}
	for id, v := range r.Entries {
		if !present[id] && v.Phase != "running" && v.Phase != "result" {
			delete(r.Entries, id)
		}
	}
	return r.save()
}
func (r *Runner) Cycle(ctx context.Context, concurrency int) error {
	if concurrency < 1 {
		return errors.New("concurrency must be positive")
	}
	if e := r.Sync(ctx); e != nil {
		return e
	}
	r.mu.Lock()
	ids := []string{}
	for id, e := range r.Entries {
		if e.Phase == "queued" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return r.Entries[ids[i]].Job.Created.Before(r.Entries[ids[j]].Job.Created) })
	r.mu.Unlock()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	busy := map[string]bool{}
	errs := make(chan error, len(ids))
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			r.mu.Lock()
			entry := r.Entries[id]
			r.mu.Unlock()
			mu.Lock()
			if busy[entry.Job.Host] {
				mu.Unlock()
				return
			}
			busy[entry.Job.Host] = true
			mu.Unlock()
			defer func() { mu.Lock(); delete(busy, entry.Job.Host); mu.Unlock() }()
			// Immediately before execution, ask for the current read-only outstanding set.
			current, e := r.jobs(ctx)
			if e != nil {
				errs <- e
				return
			}
			found := false
			for _, j := range current {
				if j.ID == id {
					found = true
					break
				}
			}
			r.mu.Lock()
			entry = r.Entries[id]
			if !found {
				delete(r.Entries, id)
				e = r.save()
				r.mu.Unlock()
				if e != nil {
					errs <- e
				}
				return
			}
			entry.Phase = "running"
			r.Entries[id] = entry
			e = r.save()
			r.mu.Unlock()
			if e != nil {
				errs <- e
				return
			}
			result := r.Execute(ctx, entry.Job)
			result.Finished = time.Now().UTC()
			r.mu.Lock()
			entry.Phase = "result"
			entry.Result = &result
			r.Entries[id] = entry
			e = r.save()
			r.mu.Unlock()
			if e != nil {
				errs <- fmt.Errorf("persist execution result: %w", e)
				return
			}
			if e := r.deliver(ctx, id, result); e != nil {
				errs <- e
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			return e
		}
	}
	return r.Sync(ctx)
}
func (r *Runner) Run(ctx context.Context, interval time.Duration, concurrency int) error {
	if interval <= 0 {
		return errors.New("poll interval must be positive")
	}
	for {
		if e := r.Cycle(ctx, concurrency); e != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("runner cycle", "error", e)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}
