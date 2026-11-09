package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/oake/infra/internal/api"
)

// legacySnapshot is the format of the dix fork used before dix 3.0
// (producer "dix-69f91d6-schema1"). dix 3 cannot read it.
type legacySnapshot struct {
	Schema  int    `json:"schema_version"`
	Root    string `json:"root"`
	Closure []struct {
		Path string `json:"path"`
		Size int64  `json:"nar_size"`
	} `json:"closure"`
	Selected []string `json:"selected"`
}

// parseSnapshot reads a dix 3 snapshot, or converts a legacy one and reports
// that it did. The result is not validated.
func parseSnapshot(raw []byte) (api.Snapshot, bool, error) {
	var current api.Snapshot
	currentErr := decodeStrict(raw, &current)
	if currentErr == nil {
		return current, false, nil
	}
	var old legacySnapshot
	if decodeStrict(raw, &old) != nil || old.Schema != 1 {
		return api.Snapshot{}, false, currentErr
	}
	s := api.Snapshot{Version: 1, Root: old.Root, Closure: make([]api.SnapshotPath, 0, len(old.Closure)), Selected: old.Selected}
	for _, p := range old.Closure {
		s.Closure = append(s.Closure, api.SnapshotPath{Path: p.Path, Size: p.Size})
	}
	if s.Selected == nil {
		s.Selected = []string{}
	}
	return s, true, nil
}

// MigrateSnapshots rewrites stored snapshots from older producers in the
// current format and points their artifacts at the new IDs. It is idempotent,
// so it runs on every start. Unreadable snapshots are logged and kept as-is.
// Old files are removed only after the new IDs are committed.
func (s *Server) MigrateSnapshots(ctx context.Context) error {
	dir := filepath.Join(s.Root, "snapshots")
	var replaced []string
	migrated, skipped := 0, 0
	err := s.Store.Update(ctx, func(st *State) error {
		replaced, migrated, skipped = nil, 0, 0
		ids := map[string]string{}
		for key, a := range st.Artifacts {
			if a.Snapshot == "" || a.Producer == Producer {
				continue
			}
			id, ok := ids[a.Snapshot]
			if !ok {
				raw, err := os.ReadFile(filepath.Join(dir, a.Snapshot+".json"))
				if errors.Is(err, os.ErrNotExist) {
					slog.Warn("snapshot migration: file missing", "artifact", key, "snapshot", a.Snapshot)
					skipped++
					continue
				}
				if err != nil {
					return err
				}
				snapshot, _, err := parseSnapshot(raw)
				if err == nil {
					id, err = writeSnapshot(s.Root, snapshot)
				}
				if err != nil {
					slog.Warn("snapshot migration: unreadable snapshot kept", "artifact", key, "snapshot", a.Snapshot, "error", err)
					skipped++
					continue
				}
				ids[a.Snapshot] = id
				if id != a.Snapshot {
					replaced = append(replaced, a.Snapshot)
				}
			}
			a.Snapshot, a.Producer = id, Producer
			st.Artifacts[key] = a
			migrated++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("migrate snapshots: %w", err)
	}
	for _, id := range replaced {
		if err := os.Remove(filepath.Join(dir, id+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("snapshot migration: remove old file", "snapshot", id, "error", err)
		}
	}
	if migrated > 0 || skipped > 0 {
		slog.Info("migrated snapshots", "migrated", migrated, "skipped", skipped)
	}
	return nil
}
