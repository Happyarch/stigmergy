package store

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/happyarch/stigmergy/internal/serr"
)

// Every agent-authored string that gets stored and rendered somewhere another
// agent will read it, run against the same matrix of hostile input.
//
// The matrix is the point. Wiring validation into one field and forgetting the
// next is the obvious way this decays, and a per-field test written by hand
// decays with it — the missing field simply has no test. Here a new entry point
// is one line in this table, and a field nobody wired up fails every hostile
// case at once.

// injector writes payload into one specific field, with every other field of
// that operation left valid.
type injector struct {
	name string
	// line is true when the field has to stay on one line.
	line bool
	// bounded is true when the field has a length limit. Paths do not: a long
	// worktree is awkward, not wrong.
	bounded bool
	// pathLike is true when the field is a path rather than prose, so the
	// benign inputs it must accept are path-shaped. A pattern containing "//"
	// is legitimately refused as unnormalised; the same string in a mail body
	// is just text.
	pathLike bool
	apply    func(t *testing.T, h *sanitationFixture, payload string) error
}

type sanitationFixture struct {
	db       *DB
	n        int
	toRoot   string
	fromRoot string
}

// uniq keeps keys, root ids and scope paths distinct across the hundreds of
// calls this table makes, so nothing fails for a reason the test is not about.
func (h *sanitationFixture) uniq(prefix string) string {
	h.n++
	return fmt.Sprintf("%s-%d", prefix, h.n)
}

