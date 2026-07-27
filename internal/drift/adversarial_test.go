package drift

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The bug this pins was invisible and total: `git ls-tree` does not support
// pathspec magic, so every GLOB pattern's anchor check failed with "pathspec
// magic not supported by this command", was treated as "cannot tell, do not
// warn", and never warned about anything. Literal patterns went down the same
// code path and worked, which is why it looked fine.
//
// A mistyped glob — internl/** for internal/** — therefore observed nothing and
// reported a confident zero forever, which is the single failure mode the
// warning exists to prevent.
func TestADeadGlobAnchorIsWarnedAbout(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	commit(t, repo, "internal/store/a.go", "1", "real work")

	live := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo),
		BaseOID: base, Paths: []Path{{Kind: "glob", Pattern: "internal/**"}}})
	if len(live.Warnings) != 0 {
		t.Errorf("a glob that matches was warned about: %v", live.Warnings)
	}
	if *live.Count != 1 {
		t.Errorf("live glob counted %d, want 1", *live.Count)
	}

	// One character wrong. It matches nothing, and must say so.
	dead := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo),
		BaseOID: base, Paths: []Path{{Kind: "glob", Pattern: "internl/**"}}})
	if *dead.Count != 0 {
		t.Fatalf("the typo somehow counted %d", *dead.Count)
	}
	if len(dead.Warnings) != 1 || !strings.Contains(dead.Warnings[0], "internl/**") {
		t.Fatalf("a dead glob anchor produced no warning: %v — it will report a confident zero forever", dead.Warnings)
	}
}

// A directory that is not a git repository must not be diagnosed as a garbage
// collected baseline. Both used to exit through missing_base, telling an agent
// its history had been pruned when the truth was that there was no repository.
func TestANonRepositoryIsNotReportedAsAMissingBaseline(t *testing.T) {
	got := only(t, Member{RepoID: "notarepo", Worktree: t.TempDir(), BaseOID: "abc123"})
	if got.Outcome == MissingBase {
		t.Fatal("a non-repository was reported as a garbage collected baseline")
	}
	if got.Outcome != GitError {
		t.Fatalf("outcome = %s, want %s", got.Outcome, GitError)
	}
}

// countGitCalls wraps nothing — it counts by watching process spawns indirectly
// through wall time and an explicit probe. Go gives no hook on exec, so the
// count here is derived from the code path deliberately: 4 fixed calls plus 2
// per declared pattern.
func expectedSubprocesses(patterns int) int { return 4 + 2*patterns }

// A policy with many declared paths must not turn one evaluation into hundreds
// of git subprocesses.
//
// unmatched() runs `git ls-tree` against BOTH the base and head tree for every
// pattern, to catch an anchor that observes nothing. That is worth doing, and it
// is per-pattern, so the cost is linear in something an agent controls with no
// stated limit.
func TestAPolicyWithManyPathsDoesNotFanOutUnboundedly(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	commit(t, repo, "internal/a.go", "1", "one")

	var paths []Path
	for i := 0; i < 200; i++ {
		paths = append(paths, Path{Kind: "literal", Pattern: fmt.Sprintf("internal/f%d.go", i)})
	}

	start := time.Now()
	got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo),
		BaseOID: base, Paths: paths})
	elapsed := time.Since(start)

	if got.Outcome != Measured {
		t.Fatalf("outcome = %s (%s)", got.Outcome, got.Detail)
	}
	t.Logf("200 patterns took %s; the code path implies ~%d git subprocesses",
		elapsed, expectedSubprocesses(len(paths)))

	// A single memory_list(include_drift) over a project with a dozen such
	// policies would multiply this again. Anything past a couple of seconds for
	// ONE member is already a problem.
	if elapsed > 5*time.Second {
		t.Errorf("one member with 200 patterns took %s — this is unbounded fan-out", elapsed)
	}
}

