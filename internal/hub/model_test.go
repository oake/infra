package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/oake/infra/internal/api"
)

func TestObservationsAndArtifactReuse(t *testing.T) {
	s, now, root := fixture(t)
	h := s.Hosts["example/secondary/unchanged"]
	count := len(s.Observations)
	if e := s.Observe(h.Observation, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if len(s.Observations) != count {
		t.Fatal("heartbeat created duplicate transition")
	}
	if !s.Hosts[h.ID].LastSeen.Equal(now.Add(time.Minute)) {
		t.Fatal("freshness not updated")
	}
	path := testPath("local-test")
	if e := s.Observe(api.Beacon{Host: h.ID, Active: path}, now.Add(2*time.Minute)); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.Artifacts[path]; ok {
		t.Fatal("unknown observation generated artifact")
	}
	if len(s.Observations) != count+1 {
		t.Fatal("unknown transition missing")
	}
	s.Reconcile(now.Add(2 * time.Minute))
	if firstConfiguration(t, s, h.ID, s.Hosts[h.ID].Observation.Active) != nil {
		t.Fatal("unknown marked known")
	}
	var old, new string
	for _, c := range s.Commits {
		for _, m := range c.Mappings {
			if m.Host == "example/primary/unchanged" {
				if c.Revision == testMain {
					old = m.System
				}
				if c.Revision == testPrior {
					new = m.System
				}
			}
		}
	}
	if old != new || old == "" {
		t.Fatal("unchanged mapping missing")
	}
	a := s.Artifacts[old]
	if a.Snapshot == "" {
		t.Fatal("missing reusable snapshot")
	}
	d, e := compare(t.Context(), s, root, testDix(t), old, new)
	if e != nil || d.Status != "unchanged" {
		t.Fatalf("unchanged comparison: %+v %v", d, e)
	}
}
func TestSchedulerPoliciesAndOfflineRetry(t *testing.T) {
	s, now, _ := fixture(t)
	for _, id := range []string{"example/secondary/unchanged", "example/primary/unchanged", "example/primary/manual", "example/secondary/current", "example/secondary/staged", "example/primary/upload-pending"} {
		for _, j := range s.Jobs {
			if j.Host == id && !j.Superseded && j.Result == nil {
				t.Fatalf("unexpected job for %s", id)
			}
		}
	}
	before, _ := json.Marshal(s)
	jobs := outstanding(t, s, "example/primary", now)
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("GET mutated state")
	}
	var eule api.Job
	for _, j := range jobs {
		if j.Host == "example/primary/offline" {
			eule = j
		}
	}
	if eule.ID == "" {
		t.Fatal("offline host requires retry without beacon")
	}
	s.Reconcile(now.Add(time.Second))
	if s.Hosts[eule.Host].Status != "Deployment queued" {
		t.Fatal("offline retry lost quiet pending reason")
	}
	if e := s.Result(eule.ID, "example/secondary", api.Result{Outcome: "staged"}, now); e == nil {
		t.Fatal("wrong repository accepted")
	}
	r := api.Result{Outcome: "staged", Detail: "ok"}
	if e := s.Result(eule.ID, "example/primary", r, now); e != nil {
		t.Fatal(e)
	}
	if e := s.Result(eule.ID, "example/primary", r, now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	if e := s.Result(eule.ID, "example/primary", api.Result{Outcome: "failed"}, now); e == nil {
		t.Fatal("conflicting result accepted")
	}
	s.Reconcile(now)
	h := s.Hosts[eule.Host]
	if h.Status != "Reboot to apply" || h.Observation.Active == h.Staged {
		t.Fatalf("staging confused with runtime: %+v", h)
	}
	if e := s.Observe(api.Beacon{Host: h.ID, Active: h.Staged, Booted: h.Staged, Profile: h.Staged}, now); e != nil {
		t.Fatal(e)
	}
	s.Reconcile(now)
	if s.Hosts[h.ID].Status != "Up to date" {
		t.Fatal("reboot observation not reflected")
	}
}
func TestAmbiguousRecovery(t *testing.T) {
	s, now, _ := fixture(t)
	jobs := outstanding(t, s, "example/primary", now)
	var j api.Job
	for _, v := range jobs {
		if v.Host == "example/primary/offline" {
			j = v
		}
	}
	h := s.Hosts[j.Host]
	current := j
	if e := s.Result(current.ID, "example/primary", api.Result{Outcome: "ambiguous"}, now); e != nil {
		t.Fatal(e)
	}
	repo := s.Repositories[h.Repository]
	repo.Main = testHead
	s.Repositories[repo.ID] = repo
	s.Reconcile(now.Add(time.Second))
	for _, v := range outstanding(t, s, "example/primary", now.Add(time.Second)) {
		if v.Host == h.ID {
			t.Fatal("new target bypassed unresolved interrupted deployment")
		}
	}
}
func TestObserveDesiredCancelsQueuedJob(t *testing.T) {
	s, now, _ := fixture(t)
	h := s.Hosts["example/primary/offline"]
	if e := s.Observe(api.Beacon{Host: h.ID, Active: h.Desired, Profile: h.Desired, Booted: h.Desired}, now); e != nil {
		t.Fatal(e)
	}
	s.Reconcile(now)
	for _, j := range outstanding(t, s, "example/primary", now) {
		if j.Host == h.ID {
			t.Fatal("unnecessary queued deployment after convergence")
		}
	}
}
func TestSnapshotValidationAndCanonicalReuse(t *testing.T) {
	s, _, root := fixture(t)
	h := s.Hosts["example/primary/offline"]
	d, e := compare(t.Context(), s, root, testDix(t), h.Observation.Active, h.Desired)
	if e != nil || d.Status != "different" || len(d.Packages) == 0 {
		t.Fatalf("actual to desired comparison: %+v %v", d, e)
	}
	unknown, e := compare(t.Context(), s, root, testDix(t), testPath("unknown"), h.Desired)
	if e != nil || unknown.Status != "unavailable" {
		t.Fatal("unknown path synthesized snapshot")
	}
	snap := api.Snapshot{Schema: 1, Root: h.Desired, Closure: []api.SnapshotPath{{Path: h.Desired, Size: 1}}, Selected: []string{h.Desired}}
	id, e := writeSnapshot(root, snap)
	if e != nil {
		t.Fatal(e)
	}
	id2, e := writeSnapshot(root, snap)
	if e != nil || id != id2 {
		t.Fatal("snapshot not reusable")
	}
	snap.Closure = append(snap.Closure, snap.Closure[0])
	if e = validateSnapshot(snap); e == nil {
		t.Fatal("duplicate path accepted")
	}
}
func TestBuildEventsAreIdempotentAndIndependent(t *testing.T) {
	s, now, _ := fixture(t)
	h := s.Hosts["example/primary/offline"]
	event := api.BuildEvent{ID: "event-1", Kind: "build", Repository: h.Repository, Revision: h.Revision, Artifact: h.Desired, Status: "failed", Detail: "builder disconnected"}
	b, _ := json.Marshal(event)
	hash := api.ID(string(b))
	for range 2 {
		if e := s.ApplyEvent(event, hash, now); e != nil {
			t.Fatal(e)
		}
	}
	if s.Artifacts[h.Desired].ReadyStatus != "success" {
		t.Fatal("build event erased independent cache fact")
	}
	if e := s.ApplyEvent(event, "different content", now); e == nil {
		t.Fatal("reused event ID allowed differing payload")
	}
	if s.Ready(h.Desired) {
		t.Fatal("failed build marked eligible")
	}
}
func TestProfileWrappersAndManualReplacement(t *testing.T) {
	s, now, _ := fixture(t)
	h := s.Hosts["example/primary/offline"]
	var job api.Job
	for _, j := range outstanding(t, s, h.Repository, now) {
		if j.Host == h.ID {
			job = j
		}
	}
	if e := s.Result(job.ID, h.Repository, api.Result{Outcome: "staged"}, now); e != nil {
		t.Fatal(e)
	}
	report := h.Observation
	report.Profile = job.Activation
	if e := s.Observe(report, now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	s.Reconcile(now.Add(time.Second))
	h = s.Hosts[h.ID]
	if h.ProfileSystem != job.System || !h.StagedCurrent || h.Status != "Reboot to apply" {
		t.Fatalf("wrapper not associated: %+v", h)
	}
	report.Active = testPath("manual-experiment")
	report.Profile = report.Active
	if e := s.Observe(report, now.Add(2*time.Second)); e != nil {
		t.Fatal(e)
	}
	s.Reconcile(now.Add(2 * time.Second))
	h = s.Hosts[h.ID]
	if h.StagedCurrent {
		t.Fatal("old command result incorrectly proves current staging")
	}
	if h.Status != "Paused" {
		t.Fatal("unknown configuration was not paused")
	}
	for _, queued := range outstanding(t, s, h.Repository, now.Add(2*time.Second)) {
		if queued.Host == h.ID {
			t.Fatal("unknown configuration queued automatically")
		}
	}
}
