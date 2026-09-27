package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oake/infra/internal/api"
)

func TestFailureSummariesPersistOnce(t *testing.T) {
	for _, answer := range []string{`{"summary":"Missing kernel module tpm-crb."}`, `{"summary":null}`, `fail`} {
		t.Run(answer, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "infra.db")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test" {
					t.Error("wrong API request")
				}
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				if b["model"] != "gpt-6-luna" || !strings.HasSuffix(b["input"].(string), "THE ERROR") || len(b["input"].(string)) > 100050 {
					t.Error("wrong model or log bound")
				}
				if answer == "fail" {
					w.WriteHeader(503)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": answer}}}}})
			}))
			defer remote.Close()
			repo, rev := "owner/repo", strings.Repeat("a", 40)
			url := "https://builder/#/builders/2/builds/3/steps/1/logs/nix_error"
			detail := strings.Repeat("x", 110000) + "THE ERROR"
			err = store.Update(ctx, func(st *State) error {
				st.Repositories[repo] = Repository{ID: repo}
				st.Hosts[repo+"/malina"] = Host{ID: repo + "/malina", Name: "malina", Repository: repo}
				st.Commits[api.ID(repo, rev)] = Commit{ID: api.ID(repo, rev), Repository: repo, Revision: rev, Evaluation: "failed", Errors: map[string]string{"checks.aarch64-linux.nixos-malina": detail}, ErrorURLs: map[string]string{"checks.aarch64-linux.nixos-malina": url}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			server := &Server{Store: store, Summarizer: &Summarizer{Token: "test", BaseURL: remote.URL + "/v1"}}
			if err = server.SummarizeFailures(ctx); err != nil {
				t.Fatal(err)
			}
			store.DB.Close()
			store, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.DB.Close()
			server.Store = store
			if err = server.SummarizeFailures(ctx); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			st, _ := store.Read(ctx)
			_, f := st.BuildStatus(repo, rev)
			if len(f) != 1 || f[0].Name != "malina" || f[0].URL != url {
				t.Fatalf("bad failure: %+v", f)
			}
			if answer == "fail" || strings.Contains(answer, "null") {
				if f[0].Summary != "" {
					t.Fatal("expected no summary")
				}
			} else if f[0].Summary == "" {
				t.Fatal("missing summary")
			}
			comparison, err := server.CompareCommits(ctx, repo, rev, rev)
			if err != nil {
				t.Fatal(err)
			}
			if len(comparison.FailedChecks) != 1 || comparison.FailedChecks[0].Name != "malina" || comparison.FailedChecks[0].Summary != f[0].Summary {
				t.Fatalf("comparison lost summary/host: %+v", comparison.FailedChecks)
			}
			raw, _ := json.Marshal(f)
			if strings.Contains(string(raw), "THE ERROR") {
				t.Fatal("raw error leaked")
			}
		})
	}
}

func TestEvaluationLogDoesNotOverwriteChangedError(t *testing.T) {
	st := NewState()
	repo, rev := "owner/repo", strings.Repeat("a", 40)
	id := api.ID(repo, rev)
	st.Repositories[repo] = Repository{ID: repo}
	st.Commits[id] = Commit{Errors: map[string]string{"check": "new"}}
	e := api.BuildEvent{ID: "log", Repository: repo, Revision: rev, Kind: "evaluation-log", Status: "failed", Errors: map[string]string{"check": "old"}, LogURL: "log"}
	if st.ApplyEvent(e, "hash", time.Now()) == nil {
		t.Fatal("stale error accepted")
	}
	e.Errors["check"] = "new"
	if err := st.ApplyEvent(e, "hash", time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Commits[id].ErrorURLs["check"] != "log" {
		t.Fatal("missing child log")
	}
}
