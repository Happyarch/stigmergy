// Package explore runs read-only exploration in a separate Codex process.
//
// It exists because Codex's native subagents are not a boundary: they inherit
// the parent's sandbox and can accept live permission overrides, so a subagent
// asked only to "look around" can still write to the tree and call stigmergy's
// mutating tools. A separate `codex exec` process can be genuinely confined —
// read-only sandbox, no approval escalation, no stigmergy tools — which is what
// makes it safe to fan out exploration without a swarm of agents writing
// memory and taking claims behind the root's back.
package explore

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// DefaultTimeout bounds an exploration. An explorer that never returns would
// otherwise hold up the root indefinitely.
const DefaultTimeout = 10 * time.Minute

// Request is one exploration.
type Request struct {
	Prompt   string
	Worktree string
	Timeout  time.Duration
	JSON     bool
}

// Args are the flags that confine the explorer. Each one is load-bearing:
//
//   - --sandbox read-only: it cannot write the tree, so it cannot make an edit
//     that dodges the claim guard.
//   - -c approval_policy="never": in a non-interactive run, Codex fails closed on
//     any escalation rather than silently prompting into the void. This is set as
//     a config override and not as --ask-for-approval, which does not exist on
//     `codex exec`: the flag is top-level only (the interactive TUI, where there
//     is a human to ask), and passing it to exec is a hard argument error that
//     kills the explorer before it starts. The value is validated — codex accepts
//     only untrusted|on-failure|on-request|granular|never — so a typo here fails
//     loudly rather than silently leaving the policy at its default.
//   - --ephemeral: no session state is carried between explorations.
//   - mcp_servers.stigmergy.enabled=false: the explorer cannot see stigmergy's
//     tools at all, so it cannot register a root, take a claim, or write a
//     memory. Findings go back to the root, which decides what to record.
//
// Note that an unknown -c key is accepted silently (only --strict-config rejects
// one), so a future confinement flag added here is worth testing against a live
// codex rather than trusting that it took.
func Args(req Request) []string {
	args := []string{
		"exec",
		"--sandbox", "read-only",
		"-c", `approval_policy="never"`,
		"--ephemeral",
		"-c", "mcp_servers.stigmergy.enabled=false",
	}
	if req.JSON {
		args = append(args, "--json")
	}
	return append(args, "--", req.Prompt)
}

// Run executes the explorer and streams its output.
func Run(ctx context.Context, req Request, stdout, stderr io.Writer) error {
	if req.Prompt == "" {
		return fmt.Errorf("an exploration needs a prompt")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return fmt.Errorf("codex is not on PATH, so exploration is unavailable: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "codex", Args(req)...)
	cmd.Dir = req.Worktree
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("the exploration exceeded its %s timeout and was stopped", timeout)
	}
	return err
}
