package hub

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oake/infra/internal/api"
	"github.com/oake/infra/web"
)

type repositoryArgs struct {
	Repository string `json:"repository,omitempty" jsonschema:"Optional owner/repo filter"`
}
type hostArgs struct {
	Host string `json:"host" jsonschema:"Fully qualified host ID: owner/repo/host"`
}
type timelineArgs struct {
	Host   string `json:"host" jsonschema:"Fully qualified host ID: owner/repo/host"`
	Cursor string `json:"cursor,omitempty" jsonschema:"Next cursor from the previous page"`
	Before string `json:"before,omitempty" jsonschema:"Optional before configuration store path"`
	After  string `json:"after,omitempty" jsonschema:"Optional after configuration store path"`
}
type commitArgs struct {
	Repository string `json:"repository" jsonschema:"Repository as owner/repo"`
	Before     string `json:"before" jsonschema:"Full 40-character base commit SHA"`
	After      string `json:"after" jsonschema:"Full 40-character head commit SHA"`
}
type configurationArgs struct {
	Before string `json:"before" jsonschema:"Before configuration Nix store path"`
	After  string `json:"after" jsonschema:"After configuration Nix store path"`
}
type blockArgs struct {
	Text  string `json:"text" jsonschema:"Package name or pattern to exclude from comparisons only"`
	Match string `json:"match" jsonschema:"Matching mode,enum=exact,enum=starts,enum=contains"`
}
type deleteBlockArgs struct {
	ID string `json:"id" jsonschema:"Comparison exclusion ID returned by list_package_blocks"`
}

// mcpTool uses inferred input schemas and returns both structured JSON and text.
func mcpTool[I any](server *mcp.Server, name, title, description string, read, destructive bool, fn func(context.Context, I) (any, error)) {
	mcp.AddTool(server, &mcp.Tool{
		Name: name, Title: title, Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: read, DestructiveHint: &destructive},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input I) (*mcp.CallToolResult, any, error) {
		out, err := fn(ctx, input)
		return nil, out, err
	})
}

