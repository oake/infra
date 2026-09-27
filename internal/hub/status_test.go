package hub

import (
	"github.com/oake/infra/internal/api"
	"testing"
	"time"
)

func TestSixFleetStatuses(t *testing.T) {
	s, now, _ := fixture(t)
	checks := map[string]string{
		"example/primary/unchanged":      "Up to date",
		"example/primary/manual":         "Outdated",
		"example/primary/upload-pending": "Processing",
		"example/primary/offline":        "Deployment queued",
		"example/primary/failed":         "Deploy failed",
		"example/secondary/staged":       "Reboot to apply",
	}
	for id, want := range checks {
		if got := s.Hosts[id].Status; got != want {
			t.Errorf("%s: got %s, want %s", id, got, want)
		}
	}
	// Upload receipts remain valid; time alone does not change readiness.
	s.Reconcile(now.Add(time.Hour))
	if s.Hosts["example/primary/failed"].Status != "Deploy failed" || s.Hosts["example/primary/unchanged"].Status != "Up to date" {
		t.Fatal("time changed terminal state")
	}
	if s.Hosts["example/primary/offline"].Status != "Deployment queued" {
		t.Fatal("upload receipt expired")
	}
}

func TestFirstConfigurationIgnoresLaterUnchangedCommits(t *testing.T) {
	s, _, _ := fixture(t)
	h := s.Hosts["example/primary/unchanged"]
	first := firstConfiguration(t, s, h.ID, h.Observation.Active)
	if first == nil || first.Revision != testPrior {
		t.Fatalf("wrong introduction: %+v", first)
	}
	// An older branch commit must not replace main provenance. Once it is
	// part of current main history, its earlier date takes precedence.
	earlier := *first
	earlier.ID = "earlier"
	earlier.Revision = api.ID("earlier")[:40]
	earlier.Created = first.Created.Add(-time.Hour)
	earlier.Title = "Initial configuration"
	s.Commits[earlier.ID] = earlier
	if got := firstConfiguration(t, s, h.ID, h.Observation.Active); got.ID != first.ID {
		t.Fatal("off-main commit replaced main provenance")
	}
	repo := s.Repositories[h.Repository]
	repo.MainHistory = append(repo.MainHistory, earlier.Revision)
	s.Repositories[h.Repository] = repo
	if got := firstConfiguration(t, s, h.ID, h.Observation.Active); got.ID != earlier.ID {
		t.Fatal("late historical mapping did not update origin")
	}
	if firstConfiguration(t, s, "other/repo/fruity", h.Observation.Active) != nil {
		t.Fatal("configuration association crossed host identity")
	}
}

func TestOfflineJobReplacedByNewConfiguration(t *testing.T) {
	s, now, _ := fixture(t)
	id := "example/primary/offline"
	var previous api.Job
	for _, j := range outstanding(t, s, "example/primary", now) {
		if j.Host == id {
			previous = j
		}
	}
	if previous.ID == "" {
		t.Fatal("missing initial offline retry")
	}
	repo := s.Repositories[previous.Repository]
	repo.Main = testHead
	s.Repositories[repo.ID] = repo
	s.Reconcile(now.Add(time.Second))
	if !s.Jobs[previous.ID].Superseded {
		t.Fatal("old offline retry remained live")
	}
	var replacement api.Job
	for _, j := range outstanding(t, s, repo.ID, now.Add(time.Second)) {
		if j.Host == id {
			if replacement.ID != "" {
				t.Fatal("multiple outstanding targets")
			}
			replacement = j
		}
	}
	if replacement.ID == "" || replacement.System == previous.System || replacement.Revision != testHead {
		t.Fatal("latest target not queued")
	}
}

func TestLiveStagedAndQueued(t *testing.T) {
	s, now, _ := fixture(t)
	h := s.Hosts["example/secondary/queued-after-staged"]
	if h.Observation.Active == h.Staged || h.Staged == h.Desired || h.Observation.Active == h.Desired || !h.StagedCurrent || h.Status != "Deployment queued" {
		t.Fatalf("expected three distinct configurations: %+v", h)
	}
	for _, j := range outstanding(t, s, h.Repository, now) {
		if j.Host == h.ID && j.System == h.Desired {
			return
		}
	}
	t.Fatal("new target is not queued")
}