// The warning pass must not be able to outlive its deadline: it runs after the
// count has already succeeded, so a cancelled context has to stop it rather than
// grinding through every remaining pattern.
func TestTheWarningPassRespectsTheDeadline(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	commit(t, repo, "a.go", "1", "one")

	var paths []Path
	for i := 0; i < 300; i++ {
		paths = append(paths, Path{Kind: "literal", Pattern: fmt.Sprintf("nowhere%d.go", i)})
	}

	// Enough time to get through rev-list, not enough for 600 ls-tree calls.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	res := Evaluate(ctx, []Member{{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo),
		BaseOID: base, Paths: paths}})
	elapsed := time.Since(start)

	t.Logf("outcome=%s elapsed=%s warnings=%d", res.Members[0].Outcome, elapsed, len(res.Members[0].Warnings))
	// Whatever it concludes, it must stop near the deadline rather than running
	// to completion regardless.
	if elapsed > 3*time.Second {
		t.Errorf("evaluation ran %s past a 400ms deadline — the warning pass ignores the context", elapsed)
	}
}

// Patterns an agent can legitimately type that are not ordinary file names.
func TestPathologicalPatterns(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	commit(t, repo, "a file with spaces.md", "1", "spaces")
	commit(t, repo, "ünïcode.go", "2", "unicode")
	commit(t, repo, "dash-file.go", "3", "dash")

	cases := []struct {
		name    string
		path    Path
		wantMin int
	}{
		{"a name with spaces", Path{Kind: "literal", Pattern: "a file with spaces.md"}, 1},
		{"a non-ascii name", Path{Kind: "literal", Pattern: "ünïcode.go"}, 1},
		{"a leading dash", Path{Kind: "literal", Pattern: "dash-file.go"}, 1},
		{"a glob with a brace", Path{Kind: "glob", Pattern: "*.{go,md}"}, 0},
		{"the repo root as a literal dot", Path{Kind: "literal", Pattern: "."}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo),
				BaseOID: base, Paths: []Path{tc.path}})
			if got.Outcome != Measured {
				t.Fatalf("outcome = %s (%s) — a legal pattern broke the evaluation", got.Outcome, got.Detail)
			}
			if got.Count == nil {
				t.Fatal("measured with no count")
			}
			t.Logf("%q counted %d, warnings=%v", tc.path.Pattern, *got.Count, got.Warnings)
			if *got.Count < tc.wantMin {
				t.Errorf("count = %d, want at least %d", *got.Count, tc.wantMin)
			}
		})
	}
}

// A dash-led pattern must never be read as a git FLAG. Everything after "--" is
// a pathspec, so this should be safe — but it is exactly the kind of thing that
// is safe only by accident until someone asserts it.
func TestALeadingDashIsNotReadAsAFlag(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	commit(t, repo, "real.go", "1", "real")

	got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo),
		BaseOID: base, Paths: []Path{{Kind: "literal", Pattern: "--all"}}})
	if got.Outcome != Measured {
		t.Fatalf("outcome = %s (%s) — \"--all\" was taken as a flag", got.Outcome, got.Detail)
	}
	if *got.Count != 0 {
		t.Errorf("count = %d, want 0: there is no file called --all", *got.Count)
	}
}

// Concurrency: Evaluate writes into a shared slice from N goroutines. Indexed
// writes to distinct elements are safe, but only if nothing ever appends.
func TestEvaluateIsRaceFreeAcrossManyMembers(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	commit(t, repo, "a.go", "1", "one")

	var members []Member
	for i := 0; i < 24; i++ {
		members = append(members, Member{
			RepoID: fmt.Sprintf("r%d", i), Worktree: repo,
			CommonDir: commonDir(t, repo), BaseOID: base,
		})
	}
	res := Evaluate(context.Background(), members)
	if res.Coverage != Complete {
		t.Fatalf("coverage = %s", res.Coverage)
	}
	for i, m := range res.Members {
		if m.Repo != fmt.Sprintf("r%d", i) {
			t.Fatalf("member %d is %q — the result slice was written out of order", i, m.Repo)
		}
	}
}

// A worktree path that exists but is not a git repository at all.
func TestANonRepositoryWorktreeIsAnOutcomeNotACrash(t *testing.T) {
	got := only(t, Member{RepoID: "notarepo", Worktree: t.TempDir(), BaseOID: "abc123"})
	if got.Outcome == Measured {
		t.Fatal("a directory that is not a repository reported a measurement")
	}
	t.Logf("outcome = %s (%s)", got.Outcome, got.Detail)
}
