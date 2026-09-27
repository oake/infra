package beacon

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/oake/infra/internal/api"
)

func TestReportAcknowledgement(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state", "receipt.json")
	b := api.Beacon{Host: "owner/repo/host", Active: "live", Profile: "staged"}
	calls := 0
	failed := false
	send := func() error {
		calls++
		if failed {
			return errors.New("offline")
		}
		return nil
	}
	report := func() error { return Report(state, b, send) }
	if err := report(); err != nil {
		t.Fatal(err)
	}
	if err := report(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unchanged observation resent")
	}
	b.Profile = "new-stage"
	failed = true
	for range 2 {
		if report() == nil {
			t.Fatal("failed delivery acknowledged")
		}
	}
	// Retry the latest observation, not an obsolete failed report.
	b.Profile = "newest-stage"
	failed = false
	if err := report(); err != nil {
		t.Fatal(err)
	}
	if err := report(); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("calls = %d", calls)
	}
}