func newSanitationFixture(t *testing.T) *sanitationFixture {
	t.Helper()
	db := testProject(t)
	h := &sanitationFixture{db: db}

	from, _, err := db.RegisterRoot(Registration{
		RootID: "r-sender", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sess-from",
	})
	if err != nil {
		t.Fatal(err)
	}
	to, _, err := db.RegisterRoot(Registration{
		RootID: "r-recipient", AgentKind: "codex", Worktree: "/wt", SessionLabel: "sess-to",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.fromRoot, h.toRoot = from.RootID, to.RootID

	if err := db.AddRepo("app", "/tmp/app/.git", "/tmp/app"); err != nil {
		t.Fatal(err)
	}
	return h
}

func injectors() []injector {
	return []injector{
		{"memory.description", true, true, false, func(t *testing.T, h *sanitationFixture, p string) error {
			_, err := h.db.WriteMemory(MemoryWrite{
				Key: h.uniq("mem"), Type: "project", Description: p, Body: "a body", UpdatedBy: "r-test",
			}, "claude-code")
			return err
		}},
		{"memory.body", false, false, false, func(t *testing.T, h *sanitationFixture, p string) error {
			_, err := h.db.WriteMemory(MemoryWrite{
				Key: h.uniq("mem"), Type: "project", Description: "a description", Body: p, UpdatedBy: "r-test",
			}, "claude-code")
			return err
		}},
		{"mail.subject", true, true, false, func(t *testing.T, h *sanitationFixture, p string) error {
			_, err := h.db.SendMessage(SendRequest{
				FromRoot: h.fromRoot, ToRoot: h.toRoot, Subject: p, Body: "a body",
			})
			return err
		}},
		{"mail.body", false, false, false, func(t *testing.T, h *sanitationFixture, p string) error {
			_, err := h.db.SendMessage(SendRequest{
				FromRoot: h.fromRoot, ToRoot: h.toRoot, Subject: "a subject", Body: p,
			})
			return err
		}},
		{"claim.reason", true, true, false, func(t *testing.T, h *sanitationFixture, p string) error {
			_, err := h.db.AcquireClaim(ClaimRequest{
				ScopePath: h.uniq("path"), RootID: "r-sender", Worktree: "/wt", Reason: p,
			})
			return err
		}},
		{"claim.branch", true, true, false, func(t *testing.T, h *sanitationFixture, p string) error {
			_, err := h.db.AcquireClaim(ClaimRequest{
				ScopePath: h.uniq("path"), RootID: "r-sender", Worktree: "/wt",
				Reason: "a reason", Branch: p,
			})
			return err
		}},
		{"claim.worktree", true, false, true, func(t *testing.T, h *sanitationFixture, p string) error {
			_, err := h.db.AcquireClaim(ClaimRequest{
				ScopePath: h.uniq("path"), RootID: "r-sender", Worktree: "/wt/" + p, Reason: "a reason",
			})
			return err
		}},
		{"root.session_label", true, true, false, func(t *testing.T, h *sanitationFixture, p string) error {
			_, _, err := h.db.RegisterRoot(Registration{
				RootID: h.uniq("r"), AgentKind: "claude-code", Worktree: "/wt", SessionLabel: p,
			})
			return err
		}},
		{"root.model", true, true, false, func(t *testing.T, h *sanitationFixture, p string) error {
			_, _, err := h.db.RegisterRoot(Registration{
				RootID: h.uniq("r"), AgentKind: "claude-code", Worktree: "/wt",
				SessionLabel: h.uniq("sess"), Model: p,
			})
			return err
		}},
		{"root.branch", true, true, false, func(t *testing.T, h *sanitationFixture, p string) error {
			_, _, err := h.db.RegisterRoot(Registration{
				RootID: h.uniq("r"), AgentKind: "claude-code", Worktree: "/wt",
				SessionLabel: h.uniq("sess"), Branch: p,
			})
			return err
		}},
		{"root.worktree", true, false, true, func(t *testing.T, h *sanitationFixture, p string) error {
			_, _, err := h.db.RegisterRoot(Registration{
				RootID: h.uniq("r"), AgentKind: "claude-code", Worktree: "/wt/" + p,
				SessionLabel: h.uniq("sess"),
			})
			return err
		}},
		{"verification.reason", false, false, false, func(t *testing.T, h *sanitationFixture, p string) error {
			key := h.uniq("verified")
			if _, err := h.db.WriteMemory(MemoryWrite{
				Key: key, Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
			}, "claude-code"); err != nil {
				t.Fatal(err)
			}
			_, err := h.db.RecordVerification(VerificationRecord{
				Key: key, Outcome: Reaffirmed, ExpectedMemoryVersion: 1, Reason: p, Actor: "r-test",
			})
			return err
		}},
		{"evidence.pattern", true, false, true, func(t *testing.T, h *sanitationFixture, p string) error {
			key := h.uniq("anchored")
			if _, err := h.db.WriteMemory(MemoryWrite{
				Key: key, Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
			}, "claude-code"); err != nil {
				t.Fatal(err)
			}
			_, err := h.db.SetEvidencePolicy(EvidenceSet{
				Key: key, ExpectedMemoryVersion: 1, Actor: "r-test",
				Members: []EvidenceMember{{RepoID: "app", BaseOID: "abc", Paths: []EvidencePath{
					{Kind: PathLiteral, Pattern: p},
				}}},
			})
			return err
		}},
	}
}

// hostile payloads must be refused by every field, one-line or not.
var hostile = map[string]string{
	"NUL":                   "before\x00after",
	"an ANSI colour escape": "\x1b[31mred text\x1b[0m",
	"an ANSI screen clear":  "text\x1b[2J\x1b[H",
	"a backspace":           "visible\x08\x08\x08hidden",
	"DEL":                   "before\x7fafter",
	"a vertical tab":        "before\x0bafter",
	"a form feed":           "before\x0cafter",
	"a bidi override":       "the file is \u202esuoicilam\u202c",
	"a bidi isolate":        "text \u2066reordered\u2069 here",
	"invalid UTF-8":         "text with \xff\xfe raw bytes",
	"a lone surrogate":      "text with \xed\xa0\x80 in it",
}

// benignPaths are what the path-shaped fields must accept: they have their own
// grammar, and prose is not it.
var benignPaths = map[string]string{
	"an ordinary path":      "internal/store/memories.go",
	"a path with spaces":    "docs/a file with spaces.md",
	"a non-ascii path":      "docs/café.md",
	"a dashed path":         "internal/some-dir/a_file.go",
	"a path with a bracket": "internal/gen[0]/x.go",
}

// benign payloads must be accepted by every prose field.
var benign = map[string]string{
	"plain ascii":            "an ordinary reason for doing the thing",
	"unicode prose":          "café — naïve 日本語 🎉 ✓",
	"a short literal escape": `split on \n, never on \r\n`,
	"markdown punctuation":   "`code`, **bold**, [link](http://x), 100% — done",
	"quotes and backslashes": `he said "it\'s fine" \\ probably`,
}

func TestEveryTextFieldRefusesHostileInput(t *testing.T) {
	for _, inj := range injectors() {
		t.Run(inj.name, func(t *testing.T) {
			for name, payload := range hostile {
				t.Run(name, func(t *testing.T) {
					h := newSanitationFixture(t)
					err := inj.apply(t, h, payload)
					if err == nil {
						t.Fatalf("%s accepted %s — it will be rendered into another agent's terminal", inj.name, name)
					}
					requireCode(t, err, serr.InvalidInput)
				})
			}
		})
	}
}

// benignFor picks the input set a field is actually meant to hold.
func benignFor(inj injector) map[string]string {
	if inj.pathLike {
		return benignPaths
	}
	return benign
}

func TestEveryTextFieldAcceptsBenignInput(t *testing.T) {
	for _, inj := range injectors() {
		t.Run(inj.name, func(t *testing.T) {
			for name, payload := range benignFor(inj) {
				t.Run(name, func(t *testing.T) {
					h := newSanitationFixture(t)
					if err := inj.apply(t, h, payload); err != nil {
						t.Fatalf("%s refused %s (%q): %v", inj.name, name, payload, err)
					}
				})
			}
		})
	}
}

// A line break belongs in a body and nowhere else. The same payload has to be
// refused by one set of fields and accepted by the other, which is the check
// that the line/block distinction is actually wired per field rather than
// applied uniformly.
func TestLineFieldsRefuseWhatBlockFieldsAccept(t *testing.T) {
	for _, inj := range injectors() {
		t.Run(inj.name, func(t *testing.T) {
			h := newSanitationFixture(t)
			err := inj.apply(t, h, "first line\nsecond line")
			if inj.line && err == nil {
				t.Errorf("%s is a one-line field but accepted a line break", inj.name)
			}
			if !inj.line && err != nil {
				t.Errorf("%s is a block field but refused a line break: %v", inj.name, err)
			}
		})
	}
}

func TestBoundedFieldsRefuseOverlongInput(t *testing.T) {
	long := strings.Repeat("x", MaxLineLength+1)
	for _, inj := range injectors() {
		t.Run(inj.name, func(t *testing.T) {
			h := newSanitationFixture(t)
			err := inj.apply(t, h, long)
			if inj.bounded && err == nil {
				t.Errorf("%s accepted %d characters on one line", inj.name, len(long))
			}
			if !inj.bounded && err != nil {
				t.Errorf("%s has no length limit but refused a long value: %v", inj.name, err)
			}
		})
	}
}

// The carriage return is DEFUSED rather than refused, and the distinction is
// deliberate enough to pin.
//
// In a terminal, "\r" returns the cursor to the start of the line, so
// "the real text\rthe fake text" DISPLAYS as only the fake text — the first half
// is overwritten and never seen. That is a real way to hide content from a
// reader inside text a reviewer believes they have read.
//
// Refusing every CR would be wrong: CRLF is overwhelmingly just a Windows line
// ending, and rejecting it would turn "your editor saved this file" into an
// error. Converting it to a newline removes the attack completely — both halves
// end up visible on their own lines — and costs a legitimate author nothing. In
// a one-line field it becomes a newline and is then refused for being one.
func TestACarriageReturnIsDefusedNotStored(t *testing.T) {
	h := newSanitationFixture(t)
	const attack = "the real text\rthe fake text"

	if _, err := h.db.WriteMemory(MemoryWrite{
		Key: "cr-body", Type: "project", Description: "d", Body: attack, UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatalf("a block field refused a carriage return outright: %v", err)
	}
	m, err := h.db.ReadMemory("cr-body")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(m.Body, '\r') {
		t.Fatal("a carriage return was stored: it will overwrite the line it follows")
	}
	if m.Body != "the real text\nthe fake text" {
		t.Fatalf("body = %q — both halves must survive, on separate lines", m.Body)
	}

	// The same payload in a one-line field has nowhere to put the newline it
	// becomes, so it is refused.
	_, err = h.db.WriteMemory(MemoryWrite{
		Key: "cr-desc", Type: "project", Description: attack, Body: "b", UpdatedBy: "r-test",
	}, "claude-code")
	requireCode(t, err, serr.InvalidInput)
}

// A twice-escaped blob is the pokeyellow failure, and it is not specific to
// memory bodies — mail and verification reasons take the same text from the same
// agents through the same serialisers.
func TestEveryTextFieldRefusesADoubleEscapedBlob(t *testing.T) {
	blob := strings.Repeat(`some text about the thing\nand the next line\n`, 8)
	if strings.Contains(blob, "\n") {
		t.Fatal("fixture is wrong: it must contain no real newline")
	}
	for _, inj := range injectors() {
		t.Run(inj.name, func(t *testing.T) {
			h := newSanitationFixture(t)
			if err := inj.apply(t, h, blob); err == nil {
				t.Errorf("%s stored a %d-character blob with no line breaks in it", inj.name, len(blob))
			}
		})
	}
}

// Whatever survives validation has to come back byte-identical. Silent
// alteration of stored content is its own kind of corruption.
func TestAcceptedTextRoundTripsExactly(t *testing.T) {
	db := testProject(t)
	for name, payload := range benign {
		t.Run(name, func(t *testing.T) {
			key := "roundtrip-" + strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' {
					return r
				}
				return '-'
			}, name)
			if _, err := db.WriteMemory(MemoryWrite{
				Key: key, Type: "project", Description: "d", Body: payload, UpdatedBy: "r-test",
			}, "claude-code"); err != nil {
				t.Fatal(err)
			}
			m, err := db.ReadMemory(key)
			if err != nil {
				t.Fatal(err)
			}
			if m.Body != payload {
				t.Errorf("body changed in storage:\n  sent %q\n   got %q", payload, m.Body)
			}
		})
	}
}

