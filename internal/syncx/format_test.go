package syncx

import (
	"strings"
	"testing"
)

// The digest covers content and nothing else (docs/sync-model.md §3.3).
//
// This is what makes convergence possible rather than merely likely: the same
// correction typed independently on two machines produces two rows with
// different versions, different updated_by, and different timestamps, and they
// must still agree. A digest that covered any of those would turn every such
// pair into a conflict for a human to resolve by picking one of two identical
// texts.
func TestDigestCoversContentOnly(t *testing.T) {
	a := Digest("alpha", "project", "a description", "the body")
	b := Digest("alpha", "project", "a description", "the body")
	if a != b {
		t.Fatalf("identical content produced different digests: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Errorf("digest %q does not carry its algorithm; store.BodyHash sets the sha256-hex convention", a)
	}
}

// Every covered field changes the digest. A field that does not is a field two
// machines can silently disagree on forever.
func TestDigestChangesWithEveryCoveredField(t *testing.T) {
	base := Digest("alpha", "project", "a description", "the body")
	cases := map[string]string{
		"key":         Digest("beta", "project", "a description", "the body"),
		"type":        Digest("alpha", "reference", "a description", "the body"),
		"description": Digest("alpha", "project", "another description", "the body"),
		"body":        Digest("alpha", "project", "a description", "another body"),
	}
	for field, got := range cases {
		if got == base {
			t.Errorf("changing %s did not change the digest", field)
		}
	}
}

// Field boundaries are not ambiguous. Concatenating the fields without
// delimiting them would make ("ab","c") and ("a","bc") hash alike, so two
// genuinely different memories would read as agreed and neither would ever
// sync.
func TestDigestIsNotAmbiguousAcrossFieldBoundaries(t *testing.T) {
	if Digest("alpha", "project", "ab", "c") == Digest("alpha", "project", "a", "bc") {
		t.Fatal("digest does not delimit its fields: two different memories hash alike")
	}
}

func fullRecord() MemoryRecord {
	r := MemoryRecord{
		Key:             "shipping-a-migration-locks-the-repo",
		Type:            "project",
		Description:     "A new migration blocks every adopted project until doctor runs.",
		Body:            "The hook that guards edits refuses to run against a database\nwhose schema version differs from the binary's.\n",
		CreatedAt:       "2026-07-21T09:14:02.113004212Z",
		UpdatedAt:       "2026-08-02T15:00:06.547664879Z",
		OriginDevice:    "d-3f2a9c81b4de7a05",
		OriginUpdatedBy: "r-e226af35bd46",
		OriginVersion:   6,
	}
	r.Digest = Digest(r.Key, r.Type, r.Description, r.Body)
	return r
}

func TestMemoryRoundTrip(t *testing.T) {
	want := fullRecord()

	encoded, err := MarshalMemory(want)
	if err != nil {
		t.Fatalf("MarshalMemory: %v", err)
	}
	got, err := UnmarshalMemory(encoded)
	if err != nil {
		t.Fatalf("UnmarshalMemory: %v", err)
	}

	if got.Key != want.Key || got.Type != want.Type || got.Description != want.Description {
		t.Errorf("frontmatter did not round-trip: %+v", got)
	}
	if got.Body != want.Body {
		t.Errorf("body did not round-trip:\n got %q\nwant %q", got.Body, want.Body)
	}
	if got.Digest != want.Digest {
		t.Errorf("digest did not round-trip: got %q want %q", got.Digest, want.Digest)
	}
	if got.CreatedAt != want.CreatedAt || got.UpdatedAt != want.UpdatedAt {
		t.Errorf("timestamps did not round-trip: got %q/%q want %q/%q",
			got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
	if got.OriginDevice != want.OriginDevice || got.OriginUpdatedBy != want.OriginUpdatedBy || got.OriginVersion != want.OriginVersion {
		t.Errorf("provenance did not round-trip: %+v", got)
	}
}

// Marshalling is deterministic. The exporter writes into a git working tree, so
// a record that re-serialises differently each run produces a commit on every
// sync and a diff nobody can read — and it would defeat the export → import →
// export byte-identity check that proves the format lossless.
func TestMarshalMemoryIsDeterministic(t *testing.T) {
	r := fullRecord()
	first, err := MarshalMemory(r)
	if err != nil {
		t.Fatalf("MarshalMemory: %v", err)
	}
	for i := 0; i < 8; i++ {
		again, err := MarshalMemory(r)
		if err != nil {
			t.Fatalf("MarshalMemory: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("MarshalMemory is not deterministic:\nfirst:\n%s\nagain:\n%s", first, again)
		}
	}
}

// A field written by a NEWER stigmergy survives a rewrite by this one.
//
// docs/sync-model.md §5 makes this a hard requirement rather than a nicety:
// the wire format is versioned as a whole and the two machines are not required
// to run the same binary, so an older laptop that round-trips the tree must not
// strip what the newer machine wrote. Dropping the field would be silent, and
// the data would be gone from both machines by the following sync.
func TestUnknownFieldsSurviveARewrite(t *testing.T) {
	original := []byte(`---
key: alpha
type: project
description: a description
digest: sha256:0000
created_at: 2026-08-01T00:00:00.000000000Z
updated_at: 2026-08-02T00:00:00.000000000Z
origin_device: d-newer
origin_updated_by: r-newer
origin_version: 3
confidence_from_a_later_stigmergy: 0.87
---
the body
`)

	parsed, err := UnmarshalMemory(original)
	if err != nil {
		t.Fatalf("UnmarshalMemory: %v", err)
	}
	if len(parsed.Unknown) == 0 {
		t.Fatal("an unrecognised frontmatter field was dropped at parse time")
	}

	rewritten, err := MarshalMemory(parsed)
	if err != nil {
		t.Fatalf("MarshalMemory: %v", err)
	}
	if !strings.Contains(string(rewritten), "confidence_from_a_later_stigmergy") {
		t.Fatalf("rewriting stripped a newer binary's field:\n%s", rewritten)
	}

	again, err := UnmarshalMemory(rewritten)
	if err != nil {
		t.Fatalf("UnmarshalMemory after rewrite: %v", err)
	}
	if again.Body != parsed.Body || again.Key != parsed.Key {
		t.Errorf("record did not survive a second round-trip: %+v", again)
	}
}

// A body that itself looks like frontmatter must not be mistaken for it. Memory
// bodies in this repository routinely quote YAML, SQL and markdown rules, and a
// parser that stops at the first "---" would truncate one silently.
func TestBodyContainingFrontmatterDelimiterRoundTrips(t *testing.T) {
	r := fullRecord()
	r.Body = "A memory file starts like this:\n\n---\nkey: example\n---\n\nand that is the whole trap.\n"
	r.Digest = Digest(r.Key, r.Type, r.Description, r.Body)

	encoded, err := MarshalMemory(r)
	if err != nil {
		t.Fatalf("MarshalMemory: %v", err)
	}
	got, err := UnmarshalMemory(encoded)
	if err != nil {
		t.Fatalf("UnmarshalMemory: %v", err)
	}
	if got.Body != r.Body {
		t.Fatalf("body containing a delimiter was truncated:\n got %q\nwant %q", got.Body, r.Body)
	}
	if got.Digest != Digest(got.Key, got.Type, got.Description, got.Body) {
		t.Error("round-tripped record no longer matches its own digest")
	}
}

// The body is stored verbatim. Bodies here carry indented SQL, code fences and
// trailing newlines that ValidateBlock already governs, and a serializer that
// re-wrapped or trimmed them would change the digest and manufacture a conflict
// out of a round-trip.
func TestBodyIsNotReflowedOrTrimmed(t *testing.T) {
	for _, body := range []string{
		"    indented four spaces\n",
		"trailing spaces at the end   \nnext line\n",
		"a very long single line that a wrapping serializer would happily fold at some column boundary and thereby change the content of a memory without anyone asking it to\n",
		"tab\tseparated\tvalues\n",
	} {
		r := fullRecord()
		r.Body = body
		r.Digest = Digest(r.Key, r.Type, r.Description, r.Body)

		encoded, err := MarshalMemory(r)
		if err != nil {
			t.Fatalf("MarshalMemory(%q): %v", body, err)
		}
		got, err := UnmarshalMemory(encoded)
		if err != nil {
			t.Fatalf("UnmarshalMemory(%q): %v", body, err)
		}
		if got.Body != body {
			t.Errorf("body altered in transit:\n got %q\nwant %q", got.Body, body)
		}
	}
}
