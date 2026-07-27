package store

import (
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/serr"
)

// The shapes below are taken from real memories on this machine, because the
// whole difficulty of this validation is telling two of them apart.

// A whole document collapsed onto one line by an agent that escaped its body
// twice. Three memories in pokeyellow_msdos look exactly like this — 31, 58 and
// 76 literal \n, not one real line break — and they are unreadable.
func doubleEscapedBody() string {
	return `menu-intro timing verification. Reusable method for verifying any ANIMATED cinematic.\n` +
		`TWO CLASSES of animation, TWO methods:\n\n=== CLASS 1: DETERMINISTIC animation ===\n` +
		`FAITHFULNESS is the record-by-record proof, no pixel trace needed.\n` +
		`Verify with faithdiff, then compare the frame bins against the golden run.\n`
}

// Legitimate prose that happens to contain a literal \n, alongside real
// paragraphs. Four memories look like this and every one of them is correct —
// they document printf formats, regexes and escape-sequence bugs.
func bodyAboutEscapeSequences() string {
	return "The charmap bug: a `\\n` in a char literal was assembled as ASCII 0x0A\n" +
		"instead of the game's own newline token.\n\n" +
		"Grep for `\\n` in the text scripts before trusting any of them.\n"
}

func TestDoubleEscapedBodiesAreRefused(t *testing.T) {
	db := testProject(t)
	_, err := db.WriteMemory(MemoryWrite{
		Key: "collapsed", Type: "project", Description: "a body escaped twice",
		Body: doubleEscapedBody(), UpdatedBy: "r-test",
	}, "claude-code")
	e := requireCode(t, err, serr.InvalidInput)
	if !strings.Contains(e.Message, "escaped twice") {
		t.Errorf("the error does not say what went wrong: %s", e.Message)
	}
	if !strings.Contains(e.Message, "backslash and n") {
		t.Errorf("the error does not say how to fix it: %s", e.Message)
	}
}

// The test that stops the cure being worse than the disease. A rule that simply
// banned literal \n would refuse four correct memories to catch three broken
// ones.
func TestProseAboutEscapeSequencesIsAccepted(t *testing.T) {
	db := testProject(t)
	if _, err := db.WriteMemory(MemoryWrite{
		Key: "charmap-bug", Type: "reference", Description: "a char literal assembled as ASCII",
		Body: bodyAboutEscapeSequences(), UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatalf("a legitimate body about escape sequences was refused: %v", err)
	}
	m, err := db.ReadMemory("charmap-bug")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Body, `\n`) {
		t.Error("the literal escape sequence was stripped out of prose that was about it")
	}
}

// A short single-line body mentioning \n is not a serialisation failure. The
// length floor is what separates "a sentence about escapes" from "a document
// that lost its line breaks".
func TestAShortLineMentioningEscapesIsAccepted(t *testing.T) {
	db := testProject(t)
	if _, err := db.WriteMemory(MemoryWrite{
		Key: "short-note", Type: "project", Description: "line endings",
		Body: `Split on \n, never on \r\n — the parser normalises first.`, UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatalf("a short line about escapes was refused: %v", err)
	}
}

func TestControlCharactersAreRefused(t *testing.T) {
	db := testProject(t)
	cases := map[string]string{
		"NUL":                   "a body with a \x00 in it, which truncates in anything that reaches C",
		"an ANSI escape":        "a body with \x1b[31mred text\x1b[0m smuggled into it",
		"a backspace":           "a body with a \x08 that erases what came before it",
		"DEL":                   "a body with \x7f in it",
		"a vertical tab":        "a body with \x0b in it",
		"an invalid UTF-8 byte": "a body with \xff\xfe raw bytes",
		"a bidi override":       "a body with \u202e reversed text",
		"an isolate":            "a body with \u2066 an isolate in it",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := db.WriteMemory(MemoryWrite{
				Key: "ctrl", Type: "project", Description: "d",
				Body: body, UpdatedBy: "r-test",
			}, "claude-code")
			requireCode(t, err, serr.InvalidInput)
		})
	}
}

