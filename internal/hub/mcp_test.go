package hub

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oake/infra/internal/api"
	"github.com/oake/infra/web"
)

func TestMCPToolsOverHTTP(t *testing.T) {
	store := testStore(t)
	fixtureState, _, root := fixture(t)
	testDix(t)
	if err := store.Update(t.Context(), func(dst *State) error { *dst = *fixtureState; return nil }); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&Server{Store: store, Root: root}).Handler(web.Handler()))
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	info := session.InitializeResult().ServerInfo
	if info.Title != "Infra" || !strings.Contains(info.Description, "NixOS") || len(info.Icons) != 1 {
		t.Fatalf("metadata: %+v", info)
	}
	svg, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(info.Icons[0].Source, "data:image/svg+xml;base64,"))
	if err != nil || !bytes.Contains(svg, []byte("<svg")) {
		t.Fatalf("invalid SVG icon: %v", err)
	}
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 12 {
		t.Fatalf("got %d tools", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if tool.Title == "" || tool.Description == "" || tool.InputSchema == nil || tool.Annotations == nil {
			t.Fatalf("missing tool metadata: %+v", tool)
		}
		destructive := tool.Name == "unpause_deployment" || tool.Name == "retry_deployment" || tool.Name == "reboot_host" || tool.Name == "delete_package_block"
		read := !destructive && tool.Name != "add_package_block"
		if tool.Annotations.ReadOnlyHint != read || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != destructive {
			t.Fatalf("annotations: %+v", tool)
		}
	}
	call := func(name string, args map[string]any, wantError bool) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("%s: %+v", name, result)
		}
		if !wantError && result.StructuredContent == nil {
			t.Fatalf("%s missing structured output", name)
		}
		return result
	}
	for _, name := range []string{"list_repositories", "get_fleet", "list_pull_requests", "list_package_blocks"} {
		call(name, map[string]any{}, false)
	}
	call("get_fleet", map[string]any{"repository": "example/primary"}, false)
	call("get_host_timeline", map[string]any{"host": "example/primary/manual"}, false)
	call("compare_commits", map[string]any{"repository": "example/primary", "before": testPrior, "after": testMain}, false)
	for path := range fixtureState.Artifacts {
		call("compare_configurations", map[string]any{"before": path, "after": path}, false)
		break
	}
	call("unpause_deployment", map[string]any{"host": "example/primary/manual"}, true)

	call("add_package_block", map[string]any{"text": "linux", "match": "exact"}, false)
	rules, err := store.PackageBlocks(t.Context())
	if err != nil || rules[api.ID("exact", "linux")].Text != "linux" {
		t.Fatalf("block not saved: %v %v", rules, err)
	}
	call("delete_package_block", map[string]any{"id": api.ID("exact", "linux")}, false)
	rules, err = store.PackageBlocks(t.Context())
	if err != nil || len(rules) != 0 {
		t.Fatalf("block not removed: %v %v", rules, err)
	}
	call("add_package_block", map[string]any{"text": "linux", "match": "invalid"}, true)
	call("get_fleet", map[string]any{"repository": "invalid"}, true)
	call("compare_commits", map[string]any{"repository": "example/primary", "before": "bad", "after": "bad"}, true)
	call("compare_configurations", map[string]any{"before": "/etc/passwd", "after": "/etc/passwd"}, true)
	for _, name := range []string{"unpause_deployment", "retry_deployment", "reboot_host"} {
		call(name, map[string]any{"host": "example/primary/nonexistent"}, true)
	}
	// The MCP endpoint retains the hub's existing cross-origin mutation protection.
	req, _ := http.NewRequest("POST", server.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Origin", "https://other.example")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status: %d", response.StatusCode)
	}
}
