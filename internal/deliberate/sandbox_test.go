package deliberate

import (
	"os"
	"slices"
	"testing"
)

// indexOfPair returns the position of `flag value` in args, or -1.
func indexOfPair(args []string, flag, val string) int {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == val {
			return i
		}
	}
	return -1
}

// TestOrderIsMaskFirstBindLast is the whole correctness of BwrapArgs.
//
// bwrap applies operations in sequence and a later mount masks an earlier one, so
// the ordering is not style — it is the difference between a sandbox that works
// and one that silently unmounts its own workspace. Two rules, each learned the
// hard way:
//
//   - --tmpfs /tmp must precede every other /tmp mount, or it masks any path under
//     /tmp. This was a real failure: "bwrap: Can't chdir to /tmp/…: No such file
//     or directory", found only because one probe happened to put its workspace
//     there.
//   - a --bind that must survive must come AFTER the overlay covering it, or the
//     overlay swallows it and the host silently loses its sessions.
//
// Neither is visible to the compiler and both fail conditionally, which is the
// worst way for a boundary to fail.
func TestOrderIsMaskFirstBindLast(t *testing.T) {
	repo := "/tmp/some/repo" // deliberately under /tmp: the case that broke
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	// A state dir that certainly exists, so it is not skipped.
	state := home
	args := BwrapArgs(Sandbox{Repos: []string{repo}, Chdir: repo, StateDirs: []string{state}, PayloadPath: ""}, []string{"true"})

	tmpfs := slices.Index(args, "--tmpfs")
	repoOverlay := indexOfPair(args, "--tmp-overlay", repo)
	homeOverlay := indexOfPair(args, "--tmp-overlay", home)
	bind := indexOfPair(args, "--bind", state)

	if tmpfs == -1 || repoOverlay == -1 || bind == -1 {
		t.Fatalf("expected --tmpfs, a repo overlay and a state bind; got %v", args)
	}
	if tmpfs > repoOverlay {
		t.Errorf("--tmpfs /tmp comes after the repo overlay, so a repo under /tmp gets masked\ngot: %v", args)
	}
	if homeOverlay != -1 && bind < homeOverlay {
		t.Errorf("the state bind comes BEFORE the home overlay, so the overlay swallows it and "+
			"the host loses its sessions\ngot: %v", args)
	}
}

// TestTheWorkerWritesNothingReal pins the boundary: the repo is overlaid, never
// bound, so a worker at the real path cannot touch the real files.
func TestTheWorkerWritesNothingReal(t *testing.T) {
	repo := "/mnt/code/project"
	args := BwrapArgs(Sandbox{Repos: []string{repo}, Chdir: repo, StateDirs: nil, PayloadPath: ""}, []string{"claude", "-p"})

	if indexOfPair(args, "--ro-bind", "/") != 0 {
		t.Errorf("the base layer must be --ro-bind / / and must come first; got %v", args[:4])
	}
	if indexOfPair(args, "--tmp-overlay", repo) == -1 {
		t.Error("the repo must be overlaid: the worker works at the real path and its writes must go nowhere")
	}
	if i := indexOfPair(args, "--bind", repo); i != -1 {
		t.Error("the repo must NEVER be bound writable — that is the entire boundary")
	}
	if indexOfPair(args, "--chdir", repo) == -1 {
		t.Error("the worker must start in the repository")
	}
}

// TestTheRunDirIsHiddenFromWorkers pins the payload contract at the filesystem.
//
// The driver keeps every agent's turn under .git/deliberate/<run-id>/, and a
// worker stands at the repository root with that directory in plain sight. Left
// readable it is an out-of-band channel: the Adversary could read the Guide's
// questions, and a Planner could read the critique history §5 deliberately
// withholds — rebuilding the conformity gradient the rotation exists to prevent.
//
// Verified against a live sandbox before this test existed: without the tmpfs a
// worker could `cat` another slot's turn.
func TestTheRunDirIsHiddenFromWorkers(t *testing.T) {
	repo := "/mnt/code/project"
	args := BwrapArgs(Sandbox{Repos: []string{repo}, Chdir: repo, StateDirs: nil, PayloadPath: ""}, []string{"true"})

	want := repo + "/.git/deliberate"
	i := indexOfPair(args, "--tmpfs", want)
	if i == -1 {
		t.Fatalf("the run dir must be masked by a tmpfs or one agent can read another's turns\ngot: %v", args)
	}
	// It must land after the repo overlay, or the overlay puts the real contents
	// back.
	if o := indexOfPair(args, "--tmp-overlay", repo); o > i {
		t.Error("the run-dir tmpfs is applied before the repo overlay, so the overlay restores it")
	}
}

// TestHomeIsOverlaidSoToolsWork: a worker under a plain --ro-bind can run a
// compiler but not cache anything, so real builds fail. Overlaying $HOME makes
// GOCACHE, ~/.cargo and the rest writable at once, with no per-tool list to
// maintain.
func TestHomeIsOverlaidSoToolsWork(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	args := BwrapArgs(Sandbox{Repos: []string{"/mnt/code/p"}, Chdir: "/mnt/code/p", StateDirs: nil, PayloadPath: ""}, []string{"true"})
	if indexOfPair(args, "--tmp-overlay", home) == -1 {
		t.Error("$HOME must be overlaid or every tool cache is read-only and builds fail")
	}
}

// TestStateDirsSurvive: without a real bind a host cannot persist a session, and
// resume — the entire transport — dies.
func TestStateDirsSurvive(t *testing.T) {
	// /etc exists everywhere; BwrapArgs skips missing sources because bwrap fails
	// hard on one.
	args := BwrapArgs(Sandbox{Repos: []string{"/mnt/code/p"}, Chdir: "/mnt/code/p", StateDirs: []string{"/etc", "/definitely/not/here"}, PayloadPath: ""}, []string{"true"})

	if indexOfPair(args, "--bind", "/etc") == -1 {
		t.Error("an existing state dir must be bound writable or session resume dies")
	}
	if slices.Contains(args, "/definitely/not/here") {
		t.Error("a missing state dir must be skipped: bwrap fails hard on a missing source")
	}
}

// TestCommandComesLast: everything after -- is the host's argv, not bwrap's.
func TestCommandComesLast(t *testing.T) {
	args := BwrapArgs(Sandbox{Repos: []string{"/mnt/code/p"}, Chdir: "/mnt/code/p", StateDirs: nil, PayloadPath: ""}, []string{"codex", "exec", "--json"})
	i := slices.Index(args, "--")
	if i == -1 {
		t.Fatal("the host command must be separated by --")
	}
	if got := args[i+1:]; !slices.Equal(got, []string{"codex", "exec", "--json"}) {
		t.Errorf("host argv mangled: %v", got)
	}
}
