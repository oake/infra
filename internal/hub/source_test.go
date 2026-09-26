package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/oake/infra/internal/api"
)

func TestGitInventoryOrdering(t *testing.T) {
	for _, sourceFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "source-first", false: "evaluation-first"}[sourceFirst], func(t *testing.T) {
			s := NewState()
			repo := "example/config"
			rev := strings.Repeat("a", 40)
			now := time.Now().UTC()
			s.Repositories[repo] = Repository{ID: repo}
			s.applyInventory(repo, []Host{{Name: "old", Platform: "nixos"}}, true)
			evaluation := api.BuildEvent{ID: "eval", Repository: repo, Revision: rev, Kind: "evaluation", Status: "success", ObservedAt: now, InventoryComplete: true, Mappings: []api.Mapping{{Host: repo + "/new", Platform: "nixos", Automatic: true, System: testPath("system"), Activation: testPath("activation")}}}
			history := GitHistory{Main: rev, MainHistory: []string{rev}, Commits: map[string]UICommit{rev: {Repository: repo, Revision: rev, Title: "From Git", Created: now, Branch: "main"}}}
			applyGit := func() {
				if err := s.ApplyGit(repo, history, now); err != nil {
					t.Fatal(err)
				}
			}
			if sourceFirst {
				applyGit()
			}
			if err := s.ApplyEvent(evaluation, evaluation.ID, now); err != nil {
				t.Fatal(err)
			}
			if !sourceFirst {
				applyGit()
			}
			if c := s.Commits[api.ID(repo, rev)]; c.Title != "From Git" || !c.Created.Equal(now) {
				t.Fatal("evaluation lost Git metadata")
			}
			if !s.Hosts[repo+"/old"].Removed || !s.Hosts[repo+"/new"].Automatic {
				t.Fatal("current main inventory not applied")
			}
			evaluation.ID = "pr"
			evaluation.Revision = strings.Repeat("c", 40)
			evaluation.Mappings = nil
			if err := s.ApplyEvent(evaluation, evaluation.ID, now); err != nil {
				t.Fatal(err)
			}
			if s.Hosts[repo+"/new"].Removed {
				t.Fatal("PR retired main host")
			}
		})
	}
}

func TestPartialEvaluationDoesNotBlockOtherHost(t *testing.T) {
	s, now, _ := fixture(t)
	id := "example/primary/offline"
	h := s.Hosts[id]
	c := s.Commits[api.ID(h.Repository, s.Repositories[h.Repository].Main)]
	c.Evaluation = "failed"
	c.InventoryComplete = false
	c.Errors = map[string]string{"unrelated": "evaluation failed"}
	check := testPath("independent-check")
	for i := range c.Mappings {
		if c.Mappings[i].Host == id {
			c.Mappings[i].Checks = []string{check}
		}
	}
	s.Commits[c.ID] = c
	a := s.Artifact(check)
	a.Build = "success"
	s.Artifacts[check] = a
	s.Reconcile(now)
	if s.Hosts[id].Status != "Deployment queued" {
		t.Fatal("unrelated failure blocked ready host", s.Hosts[id].Status)
	}
	a.Build = "failed"
	s.Artifacts[check] = a
	s.Reconcile(now)
	if s.Hosts[id].Status != "Processing" {
		t.Fatal("failed host check remained eligible")
	}
}

func TestPartialMainUpdatesPolicyWithoutRetiringMissingHosts(t *testing.T) {
	s := NewState()
	repo := "a/b"
	rev := strings.Repeat("a", 40)
	s.Repositories[repo] = Repository{ID: repo, Main: rev}
	s.applyInventory(repo, []Host{{Name: "one", Platform: "nixos", Automatic: true}, {Name: "two", Platform: "nixos", Automatic: true}}, true)
	c := Commit{Repository: repo, Revision: rev, Mappings: []api.Mapping{{Host: repo + "/one", Platform: "nixos", Automatic: false}}}
	if err := s.inventoryFromCommit(c); err != nil {
		t.Fatal(err)
	}
	if s.Hosts[repo+"/one"].Automatic || s.Hosts[repo+"/two"].Removed {
		t.Fatal("partial inventory ignored opt-out or retired absent host")
	}
}
