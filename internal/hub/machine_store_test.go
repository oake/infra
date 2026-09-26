package hub

import (
	"testing"
	"time"
)

func TestUnchangedBeaconRefreshAndStagingBoundary(t *testing.T) {
	store := testStore(t)
	s, now, _ := fixture(t)
	id := "example/primary/offline"
	if err := store.Update(t.Context(), func(st *State) error { *st = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	report := s.Hosts[id].Observation
	at := now.Add(time.Minute)
	if err := store.Observe(t.Context(), report, at); err != nil {
		t.Fatal(err)
	}
	state, err := store.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Hosts[id].LastSeen.Equal(at) || len(state.Observations) != len(s.Observations) {
		t.Fatal("heartbeat changed history or lost receipt time")
	}
	// The first post-stage beacon matters even when all paths equal the prior report.
	if err := store.Update(t.Context(), func(st *State) error {
		h := st.Hosts[id]
		h.Staged = h.Desired
		h.StagedAt = at.Add(time.Second)
		st.Hosts[id] = h
		st.Reconcile(h.StagedAt)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(t.Context(), report, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	state, err = store.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Hosts[id].StagedCurrent {
		t.Fatal("unchanged old profile incorrectly confirmed staged target")
	}
}
