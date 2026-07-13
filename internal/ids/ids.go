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
