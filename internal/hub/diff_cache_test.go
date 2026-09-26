package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oake/infra/internal/api"
)

func TestDiffWorkCancellationAndRelease(t *testing.T) {
	release, err := lockDiff(context.Background(), "test-pair")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = lockDiff(ctx, "test-pair"); err != context.Canceled {
		t.Fatal("wait ignored cancellation", err)
	}
	other, err := lockDiff(context.Background(), "other-pair")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	release, err = lockDiff(context.Background(), "test-pair")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestEngineFingerprintChangesWithBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dix")
	for _, data := range []string{"first", "a different binary"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			got, err := fingerprint(path)
			if err != nil || got != api.ID(data) {
				t.Fatal(got, err)
			}
		}
	}
}
