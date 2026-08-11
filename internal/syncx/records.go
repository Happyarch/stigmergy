// Package syncx holds the wire format and the merge rules for cross-machine
// memory sync (docs/sync-model.md). It is named syncx rather than sync because
// the standard library already owns that name and is imported widely enough
// that aliasing it at every call site would read as a mistake.
//
// This package touches no database and no network, and it is deliberately kept
// that way — docs/sync-model.md §8, Stage A: "No git, no network, no
// internal/store write calls." Reconcile takes two record sets and a base and
// returns a plan; nothing here decides whether that plan is ever carried out.
// That is what makes the merge rules provable as pure functions, the way
// internal/claims is pure over path algebra.
package syncx

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// FormatVersion is the wire format's own version, carried as the single line
// of the FORMAT file at the root of an exported tree (docs/sync-model.md §5).
//
// It is separate from a database's schema version, and that separation is the
// point (D8): the wire format is defined in terms of fields, not tables, so a
// machine on project schema v13 and one on v14 can still exchange memories
// without either migrating. A reader finding a FORMAT value higher than this
// constant refuses the whole run rather than guessing at fields it has never
// seen; a lower value is read and rewritten at the current version on the next
// export.
const FormatVersion = 1

// MemoryRecord is one memory as it travels between machines: the YAML
// frontmatter of a "<key>.md" file under memories/, plus the markdown body
// below it. The shape matches what internal/importer already parses, because
// that format was not invented for this — a human can open a memory file in an
// editor, and a git diff of one is a diff of prose, which is the entire reason
// docs/sync-model.md §2 chooses a directory of text files as the transport.
//
// Unknown carries every frontmatter field this binary does not recognise.
// docs/sync-model.md §5 makes preserving it a hard requirement, not a
// convenience: an older binary rewriting a record a newer one wrote must
// round-trip fields it cannot interpret, or a sync from an old laptop silently
// strips whatever the newer machine added. yaml's ",inline" tag on a
// map[string]any is what makes that automatic — every key the named fields
// below do not claim lands here instead of being dropped on decode, and is
// re-emitted (sorted, so the output stays deterministic) on encode.
type MemoryRecord struct {
	Key             string         `yaml:"key"`
	Type            string         `yaml:"type"`
	Description     string         `yaml:"description"`
	Digest          string         `yaml:"digest"`
	CreatedAt       string         `yaml:"created_at"`
	UpdatedAt       string         `yaml:"updated_at"`
	OriginDevice    string         `yaml:"origin_device"`
	OriginUpdatedBy string         `yaml:"origin_updated_by"`
	OriginVersion   int            `yaml:"origin_version"`
	Unknown         map[string]any `yaml:",inline"`
	// Body is the markdown that follows the frontmatter's closing "---", never
	// part of it — hence yaml:"-". Keeping it out of the struct that yaml
	// marshals is what lets MarshalMemory place it verbatim after the fence
	// rather than needing it escaped into a scalar.
	Body string `yaml:"-"`
}

// frontmatterRe splits a record file into its YAML header and its body,
// matching the shape internal/importer's own frontmatterRe accepts: a leading
// "---" fence, the header, a closing "---" fence, optionally followed by a
// newline before the body starts. Kept as a private copy rather than an
// import of internal/importer — that package pulls in internal/store, and this
// one must not (see the package doc: no store write calls, and no store
// dependency at all).
var frontmatterRe = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?`)

// MarshalMemory renders one memory as markdown with YAML frontmatter.
//
// Exactly one newline is appended after the body, UNCONDITIONALLY — even when
// Body already ends with one of its own, which a body ending in a blank line
// or a trailing paragraph break legitimately does. That newline belongs to
// the file, not to the body: it is what lets a text editor treat the file as
// ordinary text ending in a newline, the way every well-formed one does.
// UnmarshalMemory strips exactly one trailing newline back off — never more —
// so the two are exact inverses of each other regardless of what Body itself
// ends with: a body with no trailing newline round-trips with none, and a
// body that already ends in "\n" round-trips with that newline intact rather
// than silently trimmed away. Conditioning the append on whether Body already
// had a trailing newline (the first version of this function did) breaks
// exactly that second case: the file would still end in one newline, but
// UnmarshalMemory's unconditional strip would remove one the body actually
// meant to keep.
func MarshalMemory(r MemoryRecord) ([]byte, error) {
	fm, err := yaml.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("syncx: marshal frontmatter for %q: %w", r.Key, err)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fm)
	buf.WriteString("---\n")
	buf.WriteString(r.Body)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// UnmarshalMemory reads one memory record back from the shape MarshalMemory
// produces. A file with no frontmatter fence at all is refused rather than
// treated as a bodyless record — every record this package writes has one, so
// its absence means the file came from somewhere else or was truncated.
func UnmarshalMemory(b []byte) (MemoryRecord, error) {
	m := frontmatterRe.FindSubmatch(b)
	if m == nil {
		return MemoryRecord{}, fmt.Errorf("syncx: no YAML frontmatter fence found")
	}
	var r MemoryRecord
	if err := yaml.Unmarshal(m[1], &r); err != nil {
		return MemoryRecord{}, fmt.Errorf("syncx: unmarshal frontmatter: %w", err)
	}
	body := b[len(m[0]):]
	r.Body = strings.TrimSuffix(string(body), "\n")
	return r, nil
}

// Base is the content both sides last agreed on for one key — the causality
// signal docs/sync-model.md §3.2 needs because version never travels (D2).
// Stored per scope in memory_sync_base, one row per key, cascade-deleted the
// moment the memory is (§3.2): a locally deleted memory loses its base row and
// gains a Tombstone instead, and Reconcile is written for exactly that pair of
// states, never for both existing at once.
type Base struct {
	Key      string
	Digest   string
	DeviceID string
	At       string
}

// Tombstone records that a memory or a link once existed here and was
// deliberately deleted, so a later sync can propagate the delete instead of
// treating the deleted side as merely "never had it" and resurrecting the
// content from whichever machine still holds a copy (docs/sync-model.md §3.7,
// D5). Kind distinguishes the two identity shapes sync ever deletes; Stage A
// writes memory tombstones from DeleteMemory and link tombstones from
// DeleteLink, but Reconcile itself only ever reasons about the memory kind —
// link merge is union-by-identity (§3.6), not a Decision.
type Tombstone struct {
	Kind     string `json:"kind"`  // "memory" | "link"
	Ident    string `json:"ident"` // the key, or "<key_a> <key_b>" — no space is legal in a key
	Digest   string `json:"digest"`
	DeviceID string `json:"device_id"`
	At       string `json:"at"`
}
