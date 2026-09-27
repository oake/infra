package hub

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/oake/infra/internal/api"
)

type UICommit struct {
	Repository string    `json:"repository"`
	Revision   string    `json:"revision"`
	Title      string    `json:"title"`
	Created    time.Time `json:"created"`
	Branch     string    `json:"branch,omitempty"`
}
type UIHost struct {
	Host
	Configurations map[string]UICommit `json:"configurations"`
}
type FleetState struct {
	Hosts        map[string]UIHost      `json:"hosts"`
	Repositories map[string]Repository  `json:"repositories"`
	PullRequests map[string]PullRequest `json:"pull_requests"`
}

func readUIRows[T any](ctx context.Context, tx *sql.Tx, query string, args ...any) (map[string]T, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]T{}
	for rows.Next() {
		var id string
		var raw []byte
		var v T
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}
func (s *Store) Fleet(ctx context.Context) (FleetState, error) {
	out := FleetState{}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if out.Hosts, err = readUIRows[UIHost](ctx, tx, "SELECT id,body FROM hosts"); err != nil {
		return out, err
	}
	if out.Repositories, err = readUIRows[Repository](ctx, tx, "SELECT id,json_remove(body, '$.main_history') FROM repositories"); err != nil {
		return out, err
	}
	if out.PullRequests, err = readUIRows[PullRequest](ctx, tx, "SELECT id,body FROM pull_requests"); err != nil {
		return out, err
	}
	// Only provenance for the two paths displayed in each fleet row.
	rows, err := tx.QueryContext(ctx, `SELECT host,path,body FROM (
 SELECT h.id AS host,m.value->>'system' AS path,json_remove(c.body,'$.mappings') AS body,
 row_number() OVER (PARTITION BY h.id,m.value->>'system' ORDER BY (c.body->>'revision'=r.body->>'main' OR c.body->>'revision' IN (SELECT value FROM json_each(r.body,'$.main_history'))) DESC,julianday(c.body->>'created'),c.body->>'revision') AS ordinal
 FROM hosts h JOIN repositories r ON r.id=h.body->>'repository' JOIN commits c ON c.body->>'repository'=h.body->>'repository'
 JOIN json_each(c.body,'$.mappings') m
 WHERE m.value->>'host'=h.id
 AND m.value->>'system' IN (json_extract(h.body,'$.observation.active'),h.body->>'desired')
 ) WHERE ordinal=1`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, path string
		var raw []byte
		var c UICommit
		if err = rows.Scan(&id, &path, &raw); err != nil {
			rows.Close()
			return out, err
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			rows.Close()
			return out, err
		}
		h := out.Hosts[id]
		if h.Configurations == nil {
			h.Configurations = map[string]UICommit{}
		}
		h.Configurations[path] = c
		out.Hosts[id] = h
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

type TimelineEntry struct {
	Commit    UICommit `json:"commit"`
	Path      string   `json:"path"`
	OffBranch bool     `json:"offBranch"`
}
type ComparisonPair struct {
	Before string `json:"before"`
	After  string `json:"after"`
}
type TimelinePage struct {
	Host       Host            `json:"host"`
	Entries    []TimelineEntry `json:"entries"`
	Staged     string          `json:"staged"`
	Unknown    bool            `json:"unknown"`
	Comparison ComparisonPair  `json:"comparison"`
	Attempt    *api.Result     `json:"attempt,omitempty"`
	NextCursor string          `json:"next_cursor"`
	Version    string          `json:"version"`
}

var errCursor = errors.New("timeline changed; reload the first page")
var errBadCursor = errors.New("invalid timeline cursor")

func timelinePage(h Host, history []TimelineEntry, cursor string) (TimelinePage, error) {
	p := TimelinePage{Host: h, Entries: []TimelineEntry{}, Unknown: true}
	if h.ProfileSystem != "" && h.ProfileSystem != h.Observation.Active {
		p.Staged = h.ProfileSystem
	} else if h.StagedCurrent {
		p.Staged = h.Staged
	}
	raw, _ := json.Marshal(struct {
		Host, Live, Staged string
		Entries            []TimelineEntry
	}{h.ID, h.Observation.Active, p.Staged, history})
	p.Version = api.ID(string(raw))
	main := []TimelineEntry{}
	live := -1
	for i, e := range history {
		if e.Path == h.Observation.Active {
			live = i
			p.Unknown = false
		}
		if !e.OffBranch {
			main = append(main, e)
		}
	}
	if len(main) > 0 {
		p.Comparison = ComparisonPair{h.Observation.Active, main[0].Path}
		if h.Observation.Active == main[0].Path {
			p.Comparison.Before = ""
			if len(main) > 1 {
				p.Comparison.Before = main[1].Path
			}
		}
	}
	limit := 5
	if live >= 0 {
		limit = live + 2
	}
	initial, remaining := []TimelineEntry{}, []TimelineEntry{}
	for i, e := range history {
		if i < limit || e.Path == p.Staged {
			initial = append(initial, e)
		} else {
			remaining = append(remaining, e)
		}
	}
	offset := 0
	if cursor == "" {
		p.Entries = initial
	} else {
		var c struct {
			Version string
			Offset  int
		}
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(data, &c) != nil {
			return p, errBadCursor
		}
		if c.Version != p.Version {
			return p, errCursor
		}
		if c.Offset < 0 || c.Offset >= len(remaining) {
			return p, errBadCursor
		}
		offset = c.Offset
		end := min(offset+10, len(remaining))
		p.Entries = remaining[offset:end]
		offset = end
	}
	if offset < len(remaining) {
		raw, _ := json.Marshal(struct {
			Version string
			Offset  int
		}{p.Version, offset})
		p.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return p, nil
}
func (s *Store) HostTimeline(ctx context.Context, id, cursor string) (TimelinePage, error) {
	var h Host
	var repo Repository
	var p TimelinePage
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM hosts WHERE id=$1", id).Scan(&raw); err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &h); err != nil {
		return p, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT body FROM repositories WHERE id=$1", h.Repository).Scan(&raw); err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &repo); err != nil {
		return p, err
	}
	staged := h.ProfileSystem
	if staged == h.Observation.Active {
		staged = ""
	}
	if staged == "" && h.StagedCurrent {
		staged = h.Staged
	}
	main := append(repo.MainHistory, repo.Main)
	// Visibility follows configuration paths on main, plus Live/staged paths.
	// Prefer the oldest introducing commit on current main; use other branches
	// only for Live/staged configurations with no main provenance.
	rows, err := tx.QueryContext(ctx, `SELECT path,body,NOT on_main FROM (
 SELECT m.value->>'system' AS path,json_remove(c.body,'$.mappings') AS body,
 max(c.body->>'revision' IN (SELECT value FROM json_each($3))) OVER (PARTITION BY m.value->>'system') AS on_main,
 row_number() OVER (PARTITION BY m.value->>'system' ORDER BY (c.body->>'revision' IN (SELECT value FROM json_each($3))) DESC,julianday(c.body->>'created'),c.body->>'revision') AS ordinal
 FROM commits c JOIN json_each(c.body,'$.mappings') m
 WHERE c.body->>'repository'=$1 AND m.value->>'host'=$2
 ) WHERE ordinal=1 AND (on_main OR path IN (SELECT value FROM json_each($4)))`, h.Repository, id, jsonStrings(main), jsonStrings([]string{h.Observation.Active, staged}))
	if err != nil {
		return p, err
	}
	history := []TimelineEntry{}
	for rows.Next() {
		var e TimelineEntry
		if err = rows.Scan(&e.Path, &raw, &e.OffBranch); err != nil {
			rows.Close()
			return p, err
		}
		if err = json.Unmarshal(raw, &e.Commit); err != nil {
			rows.Close()
			return p, err
		}
		history = append(history, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	sort.Slice(history, func(i, j int) bool {
		a, b := history[i].Commit, history[j].Commit
		if a.Created.Equal(b.Created) {
			return a.Revision > b.Revision
		}
		return a.Created.After(b.Created)
	})
	p, err = timelinePage(h, history, cursor)
	if err != nil {
		return p, err
	}
	if h.Status == "Deploy failed" || h.Status == "Deployment queued" {
		var result []byte
		err = tx.QueryRowContext(ctx, `SELECT body->'result' FROM jobs WHERE body->>'host'=$1 AND COALESCE(body->>'superseded',0)=0 AND json_extract(body,'$.result') IS NOT NULL
  AND ((body->>'system'=$2 AND body->>'activation'=$3) OR json_extract(body,'$.result.outcome')='ambiguous') ORDER BY julianday(json_extract(body,'$.result.finished')) DESC LIMIT 1`, id, h.Desired, h.Activation).Scan(&result)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
		if err == nil {
			var r api.Result
			if err = json.Unmarshal(result, &r); err != nil {
				return p, err
			}
			if r.Outcome != "staged" && (h.Status == "Deploy failed" || r.Outcome == "unreachable") {
				p.Attempt = &r
			}
		}
	}
	return p, tx.Commit()
}
func uiError(werr error) (int, error) {
	switch {
	case errors.Is(werr, sql.ErrNoRows):
		return 404, fmt.Errorf("not found")
	case errors.Is(werr, errCursor):
		return 409, werr
	case errors.Is(werr, errBadCursor):
		return 400, werr
	default:
		return 500, werr
	}
}

func (s *Store) DiffArtifacts(ctx context.Context, old, next string) (*State, error) {
	state := NewState()
	rows, err := s.DB.QueryContext(ctx, "SELECT body FROM artifacts WHERE body->>'path' IN (SELECT value FROM json_each($1))", jsonStrings([]string{old, next}))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var a Artifact
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		state.Artifacts[a.Path] = a
	}
	return state, rows.Err()
}
