package syncx

import (
	"crypto/sha256"
	"encoding/hex"
)

// Digest returns "sha256:<hex>" over exactly the fields docs/sync-model.md
// §3.3 names as synced: key, type, description, body. Deliberately not
// version, not updated_by, not either timestamp — two machines on which a
// developer independently typed the identical correction must converge, not
// conflict, and they do here because the digest depends only on what was
// said, never on who said it or when.
//
// internal/store.BodyHash already establishes sha256-hex as this codebase's
// hashing convention for content fingerprints; this reuses its shape rather
// than inventing a second one.
//
// Each field is written with a trailing NUL as a separator before the digest
// is computed. Without one, ("ab", "c") and ("a", "bc") would hash identically
// whenever they happened to concatenate to the same bytes — a real risk here,
// since description and body are both free text of arbitrary length and a key
// grammar collision is exactly the kind of thing this digest exists to catch,
// not cause.
func Digest(key, typ, description, body string) string {
	h := sha256.New()
	for _, s := range []string{key, typ, description, body} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
