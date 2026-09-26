package deployer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/oake/infra/internal/api"
)

type Executor struct {
	Command func(context.Context, string, ...string) (string, error)
}

func (e Executor) Execute(ctx context.Context, j api.Job) api.Result {
	result := func(outcome string, err error) api.Result {
		detail := "Staged for next boot. Runtime convergence awaits a beacon observation."
		if err != nil {
			detail = err.Error()
		}
		if len(detail) > 32768 {
			detail = detail[len(detail)-32768:]
		}
		return api.Result{Outcome: outcome, Detail: detail}
	}
	if !api.Repository.MatchString(j.Repository) || !api.Revision.MatchString(j.Revision) || !api.Name.MatchString(j.Node) || !api.StorePath.MatchString(j.System) || !api.StorePath.MatchString(j.Activation) {
		return result("failed", errors.New("invalid immutable deployment target"))
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	run := e.Command
	if run == nil {
		run = command
	}
	source := "github:" + j.Repository + "/" + j.Revision
	// Refuse a mismatched mapping before invoking deploy-rs. Automatic staging is
	// intentionally NixOS-only; Darwin is monitored until a manual policy exists.
	system, err := run(ctx, "nix", "eval", "--raw", source+"#nixosConfigurations."+j.Node+".config.system.build.toplevel", "--no-write-lock-file")
	if err != nil {
		return result("failed", err)
	}
	if strings.TrimSpace(system) != j.System {
		return result("failed", errors.New("assigned system does not match immutable source"))
	}
	activation, err := run(ctx, "nix", "eval", "--raw", source+"#deploy.nodes."+j.Node+".profiles.system.path", "--no-write-lock-file")
	if err != nil {
		return result("failed", err)
	}
	if strings.TrimSpace(activation) != j.Activation {
		return result("failed", errors.New("assigned activation does not match immutable source"))
	}
	// Read the same target settings that deploy-rs inherits from its flake.
	apply := fmt.Sprintf(`d: let n = d.nodes.%s; p = n.profiles.system; in { hostname = n.hostname; user = p.sshUser or n.sshUser or d.sshUser or "root"; opts = p.sshOpts or n.sshOpts or d.sshOpts or []; remoteBuild = p.remoteBuild or n.remoteBuild or d.remoteBuild or false; }`, j.Node)
	settings, err := run(ctx, "nix", "eval", "--json", source+"#deploy", "--apply", apply, "--no-write-lock-file")
	if err != nil {
		return result("failed", err)
	}
	var ssh struct {
		Hostname    string   `json:"hostname"`
		User        string   `json:"user"`
		Opts        []string `json:"opts"`
		RemoteBuild bool     `json:"remoteBuild"`
	}
	if err = json.Unmarshal([]byte(settings), &ssh); err != nil {
		return result("failed", err)
	}
	if ssh.RemoteBuild {
		return result("failed", errors.New("remoteBuild must be disabled for cache-only staging"))
	}
	if ssh.Hostname == "" || strings.HasPrefix(ssh.Hostname, "-") || strings.ContainsAny(ssh.Hostname, " \n\r\t") || !api.Name.MatchString(ssh.User) {
		return result("failed", errors.New("invalid SSH destination"))
	}
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "-o", "StrictHostKeyChecking=yes"}
	sshArgs = append(sshArgs, ssh.Opts...)
	sshArgs = append(sshArgs, ssh.User+"@"+ssh.Hostname, "true")
	probe, cancelProbe := context.WithTimeout(ctx, 15*time.Second)
	_, err = run(probe, "ssh", sshArgs...)
	cancelProbe()
	if err != nil {
		if strings.Contains(err.Error(), "Connection timed out") || strings.Contains(err.Error(), "Connection refused") || strings.Contains(err.Error(), "No route to host") || strings.Contains(err.Error(), "network is unreachable") || errors.Is(err, context.DeadlineExceeded) {
			return result("unreachable", err)
		}
		return result("failed", err)
	}
	// No local or remote builders. If substitution cannot realize the exact
	// artifacts, deployment fails rather than compiling a system unexpectedly.
	_, err = run(ctx, "deploy", source+"#"+j.Node+".system", "--boot", "--magic-rollback", "false", "--auto-rollback", "false", "--rollback-succeeded", "false", "--interactive-sudo", "false", "--skip-checks", "--", "--no-write-lock-file", "--max-jobs", "0", "--builders", "")
	if err != nil {
		if ctx.Err() != nil {
			return result("ambiguous", err)
		}
		return result("failed", err)
	}
	return result("staged", nil)
}

type tail struct{ data []byte }

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	t.data = append(t.data, p...)
	if len(t.data) > 32768 {
		t.data = t.data[len(t.data)-32768:]
	}
	return n, nil
}
func command(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr tail
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	e := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if e != nil {
		return "", fmt.Errorf("%s: %w: %s", name, e, string(stderr.data))
	}
	return stdout.String(), nil
}
