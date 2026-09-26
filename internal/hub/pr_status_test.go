package hub

import (
	"github.com/oake/infra/internal/api"
	"strings"
	"testing"
	"time"
)

func TestPRBuildbotResults(t *testing.T) {
	st := NewState()
	repo, rev := "a/b", strings.Repeat("a", 40)
	st.Repositories[repo] = Repository{ID: repo}
	st.PullRequests["pr"] = PullRequest{Repository: repo, Head: rev, State: "open", Checks: "success", FailedChecks: []CheckFailure{{Name: "stale failure"}}}
	apply := func(e api.BuildEvent) {
		t.Helper()
		e.Repository, e.Revision = repo, rev
		if err := st.ApplyEvent(e, e.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		st.RefreshPullRequests()
	}
	assert := func(want string, failures int) {
		t.Helper()
		p := st.PullRequests["pr"]
		if p.Checks != want || len(p.FailedChecks) != failures {
			t.Fatalf("got %+v", p)
		}
	}
	st.RefreshPullRequests()
	assert("pending", 0)
	path, other := testPath("check"), testPath("non-host-check")
	apply(api.BuildEvent{ID: "eval", Kind: "evaluation", Status: "success", Outputs: map[string]string{"checks.linux.host": path, "checks.linux.format": other}})
	assert("pending", 0)
	apply(api.BuildEvent{ID: "build1", Kind: "build", Status: "success", Artifact: path})
	assert("pending", 0)
	apply(api.BuildEvent{ID: "build2", Kind: "build", Status: "failed", Artifact: other, Detail: "format failed", LogURL: "https://buildbot.example/log"})
	assert("failure", 1)
	p := st.PullRequests["pr"]
	if p.FailedChecks[0].Name != "checks.linux.format" || p.FailedChecks[0].URL != "https://buildbot.example/log" {
		t.Fatal(p)
	}
	apply(api.BuildEvent{ID: "retry", Kind: "build", Status: "success", Artifact: other})
	assert("success", 0)
	p = st.PullRequests["pr"]
	p.Head = strings.Repeat("b", 40)
	st.PullRequests["pr"] = p
	st.RefreshPullRequests()
	assert("pending", 0)
	apply(api.BuildEvent{ID: "cache", Kind: "build", Status: "failed", Artifact: path})
	assert("pending", 0) // old head's failure does not affect the new PR revision.
}

func TestMissingEvaluationOutputsStayPending(t *testing.T) {
	st := NewState()
	repo, rev := "a/b", strings.Repeat("a", 40)
	c := Commit{Repository: repo, Revision: rev, Evaluation: "success"}
	st.Commits[api.ID(repo, rev)] = c
	status, _ := st.BuildStatus(repo, rev)
	if status != "pending" {
		t.Fatal(status)
	}
	c.Evaluation = "failed"
	c.Detail = "evaluation exploded"
	c.LogURL = "https://buildbot.example/eval"
	st.Commits[api.ID(repo, rev)] = c
	status, failures := st.BuildStatus(repo, rev)
	if status != "failure" || len(failures) != 1 || failures[0].Detail != c.Detail || failures[0].URL != c.LogURL {
		t.Fatal(status, failures)
	}
}
