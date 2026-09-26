package hub

import (
	"sort"

	"github.com/oake/infra/internal/api"
)

// A nil output list means the complete Buildbot evaluation was never received.
// Imported snapshots alone cannot prove that all attributes built successfully.
func (c Commit) OutputsKnown() bool { return c.Outputs != nil }

func (st *State) RefreshPullRequests() {
	for id, p := range st.PullRequests {
		p.Checks, p.FailedChecks = st.BuildStatus(p.Repository, p.Head)
		p.Hosts = []string{}
		// Derive impacted hosts from evaluated immutable paths, never from a PR title.
		head, hOK := st.Commits[api.ID(p.Repository, p.Head)]
		base, bOK := st.Commits[api.ID(p.Repository, p.Base)]
		p.Evaluated = hOK && bOK && head.Evaluation == "success" && base.Evaluation == "success" && head.OutputsKnown() && base.OutputsKnown()

		if p.Evaluated {
			old := map[string]string{}
			for _, m := range base.Mappings {
				old[m.Host] = m.System
			}
			for _, m := range head.Mappings {
				if old[m.Host] != m.System {
					p.Hosts = append(p.Hosts, m.Host)
				}
				delete(old, m.Host)
			}
			for host := range old {
				p.Hosts = append(p.Hosts, host)
			}
			sort.Strings(p.Hosts)
		}

		st.PullRequests[id] = p
	}
}

func (st *State) BuildStatus(repo, revision string) (string, []CheckFailure) {
	c, ok := st.Commits[api.ID(repo, revision)]
	if !ok {
		return "pending", nil
	}
	pending := c.Evaluation != "success" || !c.OutputsKnown()
	failures := []CheckFailure{}
	for name, detail := range c.Errors {
		failures = append(failures, CheckFailure{Name: name, Detail: detail, URL: c.LogURL})
	}
	if c.Evaluation == "failed" && len(failures) == 0 {
		failures = append(failures, CheckFailure{Name: "Evaluation", Detail: c.Detail, URL: c.LogURL})
	}
	sort.Slice(failures, func(i, j int) bool { return failures[i].Name < failures[j].Name })
	names := make([]string, 0, len(c.Outputs))
	for name := range c.Outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		a := st.Artifacts[c.Outputs[name]]
		switch a.Build {
		case "failed":
			failures = append(failures, CheckFailure{Name: name, Detail: a.Detail, URL: a.LogURL})
		case "success":
		default:
			pending = true
		}
	}
	if len(failures) > 0 {
		return "failure", failures
	}
	if pending {
		return "pending", nil
	}
	return "success", nil
}
