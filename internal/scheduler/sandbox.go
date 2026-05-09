package scheduler

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/rot13maxi/clankwork/internal/config"
)

const defaultSandboxBinary = "nono"

// wrapForSandbox rewrites (command, args) to invoke the configured sandbox
// (nono by default) wrapping the agent command. The agent worktree is always
// granted read-write; $CLANKWORK_HOME is always granted read-write so the
// agent can connect to the daemon socket. If the runtime's Sandbox.Enabled
// flag is false, the inputs are returned unchanged.
func wrapForSandbox(rt config.RuntimeConfig, workdir, homeDir, command string, args []string) (string, []string) {
	sb := rt.Sandbox
	if !sb.Enabled {
		return command, args
	}

	bin := sb.Command
	if bin == "" {
		bin = defaultSandboxBinary
	}

	out := []string{"run"}
	if workdir != "" {
		out = append(out, "--allow", filepath.Clean(workdir))
	}
	if homeDir != "" {
		// Read+write because the agent connects() to clankwork.sock, which
		// some kernels classify as a write op on the socket inode.
		out = append(out, "--allow", filepath.Clean(homeDir))
	}
	for _, p := range sb.ExtraReadPaths {
		if p == "" {
			continue
		}
		out = append(out, "--read", filepath.Clean(p))
	}
	for _, p := range sb.ExtraWritePaths {
		if p == "" {
			continue
		}
		out = append(out, "--allow", filepath.Clean(p))
	}
	if sb.Profile != "" {
		out = append(out, "--profile", sb.Profile)
	}
	if sb.BlockNet {
		out = append(out, "--block-net")
	}
	for _, d := range sb.AllowDomains {
		if d == "" {
			continue
		}
		out = append(out, "--allow-domain", d)
	}
	out = append(out, "--", command)
	out = append(out, args...)
	return bin, out
}

// preflightSandbox returns nil if the sandbox is disabled or the configured
// sandbox binary is on PATH and the transport is supported. Used at dispatch
// time so a misconfigured sandbox surfaces a clear error before we attempt to
// spawn the agent.
func preflightSandbox(rt config.RuntimeConfig) error {
	if !rt.Sandbox.Enabled {
		return nil
	}
	if config.RuntimeTransport(rt) != config.TransportACP {
		return fmt.Errorf("sandbox is only supported on the acp transport (got %q)", config.RuntimeTransport(rt))
	}
	bin := rt.Sandbox.Command
	if bin == "" {
		bin = defaultSandboxBinary
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("sandbox binary %q not found on PATH (install with `brew install nono` or set runtime.sandbox.command): %w", bin, err)
	}
	if rt.Sandbox.BlockNet && len(rt.Sandbox.AllowDomains) > 0 {
		return fmt.Errorf("sandbox.block_net is mutually exclusive with sandbox.allow_domains; pick one")
	}
	for _, p := range rt.Sandbox.ExtraReadPaths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("sandbox.extra_read_paths entry %q must be absolute", p)
		}
	}
	for _, p := range rt.Sandbox.ExtraWritePaths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("sandbox.extra_write_paths entry %q must be absolute", p)
		}
	}
	return nil
}
