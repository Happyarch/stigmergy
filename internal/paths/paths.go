// Package paths normalizes the file paths agents hand us into the one form
// claims are stored and compared in: repo-relative, POSIX, symlink-resolved.
//
// This is security-adjacent in effect if not in intent: if two spellings of the
// same file normalize differently, a claim on one does not protect the other,
// and the whole coordination guarantee quietly stops holding.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrOutsideWorktree reports a path that resolves outside the repository.
// Claims cannot govern it, so it is not an error to edit — just not our
// business.
var ErrOutsideWorktree = errors.New("paths: path is outside the worktree")

// Normalize turns any path an agent supplied into a repo-relative POSIX path.
//
// Relative paths are joined against cwd (the host tells us the agent's working
// directory). Symlinks are resolved, so /repo/link/f.go and /repo/real/f.go
// cannot both be claimed independently.
func Normalize(worktree, cwd, path string) (string, error) {
	if path == "" {
		return "", errors.New("paths: empty path")
	}
	if !filepath.IsAbs(path) {
		if cwd == "" {
			return "", errors.New("paths: relative path with no working directory to resolve it against")
		}
		path = filepath.Join(cwd, path)
	}

	resolved, err := resolveExisting(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	root, err := resolveExisting(filepath.Clean(worktree))
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", ErrOutsideWorktree
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", ErrOutsideWorktree
	}
	return rel, nil
}

// resolveExisting resolves symlinks in a path that may not exist yet.
//
// EvalSymlinks fails outright on a missing path, but agents constantly write
// files that do not exist yet — that is what a Write tool does. So walk up to
// the deepest ancestor that does exist, resolve that, and re-append the missing
// tail. A claim on a directory must still cover the new file about to appear
// inside it.
func resolveExisting(path string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}

	var missing []string
	cur := path
	for {
		parent := filepath.Dir(cur)
		if parent == cur { // reached the filesystem root
			return "", errors.New("paths: no existing ancestor for " + path)
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		if _, err := os.Lstat(parent); err == nil {
			resolved, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return "", err
			}
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}
		cur = parent
	}
}

// ParseScope splits a claim scope into the repository it names and the path
// inside it.
//
// A project may span several git repositories, so a bare path no longer
// identifies a file: "README.md" exists in every one of them. The spelling is
// "repo:path" — and it is a spelling rather than a second argument because the
// same string has to come back OUT of stigmergy too, in conflict messages, the
// roster, the audit trail and the hook's denial text. One notation, written and
// read the same way, beats an input field paired with a different output format.
//
// The ':' is not ambiguous in practice, and the rule is what makes that true:
// split on the FIRST colon, and treat the head as a repository only if it is
// actually a member. Anything else is a path in its entirety. So a file called
// "weird:name.go" still works, because there is no repository called "weird" —
// and the only way to collide is to have a path component that is exactly a
// member's name followed by a colon, in a project that has that member.
//
// members is the roster; self is the repository to assume when none is named,
// which is the one the agent opened. An empty self is the single-repository
// project, where there is nothing to name and nothing to disambiguate.
func ParseScope(spec string, members []string, self string) (repoID, scope string, err error) {
	if spec == "" {
		return "", "", errors.New("scope_path must not be empty; use \".\" for the whole repository")
	}
	repoID = self
	if head, tail, found := strings.Cut(spec, ":"); found && slices.Contains(members, head) {
		if tail == "" {
			return "", "", fmt.Errorf("scope_path %q names repository %q but no path in it; use %q for the whole repository",
				spec, head, head+":.")
		}
		repoID, spec = head, tail
	}
	scope, err = ValidateScope(spec)
	if err != nil {
		return "", "", err
	}
	return repoID, scope, nil
}

// UnknownRepoHint explains a scope whose prefix looks like it meant to name a
// repository but did not match one.
//
// This exists because the failure is otherwise silent and wrong in the worst
// way: "sidecar:src/main.go" with the member actually called "naviamp-sidecar"
// parses as a PATH called "sidecar:src/main.go", the claim succeeds, and it
// guards a file that does not exist while the real one stays unprotected.
// Nothing errors. So when a scope contains a colon and the head is not a member,
// say so — the agent gets to notice before the claim is useless.
func UnknownRepoHint(spec string, members []string) string {
	head, _, found := strings.Cut(spec, ":")
	if !found || slices.Contains(members, head) || len(members) == 0 {
		return ""
	}
	return fmt.Sprintf("note: %q is not a repository in this project (%s), "+
		"so this was read as a path called %q. If you meant a repository, spell it exactly.",
		head, strings.Join(members, ", "), spec)
}

// ValidateScope checks a claim scope as an agent supplied it: repo-relative,
// POSIX, no escapes. The repo root is spelled "." and is only claimable
// recursively — a non-recursive claim on "." would name a file that is a
// directory, which no edit can ever match.
func ValidateScope(scope string) (string, error) {
	if scope == "" {
		return "", errors.New("scope_path must not be empty; use \".\" for the whole repository")
	}
	if filepath.IsAbs(scope) {
		return "", errors.New("scope_path must be relative to the repository root, not absolute")
	}
	clean := filepath.ToSlash(filepath.Clean(scope))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("scope_path must stay inside the repository")
	}
	return clean, nil
}
