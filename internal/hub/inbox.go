package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oake/infra/internal/api"
)

type rejectedInput struct{ error }

func reject(err error) error { return rejectedInput{err} }

func readInboxJSON(path string, target any, limit int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return reject(errors.New("expected a bounded regular file"))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(target); err != nil {
		return reject(err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return reject(errors.New("expected one JSON value"))
	}
	return nil
}

// ScanInbox imports evaluations before their dependent files. Unrelated/invalid
// inputs are discarded; only temporary storage failures leave files for retry.
func (s *Server) ScanInbox(ctx context.Context, inbox string) error {
	s.inboxMu.Lock()
	defer s.inboxMu.Unlock()
	entries, err := os.ReadDir(inbox)
	if err != nil {
		return err
	}
	type input struct {
		name  string
		order int
	}
	inputs := []input{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || entry.IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(name, ".event.json"):
			var e api.BuildEvent
			order := 2
			if readInboxJSON(filepath.Join(inbox, name), &e, 4<<20) == nil {
				if e.Kind == "evaluation" {
					order = 0
				}
			}
			inputs = append(inputs, input{name, order})
		case strings.HasSuffix(name, ".snapshot.json"):
			inputs = append(inputs, input{name, 3})
		}
	}
	sort.SliceStable(inputs, func(i, j int) bool { return inputs[i].order < inputs[j].order })
	for _, input := range inputs {
		path := filepath.Join(inbox, input.name)
		if input.order == 3 {
			err = s.ingestSnapshot(ctx, path)
		} else {
			err = s.ingestEvent(ctx, path)
		}
		var invalid rejectedInput
		if errors.As(err, &invalid) {
			slog.Warn("discarding inbox input", "file", input.name, "reason", err)
		} else if err != nil {
			return fmt.Errorf("inbox %s: %w", input.name, err)
		}
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Server) ingestEvent(ctx context.Context, path string) error {
	var event api.BuildEvent
	if err := readInboxJSON(path, &event, 4<<20); err != nil {
		return err
	}
	if event.ID == "" || !api.Revision.MatchString(event.Revision) || !api.Repository.MatchString(event.Repository) || (event.Status != "success" && event.Status != "failed") {
		return reject(errors.New("invalid event identity, revision, repository or status"))
	}
	b, _ := json.Marshal(event)
	err := s.Store.Update(ctx, func(st *State) error {
		if event.Kind != "evaluation" {
			if _, ok := st.Repositories[event.Repository]; !ok {
				return reject(errors.New("no matching evaluation metadata"))
			}
		}
		if event.Kind == "build" || event.Kind == "ready" {
			if !api.StorePath.MatchString(event.Artifact) {
				return reject(errors.New("invalid artifact path"))
			}
			if _, ok := st.Repositories[event.Repository]; !ok {
				return reject(errors.New("no matching evaluation metadata"))
			}
			if _, ok := st.Commits[api.ID(event.Repository, event.Revision)]; !ok {
				return reject(errors.New("no matching evaluation metadata"))
			}
			if c := st.Commits[api.ID(event.Repository, event.Revision)]; c.Evaluation == "pending" {
				return reject(errors.New("no matching evaluation metadata"))
			}
			if _, ok := st.Artifacts[event.Artifact]; !ok {
				return reject(errors.New("artifact absent from evaluation"))
			}
		}
		if err := st.ApplyEvent(event, api.ID(string(b)), time.Now().UTC()); err != nil {
			return reject(err)
		}
		st.Reconcile(time.Now().UTC())
		return nil
	})
	if err == nil && event.Kind == "evaluation" {
		s.NotifyEvaluation()
	}
	return err
}

func (s *Server) ingestSnapshot(ctx context.Context, path string) error {
	var snapshot api.Snapshot
	if err := readInboxJSON(path, &snapshot, 64<<20); err != nil {
		return err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return reject(err)
	}
	sort.Slice(snapshot.Closure, func(i, j int) bool { return snapshot.Closure[i].Path < snapshot.Closure[j].Path })
	sort.Strings(snapshot.Selected)
	b, _ := json.Marshal(snapshot)
	id := api.ID(Producer, string(b))
	return s.Store.Update(ctx, func(st *State) error {
		a, ok := st.Artifacts[snapshot.Root]
		if !ok {
			return reject(errors.New("no matching evaluation metadata"))
		}
		if a.Snapshot != "" && a.Snapshot != id {
			return reject(errors.New("conflicting snapshot for immutable artifact"))
		}
		dir := filepath.Join(s.Root, "snapshots")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		// Inbox and permanent storage share a filesystem. Link then unlink after the
		// DB commit avoids copying and preserves a retryable input on DB failure.
		if err := os.Link(path, filepath.Join(dir, id+".json")); err != nil && !os.IsExist(err) {
			return err
		}
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
		a.Snapshot, a.Producer = id, Producer
		st.Artifacts[snapshot.Root] = a
		return nil
	})
}

func (s *Server) RunInbox(ctx context.Context, inbox string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if err := s.ScanInbox(ctx, inbox); err != nil && ctx.Err() == nil {
			slog.Error("inbox scan", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
