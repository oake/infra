package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/oake/infra/internal/api"
)

// Like nix-diffs, All hosts compares unions of available matched snapshots.
// Shared store paths and selected packages count once, not once per host.
func unionSnapshots(snapshots []api.Snapshot) (api.Snapshot, error) {
	result := api.Snapshot{Schema: 1, Closure: []api.SnapshotPath{}, Selected: []string{}}
	paths := map[string]int64{}
	selected := map[string]bool{}
	for _, s := range snapshots {
		if err := validateSnapshot(s); err != nil {
			return result, err
		}
		if result.Root == "" || s.Root < result.Root {
			result.Root = s.Root
		}
		for _, p := range s.Closure {
			if old, ok := paths[p.Path]; ok && old != p.Size {
				return result, fmt.Errorf("conflicting snapshot sizes for %s", p.Path)
			}
			paths[p.Path] = p.Size
		}
		for _, p := range s.Selected {
			selected[p] = true
		}
	}
	for path, size := range paths {
		result.Closure = append(result.Closure, api.SnapshotPath{Path: path, Size: size})
	}
	for path := range selected {
		result.Selected = append(result.Selected, path)
	}
	sort.Slice(result.Closure, func(i, j int) bool { return result.Closure[i].Path < result.Closure[j].Path })
	sort.Strings(result.Selected)
	return result, validateSnapshot(result)
}
func (s *Server) combinedComparison(ctx context.Context, state *State, hosts []HostComparison, pairs map[string]ComparisonPair) Comparison {
	result := Comparison{Status: "unavailable", Packages: []PackageDiff{}}
	before, after := []api.Snapshot{}, []api.Snapshot{}
	loaded := map[string]api.Snapshot{}
	read := func(path string) (api.Snapshot, error) {
		id := state.Artifacts[path].Snapshot
		if snap, ok := loaded[id]; ok {
			return snap, nil
		}
		var snap api.Snapshot
		raw, err := os.ReadFile(filepath.Join(s.Root, "snapshots", id+".json"))
		if err != nil {
			return snap, err
		}
		if err = json.Unmarshal(raw, &snap); err != nil {
			return snap, err
		}
		loaded[id] = snap
		return snap, nil
	}
	for _, h := range hosts {
		if err := ctx.Err(); err != nil {
			result.Status = "error"
			result.Error = err.Error()
			return result
		}
		key := h.Pair
		pair := pairs[key]
		if state.Artifacts[pair.Before].Snapshot == "" || state.Artifacts[pair.After].Snapshot == "" {
			result.MissingHosts = append(result.MissingHosts, h.Name)
			continue
		}
		a, e1 := read(pair.Before)
		b, e2 := read(pair.After)
		if e1 != nil || e2 != nil {
			result.MissingHosts = append(result.MissingHosts, h.Name)
			continue
		}
		before = append(before, a)
		after = append(after, b)
	}
	if len(before) == 0 {
		return result
	}
	unions := NewState()
	keys := []string{}
	for _, snapshots := range [][]api.Snapshot{before, after} {
		snapshot, err := unionSnapshots(snapshots)
		if err != nil {
			result.Status = "error"
			result.Error = err.Error()
			return result
		}
		id, err := writeSnapshot(s.Root, snapshot)
		if err != nil {
			result.Status = "error"
			result.Error = err.Error()
			return result
		}
		// An aggregate is not a host system. Use its content identity to avoid the
		// unchanged-system shortcut when other hosts changed but its root did not.
		key := "union:" + id
		keys = append(keys, key)
		unions.Artifacts[key] = Artifact{Snapshot: id}
	}
	d, err := compare(ctx, unions, s.Root, s.Dix, keys[0], keys[1])
	if err != nil {
		d.Status = "error"
		d.Error = err.Error()
	}
	d.MissingHosts = result.MissingHosts
	return d
}