func (s *Server) mcpHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name: "infra", Title: "Infra", Version: "1.0.0",
		Description: "Manage NixOS fleet auto deployments and track configurations.",
		Icons:       []mcp.Icon{{Source: web.IconDataURI(), MIMEType: "image/svg+xml", Sizes: []string{"any"}}},
	}, nil)
	mcpTool(server, "list_repositories", "List repositories", "List repositories tracked by the infra hub.", true, false,
		func(ctx context.Context, _ struct{}) (any, error) {
			rows, err := s.Store.DB.QueryContext(ctx, "SELECT id FROM repositories ORDER BY id")
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			ids := []string{}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return nil, err
				}
				ids = append(ids, id)
			}
			return map[string]any{"repositories": ids}, rows.Err()
		})
	mcpTool(server, "get_fleet", "Get fleet status", "Get hosts, deployment status, and active and desired configurations. Optionally filter by repository.", true, false,
		func(ctx context.Context, a repositoryArgs) (any, error) {
			if err := validRepositoryFilter(a.Repository); err != nil {
				return nil, err
			}
			fleet, err := s.Store.Fleet(ctx)
			if a.Repository != "" {
				for id, h := range fleet.Hosts {
					if h.Repository != a.Repository {
						delete(fleet.Hosts, id)
					}
				}
				for id := range fleet.Repositories {
					if id != a.Repository {
						delete(fleet.Repositories, id)
					}
				}
				for id, p := range fleet.PullRequests {
					if p.Repository != a.Repository {
						delete(fleet.PullRequests, id)
					}
				}
			}
			return fleet, err
		})
	mcpTool(server, "list_pull_requests", "List pull requests", "List tracked pull requests with check status and affected hosts. Optionally filter by repository.", true, false,
		func(ctx context.Context, a repositoryArgs) (any, error) {
			if err := validRepositoryFilter(a.Repository); err != nil {
				return nil, err
			}
			tx, err := s.Store.DB.BeginTx(ctx, nil)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback()
			prs, err := readUIRows[PullRequest](ctx, tx, "SELECT id,body FROM pull_requests")
			for id, p := range prs {
				if a.Repository != "" && p.Repository != a.Repository {
					delete(prs, id)
				}
			}
			return map[string]any{"pull_requests": prs}, err
		})
	mcpTool(server, "get_host_timeline", "Get host configuration history", "Get configuration history, staged configuration and latest deployment attempt, with pagination.", true, false,
		func(ctx context.Context, a timelineArgs) (any, error) {
			if err := validHost(a.Host); err != nil {
				return nil, err
			}
			return s.Store.HostTimeline(ctx, a.Host, a.Cursor, a.Before, a.After)
		})
	mcpTool(server, "compare_commits", "Compare commits", "Compare revisions, including flake inputs, affected hosts and package changes. Applies package comparison exclusions.", true, false,
		func(ctx context.Context, a commitArgs) (any, error) {
			if !api.Repository.MatchString(a.Repository) || !api.Revision.MatchString(a.Before) || !api.Revision.MatchString(a.After) {
				return nil, errors.New("provide owner/repo and two full commit SHAs")
			}
			return s.CompareCommits(ctx, a.Repository, a.Before, a.After)
		})
	mcpTool(server, "compare_configurations", "Compare configurations", "Compare packages and closure sizes between two Nix store configurations. Applies package comparison exclusions.", true, false,
		func(ctx context.Context, a configurationArgs) (any, error) {
			if !api.StorePath.MatchString(a.Before) || !api.StorePath.MatchString(a.After) {
				return nil, errors.New("provide two valid Nix store paths")
			}
			state, err := s.Store.DiffArtifacts(ctx, a.Before, a.After)
			if err != nil {
				return nil, err
			}
			c, err := compare(ctx, state, s.Root, a.Before, a.After)
			if err != nil {
				return nil, err
			}
			rules, err := s.Store.PackageBlocks(ctx)
			if err != nil {
				return nil, err
			}
			return filterPackages(c, rules), nil
		})
	for _, action := range []struct{ name, title, description, action string }{
		{"unpause_deployment", "Unpause deployment", "Unpause deployment to a host's known target, subject to existing eligibility checks. Can change the running system.", "deploy"},
		{"retry_deployment", "Retry deployment", "Retry a failed or interrupted deployment. Can change the running system.", "retry"},
		{"reboot_host", "Reboot host", "Queue a host reboot, subject to existing eligibility checks. Interrupts running workloads.", "reboot"},
	} {
		mcpTool(server, action.name, action.title, action.description, false, true,
			func(ctx context.Context, a hostArgs) (any, error) {
				if err := validHost(a.Host); err != nil {
					return nil, err
				}
				return s.mcpMutation(ctx, func(state *State) error { return state.hostAction(a.Host, action.action) })
			})
	}
	mcpTool(server, "list_package_blocks", "List package comparison exclusions", "List rules that hide packages from comparison output only.", true, false,
		func(ctx context.Context, _ struct{}) (any, error) {
			rules, err := s.Store.PackageBlocks(ctx)
			return map[string]any{"package_blocks": rules}, err
		})
	mcpTool(server, "add_package_block", "Add package comparison exclusion", "Add an exact, prefix or substring exclusion for all package comparisons. Does not affect installed packages or deployments.", false, false,
		func(ctx context.Context, a blockArgs) (any, error) {
			a.Text = strings.TrimSpace(a.Text)
			if a.Text == "" || len(a.Text) > 512 || (a.Match != "exact" && a.Match != "starts" && a.Match != "contains") {
				return nil, errors.New("provide text (1–512 bytes) and match: exact, starts or contains")
			}
			return s.mcpMutation(ctx, func(state *State) error {
				state.PackageBlocks[api.ID(a.Match, a.Text)] = PackageBlock{Text: a.Text, Match: a.Match}
				return nil
			})
		})
	mcpTool(server, "delete_package_block", "Delete package comparison exclusion", "Delete a comparison exclusion, making matching packages visible in comparisons again. Does not affect installed packages or deployments.", false, true,
		func(ctx context.Context, a deleteBlockArgs) (any, error) {
			if a.ID == "" {
				return nil, errors.New("provide an exclusion ID")
			}
			return s.mcpMutation(ctx, func(state *State) error { delete(state.PackageBlocks, a.ID); return nil })
		})
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

func validRepositoryFilter(repository string) error {
	if repository != "" && !api.Repository.MatchString(repository) {
		return errors.New("repository must be owner/repo")
	}
	return nil
}
func validHost(host string) error {
	parts := strings.Split(host, "/")
	if len(parts) != 3 || !api.Repository.MatchString(strings.Join(parts[:2], "/")) || !api.Name.MatchString(parts[2]) {
		return errors.New("host must be owner/repo/host")
	}
	return nil
}
func (s *Server) mcpMutation(ctx context.Context, fn func(*State) error) (any, error) {
	err := s.Store.Update(ctx, func(state *State) error {
		if err := fn(state); err != nil {
			return err
		}
		state.Reconcile(time.Now().UTC())
		return nil
	})
	return map[string]bool{"ok": err == nil}, err
}
