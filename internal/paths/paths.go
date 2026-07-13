// Package paths normalizes the file paths agents hand us into the one form
// claims are stored and compared in: repo-relative, POSIX, symlink-resolved.
//
// This is security-adjacent in effect if not in intent: if two spellings of the
// same file normalize differently, a claim on one does not protect the other,
// and the whole coordination guarantee quietly stops holding.
package paths

import (
	"errors"
	"os"
	"path/filepath"
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
