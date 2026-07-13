// Package claims holds the pure overlap logic: whether two claim scopes cover
// any of the same files. It has no database and no I/O, because this is the
// rule the whole enforcement layer rests on and it must be trivially testable.
package claims

import "strings"

// Scope is a claimed path: a repo-relative POSIX path, plus whether it covers
// a whole subtree. A non-recursive scope covers exactly one file.
type Scope struct {
	Path      string
	Recursive bool
}

// Overlaps reports whether two scopes cover any file in common.
//
// The comparison is component-wise, never a raw string prefix: "foo" must not
// be treated as covering "foobar". That trap is the entire reason this is a
// separate, tested function — a prefix check here would silently block edits to
// unrelated files whose names happen to share a prefix with a claimed one.
func Overlaps(a, b Scope) bool {
	switch {
	case !a.Recursive && !b.Recursive:
		return a.Path == b.Path
	case a.Recursive && !b.Recursive:
		return covers(a.Path, b.Path)
	case !a.Recursive && b.Recursive:
		return covers(b.Path, a.Path)
	default:
		// Two subtrees overlap when either contains the other.
		return covers(a.Path, b.Path) || covers(b.Path, a.Path)
	}
}

// covers reports whether the subtree rooted at dir contains path (or is it).
func covers(dir, path string) bool {
	if dir == path {
		return true
	}
	// The repo root is spelled "." and contains everything, but paths under it
	// are spelled "src/main.go", not "./src/main.go", so the generic prefix
	// test would never match it.
	if dir == "." {
		return true
	}
	return strings.HasPrefix(path, dir+"/")
}

// Covers reports whether a scope governs a specific file path.
func Covers(s Scope, path string) bool {
	return Overlaps(s, Scope{Path: path})
}
