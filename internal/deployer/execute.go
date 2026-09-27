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
		if j.Kind == "reboot" && !j.Expires.IsZero() && !time.Now().Before(j.Expires) {
			outcome = "expired"
		}
		detail := "Staged for next boot. Runtime convergence awaits a beacon observation."
		if err != nil {
			detail = err.Error()
		}
		if len(detail) > 32768 {
			detail = detail[len(detail)-32768:]
		}
		return api.Result{Outcome: outcome, Detail: detail}
	}
	if !api.Repository.MatchString(j.Repository) || !api.Revision.MatchString(j.Revision) || !api.Name.MatchString(j.Node) || (j.Kind != "reboot" && (!api.StorePath.MatchString(j.System) || !api.StorePath.MatchString(j.Activation))) {
		return result("failed", errors.New("invalid immutable deployment target"))
	}
	if j.Kind != "" && j.Kind != "reboot" {
		return result("failed", errors.New("unknown job kind"))
	}
	if j.Kind == "reboot" && (j.Expires.IsZero() || !time.Now().Before(j.Expires)) {
		return result("expired", errors.New("reboot request expired"))
	}
	deadline := time.Now().Add(30 * time.Minute)
	if j.Kind == "reboot" {
		deadline = j.Expires
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	run := e.Command
	if run == nil {
		run = command
	}
	source := "github:" + j.Repository + "/" + j.Revision
	// Refuse a mismatched mapping before invoking deploy-rs. Automatic staging is
	// intentionally NixOS-only; Darwin is monitored until a manual policy exists.
	if j.Kind != "reboot" {
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
	}
	// Read the same target settings that deploy-rs inherits from its flake.
	expression := fmt.Sprintf(`let f = builtins.getFlake %q; d = f.deploy; n = d.nodes.%s; p = n.profiles.system; hosts = f.nixosConfigurations.%s.config.infra.deploy.fqdn; in { hostnames = if builtins.isList hosts then hosts else [ hosts ]; user = p.sshUser or n.sshUser or d.sshUser or "root"; opts = p.sshOpts or n.sshOpts or d.sshOpts or []; remoteBuild = p.remoteBuild or n.remoteBuild or d.remoteBuild or false; }`, source, j.Node, j.Node)
	settings, err := run(ctx, "nix", "eval", "--json", "--expr", expression, "--no-write-lock-file")
	if err != nil {
		return result("failed", err)
	}
	var ssh struct {
		Hostnames   []string `json:"hostnames"`
		User        string   `json:"user"`
		Opts        []string `json:"opts"`
		RemoteBuild bool     `json:"remoteBuild"`
	}
	if err = json.Unmarshal([]byte(settings), &ssh); err != nil {
		return result("failed", err)
	}
	if ssh.RemoteBuild && j.Kind != "reboot" {
		return result("failed", errors.New("remoteBuild must be disabled for cache-only staging"))
	}
	if len(ssh.Hostnames) == 0 || !api.Name.MatchString(ssh.User) {
		return result("failed", errors.New("invalid SSH destination"))
	}
	for _, hostname := range ssh.Hostnames {
		if hostname == "" || strings.HasPrefix(hostname, "-") || strings.ContainsAny(hostname, " \n\r\t") {
			return result("failed", errors.New("invalid SSH destination"))
		}
	}
	sshBase := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "-o", "StrictHostKeyChecking=yes"}
	sshBase = append(sshBase, ssh.Opts...)
	var sshArgs []string
	var hostname string
	failures := []string{}
	for _, candidate := range ssh.Hostnames {
		if ctx.Err() != nil {
			return result("unreachable", ctx.Err())
		}
		sshArgs = append(append([]string{}, sshBase...), ssh.User+"@"+candidate, "true")
		probe, cancelProbe := context.WithTimeout(ctx, 15*time.Second)
		_, err = run(probe, "ssh", sshArgs...)
		probeErr := probe.Err()
		cancelProbe()
		if err == nil {
			hostname = candidate
			break
		}
		if !unreachable(err) && !errors.Is(probeErr, context.DeadlineExceeded) {
			return result("failed", fmt.Errorf("%s: %w", candidate, err))
		}
		failures = append(failures, candidate+": "+err.Error())
	}
	if hostname == "" {
		return result("unreachable", errors.New(strings.Join(failures, "\n")))
	}
	if j.Kind == "reboot" {
		if ctx.Err() != nil || !time.Now().Before(j.Expires) {
			return result("expired", errors.New("reboot request expired"))
		}
		privilege := ""
		if ssh.User != "root" {
			privilege = "sudo -n "
		}
		remote := privilege + "/run/current-system/sw/bin/systemctl reboot --no-block"
		sshArgs[len(sshArgs)-1] = remote
		_, err = run(ctx, "ssh", sshArgs...)
		if err != nil {
			var exit *exec.ExitError
			if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() != 255 {
				return result("failed", err)
			}
			// The host may have rebooted before SSH could acknowledge the command.
			// Never repeat an uncertain reboot.
			return result("ambiguous", err)
		}
		return api.Result{Outcome: "rebooting", Detail: "Reboot command accepted. Awaiting beacon confirmation."}
	}
	// No local or remote builders. If substitution cannot realize the exact
	// artifacts, deployment fails rather than compiling a system unexpectedly.
	_, err = run(ctx, "deploy", source+"#"+j.Node+".system", "--hostname", hostname, "--boot", "--magic-rollback", "false", "--auto-rollback", "false", "--rollback-succeeded", "false", "--interactive-sudo", "false", "--skip-checks", "--", "--no-write-lock-file", "--max-jobs", "0", "--builders", "")
	if err != nil {
		if ctx.Err() != nil {
			return result("ambiguous", err)
		}
		return result("failed", err)
	}
	return result("staged", nil)
}

func unreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, text := range []string{"connection timed out", "connection refused", "no route to host", "network is unreachable", "network is down", "could not resolve hostname", "temporary failure in name resolution"} {
		if strings.Contains(message, text) {
			return true
		}
	}
	return false
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
