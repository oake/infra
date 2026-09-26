package hub

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/oake/infra/internal/api"
)

type Host struct {
	Removed        bool       `json:"removed"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Platform       string     `json:"platform"`
	Repository     string     `json:"repository"`
	Node           string     `json:"node"`
	Automatic      bool       `json:"automatic"`
	Observation    api.Beacon `json:"observation"`
	LastSeen       time.Time  `json:"last_seen"`
	Desired        string     `json:"desired"`
	Activation     string     `json:"activation"`
	Revision       string     `json:"revision"`
	Staged         string     `json:"staged"`
	StagedRevision string     `json:"staged_revision"`
	StagedAt       time.Time  `json:"staged_at"`
	Status         string     `json:"status"`
	ProfileSystem  string     `json:"profile_system"`
	StagedCurrent  bool       `json:"staged_current"`
}
type Repository struct {
	RefAt       time.Time `json:"ref_at,omitempty"`
	PRError     string    `json:"pr_error,omitempty"`
	MainHistory []string  `json:"main_history,omitempty"`
	ID          string    `json:"id"`
	Main        string    `json:"main"`
}
type Commit struct {
	InventoryComplete bool              `json:"inventory_complete,omitempty"`
	EvaluatedAt       time.Time         `json:"evaluated_at,omitempty"`
	Errors            map[string]string `json:"errors,omitempty"`
	Outputs           map[string]string `json:"outputs"`
	LogURL            string            `json:"log_url,omitempty"`
	Branch            string            `json:"branch,omitempty"`
	ID                string            `json:"id"`
	Repository        string            `json:"repository"`
	Revision          string            `json:"revision"`
	Title             string            `json:"title"`
	Evaluation        string            `json:"evaluation"`
	Detail            string            `json:"detail"`
	Mappings          []api.Mapping     `json:"mappings"`
	Created           time.Time         `json:"created"`
}
type Artifact struct {
	BuildAt     time.Time `json:"build_at,omitempty"`
	LogURL      string    `json:"log_url,omitempty"`
	ID          string    `json:"id"`
	Path        string    `json:"path"`
	Build       string    `json:"build"`
	Detail      string    `json:"detail"`
	Snapshot    string    `json:"snapshot"`
	Producer    string    `json:"producer"`
	ReadyStatus string    `json:"ready_status"`
	ReadyAt     time.Time `json:"ready_at"`
	ReadyDetail string    `json:"ready_detail"`
}
type Observation struct {
	ID     string     `json:"id"`
	Host   string     `json:"host"`
	Report api.Beacon `json:"report"`
	At     time.Time  `json:"at"`
}
type InputChange struct {
	BeforeDate string `json:"before_date,omitempty"`
	AfterDate  string `json:"after_date,omitempty"`
	CompareURL string `json:"compare_url,omitempty"`
	Name       string `json:"name"`
	Before     string `json:"before"`
	After      string `json:"after"`
}
type CheckFailure struct {
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	Detail string `json:"detail,omitempty"`
}
type PullRequest struct {
	FailedChecks []CheckFailure `json:"failed_checks,omitempty"`
	HeadCommit   *UICommit      `json:"head_commit,omitempty"`
	BaseHead     string         `json:"base_head,omitempty"`
	InputsCached bool           `json:"inputs_cached"`
	Draft        bool           `json:"draft"`
	Evaluated    bool           `json:"evaluated"`
	Detail       string         `json:"detail,omitempty"`
	ID           string         `json:"id"`
	Repository   string         `json:"repository"`
	Number       int            `json:"number"`
	Title        string         `json:"title"`
	Head         string         `json:"head"`
	Base         string         `json:"base"`
	State        string         `json:"state"`
	Checks       string         `json:"checks"`
	Inputs       []InputChange  `json:"inputs"`
	Hosts        []string       `json:"hosts"`
}
type Event struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}
type State struct {
	Hosts        map[string]Host        `json:"hosts"`
	Repositories map[string]Repository  `json:"repositories"`
	Commits      map[string]Commit      `json:"commits"`
	Artifacts    map[string]Artifact    `json:"artifacts"`
	Observations map[string]Observation `json:"observations"`
	Jobs         map[string]api.Job     `json:"jobs"`
	PullRequests map[string]PullRequest `json:"pull_requests"`
	Events       map[string]Event       `json:"-"`
}

func NewState() *State {
	return &State{map[string]Host{}, map[string]Repository{}, map[string]Commit{}, map[string]Artifact{}, map[string]Observation{}, map[string]api.Job{}, map[string]PullRequest{}, map[string]Event{}}
}
func (s *State) Artifact(path string) Artifact {
	a, ok := s.Artifacts[path]
	if !ok {
		a = Artifact{ID: api.ID(path), Path: path, Build: "pending"}
	}
	return a
}
func (s *State) Observe(b api.Beacon, now time.Time) error {
	h, ok := s.Hosts[b.Host]
	if !ok {
		return errors.New("host not enrolled")
	}
	if !api.StorePath.MatchString(b.Active) || (b.Booted != "" && !api.StorePath.MatchString(b.Booted)) || (b.Profile != "" && !api.StorePath.MatchString(b.Profile)) {
		return errors.New("invalid store path")
	}
	if h.Platform == "darwin" && b.Booted != "" {
		return errors.New("Darwin has no booted-system observation")
	}
	if h.Observation != b {
		id := api.ID(b.Host, now.String())
		s.Observations[id] = Observation{id, b.Host, b, now}
	}
	h.Observation = b
	h.LastSeen = now
	s.Hosts[b.Host] = h
	return nil
}
func (s *State) ApplyEvent(e api.BuildEvent, hash string, now time.Time) error {
	if e.ID == "" || !api.Revision.MatchString(e.Revision) {
		return errors.New("event ID and exact revision required")
	}
	if old, ok := s.Events[e.ID]; ok {
		if old.Hash != hash {
			return errors.New("event ID already used for different content")
		}
		return nil
	}
	_, knownRepo := s.Repositories[e.Repository]
	if !api.Repository.MatchString(e.Repository) || (!knownRepo && e.Kind != "evaluation") {
		return errors.New("unknown repository")
	}
	if e.Status != "success" && e.Status != "failed" {
		return errors.New("invalid event status")
	}
	at := e.ObservedAt
	if at.IsZero() {
		at = now
	}
	switch e.Kind {
	case "evaluation":
		if !knownRepo && len(e.Mappings) == 0 {
			return errors.New("new repository requires host metadata")
		}
		for name, path := range e.Outputs {
			if strings.TrimSpace(name) == "" || !api.StorePath.MatchString(path) {
				return errors.New("invalid evaluation output")
			}
		}
		seen := map[string]bool{}
		for _, m := range e.Mappings {
			name := strings.TrimPrefix(m.Host, e.Repository+"/")
			if name == m.Host || !api.Name.MatchString(name) || seen[m.Host] {
				return errors.New("unknown, duplicate or mismatched host")
			}
			if (!knownRepo || m.Platform != "") && m.Platform != "nixos" && m.Platform != "darwin" {
				return errors.New("invalid host platform")
			}
			seen[m.Host] = true
			if !api.StorePath.MatchString(m.System) || !api.StorePath.MatchString(m.Activation) {
				return errors.New("invalid mapping paths")
			}
			for _, p := range m.Checks {
				if !api.StorePath.MatchString(p) {
					return errors.New("invalid check path")
				}
			}
		}
		id := api.ID(e.Repository, e.Revision)
		meta := s.Commits[id]
		c := Commit{Branch: meta.Branch, ID: id, Repository: e.Repository, Revision: e.Revision, Title: meta.Title, Evaluation: e.Status, Detail: e.Detail, Mappings: e.Mappings, Created: meta.Created, Outputs: e.Outputs, LogURL: e.LogURL, Errors: e.Errors, EvaluatedAt: at, InventoryComplete: e.InventoryComplete}
		if old, ok := s.Commits[id]; ok {
			if old.EvaluatedAt.After(at) {
				s.Events[e.ID] = Event{e.ID, hash}
				return nil
			}
			if old.Evaluation == "success" && (!reflect.DeepEqual(old.Mappings, e.Mappings) || !reflect.DeepEqual(old.Outputs, e.Outputs)) {
				return errors.New("successful evaluation mappings are immutable")
			}
		}
		if !knownRepo {
			s.Repositories[e.Repository] = Repository{ID: e.Repository}
		}
		s.Commits[id] = c
		if err := s.inventoryFromCommit(c); err != nil {
			return err
		}
		{
			for _, path := range e.Outputs {
				s.Artifacts[path] = s.Artifact(path)
			}
			for _, m := range e.Mappings {
				s.Artifacts[m.System] = s.Artifact(m.System)
				s.Artifacts[m.Activation] = s.Artifact(m.Activation)
				for _, path := range m.Checks {
					s.Artifacts[path] = s.Artifact(path)
				}
			}
		}
	case "build", "ready":
		a, ok := s.Artifacts[e.Artifact]
		if !ok {
			return errors.New("artifact needs an evaluation mapping first")
		}
		if e.Kind == "build" {
			if !a.BuildAt.After(at) {
				a.Build = e.Status
				a.Detail = e.Detail
				a.LogURL = e.LogURL
				a.BuildAt = at
			}
		} else {
			if !a.ReadyAt.After(at) {
				a.ReadyStatus, a.ReadyAt, a.ReadyDetail = e.Status, at, e.Detail
			}
		}
		s.Artifacts[e.Artifact] = a
	default:
		return errors.New("unknown event kind")
	}
	s.Events[e.ID] = Event{e.ID, hash}
	return nil
}
func (s *State) Ready(path string) bool {
	a, ok := s.Artifacts[path]
	if !ok || a.Build != "success" {
		return false
	}
	return a.ReadyStatus == "success"
}

// ProfileSystem resolves known deploy-rs wrappers without replacing raw observations.
func (s *State) ProfileSystem(path string) string {
	if path == "" {
		return ""
	}
	resolved := ""
	for _, c := range s.Commits {
		for _, m := range c.Mappings {
			if m.System == path {
				return path
			}
			if m.Activation == path {
				if resolved != "" && resolved != m.System {
					return ""
				}
				resolved = m.System
			}
		}
	}
	return resolved
}

func (s *State) Reconcile(now time.Time) {
	for id, h := range s.Hosts {
		h.Desired, h.Activation, h.Revision = "", "", ""
		h.ProfileSystem = s.ProfileSystem(h.Observation.Profile)
		h.StagedCurrent = h.Staged != "" && (h.LastSeen.Before(h.StagedAt) || h.Observation.Profile == "" || h.ProfileSystem == h.Staged)
		r := s.Repositories[h.Repository]
		c, ok := s.Commits[api.ID(r.ID, r.Main)]
		hostBuilt := true
		if ok && !h.Removed {
			for _, m := range c.Mappings {
				if m.Host == id {
					h.Desired, h.Activation, h.Revision = m.System, m.Activation, c.Revision
					for _, path := range m.Checks {
						if s.Artifacts[path].Build != "success" {
							hostBuilt = false
						}
					}
				}
			}
		}
		ready := hostBuilt && s.Ready(h.Desired) && (!h.Automatic || s.Ready(h.Activation))
		matches := h.Desired != "" && h.Desired == h.Observation.Active
		staged := h.Desired != "" && !matches && ((h.Desired == h.Staged && h.StagedCurrent) || h.ProfileSystem == h.Desired)
		ambiguous := false
		for _, j := range s.Jobs {
			if j.Host == id && !j.Superseded && j.Result != nil && j.Result.Outcome == "ambiguous" {
				ambiguous = true
			}
		}
		var latest *api.Job
		outstanding, count := false, 0
		for jid, j := range s.Jobs {
			if j.Host != id {
				continue
			}
			count++
			if j.Result == nil && (j.System != h.Desired || j.Activation != h.Activation || !h.Automatic || h.Removed || !ready || matches || staged || ambiguous) {
				j.Superseded = true
				s.Jobs[jid] = j
			}
			if !j.Superseded && j.System == h.Desired && j.Activation == h.Activation {
				if j.Result == nil {
					outstanding = true
				}
				if j.Result != nil && (latest == nil || j.Result.Finished.After(latest.Result.Finished)) {
					copy := j
					latest = &copy
				}
			}
		}
		failed := latest != nil && latest.Result.Outcome != "staged" && latest.Result.Outcome != "unreachable"
		switch {
		case matches:
			h.Status = "Up to date"
		case staged:
			h.Status = "Reboot to apply"
		case ambiguous || failed:
			h.Status = "Deploy failed"
		case h.Desired == "" || !ready:
			h.Status = "Processing"
		case !h.Automatic || h.Platform == "darwin":
			h.Status = "Outdated"
		default:
			h.Status = "Deployment queued"
			if !outstanding && !h.Removed {
				notBefore := now
				if latest != nil && latest.Result.Outcome == "unreachable" {
					notBefore = latest.Result.Finished.Add(time.Minute)
				}
				jid := api.ID(id, h.Desired, h.Activation, fmt.Sprint(count))
				s.Jobs[jid] = api.Job{ID: jid, Host: id, Repository: h.Repository, Revision: h.Revision, Node: h.Node, System: h.Desired, Activation: h.Activation, Created: now, NotBefore: notBefore}
			}
		}
		s.Hosts[id] = h
	}
}

func (s *State) Result(id, repository string, result api.Result, now time.Time) error {
	j, ok := s.Jobs[id]
	if !ok || j.Repository != repository {
		return errors.New("unknown job")
	}
	if !slices.Contains([]string{"staged", "unreachable", "failed", "ambiguous"}, result.Outcome) || len(result.Detail) > 32768 {
		return errors.New("invalid result")
	}
	if j.Result != nil {
		if j.Result.Outcome != result.Outcome || j.Result.Detail != result.Detail {
			return errors.New("conflicting result")
		}
		return nil
	}
	result.Finished = now
	j.Result = &result
	s.Jobs[id] = j
	if result.Outcome == "staged" {
		h := s.Hosts[j.Host]
		h.Staged = j.System
		h.StagedRevision = j.Revision
		h.StagedAt = now
		s.Hosts[h.ID] = h
	}
	return nil
}
