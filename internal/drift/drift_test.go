package drift

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real git, real commits, no exec mocking — the same standard internal/gitx and
// internal/hooks already hold themselves to. Everything asserted here is
// documented git behaviour, and a mock would only assert what it was assumed to be.

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitAt runs git with both dates pinned, for the case where a commit must carry
// a date from before the baseline it lands after.
func gitAt(t *testing.T, dir, when string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_AUTHOR_DATE="+when, "GIT_COMMITTER_DATE="+when,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "README.md", "start")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "initial")
	return dir
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, rel, content, msg string) string {
	t.Helper()
	write(t, dir, rel, content)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", msg)
	return head(t, dir)
}

func head(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "rev-parse", "HEAD")
}

// commonDir resolves the SHARED git dir, exactly as internal/gitx does.
//
// Not --absolute-git-dir, which in a linked worktree gives that worktree's own
// git dir (.git/worktrees/<name>) — where no `shallow` file ever appears. Using
// it here made the linked-worktree case pass for the wrong reason.
func commonDir(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
}

// only evaluates a single member and returns its result.
func only(t *testing.T, m Member) MemberResult {
	t.Helper()
	res := Evaluate(context.Background(), []Member{m})
	if len(res.Members) != 1 {
		t.Fatalf("got %d member results, want 1", len(res.Members))
	}
	return res.Members[0]
}

func TestCountsCommitsSinceTheBaseline(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	commit(t, repo, "a.go", "1", "one")
	commit(t, repo, "b.go", "2", "two")

	got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base})
	if got.Outcome != Measured {
		t.Fatalf("outcome = %s (%s)", got.Outcome, got.Detail)
	}
	if got.Count == nil || *got.Count != 2 {
		t.Errorf("count = %v, want 2", got.Count)
	}
	if got.CountMode != CountMode {
		t.Errorf("count mode = %q, want the named mode", got.CountMode)
	}
	if got.HeadOID != head(t, repo) {
		t.Errorf("head = %q", got.HeadOID)
	}
}

// Zero changes and "could not look" must be different values on the wire, or an
// unreadable repository reads as a quiet one.
func TestZeroIsMeasuredAndNotAbsent(t *testing.T) {
	repo := newRepo(t)
	got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: head(t, repo)})
	if got.Outcome != Measured {
		t.Fatalf("outcome = %s (%s)", got.Outcome, got.Detail)
	}
	if got.Count == nil {
		t.Fatal("a measured member reported no count at all; zero must be a number, not an absence")
	}
	if *got.Count != 0 {
		t.Errorf("count = %d, want 0", *got.Count)
	}
}

// The case §10 of docs/memory-model.md exists for. Rebase, cherry-pick and
// fast-forward all land commits AFTER a baseline carrying committer dates from
// BEFORE it, so any --since window silently misses them. Reachability does not
// care what the dates say.
func TestCommitsCarryingOlderDatesAreStillCounted(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "checkout", "-q", "-b", "side")
	// Authored and committed long before the baseline is captured.
	write(t, repo, "old.go", "from the past")
	git(t, repo, "add", "-A")
	gitAt(t, repo, "2020-01-01T00:00:00Z", "commit", "-qm", "an old commit")
	old := head(t, repo)

	git(t, repo, "checkout", "-q", "main")
	base := head(t, repo)

	// Cherry-picking preserves the AUTHOR date; -x keeps it recognisable. The
	// commit arrives after the baseline regardless of what it says about itself.
	git(t, repo, "cherry-pick", old)

	authorDate := git(t, repo, "log", "-1", "--format=%ad", "--date=format:%Y")
	if authorDate != "2020" {
		t.Fatalf("fixture no longer reproduces the bug: author date is %q, want 2020", authorDate)
	}

	got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base})
	if got.Outcome != Measured || got.Count == nil || *got.Count != 1 {
		t.Fatalf("outcome=%s count=%v — a commit that arrived after the baseline was missed", got.Outcome, got.Count)
	}
}

