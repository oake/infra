package hub

import (
	"github.com/oake/infra/internal/api"
	"testing"
	"time"
)

func TestInventoryRemovalAndReturn(t *testing.T) {
	s := NewState()
	repo := "example/config"
	s.Repositories[repo] = Repository{ID: repo}
	hosts := []Host{{Name: "desktop", Platform: "nixos", Automatic: true}, {Name: "laptop", Platform: "darwin"}}
	if err := s.applyInventory(repo, hosts, true); err != nil {
		t.Fatal(err)
	}
	id := repo + "/desktop"
	h := s.Hosts[id]
	h.Observation.Active = testPath("observed")
	s.Hosts[id] = h
	s.Jobs["pending"] = api.Job{ID: "pending", Host: id, System: testPath("desired")}
	if err := s.applyInventory(repo, []Host{{Name: "bad/name", Platform: "nixos"}}, true); err == nil {
		t.Fatal("invalid inventory accepted")
	}
	if s.Hosts[id].Removed {
		t.Fatal("failed inventory retired hosts")
	}
	if err := s.applyInventory(repo, hosts[1:], true); err != nil {
		t.Fatal(err)
	}
	s.Reconcile(time.Now())
	if !s.Hosts[id].Removed || !s.Jobs["pending"].Superseded {
		t.Fatal("removed host retained a pending deployment")
	}
	if err := s.applyInventory(repo, hosts, true); err != nil {
		t.Fatal(err)
	}
	h = s.Hosts[id]
	if h.Removed || h.Observation.Active == "" {
		t.Fatal("returning host lost its history ")
	}
	if s.Hosts[repo+"/laptop"].Automatic {
		t.Fatal("monitor-only host became automatic")
	}
}