// Normalisation has to be applied by every entry point, not only the one it was
// written for.
func TestNormalisationIsAppliedEverywhere(t *testing.T) {
	h := newSanitationFixture(t)

	msg, err := h.db.SendMessage(SendRequest{
		FromRoot: h.fromRoot, ToRoot: h.toRoot,
		Subject: "  a padded subject  ", Body: "\ufeffbody one\r\nbody two\n\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "a padded subject" {
		t.Errorf("mail subject = %q", msg.Subject)
	}
	if msg.Body != "body one\nbody two" {
		t.Errorf("mail body = %q", msg.Body)
	}

	claim, err := h.db.AcquireClaim(ClaimRequest{
		ScopePath: "some/path", RootID: "r-sender", Worktree: "/wt", Reason: "  padded reason\r\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Reason != "padded reason" {
		t.Errorf("claim reason = %q", claim.Reason)
	}

	key := "verified-normalisation"
	if _, err := h.db.WriteMemory(MemoryWrite{
		Key: key, Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatal(err)
	}
	v, err := h.db.RecordVerification(VerificationRecord{
		Key: key, Outcome: Reaffirmed, ExpectedMemoryVersion: 1,
		Reason: "  checked it\r\nproperly  ", Actor: "r-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Reason != "checked it\nproperly" {
		t.Errorf("verification reason = %q", v.Reason)
	}
}

// FuzzTextRules asserts the properties the rules have to satisfy for any input
// at all, rather than for the cases someone thought to write down.
func FuzzTextRules(f *testing.F) {
	for _, s := range hostile {
		f.Add(s)
	}
	for _, s := range benign {
		f.Add(s)
	}
	f.Add("")
	f.Add("\ufeff\r\n  ")
	f.Add(strings.Repeat(`a\n`, 200))
	f.Add("line\nline\ttab")

	f.Fuzz(func(t *testing.T, s string) {
		got := NormalizeText(s)

		// Idempotent: normalising twice is normalising once. Otherwise the value
		// stored differs from the value a caller gets back from validating.
		if again := NormalizeText(got); again != got {
			t.Fatalf("NormalizeText is not idempotent:\n %q\n→%q\n→%q", s, got, again)
		}
		// Never manufactures invalid UTF-8 out of valid input. Trimming and
		// replacing on byte boundaries is where that would go wrong.
		if utf8.ValidString(s) && !utf8.ValidString(got) {
			t.Fatalf("NormalizeText turned valid UTF-8 into invalid: %q → %q", s, got)
		}
		// Never introduces a character that validation would then refuse. A
		// normaliser that creates its own violations is a loop nobody can exit.
		if err := ValidateBlock("x", s); err == nil {
			if err := ValidateBlock("x", got); err != nil {
				t.Fatalf("NormalizeText made valid text invalid: %q → %q (%v)", s, got, err)
			}
		}
		// No CR survives, ever: it is unconditionally a line break.
		if strings.ContainsRune(got, '\r') {
			t.Fatalf("a carriage return survived normalisation: %q", got)
		}
		// A one-line field never ends up holding a line break.
		if ValidateLine("x", got, 0) == nil && strings.ContainsRune(got, '\n') {
			t.Fatalf("ValidateLine accepted text containing a newline: %q", got)
		}
	})
}
