package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/oake/infra/internal/api"
)

// GitHub is used only for read-only PR information.
type GitHub struct {
	Token   string
	BaseURL string
	Client  *http.Client
}

func (g *GitHub) get(ctx context.Context, path string, out any) error {
	base := g.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var failure struct{ Message string }
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&failure)
		return fmt.Errorf("GitHub: HTTP %d %s", res.StatusCode, failure.Message)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(out)
}

type githubPR struct {
	Number int
	Title  string
	State  string
	Draft  bool
	Head   struct {
		SHA string
		Ref string
	}
	Base struct{ SHA string }
}

func (g *GitHub) list(ctx context.Context, repo string) ([]githubPR, error) {
	all := []githubPR{}
	for page := 1; ; page++ {
		var batch []githubPR
		if err := g.get(ctx, fmt.Sprintf("/repos/%s/pulls?state=open&per_page=100&page=%d", repo, page), &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil
		}
	}
}
func topLevelInputs(raw []byte) (map[string]json.RawMessage, error) {
	var lock struct {
		Root  string
		Nodes map[string]struct {
			Locked json.RawMessage
			Inputs map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, err
	}
	if _, ok := lock.Nodes[lock.Root]; !ok {
		return nil, fmt.Errorf("missing lockfile root")
	}
	var resolve func(json.RawMessage, map[string]bool) (string, error)
	resolve = func(ref json.RawMessage, seen map[string]bool) (string, error) {
		key := string(ref)
		if seen[key] {
			return "", fmt.Errorf("cyclic lockfile follows")
		}
		seen[key] = true
		defer delete(seen, key)
		var id string
		if json.Unmarshal(ref, &id) == nil {
			if _, ok := lock.Nodes[id]; !ok {
				return "", fmt.Errorf("missing lockfile node")
			}
			return id, nil
		}
		var path []string
		if err := json.Unmarshal(ref, &path); err != nil {
			return "", err
		}
		id = lock.Root
		for _, name := range path {
			next, ok := lock.Nodes[id].Inputs[name]
			if !ok {
				return "", fmt.Errorf("missing followed input")
			}
			var err error
			id, err = resolve(next, seen)
			if err != nil {
				return "", err
			}
		}
		return id, nil
	}
	inputs := map[string]json.RawMessage{}
	for name, ref := range lock.Nodes[lock.Root].Inputs {
		id, err := resolve(ref, map[string]bool{})
		if err != nil {
			return nil, err
		}
		if locked := lock.Nodes[id].Locked; len(locked) > 0 {
			inputs[name] = locked
		}
	}
	return inputs, nil
}

type lockedInput struct {
	Type         string
	Owner        string
	Repo         string
	Rev          string
	URL          string
	LastModified int64 `json:"lastModified"`
}

func (i lockedInput) date() string {
	if i.LastModified == 0 {
		return ""
	}
	return time.Unix(i.LastModified, 0).UTC().Format(time.RFC3339)
}
func (i lockedInput) githubRepo() string {
	if i.Type == "github" && api.Repository.MatchString(i.Owner+"/"+i.Repo) {
		return i.Owner + "/" + i.Repo
	}
	u, err := url.Parse(i.URL)
	if err == nil && u.Host == "github.com" {
		repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		if api.Repository.MatchString(repo) {
			return repo
		}
	}
	return ""
}

func lockChanges(before, after map[string]json.RawMessage) []InputChange {
	names := map[string]bool{}
	for k := range before {
		names[k] = true
	}
	for k := range after {
		names[k] = true
	}
	changes := []InputChange{}
	describe := func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return ""
		}
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		if rev, ok := v["rev"].(string); ok {
			return rev
		}
		return string(raw)
	}
	for name := range names {
		var a, b any
		_ = json.Unmarshal(before[name], &a)
		_ = json.Unmarshal(after[name], &b)
		aa, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		if string(aa) != string(bb) {
			var old, next lockedInput
			_ = json.Unmarshal(before[name], &old)
			_ = json.Unmarshal(after[name], &next)
			change := InputChange{Name: name, Before: describe(before[name]), After: describe(after[name]), BeforeDate: old.date(), AfterDate: next.date()}
			oldRepo, nextRepo := old.githubRepo(), next.githubRepo()
			if oldRepo != "" && nextRepo != "" && api.Revision.MatchString(old.Rev) && api.Revision.MatchString(next.Rev) {
				target := next.Rev
				if oldRepo != nextRepo {
					target = nextRepo + ":" + next.Rev
				}
				change.CompareURL = "https://github.com/" + oldRepo + "/compare/" + old.Rev + "..." + target
			}
			changes = append(changes, change)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	return changes
}
func (s *Server) PollPullRequests(ctx context.Context) error {
	state, err := s.Store.Read(ctx)
	if err != nil {
		return err
	}
	var problems []error
	for repo := range state.Repositories {
		if !api.Repository.MatchString(repo) {
			continue
		}
		prs, e := s.GitHub.list(ctx, repo)
		if e != nil {
			problems = append(problems, e)
			if err := s.Store.Update(ctx, func(st *State) error {
				r := st.Repositories[repo]
				r.PRError = e.Error()
				st.Repositories[repo] = r
				return nil
			}); err != nil {
				return err
			}
			continue
		}
		updates := map[string]PullRequest{}
		for _, pr := range prs {
			if !api.Revision.MatchString(pr.Head.SHA) || !api.Revision.MatchString(pr.Base.SHA) {
				return fmt.Errorf("invalid GitHub revision")
			}
			p := PullRequest{ID: api.ID(repo, fmt.Sprint(pr.Number)), Repository: repo, Number: pr.Number, Title: pr.Title, Head: pr.Head.SHA, State: "open", Checks: "unknown", Inputs: []InputChange{}, Hosts: []string{}, Draft: pr.Draft}
			var detailErrors []string
			p.BaseHead = pr.Base.SHA
			previous := state.PullRequests[p.ID]
			if previous.InputsCached && previous.Head == p.Head && previous.BaseHead == p.BaseHead {
				p.Base, p.Inputs, p.InputsCached, p.HeadCommit = previous.Base, previous.Inputs, true, previous.HeadCommit
			} else if s.Git != nil {
				pair, err := s.Git.pair(ctx, repo, p.BaseHead, p.Head, true)
				if err != nil {
					detailErrors = append(detailErrors, "Git comparison unavailable: "+err.Error())
				} else {
					p.Base, p.Inputs, p.InputsCached, p.HeadCommit = pair.Base.Revision, pair.Inputs, true, pair.Head
				}
			} else {
				detailErrors = append(detailErrors, "Git mirror unavailable")
			}
			p.Detail = strings.Join(detailErrors, "; ")
			updates[p.ID] = p
		}
		err = s.Store.Update(ctx, func(st *State) error {
			for id, p := range st.PullRequests {
				if p.Repository == repo {
					if _, ok := updates[id]; !ok {
						delete(st.PullRequests, id)
					}
				}
			}
			for id, p := range updates {
				st.PullRequests[id] = p
			}
			r := st.Repositories[repo]
			r.PRError = ""
			st.Repositories[repo] = r
			return nil
		})
		if err != nil {
			return err
		}
	}
	return errors.Join(problems...)
}
