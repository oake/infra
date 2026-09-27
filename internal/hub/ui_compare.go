package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/oake/infra/internal/api"
)

type HostComparison struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
	Pair     string `json:"pair"`
}
type ComparisonDetail struct {
	PRTitle      string                `json:"pr_title,omitempty"`
	Repository   string                `json:"repository"`
	Head         *UICommit             `json:"head"`
	Base         *UICommit             `json:"base"`
	Checks       string                `json:"checks"`
	FailedChecks []CheckFailure        `json:"failed_checks,omitempty"`
	Inputs       []InputChange         `json:"inputs"`
	GitError     string                `json:"git_error,omitempty"`
	GitHubURL    string                `json:"github_url"`
	All          HostComparison        `json:"all"`
	Hosts        []HostComparison      `json:"hosts"`
	Comparisons  map[string]Comparison `json:"comparisons"`
}

// Resolve exactly the two requested commits. Snapshot work remains keyed by paths.
func (s *Server) CompareCommits(ctx context.Context, repo, old, next string) (ComparisonDetail, error) {
	out := ComparisonDetail{Repository: repo, Hosts: []HostComparison{}, Inputs: []InputChange{}, Comparisons: map[string]Comparison{},
		GitHubURL: "https://github.com/" + repo + "/compare/" + old + ".." + next}
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var exists string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM repositories WHERE id=$1", repo).Scan(&exists); err != nil {
		return out, err
	}
	commits, err := readUIRows[Commit](ctx, tx, "SELECT id,body FROM commits WHERE id IN (SELECT value FROM json_each($1))", jsonStrings([]string{api.ID(repo, old), api.ID(repo, next)}))
	if err != nil {
		return out, err
	}
	metadata := func(rev string) *UICommit {
		c, ok := commits[api.ID(repo, rev)]
		if !ok {
			return &UICommit{Repository: repo, Revision: rev, Title: rev}
		}
		return &UICommit{Repository: repo, Revision: rev, Title: c.Title, Created: c.Created, Branch: c.Branch}
	}
	out.Base, out.Head = metadata(old), metadata(next)
	// Only a matching base/head pair gets the PR destination. No GitHub request.
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT body FROM pull_requests WHERE body->>'repository'=$1
 AND body->>'head'=$2 AND (body->>'base'=$3 OR body->>'base_head'=$3)
 ORDER BY (body->>'state'='open') DESC,CAST(body->>'number' AS INTEGER) DESC LIMIT 1`, repo, next, old).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	if err == nil {
		var p PullRequest
		if err = json.Unmarshal(raw, &p); err != nil {
			return out, err
		}
		out.PRTitle = p.Title
		out.GitHubURL = fmt.Sprintf("https://github.com/%s/pull/%d", repo, p.Number)
		if p.Base == old {
			out.Inputs = p.Inputs
		}
		if _, ok := commits[api.ID(repo, next)]; !ok && p.HeadCommit != nil {
			out.Head = p.HeadCommit
		}
	}
	hosts, err := readUIRows[Host](ctx, tx, "SELECT id,body FROM hosts WHERE body->>'repository'=$1", repo)
	if err != nil {
		return out, err
	}
	base, head := map[string]string{}, map[string]string{}
	for _, m := range commits[api.ID(repo, old)].Mappings {
		base[m.Host] = m.System
	}
	for _, m := range commits[api.ID(repo, next)].Mappings {
		head[m.Host] = m.System
	}

	pairs := map[string]ComparisonPair{}
	paths := map[string]bool{}
	add := func(old, next string) string {
		key := api.ID(old, next)
		pairs[key] = ComparisonPair{old, next}
		paths[old] = true
		paths[next] = true
		return key
	}
	hostIDs := map[string]bool{}
	for id := range base {
		hostIDs[id] = true
	}
	for id := range head {
		hostIDs[id] = true
	}
	for id := range hostIDs {
		if base[id] != "" && base[id] == head[id] {
			continue
		}
		h := hosts[id]
		name := h.Name
		if name == "" {
			name = strings.TrimPrefix(id, repo+"/")
		}
		out.Hosts = append(out.Hosts, HostComparison{ID: id, Name: name, Platform: h.Platform, Pair: add(base[id], head[id])})
	}
	// Checks cover all evaluated attributes, including non-host outputs.
	for _, path := range commits[api.ID(repo, next)].Outputs {
		paths[path] = true
	}

	sort.Slice(out.Hosts, func(i, j int) bool { return out.Hosts[i].ID < out.Hosts[j].ID })
	ids := []string{}
	for path := range paths {
		if path != "" {
			ids = append(ids, path)
		}
	}
	artifacts, err := readUIRows[Artifact](ctx, tx, "SELECT id,body FROM artifacts WHERE id IN (SELECT value FROM json_each($1))", jsonStrings(ids))
	if err != nil {
		return out, err
	}
	state := NewState()
	state.Commits = commits
	state.Hosts = hosts
	for _, a := range artifacts {
		state.Artifacts[a.Path] = a
	}
	out.Checks, out.FailedChecks = state.BuildStatus(repo, next)
	summaryIDs := []string{}
	for _, f := range out.FailedChecks {
		summaryIDs = append(summaryIDs, f.ID)
	}
	summaries, err := readUIRows[FailureSummary](ctx, tx, "SELECT id,body FROM summaries WHERE id IN (SELECT value FROM json_each($1))", jsonStrings(summaryIDs))
	if err != nil {
		return out, err
	}
	for i := range out.FailedChecks {
		out.FailedChecks[i].Summary = summaries[out.FailedChecks[i].ID].Summary
	}
	rules, err := readUIRows[PackageBlock](ctx, tx, "SELECT id,body FROM package_blocks")
	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	if s.Git != nil {
		pair, err := s.Git.pair(ctx, repo, old, next, false)
		if err != nil {
			out.GitError = "Git metadata unavailable: " + err.Error()
		} else {
			out.Base, out.Head, out.Inputs = pair.Base, pair.Head, pair.Inputs
		}
	}

	type task struct {
		key  string
		pair ComparisonPair
	}
	type result struct {
		key  string
		diff Comparison
	}
	work := make(chan task, len(pairs))
	done := make(chan result, len(pairs))
	for key, pair := range pairs {
		work <- task{key, pair}
	}
	close(work)
	for range min(4, len(pairs)) {
		go func() {
			for task := range work {
				d, e := compare(ctx, state, s.Root, s.Dix, task.pair.Before, task.pair.After)
				if e != nil {
					d.Status = "error"
					d.Error = e.Error()
				}
				done <- result{task.key, d}
			}
		}()
	}
	for range len(pairs) {
		r := <-done
		out.Comparisons[r.key] = r.diff
	}
	out.All = HostComparison{Name: "All hosts", Pair: "all"}
	if len(hostIDs) > 0 && len(out.Hosts) == 0 {
		out.Comparisons[out.All.Pair] = Comparison{Status: "unchanged", Packages: []PackageDiff{}}
	} else {
		out.Comparisons[out.All.Pair] = s.combinedComparison(ctx, state, out.Hosts, pairs)
	}
	for key, c := range out.Comparisons {
		out.Comparisons[key] = filterPackages(c, rules)
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}
