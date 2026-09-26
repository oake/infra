package hub

import (
	"github.com/oake/infra/internal/api"
	"testing"
)

func TestUnionSnapshotsDeduplicatesSharedPathsAndPackages(t *testing.T) {
	a, b, shared := testPath("a"), testPath("b"), testPath("shared")
	one := api.Snapshot{Schema: 1, Root: a, Closure: []api.SnapshotPath{{Path: a, Size: 10}, {Path: shared, Size: 100}}, Selected: []string{shared}}
	two := api.Snapshot{Schema: 1, Root: b, Closure: []api.SnapshotPath{{Path: b, Size: 20}, {Path: shared, Size: 100}}, Selected: []string{shared}}
	union, err := unionSnapshots([]api.Snapshot{one, two})
	if err != nil {
		t.Fatal(err)
	}
	if len(union.Closure) != 3 || len(union.Selected) != 1 {
		t.Fatalf("union double-counted shared paths: %+v", union)
	}
	var size int64
	for _, p := range union.Closure {
		size += p.Size
	}
	if size != 130 {
		t.Fatal("incorrect combined closure size")
	}
	two.Closure[1].Size = 101
	if _, err = unionSnapshots([]api.Snapshot{one, two}); err == nil {
		t.Fatal("conflicting size accepted")
	}
}
