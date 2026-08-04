// Package pathglob matches a repo-relative path against a glob pattern under
// git's `:(top,glob)` semantics: `*` matches any run of characters within one
// path segment and never crosses `/`; `**` matches zero or more whole
// segments, crossing as many `/` as it needs to.
//
// This exists because priming (internal/hooks/priming.go) has to decide
// whether a declared evidence glob overlaps a claimed scope, on the hook
// path — and internal/drift, which already compiles patterns into git
// pathspecs, spawns git subprocesses and must never become reachable from
// internal/hooks (see the transitive-import test in internal/hooks). This
// package has no I/O and no dependency on drift; it is pure string matching,
// safe for the 250ms hook budget.
//
// It does not implement everything git's glob magic can: no brace
// expansion, no character classes, no `?`. Declared evidence patterns are
// validated at write time (store.validatePattern) to be ordinary paths with
// `*`/`**` wildcards, so that is what this matches.
package pathglob

import "strings"

// Match reports whether pattern matches path.
func Match(pattern, path string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchSegments(pat, seg []string) bool {
	if len(pat) == 0 {
		return len(seg) == 0
	}
	if pat[0] == "**" {
		// "**" matches zero segments (skip it) or one more segment and try
		// again — the standard doublestar recursion.
		if matchSegments(pat[1:], seg) {
			return true
		}
		if len(seg) > 0 && matchSegments(pat, seg[1:]) {
			return true
		}
		return false
	}
	if len(seg) == 0 {
		return false
	}
	return matchSegment(pat[0], seg[0]) && matchSegments(pat[1:], seg[1:])
}

// matchSegment matches a single path component against a pattern component
// that may contain "*" (never "/", since both arguments are already one
// path.Split segment). Standard greedy two-pointer wildcard matching.
func matchSegment(pattern, s string) bool {
	pi, si := 0, 0
	starAt, matchFrom := -1, 0
	for si < len(s) {
		switch {
		case pi < len(pattern) && pattern[pi] == s[si]:
			pi++
			si++
		case pi < len(pattern) && pattern[pi] == '*':
			starAt, matchFrom = pi, si
			pi++
		case starAt != -1:
			pi = starAt + 1
			matchFrom++
			si = matchFrom
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// StaticPrefix returns the leading path components of pattern that contain no
// wildcard — the region a claim must overlap before the pattern could
// possibly match anything under it. Used to test a RECURSIVE claim (a
// directory, not a single file) against a glob for overlap, since there is no
// file list to match candidates against on the hook path.
//
// "." means the repository root: either the pattern has no static leading
// segment at all (e.g. "*.go"), or it is empty.
func StaticPrefix(pattern string) string {
	var out []string
	for _, seg := range strings.Split(pattern, "/") {
		if strings.ContainsRune(seg, '*') {
			break
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return "."
	}
	return strings.Join(out, "/")
}