// Merge shapes are asserted against what git actually does, not against an
// assumed "one commit per change". The count mode is named for exactly this
// reason: it is not a stable unit of integration events.
func TestMergeAndRevertShapes(t *testing.T) {
	t.Run("a merge of path changes counts its constituents", func(t *testing.T) {
		repo := newRepo(t)
		base := head(t, repo)
		git(t, repo, "checkout", "-q", "-b", "feature")
		commit(t, repo, "src/f.go", "feature", "feature work")
		git(t, repo, "checkout", "-q", "main")
		commit(t, repo, "other.go", "main", "main work")
		git(t, repo, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")

		got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base})
		if got.Outcome != Measured {
			t.Fatalf("outcome = %s (%s)", got.Outcome, got.Detail)
		}
		// Both branch commits plus the merge itself are reachable.
		if *got.Count != 3 {
			t.Errorf("count = %d, want 3 (two commits and the merge)", *got.Count)
		}
	})

	t.Run("a change and its revert are two changes, not zero", func(t *testing.T) {
		repo := newRepo(t)
		base := head(t, repo)
		commit(t, repo, "c.go", "added", "add c")
		git(t, repo, "revert", "--no-edit", "HEAD")

		got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base})
		// The tree is identical to the baseline again, and the count is still 2.
		// That is correct and worth pinning: this measures history, not diffs,
		// and "back where it started" is not "nothing happened".
		if *got.Count != 2 {
			t.Errorf("count = %d, want 2 — the revert is itself a change", *got.Count)
		}
	})
}

func TestPathspecs(t *testing.T) {
	t.Run("a glob does not cross directory separators", func(t *testing.T) {
		repo := newRepo(t)
		base := head(t, repo)
		commit(t, repo, "internal/top.go", "1", "top level")
		commit(t, repo, "internal/sub/deep.go", "2", "nested")

		shallow := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base,
			Paths: []Path{{Kind: "glob", Pattern: "internal/*"}}})
		if *shallow.Count != 1 {
			t.Errorf("internal/* counted %d, want 1 — * crossed a separator", *shallow.Count)
		}
		deep := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base,
			Paths: []Path{{Kind: "glob", Pattern: "internal/**"}}})
		if *deep.Count != 2 {
			t.Errorf("internal/** counted %d, want 2", *deep.Count)
		}
	})

	t.Run("a leading colon is part of the path, not pathspec magic", func(t *testing.T) {
		repo := newRepo(t)
		base := head(t, repo)
		commit(t, repo, ":odd.go", "1", "a file whose name starts with a colon")
		commit(t, repo, "plain.go", "2", "an ordinary file")

		got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base,
			Paths: []Path{{Kind: "literal", Pattern: ":odd.go"}}})
		if got.Outcome != Measured {
			t.Fatalf("outcome = %s (%s)", got.Outcome, got.Detail)
		}
		if *got.Count != 1 {
			t.Errorf("count = %d, want 1 — the colon was read as pathspec magic", *got.Count)
		}
	})

	t.Run("a literal pattern is not a prefix match", func(t *testing.T) {
		repo := newRepo(t)
		base := head(t, repo)
		commit(t, repo, "docs/a.md", "1", "a")
		commit(t, repo, "docs-other/b.md", "2", "b")

		got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base,
			Paths: []Path{{Kind: "literal", Pattern: "docs"}}})
		if *got.Count != 1 {
			t.Errorf("count = %d, want 1 — docs-other must not match docs", *got.Count)
		}
	})
}

// A pattern matching nothing anywhere is worth saying out loud: it observes
// nothing while reporting a confident zero.
func TestWarnsAboutAPatternThatObservesNothing(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	commit(t, repo, "real.go", "1", "real")

	got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base,
		Paths: []Path{{Kind: "literal", Pattern: "nowhere.go"}}})
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "nowhere.go") {
		t.Errorf("warnings = %v, want one naming the pattern", got.Warnings)
	}
}

// A path deleted between the base and HEAD is EVIDENCE, not a bad anchor.
// Warning about it would train agents to widen a pattern that was telling them
// the truth — hence the union of both trees.
func TestADeletedPathIsEvidenceAndNotAWarning(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, "doomed.go", "1", "add")
	base := head(t, repo)
	git(t, repo, "rm", "-q", "doomed.go")
	git(t, repo, "commit", "-qm", "remove it")

	got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base,
		Paths: []Path{{Kind: "literal", Pattern: "doomed.go"}}})
	if len(got.Warnings) != 0 {
		t.Errorf("warnings = %v, want none: the path existed at the base", got.Warnings)
	}
	if *got.Count != 1 {
		t.Errorf("count = %d, want 1 — the deletion is the change", *got.Count)
	}
}

// non_ancestor and missing_base must stay distinguishable: one says the history
// moved, the other says the baseline is gone. Collapsing them loses which.
func TestBrokenBaselinesAreDistinguished(t *testing.T) {
	t.Run("a reset away from the baseline is non_ancestor", func(t *testing.T) {
		repo := newRepo(t)
		commit(t, repo, "a.go", "1", "one")
		base := head(t, repo)
		git(t, repo, "reset", "-q", "--hard", "HEAD~1")

		got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base})
		if got.Outcome != NonAncestor {
			t.Fatalf("outcome = %s, want %s", got.Outcome, NonAncestor)
		}
		if got.Count != nil {
			t.Error("a non-ancestor reported a count; there is no count across that break")
		}
	})

	t.Run("a baseline that is not in this repository is missing_base", func(t *testing.T) {
		repo := newRepo(t)
		got := only(t, Member{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo),
			BaseOID: "0123456789012345678901234567890123456789"})
		if got.Outcome != MissingBase {
			t.Fatalf("outcome = %s, want %s", got.Outcome, MissingBase)
		}
	})
}

