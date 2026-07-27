// Package drift measures what has changed in a repository since a recorded
// commit, and reports it as raw components.
//
// It emits no score, no bucket, no ranking and no verdict, and it must not grow
// one. The strongest statement anything here supports is "no observed changes
// within the declared policy, at this coverage" — never "fresh". A memory can be
// untouched by every commit in the range and still be false, and a repository
// can churn under a memory that remains exactly true.
//
// Everything is measured by REACHABILITY from a stored object id. Committer
// dates are not arrival times: rebase, cherry-pick and fast-forward land commits
// after a baseline carrying dates from before it, so a --since window silently
// misses real change. That is the single reason this package needs a base OID
// stored in the schema rather than a timestamp it could have derived.
//
// This package is not internal/gitx. gitx resolves repositories by walking the
// filesystem, carries a warning that its one subprocess must never run on the
// hook path, and collapses "not a repo" / "git missing" / "git failed" into a
// single error — which cannot express the outcomes below. drift takes worktree
// paths that someone else has already resolved, and never resolves anything.
package drift

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Per-member outcomes. Only Measured carries a count.
const (
	Measured        = "measured"
	NonAncestor     = "non_ancestor"
	MissingBase     = "missing_base"
	Shallow         = "shallow"
	MissingWorktree = "missing_worktree"
	Timeout         = "timeout"
	GitError        = "git_error"
)

// Coverage of one evaluation, computed every time and never cached. A stored
// label would eventually lie, and consecutive calls may legitimately differ —
// which is acceptable precisely because coverage is reported alongside the
// numbers rather than folded into them.
const (
	Complete    = "complete"
	Partial     = "partial"
	Unavailable = "unavailable"
)

// CountMode names exactly what the number is, and travels with it.
//
// Not "commits touching these paths" — that is not what git counts. Under path
// limiting rev-list applies history simplification: a trivial merge may be
// omitted while the commits it merged are counted, and a conflict-resolution
// merge that touches the paths counts itself. That is documented git behaviour
// rather than a stable unit of integration events, and the name says so to stop
// anyone building a threshold on it.
const CountMode = "git full-history path-limited commit count"

// Path is one declared pattern.
type Path struct {
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
}

// Member is one repository to observe, already resolved.
type Member struct {
	RepoID    string
	Worktree  string
	CommonDir string
	BaseOID   string
	Paths     []Path
}

