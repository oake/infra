package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oake/infra/internal/api"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, e := Open(t.Context(), filepath.Join(t.TempDir(), "infra.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.DB.Close() })
	return store
}
func TestSQLiteMachineAPIAndRollback(t *testing.T) {
	s, now, root := fixture(t)
	store := testStore(t)
	if e := store.Update(t.Context(), func(dst *State) error { *dst = *s; return nil }); e != nil {
		t.Fatal(e)
	}
	handler := (&Server{Store: store, Root: root, Dix: testDix(t)}).Handler(http.NotFoundHandler())
	call := func(method, path string, b any) *httptest.ResponseRecorder {
		var body bytes.Buffer
		if b != nil {
			_ = json.NewEncoder(&body).Encode(b)
		}
		r := httptest.NewRequest(method, path, &body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	h := s.Hosts["example/primary/manual"]
	h.Observation.Active = testPath("new-unknown")
	w := call("POST", "/api/beacon", h.Observation)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	read, e := store.Read(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if read.Hosts[h.ID].Observation.Active != h.Observation.Active {
		t.Fatal("observation not persisted")
	}
	before, e := store.Read(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	w = call("GET", "/api/deployer/jobs?repository=example%2Fprimary", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	after, _ := store.Read(t.Context())
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if !bytes.Equal(b, a) {
		t.Fatal("job GET wrote to SQLite")
	}
	count := len(read.Observations)
	h.Observation.Active = "invalid"
	if w = call("POST", "/api/beacon", h.Observation); w.Code != 409 {
		t.Fatal("invalid path accepted")
	}
	read, _ = store.Read(t.Context())
	if len(read.Observations) != count {
		t.Fatal("rejected mutation leaked")
	}
	for _, request := range []struct{ method, path string }{{"POST", "/api/builds/events"}, {"PUT", "/api/snapshots/removed"}} {
		if w = call(request.method, request.path, map[string]string{}); w.Code != 404 {
			t.Fatalf("removed endpoint returned %d", w.Code)
		}
	}
	_ = now
}
func TestBrowserOriginBoundary(t *testing.T) {
	store := testStore(t)
	handler := (&Server{Store: store}).Handler(http.NotFoundHandler())
	r := httptest.NewRequest("POST", "http://hub.test/api/ui/hosts/example%2Fprimary%2Foffline/retry", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://elsewhere.test")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
}

func TestHTTPRepositoryJobSelection(t *testing.T) {
	store := testStore(t)
	s, now, root := fixture(t)
	// Make a second repository eligible without relying on a named runner.
	h := s.Hosts["example/secondary/staged"]
	h.Staged = ""
	s.Hosts[h.ID] = h
	s.Reconcile(now)
	if err := store.Update(t.Context(), func(dst *State) error { *dst = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: store, Root: root, Dix: testDix(t)}).Handler(http.NotFoundHandler())
	for _, repo := range []string{"example/primary", "example/secondary"} {
		req := httptest.NewRequest("GET", "/api/deployer/jobs?repository="+url.QueryEscape(repo), nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var jobs []api.Job
		if err := json.Unmarshal(w.Body.Bytes(), &jobs); err != nil {
			t.Fatal(err)
		}
		if len(jobs) == 0 {
			t.Fatalf("expected eligible job for %s", repo)
		}
		for _, j := range jobs {
			if j.Repository != repo {
				t.Fatal("job leaked across repositories")
			}
		}
	}
	req := httptest.NewRequest("GET", "/api/deployer/jobs", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal("repository selection was optional")
	}
}
