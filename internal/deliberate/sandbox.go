package deliberate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// SandboxPayloadPath is where a worker finds its brief, inside the sandbox.
//
// Fixed rather than per-turn because the workers are told it in a constant
// sentence, and a path that varied would have to be interpolated into a prompt
// — one more thing to get wrong for no benefit. Under /tmp because that is the
// one directory guaranteed to be a fresh tmpfs no overlay reaches into.
const SandboxPayloadPath = "/tmp/stigmergy-payload.md"

// Sandbox describes one confined worker invocation.
//
// A struct rather than a parameter list because the ordering rules in BwrapArgs
// are the whole correctness of this file, and a caller that has to remember
// which of six positional arguments is the mask and which is the brief will
// eventually get it wrong in a way that fails silently.
type Sandbox struct {
	// Repos is every repository of the project, each overlaid so the worker can
	// write in all of them and none of it survives. A project spanning a client
	// and its service needs all of them: with one overlaid and the rest on the
	// read-only base, a worker can READ the others and every write to them fails
	// EROFS — which is exactly the cross-cutting change multi-repo deliberation
	// exists to plan.
	Repos []string
	// Chdir is where the worker starts. One of Repos; defaults to the outermost.
	Chdir string
	// StateDirs are host session directories, bound back over the $HOME overlay.
	StateDirs []string
	// PayloadPath is the host-side file holding this turn's brief.
	PayloadPath string
	// RunDirMask is the directory holding every turn of every agent, hidden from
	// the workers. Empty leaves it unmasked, which no real run should do.
	RunDirMask string
}

// BwrapArgs builds the argv that confines a worker.
//
// The worker runs at the **real repository path** and may write anything,
// anywhere. None of it is real: the repository and the home directory are
// overlaid, so every write lands in an invisible tmpfs that dies with the
// process. What survives is only what the sandbox binds back on top — the host's own state
// directory, without which session resume dies.
//
// This is the boundary. Not the hosts' permission flags — those handle "do not
// ask", and they have been wrong about "do not reach" every time they were checked:
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
func BwrapArgs(s Sandbox, cmd []string) []string {
	args := []string{
		// Everything, read-only. The base layer.
		"--ro-bind", "/", "/",
		"--dev-bind", "/dev", "/dev",
		"--proc", "/proc",
		// A fresh writable /tmp, before anything of stigmergy's could live under it.
		"--tmpfs", "/tmp",
	}

	// The turn's brief, bound in read-only at a fixed path.
	//
	// It goes AFTER the /tmp tmpfs or the tmpfs masks it, and it goes under /tmp
	// specifically so that neither the repository overlay nor the $HOME overlay
	// can shadow it later.
	//
	// Why a file rather than argv: Linux caps a SINGLE argv element at
	// MAX_ARG_STRLEN (32 pages = 131072 bytes), and the payload used to be one.
	// It crosses two execve boundaries — the driver execs bwrap, bwrap execs the host — so
	// a spec that grew past the cap killed the turn with E2BIG, and a failing run
	// grows its spec every round, which means it marched into the wall exactly
	// when it could least afford to. A bind has no size limit at all.
	//
	// It is also strictly more private than argv, which is world-readable through
	// /proc: each turn is its own bwrap with its own /tmp tmpfs, so one worker
	// cannot see another's brief even in principle.
	if s.PayloadPath != "" {
		args = append(args, "--ro-bind", s.PayloadPath, SandboxPayloadPath)
	}

	// Shadow every repository: the worker works at the real paths and its writes
	// go nowhere.
	//
	// Sorted outermost-first, by path length — a parent is always a shorter
	// string than anything beneath it.
	//
	// This is for determinism, NOT for correctness, and the distinction is worth
	// recording because the obvious guess is wrong. Repositories can nest, and it
	// looks like an outer overlay mounted second would swallow an inner one
	// already in place. It does not: mounting over a directory does not unmount
	// what is mounted beneath it, so the inner overlay stays visible at its own
	// mountpoint either way. Verified by reversing this sort — the nested-repo
	// test still passes.
	//
	// What the sort actually buys is a stable argv, which makes the ordering
	// tests meaningful and a failure readable. Do not delete it on the grounds
	// that it is not load-bearing; do not re-document it as though it were.
	repos := append([]string(nil), s.Repos...)
	sort.Slice(repos, func(i, j int) bool { return len(repos[i]) < len(repos[j]) })
	for _, r := range repos {
		args = append(args, "--overlay-src", r, "--tmp-overlay", r)
	}

	// Hide the run directory from the workers.
	//
	// The driver keeps every turn of every agent there, and a worker stands at a
	// repository root with the whole run in reach. Readable, it is an out-of-band
	// channel that quietly defeats the payload contract (§5): the Adversary could
	// read the Guide's questions, and a Planner could read the critique history
	// the design deliberately withholds — which is the conformity gradient the
	// rotation exists to prevent. Nothing in the prompts invites this; the point
	// is that it must not be *possible*.
	//
	// A tmpfs is the whole fix: the workers see an empty directory, and the
	// driver — which is not sandboxed — goes on writing the real one.
	//
	// ORDERING: this must come after BOTH sets of overlays above and after the
	// $HOME overlay below, because the run directory now lives under XDG state —
	// which is inside $HOME. An overlay applied after the mask would restore the
	// real contents through its lower layer, and the leak would be silent.
	// Applied here it survives, because nothing after it touches that path.
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		// Shadow the whole home directory rather than a list of cache paths.
		// Every tool that wants to write somewhere under $HOME gets to, and the
		// list never needs maintaining as tools change.
		args = append(args, "--overlay-src", h, "--tmp-overlay", h)
	}
	if s.RunDirMask != "" {
		args = append(args, "--tmpfs", s.RunDirMask)
	}
	// Legacy in-repository run directories, from before the run dir moved out of
	// the tree. Masked too: an old run left in .git/deliberate is still somebody
	// else's transcript.
	for _, r := range repos {
		args = append(args, "--tmpfs", filepath.Join(r, ".git", "deliberate"))
	}

	// Bind the state dirs back on top of the overlay, which is what makes their
	// writes real again. Verified: a bind after an overlay wins.
	for _, d := range s.StateDirs {
		// Skip what does not exist: bwrap fails hard on a missing source, and a
		// host that has never run has no state dir yet.
		if _, err := os.Stat(d); err != nil {
			continue
		}
		args = append(args, "--bind", d, d)
	}

	chdir := s.Chdir
	if chdir == "" && len(repos) > 0 {
		chdir = repos[0]
	}
	// Every worker opts out of stigmergy's own enforcement. The claim guard
	// coordinates the real tree between registered roots; a worker is an
	// ephemeral session whose writes evaporate with the overlay, so enforcing
	// the real tree's claims against it only stalls it on files it does not
	// hold. Only the opencode plugin reads this variable — the other hosts'
	// hooks are binaries that ignore it — and the plugin's stand-down covers
	// both its hooks, not just the guard. bwrap is still the boundary; this
	// only stops the worker blocking on itself.
	args = append(args, "--setenv", "STIGMERGY_UNGUARDED", "1")
	args = append(args, "--chdir", chdir, "--")
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