func TestShallowIsDetectedThroughTheCommonDir(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, "a.go", "1", "one")
	base := head(t, repo)

	clone := filepath.Join(t.TempDir(), "shallow")
	out, err := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+repo, clone).CombinedOutput()
	if err != nil {
		t.Skipf("shallow clone unavailable here: %v\n%s", err, out)
	}

	t.Run("from the clone itself", func(t *testing.T) {
		got := only(t, Member{RepoID: "app", Worktree: clone, CommonDir: commonDir(t, clone), BaseOID: base})
		if got.Outcome != Shallow {
			t.Fatalf("outcome = %s, want %s", got.Outcome, Shallow)
		}
	})

	// .git is a FILE in a linked worktree, so a check at <worktree>/.git/shallow
	// would silently never fire. CommonDir is the thing that is always a
	// directory.
	t.Run("from a linked worktree of the clone", func(t *testing.T) {
		linked := filepath.Join(t.TempDir(), "linked")
		git(t, clone, "worktree", "add", "-q", "--detach", linked)
		info, err := os.Stat(filepath.Join(linked, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		if info.IsDir() {
			t.Fatal("fixture broken: .git in a linked worktree should be a file")
		}
		got := only(t, Member{RepoID: "app", Worktree: linked, CommonDir: commonDir(t, linked), BaseOID: base})
		if got.Outcome != Shallow {
			t.Fatalf("outcome = %s, want %s", got.Outcome, Shallow)
		}
	})
}

func TestMissingWorktree(t *testing.T) {
	got := only(t, Member{RepoID: "gone", Worktree: filepath.Join(t.TempDir(), "nope"), BaseOID: "abc"})
	if got.Outcome != MissingWorktree {
		t.Fatalf("outcome = %s, want %s", got.Outcome, MissingWorktree)
	}
}

// Coverage is the axis that stops "2 of 3 measured" being read with the
// confidence of 3 of 3.
func TestCoverage(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	good := Member{RepoID: "ok", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base}
	bad := Member{RepoID: "gone", Worktree: filepath.Join(t.TempDir(), "nope"), BaseOID: base}

	cases := []struct {
		name    string
		members []Member
		want    string
	}{
		{"all measured", []Member{good}, Complete},
		{"one of three failing", []Member{good, good, bad}, Partial},
		{"all failing", []Member{bad, bad}, Unavailable},
		{"nothing declared", nil, Unavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(context.Background(), tc.members).Coverage; got != tc.want {
				t.Errorf("coverage = %s, want %s", got, tc.want)
			}
		})
	}
}

// Results keep the order they were declared in, whatever order the goroutines
// finish in. An output that reshuffles run to run is unreadable and untestable.
func TestResultsKeepDeclarationOrder(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	members := []Member{
		{RepoID: "one", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base},
		{RepoID: "two", Worktree: filepath.Join(t.TempDir(), "nope"), BaseOID: base},
		{RepoID: "three", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base},
	}
	res := Evaluate(context.Background(), members)
	for i, want := range []string{"one", "two", "three"} {
		if res.Members[i].Repo != want {
			t.Fatalf("member %d = %q, want %q", i, res.Members[i].Repo, want)
		}
	}
}

// An exhausted deadline is an outcome per member, never a lost evaluation: what
// did answer is still worth reporting, under partial or unavailable coverage.
func TestAnExhaustedDeadlineIsAnOutcome(t *testing.T) {
	repo := newRepo(t)
	base := head(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := Evaluate(ctx, []Member{{RepoID: "app", Worktree: repo, CommonDir: commonDir(t, repo), BaseOID: base}})
	if res.Members[0].Outcome != Timeout {
		t.Fatalf("outcome = %s (%s), want %s", res.Members[0].Outcome, res.Members[0].Detail, Timeout)
	}
	if res.Coverage != Unavailable {
		t.Errorf("coverage = %s, want %s", res.Coverage, Unavailable)
	}
}

func TestCaptureHead(t *testing.T) {
	repo := newRepo(t)
	got, err := CaptureHead(context.Background(), repo)
	if err != nil {
		t.Fatalf("CaptureHead: %v", err)
	}
	if got != head(t, repo) {
		t.Errorf("CaptureHead = %q, want %q", got, head(t, repo))
	}
	if _, err := CaptureHead(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("CaptureHead succeeded on a directory that is not there")
	}
}