// MemberResult is what was observed in one repository.
type MemberResult struct {
	Repo    string `json:"repo"`
	Outcome string `json:"outcome"`
	BaseOID string `json:"base_oid"`
	HeadOID string `json:"head_oid,omitempty"`
	// Count is a pointer so that "not measured" and "measured, zero changes" are
	// different values on the wire. Collapsing them would let an unreadable
	// repository read as a quiet one, which is the exact confusion this whole
	// design exists to prevent.
	Count     *int     `json:"count,omitempty"`
	CountMode string   `json:"count_mode,omitempty"`
	Paths     []Path   `json:"paths"`
	Warnings  []string `json:"warnings,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

// Result is one evaluation of one policy.
type Result struct {
	Coverage string         `json:"coverage"`
	Members  []MemberResult `json:"members"`
}

// Evaluate observes every member, concurrently, and reports what it found.
//
// It never returns an error. Every way this can fail is a per-member outcome,
// because a single unreadable repository must not erase what the others said —
// that is what partial coverage is for.
//
// Members run concurrently so one wedged repository cannot eat the whole
// deadline. Results keep the input order regardless of completion order: an
// output that reshuffles itself run to run is unreadable to the agent and
// untestable here.
func Evaluate(ctx context.Context, members []Member) Result {
	res := Result{Members: make([]MemberResult, len(members))}
	if len(members) == 0 {
		res.Coverage = Unavailable
		return res
	}

	// Once, not once per member: M members with no git on PATH would otherwise be
	// M failed spawns to learn one fact.
	gitPath, gitErr := exec.LookPath("git")

	var wg sync.WaitGroup
	for i, m := range members {
		if m.Paths == nil {
			m.Paths = []Path{}
		}
		if gitErr != nil {
			res.Members[i] = MemberResult{
				Repo: m.RepoID, BaseOID: m.BaseOID, Paths: m.Paths,
				Outcome: GitError, Detail: "git is not on PATH",
			}
			continue
		}
		wg.Add(1)
		go func(i int, m Member) {
			defer wg.Done()
			res.Members[i] = evaluateMember(ctx, gitPath, m)
		}(i, m)
	}
	wg.Wait()

	measured := 0
	for _, m := range res.Members {
		if m.Outcome == Measured {
			measured++
		}
	}
	switch {
	case measured == len(res.Members):
		res.Coverage = Complete
	case measured == 0:
		res.Coverage = Unavailable
	default:
		res.Coverage = Partial
	}
	return res
}

// CaptureHead resolves a repository's current HEAD, for recording as a
// baseline.
//
// Deliberately strict where Evaluate is forgiving: a baseline that cannot be
// captured must fail the whole declaration, not be recorded as an outcome. A
// policy is either fully anchored or not written at all — half a baseline is
// evidence that will silently under-report forever, and nothing downstream could
// tell it apart from a repository that simply has not changed.
func CaptureHead(ctx context.Context, worktree string) (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", errors.New("git is not on PATH")
	}
	if _, err := os.Stat(worktree); err != nil {
		return "", errors.New(worktree + " is not there")
	}
	cmd := exec.CommandContext(ctx, gitPath, "-C", worktree, "rev-parse", "--verify", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	b, err := cmd.Output()
	if err != nil {
		return "", errors.New("HEAD could not be resolved: " + gitMessage(err))
	}
	head := strings.TrimSpace(string(b))
	if head == "" {
		return "", errors.New("HEAD resolved to nothing — this repository may have no commits yet")
	}
	return head, nil
}

func evaluateMember(ctx context.Context, gitPath string, m Member) MemberResult {
	out := MemberResult{Repo: m.RepoID, BaseOID: m.BaseOID, Paths: m.Paths}

	if m.Worktree == "" {
		return fail(out, MissingWorktree, "this repository has no recorded worktree")
	}
	if _, err := os.Stat(m.Worktree); err != nil {
		return fail(out, MissingWorktree, m.Worktree+" is not there")
	}
	// A shallow clone has no history to reach back through, so a count over it
	// would be a real number that means nothing. Checked at CommonDir/shallow
	// rather than <worktree>/.git/shallow because .git is a FILE in a linked
	// worktree, and the check would silently never fire there.
	if m.CommonDir != "" {
		if _, err := os.Stat(filepath.Join(m.CommonDir, "shallow")); err == nil {
			return fail(out, Shallow, "this clone is shallow, so reachability cannot be established")
		}
	}

	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, gitPath, append([]string{"-C", m.Worktree}, args...)...)
		// A repository whose config or hooks prompt would hang the whole
		// evaluation behind a terminal nobody is watching.
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
		b, err := cmd.Output()
		return strings.TrimSpace(string(b)), err
	}
	timedOut := func() bool { return ctx.Err() != nil }

	// The base object is checked explicitly, and first. merge-base exiting
	// non-zero means "not an ancestor", while a base object that is absent or
	// invalid fails differently — and the two must not be collapsed, because one
	// says the history moved and the other says the baseline is gone.
	//
	// The EXIT CODE separates a third case that used to be swallowed by the
	// second. With --verify --quiet, git exits 1 for an object it cannot resolve
	// and 128 when the repository itself is unusable — not a repository at all,
	// unreadable, corrupt. Both used to be reported as missing_base, which told
	// an agent its baseline had been garbage collected when in fact the directory
	// was not a git repository. A confidently wrong diagnosis is worse than
	// git_error, which at least says "look at this".
	if _, err := git("rev-parse", "--verify", "--quiet", m.BaseOID+"^{commit}"); err != nil {
		if timedOut() {
			return fail(out, Timeout, "git did not finish within the deadline")
		}
		if exitCode(err) != 1 {
			return fail(out, GitError,
				"this repository could not be read: "+gitMessage(err))
		}
		return fail(out, MissingBase,
			"the base commit "+short(m.BaseOID)+" is not in this repository — it may have been garbage collected, or never fetched here")
	}
	head, err := git("rev-parse", "--verify", "HEAD")
	if err != nil {
		if timedOut() {
			return fail(out, Timeout, "git did not finish within the deadline")
		}
		return fail(out, GitError, "HEAD could not be resolved: "+gitMessage(err))
	}
	out.HeadOID = head

	if _, err := git("merge-base", "--is-ancestor", m.BaseOID, "HEAD"); err != nil {
		if timedOut() {
			return fail(out, Timeout, "git did not finish within the deadline")
		}
		// merge-base cannot tell a force-push from a branch switch from a reset,
		// so the causes are listed as possibilities and never as findings.
		return fail(out, NonAncestor,
			"the base commit is no longer reachable from HEAD; the branch may have been switched, reset, or force-pushed. No count is possible across that break")
	}

	args := []string{"rev-list", "--count", "--full-history", m.BaseOID + "..HEAD"}
	if specs := pathspecs(m.Paths); len(specs) > 0 {
		args = append(args, "--")
		args = append(args, specs...)
	}
	countText, err := git(args...)
	if err != nil {
		if timedOut() {
			return fail(out, Timeout, "git did not finish within the deadline")
		}
		return fail(out, GitError, "the commit count failed: "+gitMessage(err))
	}
	n, err := strconv.Atoi(countText)
	if err != nil {
		return fail(out, GitError, "git returned an uncountable answer: "+countText)
	}

	out.Outcome = Measured
	out.Count = &n
	out.CountMode = CountMode
	out.Warnings = unmatched(ctx, git, m)
	return out
}

// unmatched warns about a pattern that matches nothing in the UNION of the base
// and head trees.
//
// The union is the point. A path that existed at the base and was deleted by
// HEAD matches nothing at HEAD, and that is evidence of change rather than a bad
// anchor — warning about it would train agents to widen a pattern that was
// telling them the truth. Only a pattern matching in NEITHER tree is suspect.
//
// A too-narrow anchor is otherwise invisible: it undercounts silently and reads
// as plausibly clean, which is the most expensive failure available here.
// It uses `ls-files --with-tree`, NOT `ls-tree`. ls-tree does not support
// pathspec magic — `git ls-tree -- ':(top,glob)internal/**'` fails outright with
// "pathspec magic not supported by this command: 'glob'". Because an error here
// is deliberately treated as "cannot tell, do not warn", that made this entire
// check silently inert for every glob pattern: the one kind most likely to be
// mistyped, and the one where a dead anchor is hardest to spot by eye. It looked
// like it was working because literal patterns went down the same path and did.
//
// `--with-tree=<rev>` unions the index with that tree, so it can only ever match
// MORE than the tree alone. That asymmetry is the right way round: it can
// suppress a warning, never invent one, and a false warning is the harmful
// direction — it teaches agents to widen a pattern that was correct.
//
// HEAD is checked first and the loop stops on the first hit, so a live pattern
// costs one subprocess and only a dead one costs two.
func unmatched(ctx context.Context, git func(...string) (string, error), m Member) []string {
	var warnings []string
	for _, p := range m.Paths {
		// The count has already succeeded by the time this runs, so a blown
		// deadline must stop the warning pass rather than grinding through every
		// remaining pattern to produce advice nobody is waiting for.
		if ctx.Err() != nil {
			return warnings
		}
		spec := pathspec(p)
		hit := false
		for _, rev := range []string{"HEAD", m.BaseOID} {
			out, err := git("ls-files", "--with-tree="+rev, "--", spec)
			if err != nil {
				// Not a warning: a lookup that fails says nothing about whether
				// the pattern was sensible, and guessing here would produce advice
				// as likely to be wrong as right.
				hit = true
				break
			}
			if out != "" {
				hit = true
				break
			}
		}
		if !hit {
			warnings = append(warnings,
				"pattern "+strconv.Quote(p.Pattern)+" matched nothing in either the base or the current tree, so it is observing nothing")
		}
	}
	return warnings
}

// exitCode reports a command's exit status, or -1 when it failed for a reason
// that never produced one.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// pathspecs compiles declared patterns into git pathspecs.
//
// Never passed raw. A leading colon in agent input would otherwise be read as
// pathspec magic — including the exclusion form, which would turn a pattern
// meant to narrow observation into one that removes it. And in an ordinary
// pathspec a bare "*" crosses directory separators, so "internal/*" would
// quietly match the whole subtree; ":(glob)" gives the shell-like behaviour an
// agent actually means, where "**" is the way to cross them.
//
// ":(top)" anchors to the repository root rather than to the working directory,
// so the same policy means the same thing whatever -C was passed.
func pathspecs(paths []Path) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, pathspec(p))
	}
	return out
}

func pathspec(p Path) string {
	if p.Kind == "glob" {
		return ":(top,glob)" + p.Pattern
	}
	return ":(top,literal)" + p.Pattern
}

func fail(out MemberResult, outcome, detail string) MemberResult {
	out.Outcome = outcome
	out.Detail = detail
	return out
}

func gitMessage(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return msg
		}
	}
	return err.Error()
}

func short(oid string) string {
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}
