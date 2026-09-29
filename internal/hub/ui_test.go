package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/oake/infra/internal/api"
)

func timelineFixture() (Host, []TimelineEntry) {
	h := Host{ID: "a/b/host", Repository: "a/b", Observation: api.Beacon{Active: "p18"}}
	history := []TimelineEntry{}
	for i := 19; i >= 0; i-- {
		history = append(history, TimelineEntry{Path: fmt.Sprint("p", i), Commit: UICommit{Revision: fmt.Sprintf("%040d", i), Repository: "a/b", Title: fmt.Sprint("change ", i), Created: time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)}})
	}
	return h, history
}
func TestTimelineServerPagination(t *testing.T) {
	h, history := timelineFixture()
	p, err := timelinePage(h, history, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 3 || p.Unknown || p.Comparison.Before != "p18" || p.Comparison.After != "p19" || p.NextCursor == "" {
		t.Fatalf("bad first page: %+v", p)
	}
	second, err := timelinePage(h, history, p.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 10 || second.Entries[0].Path != "p16" {
		t.Fatalf("bad next page: %+v", second)
	}
	last, err := timelinePage(h, history, second.NextCursor)
	if err != nil || len(last.Entries) != 7 || last.NextCursor != "" {
		t.Fatalf("bad final page: %+v %v", last, err)
	}
	history[0].Commit.Title = "updated"
	if _, err = timelinePage(h, history, p.NextCursor); err != errCursor {
		t.Fatal("stale cursor accepted")
	}
	if _, err = timelinePage(h, history, "invalid"); err != errBadCursor {
		t.Fatal("malformed cursor accepted")
	}
	h.Observation.Active = "unknown"
	h.ProfileSystem = "p1"
	p, err = timelinePage(h, history, "")
	if err != nil || !p.Unknown || len(p.Entries) != 6 {
		t.Fatal("unknown live window or staged pin incorrect")
	}
	seen := map[string]bool{}
	for _, e := range p.Entries {
		seen[e.Path] = true
	}
	for p.NextCursor != "" {
		p, err = timelinePage(h, history, p.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Entries {
			if seen[e.Path] {
				t.Fatal("duplicate pinned entry")
			}
			seen[e.Path] = true
		}
	}
	h.Observation.Active = "p19"
	h.ProfileSystem = ""
	p, _ = timelinePage(h, history, "")
	if p.Comparison.Before != "p18" || p.Comparison.After != "p19" {
		t.Fatal("wrong latest-live comparison")
	}
	p, _ = timelinePage(h, history[:1], "")
	if p.Comparison.Before != "" {
		t.Fatal("invented predecessor")
	}
}
func TestScopedUIEndpoints(t *testing.T) {
	store := testStore(t)
	h, history := timelineFixture()
	s := NewState()
	s.Hosts[h.ID] = h
	repo := Repository{ID: h.Repository, Main: history[0].Commit.Revision}
	for _, entry := range history {
		c := entry.Commit
		repo.MainHistory = append(repo.MainHistory, c.Revision)
		s.Commits[api.ID(c.Repository, c.Revision)] = Commit{Repository: c.Repository, Revision: c.Revision, Title: c.Title, Created: c.Created, Evaluation: "success", Mappings: []api.Mapping{{Host: h.ID, System: entry.Path}, {Host: "a/b/other", System: "private-other-host-path"}}}
	}
	for i, name := range []string{"live", "staged", "unrelated"} {
		rev := fmt.Sprintf("%040d", 100+i)
		s.Commits[api.ID(repo.ID, rev)] = Commit{Repository: repo.ID, Revision: rev, Branch: "feature/" + name, Title: name, Created: time.Date(2026, 2, i+1, 0, 0, 0, 0, time.UTC), Evaluation: "success", Mappings: []api.Mapping{{Host: h.ID, System: name}}}
	}
	h.Observation.Active = "live"
	h.ProfileSystem = "staged"
	s.Hosts[h.ID] = h
	s.Repositories[repo.ID] = repo
	if err := store.Update(t.Context(), func(dst *State) error { *dst = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: store, Dix: testDix(t)}).Handler(http.NotFoundHandler())
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w
	}
	w := get("/api/ui/state")
	var fleet map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &fleet); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"commits", "artifacts", "jobs", "observations"} {
		if _, ok := fleet[key]; ok {
			t.Fatalf("fleet leaks %s", key)
		}
	}
	if strings.Contains(w.Body.String(), "main_history") {
		t.Fatal("fleet leaks full main ancestry")
	}
	w = get("/api/ui/hosts/a/b/host/timeline")
	var page TimelinePage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "private-other-host-path") || strings.Contains(w.Body.String(), "unrelated") {
		t.Fatal("timeline leaked unrelated mappings/branch")
	}
	exceptions := 0
	for _, e := range page.Entries {
		if e.OffBranch {
			exceptions++
			if e.Path != "live" && e.Path != "staged" {
				t.Fatal("wrong branch exception")
			}
		}
	}
	if exceptions != 2 || len(page.Entries) != 3 {
		t.Fatalf("wrong initial window: %+v", page)
	}
	w = get("/api/ui/hosts/a/b/host/timeline?cursor=" + url.QueryEscape(page.NextCursor))
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 10 {
		t.Fatal("load more not bounded to ten")
	}
}