// Tabs and newlines are the two that mean something. Refusing them would refuse
// most real memories, which are markdown with indented code.
func TestTabsAndNewlinesSurvive(t *testing.T) {
	db := testProject(t)
	body := "Here is code:\n\n\tif err != nil {\n\t\treturn err\n\t}\n\nAnd prose after it."
	if _, err := db.WriteMemory(MemoryWrite{
		Key: "with-code", Type: "reference", Description: "code in a memory",
		Body: body, UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatalf("ordinary markdown was refused: %v", err)
	}
	m, _ := db.ReadMemory("with-code")
	if !strings.Contains(m.Body, "\n\t\treturn err") {
		t.Errorf("indentation did not survive: %q", m.Body)
	}
}

// The description is what every listing renders. A newline in it breaks the
// alignment of every other row.
func TestDescriptionMustBeOneLine(t *testing.T) {
	db := testProject(t)
	_, err := db.WriteMemory(MemoryWrite{
		Key: "multiline-desc", Type: "project",
		Description: "first line\nsecond line", Body: "b", UpdatedBy: "r-test",
	}, "claude-code")
	e := requireCode(t, err, serr.InvalidInput)
	if !strings.Contains(e.Message, "single line") {
		t.Errorf("unhelpful error: %s", e.Message)
	}
}

func TestDescriptionLengthIsBounded(t *testing.T) {
	db := testProject(t)
	_, err := db.WriteMemory(MemoryWrite{
		Key: "long-desc", Type: "project",
		Description: strings.Repeat("x", MaxDescriptionLength+1), Body: "b", UpdatedBy: "r-test",
	}, "claude-code")
	requireCode(t, err, serr.InvalidInput)

	if _, err := db.WriteMemory(MemoryWrite{
		Key: "ok-desc", Type: "project",
		Description: strings.Repeat("x", MaxDescriptionLength), Body: "b", UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatalf("a description exactly at the limit was refused: %v", err)
	}
}

// Normalisation is for the cases with one obvious meaning. 74 of the 159 live
// memories carry stray whitespace, almost all of it a trailing newline.
func TestNormalisationOfUnambiguousFormatting(t *testing.T) {
	db := testProject(t)
	if _, err := db.WriteMemory(MemoryWrite{
		Key: "messy", Type: "project",
		Description: "  a padded description  ",
		Body:        "\ufeffline one\r\nline two\rline three\n\n\n",
		UpdatedBy:   "r-test",
	}, "claude-code"); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := db.ReadMemory("messy")
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "a padded description" {
		t.Errorf("description = %q", m.Description)
	}
	if m.Body != "line one\nline two\nline three" {
		t.Errorf("body = %q", m.Body)
	}
	if strings.ContainsRune(m.Body, '\r') || strings.HasPrefix(m.Body, "\ufeff") {
		t.Error("CR or BOM survived normalisation")
	}
}

func TestNormalizeText(t *testing.T) {
	cases := map[string]string{
		"\ufeffwith a bom":      "with a bom",
		"crlf\r\nendings":       "crlf\nendings",
		"lone\rcr":              "lone\ncr",
		"  padded  ":            "padded",
		"trailing newlines\n\n": "trailing newlines",
		"unchanged":             "unchanged",
	}
	for in, want := range cases {
		if got := NormalizeText(in); got != want {
			t.Errorf("NormalizeText(%q) = %q, want %q", in, got, want)
		}
	}
}

// Doctor reports what is already stored and would not be accepted now. It must
// find the collapsed body and leave it alone.
func TestUnreadableMemoriesReportsWithoutRepairing(t *testing.T) {
	db := testProject(t)
	if _, err := write(t, db, "fine", "A normal body.\n\nWith paragraphs.", nil); err != nil {
		t.Fatal(err)
	}
	// Planted behind the write path, exactly as a legacy row exists today.
	if _, err := db.Exec(
		`INSERT INTO memories(key, type, description, body, version, updated_by, created_at, updated_at)
		 VALUES('legacy-collapsed', 'project', 'a legacy row', ?, 1, 'r-old', ?, ?)`,
		doubleEscapedBody(), Now(), Now()); err != nil {
		t.Fatal(err)
	}

	problems, err := db.UnreadableMemories()
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0].Key != "legacy-collapsed" {
		t.Fatalf("problems = %+v, want just the collapsed one", problems)
	}
	if problems[0].Reason == "" {
		t.Error("the report says which memory but not what is wrong with it")
	}

	// Reporting must not have touched it: repairing means guessing what the
	// author meant, and a wrong guess is stored as if they had written it.
	var body string
	if err := db.QueryRow(`SELECT body FROM memories WHERE key = 'legacy-collapsed'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != doubleEscapedBody() {
		t.Error("the reporting pass rewrote a body")
	}
}

// A legacy row that cannot be re-validated must not become impossible to fix.
// Reading it has to keep working, or nobody can see what it was meant to say.
func TestALegacyBadMemoryCanStillBeReadAndRewritten(t *testing.T) {
	db := testProject(t)
	if _, err := db.Exec(
		`INSERT INTO memories(key, type, description, body, version, updated_by, created_at, updated_at)
		 VALUES('legacy', 'project', 'legacy', ?, 1, 'r-old', ?, ?)`,
		doubleEscapedBody(), Now(), Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadMemory("legacy"); err != nil {
		t.Fatalf("a legacy row became unreadable: %v", err)
	}
	if _, err := db.WriteMemory(MemoryWrite{
		Key: "legacy", Type: "project", Description: "legacy, fixed",
		Body: "Now with\n\nreal line breaks.", UpdatedBy: "r-test", ExpectedVersion: intp(1),
	}, "claude-code"); err != nil {
		t.Fatalf("a legacy row could not be rewritten into a valid one: %v", err)
	}
}
