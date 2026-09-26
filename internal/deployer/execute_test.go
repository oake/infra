package deployer

import (
	"context"
	"github.com/oake/infra/internal/api"
	"strings"
	"testing"
)

func testPath(hash, name string) string { return "/nix/store/" + strings.Repeat(hash, 32) + "-" + name }
func TestMappingMismatchNeverDeploys(t *testing.T) {
	j := api.Job{Repository: "owner/repo", Revision: strings.Repeat("a", 40), Node: "host", System: testPath("a", "system"), Activation: testPath("b", "activate")}
	e := Executor{Command: func(ctx context.Context, cmd string, args ...string) (string, error) {
		if cmd == "deploy" {
			t.Fatal("mismatched source deployed")
		}
		return testPath("c", "different"), nil
	}}
	r := e.Execute(t.Context(), j)
	if r.Outcome != "failed" || !strings.Contains(r.Detail, "does not match") {
		t.Fatalf("unexpected result %+v", r)
	}
}

func TestStageIsBootOnlyAndCannotBuild(t *testing.T) {
	root, activation := testPath("a", "system"), testPath("b", "activate")
	deployed := false
	ex := Executor{Command: func(ctx context.Context, name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch name {
		case "ssh":
			return "", nil
		case "nix":
			if strings.Contains(joined, "nixosConfigurations") {
				return root, nil
			}
			if strings.Contains(joined, "profiles.system.path") {
				return activation, nil
			}
			return `{"hostname":"host","user":"root","opts":[],"remoteBuild":false}`, nil
		case "deploy":
			deployed = true
			if strings.Contains(joined, "substituters") {
				t.Fatal("overrode flake substituters")
			}
			for _, want := range []string{"--boot", "--max-jobs 0", "--builders ", "--magic-rollback false", "--auto-rollback false", "--no-write-lock-file"} {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %s", want)
				}
			}
			return "", nil
		}
		t.Fatalf("unexpected command %s", name)
		return "", nil
	}}
	r := ex.Execute(t.Context(), api.Job{Repository: "owner/repo", Revision: strings.Repeat("a", 40), Node: "host", System: root, Activation: activation})
	if r.Outcome != "staged" || !deployed {
		t.Fatalf("unexpected result %+v", r)
	}
}
