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

// NewDeviceID returns an identifier like "d-3f2a9c81b4de7a05".
//
// Random, not a secret, same as NewRootID and NewProjectID: it only has to be
// unique across a developer's own machines, never authenticate anything.
// Deliberately not derived from hostname or MAC address — a hostname collides
// and changes, and a derivation would make two machines cloned from one disk
// image indistinguishable, which is exactly the case a restored-backup rollback
// (docs/sync-model.md §7.5) has to be able to detect. Minted once and stored in
// the global database's meta as sync_device_id.
func NewDeviceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: system CSPRNG unavailable: " + err.Error())
	}
	return "d-" + hex.EncodeToString(b[:])
}
