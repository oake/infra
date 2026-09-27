package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oake/infra/internal/api"
)

func TestInboxRestartOrderingRejectionAndSnapshots(t *testing.T) {
	store := testStore(t)
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	os.Mkdir(inbox, 0700)
	server := &Server{Store: store, Root: root}
	repo, rev := "a/b", strings.Repeat("a", 40)
	path := testPath("inbox-host")
	now := time.Now().UTC()
	if err := store.Update(t.Context(), func(st *State) error {
		st.Repositories[repo] = Repository{ID: repo}
		st.PullRequests["pr"] = PullRequest{Repository: repo, Head: rev, State: "open"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := atomicWrite(filepath.Join(inbox, name), b); err != nil {
			t.Fatal(err)
		}
	}
	scan := func() {
		t.Helper()
		if err := server.ScanInbox(t.Context(), inbox); err != nil {
			t.Fatal(err)
		}
	}
	build := api.BuildEvent{ID: "built", Repository: repo, Revision: rev, Kind: "build", Status: "success", Artifact: path, ObservedAt: now}
	write("1.event.json", build)
	snapshot := api.Snapshot{Schema: 1, Root: path, Closure: []api.SnapshotPath{{Path: path, Size: 42}}, Selected: []string{}}
	write("2.snapshot.json", snapshot)
	info, _ := os.Stat(filepath.Join(inbox, "2.snapshot.json"))
	eval := api.BuildEvent{ID: "eval", Repository: repo, Revision: rev, Kind: "evaluation", Status: "success", Outputs: map[string]string{"checks.host": path}, Mappings: []api.Mapping{{Host: repo + "/host", System: path, Activation: path}}, ObservedAt: now}
	write("3.event.json", eval)
	os.WriteFile(filepath.Join(inbox, "bad.event.json"), []byte("not json"), 0600)
	os.WriteFile(filepath.Join(inbox, ".writing-partial.event.json"), []byte("partial"), 0600)
	scan()
	server = &Server{Store: store, Root: root} // Restart with a pending inbox.
	scan()
	st, err := store.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.PullRequests["pr"].Checks != "success" || st.Artifacts[path].Snapshot == "" {
		t.Fatal("results not persisted")
	}
	stored, err := os.Stat(filepath.Join(root, "snapshots", st.Artifacts[path].Snapshot+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, stored) {
		t.Fatal("snapshot copied rather than moved")
	}
	if _, err := os.Stat(filepath.Join(inbox, "bad.event.json")); !os.IsNotExist(err) {
		t.Fatal("invalid input retained")
	}
	if _, err := os.Stat(filepath.Join(inbox, ".writing-partial.event.json")); err != nil {
		t.Fatal("temporary writer file touched")
	}
	// Crash after commit but before deletion: redeliver the same event and snapshot.
	write("1.event.json", build)
	write("2.snapshot.json", snapshot)
	write("3.event.json", eval)
	scan()
	// An older failure arriving later must not override successful retry evidence.
	older := build
	older.ID = "old"
	older.Status = "failed"
	older.ObservedAt = now.Add(-time.Hour)
	write("old.event.json", older)
	scan()
	st, _ = store.Read(t.Context())
	if st.Artifacts[path].Build != "success" {
		t.Fatal("late old failure won")
	}
	// Same event ID with different content is rejected without changing the result.
	conflict := build
	conflict.Status = "failed"
	write("conflict.event.json", conflict)
	scan()
	snapshot.Closure[0].Size = 100
	write("conflict.snapshot.json", snapshot)
	scan()
	for _, name := range []string{"conflict.event.json", "conflict.snapshot.json"} {
		if _, err := os.Stat(filepath.Join(inbox, name)); !os.IsNotExist(err) {
			t.Fatal("conflicting input retained")
		}
	}
}

func TestInboxDatabaseFailureRetainsInput(t *testing.T) {
	store := testStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "event.event.json")
	event := api.BuildEvent{ID: "eval", Repository: "a/b", Revision: strings.Repeat("a", 40), Kind: "evaluation", Status: "success"}
	data, _ := json.Marshal(event)
	os.WriteFile(path, data, 0600)
	store.DB.Close()
	server := &Server{Store: store, Root: root}
	if err := server.ScanInbox(t.Context(), root); err == nil {
		t.Fatal("database failure swallowed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("input lost", err)
	}
}

func TestAutomaticEnrollmentAndUnknownInputsAreDiscarded(t *testing.T) {
	store := testStore(t)
	root := t.TempDir()
	server := &Server{Store: store, Root: root}
	repo, rev := "new/flake", strings.Repeat("c", 40)
	path := testPath("new-host")
	now := time.Now().UTC()
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := atomicWrite(filepath.Join(root, name), b); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately sort result filenames before evaluation. The importer orders metadata first.
	write("0.event.json", api.BuildEvent{ID: "build", Repository: repo, Revision: rev, Kind: "build", Status: "success", Artifact: path})
	write("1.event.json", api.BuildEvent{ID: "ready", Repository: repo, Revision: rev, Kind: "ready", Status: "success", Artifact: path})
	write("9.event.json", api.BuildEvent{ID: "eval", Repository: repo, Revision: rev, Kind: "evaluation", Status: "success", InventoryComplete: true, Outputs: map[string]string{"checks.host": path}, Mappings: []api.Mapping{{Host: repo + "/host", Platform: "nixos", Automatic: true, System: path, Activation: path, Checks: []string{path}}}})
	if err := server.ScanInbox(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	// Git metadata can arrive after all Buildbot facts; no build is lost.
	if err := store.Update(t.Context(), func(st *State) error {
		if err := st.ApplyGit(repo, GitHistory{Main: rev, MainHistory: []string{rev}, Commits: map[string]UICommit{rev: {Repository: repo, Revision: rev, Title: "Git title", Created: now, Branch: "main"}}}, now); err != nil {
			return err
		}
		st.Reconcile(now)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Repositories) != 1 || len(state.Hosts) != 1 || len(outstanding(t, state, repo, now.Add(time.Minute))) != 0 {
		t.Fatal("metadata did not register repository/host without deploying an unknown configuration")
	}
	before := len(state.Events)
	write("unknown.event.json", api.BuildEvent{ID: "unknown", Repository: "other/repo", Revision: rev, Kind: "build", Status: "success", Artifact: path})
	orphan := testPath("orphan")
	write("unknown.snapshot.json", api.Snapshot{Schema: 1, Root: orphan, Closure: []api.SnapshotPath{{Path: orphan, Size: 1}}})
	write("unrelated.event.json", api.BuildEvent{ID: "unrelated", Repository: "other/repo", Revision: rev, Kind: "evaluation", Status: "success"})
	if err := server.ScanInbox(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unknown.event.json", "unknown.snapshot.json", "unrelated.event.json", "rejected"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("unrelated data retained", name)
		}
	}
	if err := store.Observe(t.Context(), api.Beacon{Host: "other/repo/host", Active: path}, now); err == nil {
		t.Fatal("unknown beacon enrolled host")
	}
	state, err = store.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Repositories) != 1 || len(state.Events) != before || len(state.Observations) != 0 {
		t.Fatal("unknown input created database records")
	}
}
