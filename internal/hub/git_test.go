package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oake/infra/internal/api"
)

type gitFixture struct {
	t     *testing.T
	dir   string
	count int
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	f := &gitFixture{t: t, dir: t.TempDir()}
	f.git("init", "-b", "main")
	f.git("config", "user.name", "Infra test")
	f.git("config", "user.email", "infra@example.test")
	f.git("config", "commit.gpgsign", "false")
	return f
}
func (f *gitFixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.dir}, args...)...)
	date := time.Date(2026, 1, 1, 0, 0, f.count, 0, time.UTC).Format(time.RFC3339)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, raw)
	}
	return strings.TrimSpace(string(raw))
}
func (f *gitFixture) commit(title, lockRev string) string {
	f.t.Helper()
	f.count++
	lock := fmt.Sprintf(`{"root":"root","nodes":{"root":{"inputs":{"nixpkgs":"nixpkgs"}},"nixpkgs":{"locked":{"type":"github","owner":"NixOS","repo":"nixpkgs","rev":%q,"lastModified":1700000000}}}}`, lockRev)
	if err := os.WriteFile(filepath.Join(f.dir, "flake.lock"), []byte(lock), 0600); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", "flake.lock")
	f.git("commit", "-m", title)
	return f.git("rev-parse", "HEAD")
}
func (f *gitFixture) mirror(root string) *GitRepos {
	return &GitRepos{Root: root, remote: func(string) string { return f.dir }}
}
func TestGitMirrorOwnsHistoryMetadataAndGenericLockComparisons(t *testing.T) {
	f := newGitFixture(t)
	base := f.commit("Initial configuration", "old")
	f.git("checkout", "-b", "feature/tune")
	feature := f.commit("Tune the host", "new")
	f.git("checkout", "main")
	head := f.commit("Main advanced", "main")
	g := f.mirror(t.TempDir())
	h, err := g.Sync(t.Context(), "a/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.Main != head || len(h.MainHistory) != 2 || h.MainHistory[1] != base {
		t.Fatal(h)
	}
	if h.Commits[feature].Branch != "feature/tune" || h.Commits[feature].Title != "Tune the host" || h.Commits[feature].Created.IsZero() {
		t.Fatal(h.Commits[feature])
	}
	path := filepath.Join(g.Root, "repos", "a", "b.git")
	if _, err = os.Stat(filepath.Join(path, "HEAD")); err != nil {
		t.Fatal(err)
	}
	pair, err := g.pair(t.Context(), "a/b", head, feature, true)
	if err != nil {
		t.Fatal(err)
	}
	if pair.Base.Revision != base || pair.Head.Revision != feature || len(pair.Inputs) != 1 || pair.Inputs[0].Before != "old" || pair.Inputs[0].After != "new" {
		t.Fatal(pair)
	}
	exact, err := g.pair(t.Context(), "a/b", head, feature, false)
	if err != nil {
		t.Fatal(err)
	}
	if exact.Base.Revision != head || exact.Inputs[0].Before != "main" {
		t.Fatal("generic comparison used merge base", exact)
	}
	// Cache survives a hub restart and does not depend on the checkout remaining online.
	restarted := f.mirror(g.Root)
	cached, err := restarted.pair(t.Context(), "a/b", head, feature, false)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(exact)
	b, _ := json.Marshal(cached)
	if string(a) != string(b) {
		t.Fatal("cached pair changed")
	}
	f.git("reset", "--hard", base)
	replacement := f.commit("Force-pushed main", "replacement")
	h, err = g.Sync(t.Context(), "a/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.Main != replacement || strings.Contains(strings.Join(h.MainHistory, ","), head) {
		t.Fatal("stale ancestry after force push")
	}
	if _, err = g.Sync(t.Context(), "../escape", nil); err == nil {
		t.Fatal("invalid repository accepted")
	}
}
func TestEvaluationRetainedWhileGitUnavailableThenReconciled(t *testing.T) {
	f := newGitFixture(t)
	rev := f.commit("Host from Git", "one")
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	if err := os.MkdirAll(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	server := &Server{Store: store, Root: root}
	server.EnableGit("")
	server.Git.remote = func(string) string { return filepath.Join(root, "offline") }
	repo := "a/b"
	path := testPath("host")
	event := api.BuildEvent{ID: "eval", Repository: repo, Revision: rev, Kind: "evaluation", Status: "success", InventoryComplete: true, Mappings: []api.Mapping{{Host: repo + "/host", Platform: "nixos", System: path, Activation: path}}}
	raw, _ := json.Marshal(event)
	if err := os.WriteFile(filepath.Join(inbox, "eval.event.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.ScanInbox(t.Context(), inbox); err != nil {
		t.Fatal(err)
	}
	if err := server.SyncRepositories(t.Context()); err == nil {
		t.Fatal("offline repository ignored")
	}
	st, err := store.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Commits[api.ID(repo, rev)].Evaluation != "success" || len(st.Repositories) != 1 {
		t.Fatal("lost Buildbot results")
	}
	select {
	case <-server.gitWake:
	default:
		t.Fatal("evaluation did not request Git sync")
	}
	server.Git.remote = func(string) string { return f.dir }
	if err = server.SyncRepositories(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err = store.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Repositories[repo].Main != rev || st.Commits[api.ID(repo, rev)].Title != "Host from Git" || len(st.Hosts) != 1 {
		t.Fatal("Git did not enrich and enroll", st)
	}
	// A Buildbot source event is no longer accepted.
	event.ID, event.Kind = "source", "source"
	if err = st.ApplyEvent(event, "hash", time.Now()); err == nil {
		t.Fatal("Buildbot was allowed to set Git state")
	}
	if err = server.SyncRepositories(context.Background()); err != nil {
		t.Fatal(err)
	}
}