func TestCommitComparisonBatchesAndDeduplicatesComparisons(t *testing.T) {
	store := testStore(t)
	root := t.TempDir()
	s := NewState()
	repo := "a/b"
	old, next := testPath("old"), testPath("next")
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	p := PullRequest{ID: "pr", Repository: repo, Number: 123, Head: head, Base: base, State: "open", Inputs: []InputChange{}, Hosts: []string{}}
	s.PullRequests[p.ID] = p
	s.Repositories[repo] = Repository{ID: repo, Main: strings.Repeat("c", 40)}
	for _, path := range []string{old, next} {
		snapshot := api.Snapshot{Schema: 1, Root: path, Closure: []api.SnapshotPath{{Path: path, Size: 12}}, Selected: []string{}}
		id, err := writeSnapshot(root, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		a := s.Artifact(path)
		a.Snapshot = id
		s.Artifacts[path] = a
	}
	for _, rev := range []string{base, head} {
		path := old
		if rev == head {
			path = next
		}
		c := Commit{Repository: repo, Revision: rev, Evaluation: "success"}
		for _, name := range []string{"one", "two"} {
			id := repo + "/" + name
			s.Hosts[id] = Host{ID: id, Repository: repo, Name: name, Observation: api.Beacon{Active: old}}
			c.Mappings = append(c.Mappings, api.Mapping{Host: id, System: path})
		}
		s.Commits[api.ID(repo, rev)] = c
	}
	if err := store.Update(t.Context(), func(dst *State) error { *dst = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: store, Root: root, Dix: testDix(t)}
	handler := server.Handler(http.NotFoundHandler())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/ui/compare/a/b/"+base+"/"+head, nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var page ComparisonDetail
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Hosts) != 2 || len(page.Comparisons) != 2 || page.Repository != repo {
		t.Fatalf("not batched/deduplicated: %+v", page)
	}
	for _, h := range page.Hosts {
		if page.Comparisons[h.Pair].Status != "different" {
			t.Fatal("missing inline comparisons")
		}
	}
	if strings.Contains(w.Body.String(), `"report"`) {
		t.Fatal("raw report leaked into response")
	}
	if page.Head == nil || page.Head.Revision != head || page.Base == nil || page.Base.Revision != base {
		t.Fatal("missing summary commits")
	}
	if strings.Contains(w.Body.String(), `"fleet"`) || strings.Contains(w.Body.String(), `"live"`) {
		t.Fatal("running comparison returned")
	}
	if page.GitHubURL != "https://github.com/a/b/pull/123" {
		t.Fatal("PR not detected", page.GitHubURL)
	}
	reverse, err := server.CompareCommits(t.Context(), repo, head, base)
	if err != nil {
		t.Fatal(err)
	}
	if reverse.Base.Revision != head || reverse.Head.Revision != base || reverse.GitHubURL != "https://github.com/a/b/compare/"+head+".."+base {
		t.Fatalf("reverse comparison changed endpoints: %+v", reverse)
	}
	if err = store.Update(t.Context(), func(st *State) error { delete(st.PullRequests, "pr"); return nil }); err != nil {
		t.Fatal(err)
	}
	plain, err := server.CompareCommits(t.Context(), repo, base, head)
	if err != nil {
		t.Fatal(err)
	}
	plain.GitHubURL = page.GitHubURL
	withPR, _ := json.Marshal(page)
	withoutPR, _ := json.Marshal(plain)
	if string(withPR) != string(withoutPR) {
		t.Fatal("PR detection changed comparison beyond GitHub link")
	}
	for _, url := range []string{"/api/ui/compare/a/b/invalid/" + head, "/api/ui/compare/a/b/" + base + "/invalid"} {
		bad := httptest.NewRecorder()
		handler.ServeHTTP(bad, httptest.NewRequest("GET", url, nil))
		if bad.Code != 400 {
			t.Fatal("invalid revision accepted")
		}
	}
}

func TestForcePushConfigurationIdentityAndVisibility(t *testing.T) {
	store := testStore(t)
	state := NewState()
	repo, host := "a/b", "a/b/host"
	p1, p2, p3, p4 := testPath("same"), testPath("staged"), testPath("latest"), testPath("discarded")
	revisions := []string{}
	for i, path := range []string{p1, p1, p2, p3, p4} {
		rev := fmt.Sprintf("%040d", i+1)
		revisions = append(revisions, rev)
		key := api.ID(repo, rev)
		state.Commits[key] = Commit{ID: key, Repository: repo, Revision: rev, Title: fmt.Sprint("commit ", i+1), Created: time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC), Branch: "main", Mappings: []api.Mapping{{Host: host, System: path}}}
	}
	state.Repositories[repo] = Repository{ID: repo}
	history := GitHistory{Main: revisions[3], MainHistory: []string{revisions[3], revisions[1]}, Commits: map[string]UICommit{}}
	for _, rev := range history.MainHistory {
		c := state.Commits[api.ID(repo, rev)]
		history.Commits[rev] = UICommit{Repository: repo, Revision: rev, Title: c.Title, Created: c.Created, Branch: "main"}
	}
	if err := state.ApplyGit(repo, history, time.Now()); err != nil {
		t.Fatal(err)
	}
	if state.Commits[api.ID(repo, revisions[2])].Branch != "" {
		t.Fatal("removed commit retained main label")
	}
	state.Hosts[host] = Host{ID: host, Repository: repo, Observation: api.Beacon{Active: p1}, ProfileSystem: p2, Desired: p3}
	save := func() {
		t.Helper()
		if err := store.Update(t.Context(), func(dst *State) error { *dst = *state; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	save()
	fleet, err := store.Fleet(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.HostTimeline(t.Context(), host, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 3 {
		t.Fatalf("wrong visible configurations: %+v", page.Entries)
	}
	for _, entry := range page.Entries {
		switch entry.Path {
		case p1:
			if entry.Commit.Revision != revisions[1] || entry.OffBranch {
				t.Fatal("configuration did not prefer current main provenance", entry)
			}
			if fleet.Hosts[host].Configurations[p1] != entry.Commit {
				t.Fatal("homepage and timeline disagree")
			}
		case p2:
			if !entry.OffBranch {
				t.Fatal("staged configuration no longer on main lacks warning")
			}
		case p3:
			if entry.Commit.Revision != revisions[3] {
				t.Fatal("changed configuration lost new commit")
			}
		default:
			t.Fatal("unobserved force-pushed configuration leaked")
		}
	}
	h := state.Hosts[host]
	h.Observation.Active = p3
	h.ProfileSystem = ""
	state.Hosts[host] = h
	save()
	page, err = store.HostTimeline(t.Context(), host, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("obsolete staged configuration retained: %+v", page.Entries)
	}
	for _, entry := range page.Entries {
		if entry.Path == p2 || entry.Path == p4 {
			t.Fatal("non-main configuration survived without Live/staged pin")
		}
	}
}
