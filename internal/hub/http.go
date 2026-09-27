package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/oake/infra/internal/api"
)

type Server struct {
	Summarizer *Summarizer
	Git        *GitRepos
	gitSyncMu  sync.Mutex
	gitWake    chan struct{}
	inboxMu    sync.Mutex
	GitHub     *GitHub

	Store *Store
	Root  string

	Dix string
}

func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, e error) {
	jsonResponse(w, status, map[string]string{"error": e.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
func (s *Server) mutation(w http.ResponseWriter, r *http.Request, fn func(*State) error) {
	e := s.Store.Update(r.Context(), func(state *State) error {
		if e := fn(state); e != nil {
			return e
		}
		state.Reconcile(time.Now().UTC())
		return nil
	})
	if e != nil {
		problem(w, 409, e)
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (s *Server) Handler(static http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := s.Store.DB.PingContext(r.Context()); e != nil {
			problem(w, 503, errors.New("database unavailable"))
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/beacon", func(w http.ResponseWriter, r *http.Request) {
		var b api.Beacon
		if e := decode(w, r, &b, 8192); e != nil {
			problem(w, 400, e)
			return
		}
		if err := s.Store.Observe(r.Context(), b, time.Now().UTC()); err != nil {
			problem(w, 409, err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/deployer/jobs", func(w http.ResponseWriter, r *http.Request) {
		repository := r.URL.Query().Get("repository")
		if !api.Repository.MatchString(repository) {
			problem(w, 400, errors.New("repository must be owner/repo"))
			return
		}
		jobs, err := s.Store.Outstanding(r.Context(), repository, time.Now().UTC())
		if err != nil {
			problem(w, 500, err)
			return
		}
		jsonResponse(w, 200, jobs)
	})
	mux.HandleFunc("POST /api/deployer/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		repository := r.URL.Query().Get("repository")
		if !api.Repository.MatchString(repository) {
			problem(w, 400, errors.New("repository must be owner/repo"))
			return
		}
		var result api.Result
		if e := decode(w, r, &result, 40<<10); e != nil {
			problem(w, 400, e)
			return
		}
		s.mutation(w, r, func(state *State) error { return state.Result(r.PathValue("id"), repository, result, time.Now().UTC()) })
	})
	ui := http.NewServeMux()
	ui.HandleFunc("GET /api/ui/state", func(w http.ResponseWriter, r *http.Request) {
		state, e := s.Store.Fleet(r.Context())
		if e != nil {
			problem(w, 500, e)
			return
		}
		jsonResponse(w, 200, state)
	})
	ui.HandleFunc("GET /api/ui/hosts/{id}/timeline", func(w http.ResponseWriter, r *http.Request) {
		page, e := s.Store.HostTimeline(r.Context(), r.PathValue("id"), r.URL.Query().Get("cursor"))
		if e != nil {
			code, err := uiError(e)
			problem(w, code, err)
			return
		}
		jsonResponse(w, 200, page)
	})
	ui.HandleFunc("GET /api/ui/compare/{owner}/{repo}/{old}/{new}", func(w http.ResponseWriter, r *http.Request) {
		repo := r.PathValue("owner") + "/" + r.PathValue("repo")
		old, next := r.PathValue("old"), r.PathValue("new")
		if !api.Repository.MatchString(repo) || !api.Revision.MatchString(old) || !api.Revision.MatchString(next) {
			problem(w, 400, fmt.Errorf("invalid repository or commit revision"))
			return
		}
		page, e := s.CompareCommits(r.Context(), repo, old, next)
		if e != nil {
			code, err := uiError(e)
			problem(w, code, err)
			return
		}
		jsonResponse(w, 200, page)
	})
	ui.HandleFunc("GET /api/ui/diff", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state, e := s.Store.DiffArtifacts(r.Context(), q.Get("old"), q.Get("new"))
		if e != nil {
			problem(w, 500, e)
			return
		}
		c, e := compare(r.Context(), state, s.Root, s.Dix, q.Get("old"), q.Get("new"))
		if e != nil {
			problem(w, 503, e)
			return
		}
		jsonResponse(w, 200, c)
	})
	ui.HandleFunc("POST /api/ui/hosts/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		action := r.PathValue("action")
		s.mutation(w, r, func(state *State) error {
			_, ok := state.Hosts[id]
			if !ok {
				return errors.New("unknown host")
			}
			switch action {
			case "retry":
				for jid, j := range state.Jobs {
					if j.Host == id && j.Result != nil && (j.Result.Outcome == "failed" || j.Result.Outcome == "ambiguous") {
						j.Superseded = true
						state.Jobs[jid] = j
					}
				}
			default:
				return errors.New("unknown action")
			}
			return nil
		})
	})
	mux.Handle("/api/ui/", ui)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.Handle("/", static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		// Browser mutations require a same-origin request. Bearer agents do not send Origin.
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, e := url.Parse(origin)
				if e != nil || u.Host != r.Host {
					problem(w, 403, errors.New("cross-origin request rejected"))
					return
				}
			} else if strings.HasPrefix(r.URL.Path, "/api/ui/") {
				problem(w, 403, errors.New("browser Origin required"))
				return
			}
		}
		defer func() {
			if e := recover(); e != nil {
				slog.Error("request panic", "error", fmt.Sprint(e))
				problem(w, 500, errors.New("internal error"))
			}
		}()
		mux.ServeHTTP(w, r)
	})
}
