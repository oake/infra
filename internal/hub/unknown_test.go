package hub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/oake/infra/internal/api"
)

func TestUnknownConfigurationRequiresExplicitDeployment(t *testing.T) {
	st, now, _ := fixture(t)
	store := testStore(t)
	id := "example/primary/offline"
	h := st.Hosts[id]
	report := api.Beacon{Host: id, Active: testPath("custom"), Profile: testPath("custom")}
	if err := st.Observe(report, now); err != nil {
		t.Fatal(err)
	}
	st.Reconcile(now)
	if st.Hosts[id].Status != "Paused" {
		t.Fatal("not paused")
	}
	for _, j := range outstanding(t, st, h.Repository, now.Add(time.Hour)) {
		if j.Host == id {
			t.Fatal("old queue survived pause")
		}
	}
	if err := store.Update(t.Context(), func(dst *State) error { *dst = *st; return nil }); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: store}).Handler(http.NotFoundHandler())
	call := func(id string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://hub.test/api/ui/hosts/"+url.PathEscape(id)+"/deploy", strings.NewReader("{}"))
		r.Header.Set("Origin", "http://hub.test")
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := call(id); code != 200 {
		t.Fatalf("deploy: %d", code)
	}
	st, _ = store.Read(t.Context())
	if st.Hosts[id].Status != "Deployment queued" {
		t.Fatal("approval did not queue")
	}
	before := len(st.Jobs)
	st.Reconcile(now.Add(time.Hour))
	if len(st.Jobs) != before {
		t.Fatal("duplicate queue")
	}
	if code := call("example/primary/manual"); code != 409 {
		t.Fatal("manual host was affected")
	}
	h = st.Hosts[id]
	h.Observation.Active = testPath("different-custom")
	st.Hosts[id] = h
	st.Reconcile(now)
	if st.Hosts[id].Status != "Paused" {
		t.Fatal("approval applied to different custom configuration")
	}
	h = st.Hosts[id]
	h.DeploymentApproval = deploymentApproval(h)
	st.Hosts[id] = h
	repo := st.Repositories[h.Repository]
	repo.Main = testHead
	st.Repositories[h.Repository] = repo
	st.Reconcile(now)
	if st.Hosts[id].Status != "Paused" {
		t.Fatal("approval applied to different target")
	}
	h = st.Hosts[id]
	// Use a mapping for this host, regardless of fixture ordering.
	for _, m := range st.Commits[api.ID(h.Repository, testPrior)].Mappings {
		if m.Host == id {
			h.Observation.Active = m.System
		}
	}
	st.Hosts[id] = h
	st.Reconcile(now)
	if st.Hosts[id].Status == "Paused" {
		t.Fatal("known configuration remained paused")
	}
}
