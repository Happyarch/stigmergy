// Package ids mints stigmergy identifiers.
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

// NewRootID returns an identifier like "r-3f2a9c81b4de".
//
// It is random but NOT a secret: root ids are handed to every agent so they can
// address each other's mailboxes. Randomness only buys uniqueness across
// concurrent sessions, never authority — stigmergy is a cooperation mechanism,
// not a security boundary.
func NewRootID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: system CSPRNG unavailable: " + err.Error())
	}
	return "r-" + hex.EncodeToString(b[:])
}

// NewProjectID returns an identifier like "p-3f2a9c81b4de7a05".
//
// Opaque on purpose: not a slug, not a path, and not derived from where the
// repositories happen to sit. It names the directory a multi-repo project's
// shared state lives in, and every member points at it, so it has to stay valid
// when a repository is renamed, moved, or checked out beside somebody else's
// project of the same name.
//
// Deriving it from a common parent directory would have been the obvious
// shortcut and is wrong: a project's repositories need share no root at all.
// Antigravity mounts unrelated directories as one workspace routinely, and two
// clones can live on different filesystems entirely.
func NewProjectID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: system CSPRNG unavailable: " + err.Error())
	}
	return "p-" + hex.EncodeToString(b[:])
}
