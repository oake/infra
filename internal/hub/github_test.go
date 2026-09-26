package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oake/infra/internal/api"
)

func TestGitHubPagination(t *testing.T) {
	page2 := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("unexpected request")
		}
		switch {
		case strings.Contains(r.URL.Path, "/pulls"):
			if r.URL.Query().Get("page") == "1" {
				json.NewEncoder(w).Encode(make([]githubPR, 100))
			} else {
				page2 = true
				fmt.Fprint(w, `[{"number":101}]`)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	g := &GitHub{BaseURL: server.URL, Token: "test"}
	prs, err := g.list(t.Context(), "a/b")
	if err != nil || len(prs) != 101 || !page2 {
		t.Fatalf("pagination: %v %d", err, len(prs))
	}
}
func TestPullRequestSyncPreservesFailuresAndReplacesHeads(t *testing.T) {
	store := testStore(t)
	f := newGitFixture(t)
	base := f.commit("Initial", "old")
	f.git("checkout", "-b", "feature")
	head := f.commit("Update input", "new")
	repo := "a/b"
	failed, closed := false, false
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/repos/a/b/pulls" {
			t.Errorf("GitHub used for non-PR data: %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if failed {
			w.WriteHeader(403)
			return
		}
		if closed {
			fmt.Fprint(w, "[]")
			return
		}
		fmt.Fprintf(w, `[{"number":1,"title":"PR title","head":{"sha":%q},"base":{"sha":%q}}]`, head, base)
	}))
	defer server.Close()
	if err := store.Update(t.Context(), func(st *State) error { st.Repositories[repo] = Repository{ID: repo, Main: base}; return nil }); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	hub := &Server{Store: store, GitHub: &GitHub{BaseURL: server.URL}, Git: f.mirror(root)}
	poll := func() *State {
		t.Helper()
		if err := hub.PollPullRequests(t.Context()); err != nil {
			t.Fatal(err)
		}
		st, err := store.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	id := api.ID(repo, "1")
	st := poll()
	p := st.PullRequests[id]
	if p.Base != base || p.Head != head || len(p.Inputs) != 1 || p.Inputs[0].After != "new" || p.HeadCommit.Title != "Update input" {
		t.Fatalf("bad PR: %+v", p)
	}
	hub = &Server{Store: store, GitHub: &GitHub{BaseURL: server.URL}, Git: f.mirror(root)}
	if !poll().PullRequests[id].InputsCached || requests != 2 {
		t.Fatal("restart lost cached PR data")
	}
	if err := store.Update(t.Context(), func(st *State) error {
		st.Commits[api.ID(repo, head)] = Commit{Repository: repo, Revision: head, Evaluation: "failed", Detail: "Buildbot failed"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if p := poll().PullRequests[id]; p.Checks != "failure" || len(p.FailedChecks) != 1 {
		t.Fatal("lost Buildbot check", p)
	}
	head = f.commit("Next input", "next")
	if p := poll().PullRequests[id]; p.Head != head || p.Checks != "pending" || p.Inputs[0].After != "next" {
		t.Fatal("stale head", p)
	}
	f.git("checkout", "main")
	originalBase := base
	base = f.commit("Base advanced", "main")
	if p := poll().PullRequests[id]; p.BaseHead != base || p.Base != originalBase || !p.InputsCached {
		t.Fatal("base change not resolved through Git", p)
	}
	failed = true
	if err := hub.PollPullRequests(t.Context()); err == nil {
		t.Fatal("failure swallowed")
	}
	st, _ = store.Read(t.Context())
	if len(st.PullRequests) != 1 || st.Repositories[repo].PRError == "" {
		t.Fatal("failed poll erased PR")
	}
	failed = false
	closed = true
	st = poll()
	if len(st.PullRequests) != 0 || st.Repositories[repo].PRError != "" {
		t.Fatal("closed PR retained")
	}
	if st.Repositories[repo].Main != originalBase {
		t.Fatal("PR API changed authoritative main")
	}
}
func TestLockChangesIncludeAddedAndRemovedInputs(t *testing.T) {
	changes := lockChanges(map[string]json.RawMessage{"removed": json.RawMessage(`{"rev":"old"}`)}, map[string]json.RawMessage{"added": json.RawMessage(`{"rev":"new"}`)})
	if len(changes) != 2 || changes[0].Name != "added" || changes[1].After != "" {
		t.Fatalf("%+v", changes)
	}
}

func TestTopLevelInputsAndCompareDates(t *testing.T) {
	before := []byte(`{"root":"root","nodes":{"root":{"inputs":{"nixpkgs":"nixpkgs_3","alias":["nixpkgs"]}},"nixpkgs_3":{"locked":{"type":"github","owner":"NixOS","repo":"nixpkgs","rev":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","lastModified":1700000000}},"transitive":{"locked":{"rev":"ignored"}}}}`)
	after := []byte(strings.ReplaceAll(strings.ReplaceAll(string(before), strings.Repeat("a", 40), strings.Repeat("b", 40)), "1700000000", "1700100000"))
	a, err := topLevelInputs(before)
	if err != nil {
		t.Fatal(err)
	}
	b, err := topLevelInputs(after)
	if err != nil {
		t.Fatal(err)
	}
	changes := lockChanges(a, b)
	if len(changes) != 2 || changes[0].Name != "alias" || changes[1].Name != "nixpkgs" {
		t.Fatalf("wrong top-level inputs: %+v", changes)
	}
	c := changes[1]
	if c.BeforeDate != "2023-11-14T22:13:20Z" || c.AfterDate == "" || c.CompareURL != "https://github.com/NixOS/nixpkgs/compare/"+strings.Repeat("a", 40)+"..."+strings.Repeat("b", 40) {
		t.Fatalf("incorrect dates/compare: %+v", c)
	}
	if len(lockChanges(a, a)) != 0 {
		t.Fatal("unchanged inputs included")
	}
	_, err = topLevelInputs([]byte(`{"root":"root","nodes":{"root":{"inputs":{"loop":["loop"]}}}}`))
	if err == nil {
		t.Fatal("cyclic follows accepted")
	}
}
