package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oake/infra/internal/api"
)

var testPrior = api.ID("prior")[:40]
var testMain = api.ID("main")[:40]
var testHead = api.ID("head")[:40]

func testPath(name string) string {
	return "/nix/store/" + strings.ReplaceAll(api.ID(name)[:32], "e", "f") + "-" + name
}

// Exercise scheduling with explicit host states, without any application seed data.
func fixture(t *testing.T) (*State, time.Time, string) {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	s := NewState()
	for _, repo := range []string{"example/primary", "example/secondary"} {
		s.Repositories[repo] = Repository{ID: repo, Main: testMain, MainHistory: []string{testMain, testPrior}}
		for i, rev := range []string{testPrior, testMain, testHead} {
			id := api.ID(repo, rev)
			s.Commits[id] = Commit{ID: id, Repository: repo, Revision: rev, Title: rev, Evaluation: "success", Created: now.Add(time.Duration(i-2) * time.Hour), Outputs: map[string]string{}}
		}
	}
	for _, tc := range []struct{ repo, name, mode string }{
		{"example/primary", "unchanged", "unchanged"},
		{"example/primary", "manual", "manual"},
		{"example/primary", "upload-pending", "upload-pending"},
		{"example/primary", "offline", "offline"},
		{"example/primary", "failed", "failed"},
		{"example/secondary", "unchanged", "unchanged"},
		{"example/secondary", "current", "current"},
		{"example/secondary", "staged", "staged"},
		{"example/secondary", "queued-after-staged", "queued-after-staged"},
	} {
		id := tc.repo + "/" + tc.name
		h := Host{ID: id, Repository: tc.repo, Name: tc.name, Node: tc.name, Platform: "nixos", Automatic: tc.mode != "manual"}
		paths := []string{testPath(tc.name + "-old"), testPath(tc.name + "-new"), testPath(tc.name + "-next")}
		if tc.mode == "unchanged" {
			paths[1] = paths[0]
		}
		for i, rev := range []string{testPrior, testMain, testHead} {
			p := paths[i]
			activation := testPath(tc.name + "-activation-" + rev)
			a := s.Artifact(p)
			a.Build = "success"
			a.ReadyStatus = "success"
			a.ReadyAt = now
			snap, err := writeSnapshot(root, api.Snapshot{Schema: 1, Root: p, Closure: []api.SnapshotPath{{Path: p, Size: 1}}, Selected: []string{}})
			if err != nil {
				t.Fatal(err)
			}
			a.Snapshot = snap
			s.Artifacts[p] = a
			w := s.Artifact(activation)
			w.Build = "success"
			w.ReadyStatus = "success"
			w.ReadyAt = now
			s.Artifacts[activation] = w
			c := s.Commits[api.ID(tc.repo, rev)]
			c.Mappings = append(c.Mappings, api.Mapping{Host: id, System: p, Activation: activation})
			c.Outputs["hosts."+tc.name] = p
			s.Commits[c.ID] = c
		}
		active, profile := paths[0], paths[0]
		if tc.mode == "current" {
			active = paths[1]
			profile = active
		}
		if tc.mode == "manual" {
			active = testPath("manual-unknown")
			profile = active
		}
		if tc.mode == "staged" {
			profile = paths[1]
		}
		if tc.mode == "queued-after-staged" {
			active = testPath("older-live")
			oldID := api.ID(tc.repo, "older")
			oldRevision := api.ID("older")[:40]
			s.Commits[oldID] = Commit{ID: oldID, Repository: tc.repo, Revision: oldRevision, Mappings: []api.Mapping{{Host: id, System: active}}}
			repo := s.Repositories[tc.repo]
			repo.MainHistory = append(repo.MainHistory, oldRevision)
			s.Repositories[tc.repo] = repo
			profile = paths[0]
		}
		if tc.mode == "queued-after-staged" {
			h.Staged = profile
			h.StagedRevision = testPrior
			h.StagedAt = now.Add(-time.Hour)
		}
		s.Hosts[id] = h
		if err := s.Observe(api.Beacon{Host: id, Active: active, Profile: profile, Booted: active}, now.Add(-30*time.Second)); err != nil {
			t.Fatal(err)
		}
		if tc.mode == "upload-pending" {
			a := s.Artifacts[paths[1]]
			a.ReadyStatus = "failed"
			s.Artifacts[a.Path] = a
		}
	}
	s.Reconcile(now)
	for id, j := range s.Jobs {
		outcome := ""
		if j.Host == "example/primary/offline" {
			outcome = "unreachable"
		}
		if j.Host == "example/primary/failed" {
			outcome = "failed"
		}
		if outcome != "" {
			if err := s.Result(id, j.Repository, api.Result{Outcome: outcome}, now.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.Reconcile(now)
	return s, now, root
}

// A subprocess stub tests the real dix execution/parser/cache path.
func testDix(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dix")
	script := `#!/bin/sh
[ "$1" = diff-snapshots ] && [ -f "$2" ] && [ -f "$3" ] && [ "$4" = --output ] && [ "$5" = json ] || exit 1
printf '%s\n' '{"size_old":1,"size_new":2,"diffs":[{"name":"package","versions":[{"kind":"changed","old":{"name":"1"},"new":{"name":"2"}}]}]}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func outstanding(t *testing.T, state *State, repo string, now time.Time) []api.Job {
	t.Helper()
	store := testStore(t)
	if err := store.Update(t.Context(), func(dst *State) error { *dst = *state; return nil }); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.Outstanding(t.Context(), repo, now)
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

func firstConfiguration(t *testing.T, state *State, host, path string) *Commit {
	t.Helper()
	store := testStore(t)
	if err := store.Update(t.Context(), func(dst *State) error { *dst = *state; return nil }); err != nil {
		t.Fatal(err)
	}
	fleet, err := store.Fleet(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, ok := fleet.Hosts[host].Configurations[path]
	if !ok {
		return nil
	}
	for _, commit := range state.Commits {
		if commit.Repository == c.Repository && commit.Revision == c.Revision {
			return &commit
		}
	}
	t.Fatal("fleet returned unknown commit")
	return nil
}
