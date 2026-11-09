package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func legacyJSON(root string) string {
	return `{"schema_version":1,"root":"` + root + `","closure":[{"path":"` + root + `","nar_size":7}],"selected":[]}`
}

func TestParseSnapshotFormats(t *testing.T) {
	root := testPath("parse")
	current, legacy, err := parseSnapshot([]byte(`{"version":1,"path":"` + root + `","closure":[{"path":"` + root + `","narSize":7}],"selected":[]}`))
	if err != nil || legacy || current.Root != root || current.Closure[0].Size != 7 {
		t.Fatalf("current: %+v %v %v", current, legacy, err)
	}
	converted, legacy, err := parseSnapshot([]byte(legacyJSON(root)))
	if err != nil || !legacy || converted.Version != 1 || converted.Root != root || converted.Closure[0].Size != 7 || validateSnapshot(converted) != nil {
		t.Fatalf("legacy: %+v %v %v", converted, legacy, err)
	}
	for _, bad := range []string{`{"schema_version":2,"root":"x","closure":[],"selected":[]}`, `{"version":1,"extra":1}`, `not json`} {
		if _, _, err := parseSnapshot([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestMigrateSnapshots(t *testing.T) {
	store := testStore(t)
	root := t.TempDir()
	dir := filepath.Join(root, "snapshots")
	os.MkdirAll(dir, 0700)
	old, missing, broken := testPath("old"), testPath("missing"), testPath("broken")
	os.WriteFile(filepath.Join(dir, "legacy-id.json"), []byte(legacyJSON(old)), 0600)
	os.WriteFile(filepath.Join(dir, "broken-id.json"), []byte("garbage"), 0600)
	if err := store.Update(t.Context(), func(st *State) error {
		st.Artifacts[old] = Artifact{Path: old, Snapshot: "legacy-id", Producer: "dix-69f91d6-schema1"}
		st.Artifacts[missing] = Artifact{Path: missing, Snapshot: "missing-id", Producer: "dix-69f91d6-schema1"}
		st.Artifacts[broken] = Artifact{Path: broken, Snapshot: "broken-id", Producer: "dix-69f91d6-schema1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: store, Root: root}
	for range 2 { // Idempotent.
		if err := server.MigrateSnapshots(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a := st.Artifacts[old]
	if a.Producer != Producer || a.Snapshot == "legacy-id" {
		t.Fatalf("not migrated: %+v", a)
	}
	raw, err := os.ReadFile(filepath.Join(dir, a.Snapshot+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if s, legacy, err := parseSnapshot(raw); err != nil || legacy || s.Root != old {
		t.Fatalf("migrated file: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "legacy-id.json")); !os.IsNotExist(err) {
		t.Fatal("legacy file kept")
	}
	// Unreadable or missing snapshots stay untouched for a later look.
	if st.Artifacts[missing].Snapshot != "missing-id" || st.Artifacts[broken].Snapshot != "broken-id" {
		t.Fatal("unmigratable snapshots changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "broken-id.json")); err != nil {
		t.Fatal("unreadable snapshot removed")
	}
}

func TestInboxConvertsLegacySnapshot(t *testing.T) {
	store := testStore(t)
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	os.Mkdir(inbox, 0700)
	path := testPath("legacy-inbox")
	if err := store.Update(t.Context(), func(st *State) error {
		st.Artifacts[path] = Artifact{Path: path}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(inbox, "x.snapshot.json"), []byte(legacyJSON(path)), 0600)
	if err := (&Server{Store: store, Root: root}).ScanInbox(t.Context(), inbox); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Read(t.Context())
	a := st.Artifacts[path]
	if a.Snapshot == "" || a.Producer != Producer {
		t.Fatalf("legacy snapshot not ingested: %+v", a)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "snapshots", a.Snapshot+".json"))
	if !strings.Contains(string(raw), `"narSize":7`) {
		t.Fatalf("stored unconverted: %s", raw)
	}
}
