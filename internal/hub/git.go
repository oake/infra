package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oake/infra/internal/api"
)

// GitRepos owns bare repositories. Buildbot never supplies Git metadata.
type GitRepos struct {
	Root   string
	Token  string
	mu     sync.Mutex
	remote func(string) string // local repository transport in tests
}
type GitHistory struct {
	Main        string
	MainHistory []string
	Commits     map[string]UICommit
}

func (g *GitRepos) path(repo string) (string, error) {
	if !api.Repository.MatchString(repo) {
		return "", fmt.Errorf("invalid repository")
	}
	return filepath.Join(g.Root, "repos", repo+".git"), nil
}
func (g *GitRepos) command(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if path != "" {
		args = append([]string{"--git-dir=" + path}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if g.Token != "" {
		header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+g.Token))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0="+header)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
func (g *GitRepos) ensure(ctx context.Context, repo string) (string, error) {
	path, err := g.path(repo)
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return path, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if err = os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(parent, ".clone-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	remote := "https://github.com/" + repo + ".git"
	if g.remote != nil {
		remote = g.remote(repo)
	}
	if _, err = g.command(ctx, "", "clone", "--bare", "--no-hardlinks", remote, tmp); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}
func (g *GitRepos) ensureCommit(ctx context.Context, path, rev string) error {
	if !api.Revision.MatchString(rev) {
		return fmt.Errorf("invalid revision")
	}
	if _, err := g.command(ctx, path, "cat-file", "-e", rev+"^{commit}"); err == nil {
		return nil
	}
	_, err := g.command(ctx, path, "fetch", "--no-tags", "origin", rev+":refs/infra/"+rev)
	return err
}
func (g *GitRepos) Sync(ctx context.Context, repo string, wanted []string) (GitHistory, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := GitHistory{Commits: map[string]UICommit{}}
	path, err := g.ensure(ctx, repo)
	if err != nil {
		return out, err
	}
	if _, err = g.command(ctx, path, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return out, err
	}
	for _, rev := range wanted {
		// Unavailable build revisions must not prevent current main from advancing.
		if err = g.ensureCommit(ctx, path, rev); err != nil {
			slog.Warn("Git revision unavailable", "repository", repo, "revision", rev, "error", err)
		}
	}
	raw, err := g.command(ctx, path, "rev-list", "--first-parent", "refs/heads/main")
	if err != nil {
		return out, err
	}
	out.MainHistory = strings.Fields(string(raw))
	if len(out.MainHistory) == 0 {
		return out, fmt.Errorf("empty main history")
	}
	out.Main = out.MainHistory[0]
	raw, err = g.command(ctx, path, "log", "--all", "--format=%H%x00%ct%x00%s")
	if err != nil {
		return out, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		fields := strings.SplitN(line, "\x00", 3)
		if len(fields) != 3 || !api.Revision.MatchString(fields[0]) {
			return out, fmt.Errorf("invalid Git commit metadata")
		}
		stamp, e := strconv.ParseInt(fields[1], 10, 64)
		if e != nil {
			return out, e
		}
		out.Commits[fields[0]] = UICommit{Repository: repo, Revision: fields[0], Title: fields[2], Created: time.Unix(stamp, 0).UTC()}
	}
	for _, rev := range out.MainHistory {
		c := out.Commits[rev]
		c.Branch = "main"
		out.Commits[rev] = c
	}
	raw, err = g.command(ctx, path, "for-each-ref", "--format=%(refname:strip=2)", "refs/heads")
	if err != nil {
		return out, err
	}
	for _, branch := range strings.Fields(string(raw)) {
		if branch == "main" {
			continue
		}
		revisions, e := g.command(ctx, path, "rev-list", "refs/heads/"+branch)
		if e != nil {
			return out, e
		}
		for _, rev := range strings.Fields(string(revisions)) {
			c := out.Commits[rev]
			if c.Branch == "" {
				c.Branch = branch
				out.Commits[rev] = c
			}
		}
	}
	return out, nil
}
func (st *State) ApplyGit(repo string, history GitHistory, now time.Time) error {
	r, ok := st.Repositories[repo]
	if !ok {
		return fmt.Errorf("unknown repository")
	}
	// Branch membership is a current Git fact, not permanent commit metadata.
	for id, c := range st.Commits {
		if c.Repository == repo {
			c.Branch = history.Commits[c.Revision].Branch
			st.Commits[id] = c
		}
	}
	for rev, meta := range history.Commits {
		id := api.ID(repo, rev)
		c := st.Commits[id]
		c.ID, c.Repository, c.Revision, c.Title, c.Created = id, repo, rev, meta.Title, meta.Created
		c.Branch = meta.Branch
		if c.Evaluation == "" {
			c.Evaluation = "pending"
		}
		st.Commits[id] = c
	}
	r.Main, r.MainHistory, r.RefAt = history.Main, history.MainHistory, now
	st.Repositories[repo] = r
	return st.inventoryFromCommit(st.Commits[api.ID(repo, r.Main)])
}

// Pair metadata is cheap local Git work, cached by the immutable revisions.
type GitPair struct {
	Base   *UICommit     `json:"base"`
	Head   *UICommit     `json:"head"`
	Inputs []InputChange `json:"inputs"`
}

func (g *GitRepos) pair(ctx context.Context, repo, old, next string, mergeBase bool) (GitPair, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out GitPair
	path, err := g.ensure(ctx, repo)
	if err != nil {
		return out, err
	}
	if !api.Revision.MatchString(old) || !api.Revision.MatchString(next) {
		return out, fmt.Errorf("invalid revision")
	}
	key := api.ID(repo, old, next, fmt.Sprint(mergeBase))
	cache := filepath.Join(g.Root, "git-comparisons", key+".json")
	if raw, e := os.ReadFile(cache); e == nil && json.Unmarshal(raw, &out) == nil && out.Base != nil && out.Head != nil {
		return out, nil
	}
	for _, rev := range []string{old, next} {
		if err = g.ensureCommit(ctx, path, rev); err != nil {
			return out, err
		}
	}
	if mergeBase {
		raw, e := g.command(ctx, path, "merge-base", old, next)
		if e != nil {
			return out, e
		}
		old = strings.TrimSpace(string(raw))
	}
	metadata := func(rev string) (*UICommit, error) {
		raw, e := g.command(ctx, path, "show", "-s", "--format=%ct%x00%s", rev)
		if e != nil {
			return nil, e
		}
		fields := strings.SplitN(strings.TrimSuffix(string(raw), "\n"), "\x00", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid Git metadata")
		}
		stamp, e := strconv.ParseInt(fields[0], 10, 64)
		if e != nil {
			return nil, e
		}
		return &UICommit{Repository: repo, Revision: rev, Title: fields[1], Created: time.Unix(stamp, 0).UTC()}, nil
	}
	if out.Base, err = metadata(old); err != nil {
		return out, err
	}
	if out.Head, err = metadata(next); err != nil {
		return out, err
	}
	inputs := func(rev string) (map[string]json.RawMessage, error) {
		// An absent lockfile is an empty input set; Git failures are not cached.
		files, e := g.command(ctx, path, "ls-tree", "--name-only", rev, "--", "flake.lock")
		if e != nil {
			return nil, e
		}
		if len(files) == 0 {
			return map[string]json.RawMessage{}, nil
		}
		raw, e := g.command(ctx, path, "show", rev+":flake.lock")
		if e != nil {
			return nil, e
		}
		return topLevelInputs(raw)
	}
	before, err := inputs(old)
	if err != nil {
		return out, err
	}
	after, err := inputs(next)
	if err != nil {
		return out, err
	}
	out.Inputs = lockChanges(before, after)
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if err = os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		return out, err
	}
	// The lock excludes readers in this process; interrupted files are recomputed.
	err = os.WriteFile(cache, raw, 0600)
	return out, err
}
func (s *Server) SyncRepositories(ctx context.Context) error {
	if s.Git == nil {
		return nil
	}
	s.gitSyncMu.Lock()
	defer s.gitSyncMu.Unlock()
	state, err := s.Store.Read(ctx)
	if err != nil {
		return err
	}
	repos := make([]string, 0, len(state.Repositories))
	for repo := range state.Repositories {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	var failures []error
	for _, repo := range repos {
		wanted := []string{}
		for _, c := range state.Commits {
			if c.Repository == repo && c.Created.IsZero() && c.Evaluation != "pending" {
				wanted = append(wanted, c.Revision)
			}
		}
		h, e := s.Git.Sync(ctx, repo, wanted)
		if e != nil {
			failures = append(failures, fmt.Errorf("%s: %w", repo, e))
			continue
		}
		e = s.Store.Update(ctx, func(st *State) error {
			if err := st.ApplyGit(repo, h, time.Now().UTC()); err != nil {
				return err
			}
			st.Reconcile(time.Now().UTC())
			return nil
		})
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (s *Server) NotifyEvaluation() {
	if s.gitWake != nil {
		select {
		case s.gitWake <- struct{}{}:
		default:
		}
	}
}
func (s *Server) RunRepositories(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if err := s.SyncRepositories(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("Git sync", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.gitWake:
		}
	}
}
func (s *Server) EnableGit(token string) {
	s.Git = &GitRepos{Root: s.Root, Token: token}
	s.gitWake = make(chan struct{}, 1)
}
