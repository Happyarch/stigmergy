package deliberate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// BwrapArgs builds the argv that confines a worker.
//
// The worker runs at the **real repository path** and may write anything,
// anywhere. None of it is real: the repository and the home directory are
// overlaid, so every write lands in an invisible tmpfs that dies with the
// process. What survives is only what we bind back on top — the host's own state
// directory, without which session resume dies.
//
// This is the boundary. Not the hosts' permission flags — those handle "do not
// ask", and they have been wrong about "do not reach" every time we checked:
// codex's --ask-for-approval does not exist, opencode's permission.ask was never
// implemented, claude's bypassPermissions escapes its workspace outright, and agy
// has no filesystem boundary at all. The kernel has not been wrong once.
//
// Why an overlay rather than a git worktree, which this replaces:
//
//   - **Tools work.** A worker under a plain --ro-bind can run a compiler but not
//     cache anything: GOCACHE, ~/.cargo and pip's site-packages are all read-only,
//     so real builds fail. Overlaying $HOME makes every one of them writable at
//     once, with no per-tool enumeration to keep up to date.
//   - **The worker sees the truth.** A worktree is checked out at HEAD, so
//     uncommitted work and ignored files are invisible — which for this repository
//     meant 44K of the user's own notes and, worse, whichever project skills
//     happened not to be committed. The overlay shows the working tree as it is.
//   - **Nothing to clean up.** No worktree to create, prune, sweep, or orphan when
//     a run is killed. The isolation ends when the process does.
//
// Order is the whole correctness of this function: bwrap applies operations in
// sequence and a later mount masks an earlier one. Masking mounts first, overlays
// next, and the binds that must survive them last.
func BwrapArgs(repo string, stateDirs []string, cmd []string) []string {
	args := []string{
		// Everything, read-only. The base layer.
		"--ro-bind", "/", "/",
		"--dev-bind", "/dev", "/dev",
		"--proc", "/proc",
		// A fresh writable /tmp, before anything of ours could live under it.
		"--tmpfs", "/tmp",
	}

	// Shadow the repository: the worker works at the real path and its writes go
	// nowhere.
	args = append(args, "--overlay-src", repo, "--tmp-overlay", repo)

	// Hide the run directory from the workers.
	//
	// The driver keeps every turn of every agent under .git/deliberate/<run-id>/,
	// and a worker now stands at the repository root with that directory in plain
	// sight. Readable, it is an out-of-band channel that quietly defeats the
	// payload contract (§5): the Adversary could read the Guide's questions, and a
	// Planner could read the critique history the design deliberately withholds —
	// which is the conformity gradient the rotation exists to prevent. Nothing in
	// the prompts invites this; the point is that it must not be *possible*.
	//
	// A tmpfs is the whole fix: the workers see an empty directory, and the driver
	// — which is not sandboxed — goes on writing the real one.
	args = append(args, "--tmpfs", filepath.Join(repo, ".git", "deliberate"))

	// Shadow the whole home directory rather than a list of cache paths. Every
	// tool that wants to write somewhere under $HOME gets to, and the list never
	// needs maintaining as tools change.
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		args = append(args, "--overlay-src", h, "--tmp-overlay", h)
	}

	// Bind the state dirs back on top of the overlay, which is what makes their
	// writes real again. Verified: a bind after an overlay wins.
	for _, d := range stateDirs {
		// Skip what does not exist: bwrap fails hard on a missing source, and a
		// host that has never run has no state dir yet.
		if _, err := os.Stat(d); err != nil {
			continue
		}
		args = append(args, "--bind", d, d)
	}

	args = append(args, "--chdir", repo, "--")
	return append(args, cmd...)
}

// RequireBwrap fails early and clearly when the boundary is unavailable.
//
// There is deliberately no unwrapped fallback. It would be the one configuration
// nobody tested, entered automatically at the exact moment the safety net failed
// — and on a host like Antigravity it would hand an agent with auto-approved
// permissions the run of the real repository.
func RequireBwrap() error {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("bwrap (bubblewrap) is not on PATH, so workers cannot be confined and " +
			"deliberation is unavailable: install bubblewrap. There is no unconfined mode — the hosts " +
			"cannot be trusted to stay inside their workspace, and one of them provably does not")
	}
	return nil
}
