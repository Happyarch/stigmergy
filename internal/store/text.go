package store

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/happyarch/stigmergy/internal/serr"
)

// Memory text is written by agents, read by agents, and rendered into terminals
// and transcripts on the way. This file is what stands between "an agent
// serialised its body wrong" and "a memory nobody can read again".
//
// The rules split into two kinds, and the split is the design:
//
//   - NORMALISE what has one obvious intended meaning: a BOM, CRLF line
//     endings, a trailing newline. Rejecting those would be pedantry; they are
//     unambiguous and fixing them silently costs nothing.
//   - REJECT what is ambiguous or destructive: control characters, a body that
//     was clearly escaped twice, a description that is not one line. Silently
//     "fixing" those means guessing what the agent meant, and a wrong guess is
//     stored forever as if it were what they wrote.

// DoubleEscapeMinLength is how long a single-line body has to be before literal
// "\n" sequences in it are treated as a serialisation failure rather than as
// prose about escape sequences.
//
// Both halves of that test matter, and the live data is what set them. Across
// 159 memories on this machine, four bodies contained literal "\n" alongside
// hundreds of real newlines — they document printf formats and regexes, and are
// entirely correct. Three others contained 31, 58 and 76 literal "\n" and NOT
// ONE real newline: whole documents collapsed to a single line, which is the
// shape an agent produces when it JSON-encodes a body that was already going to
// be JSON-encoded. A rule that only counted literal "\n" would have destroyed
// the first group to catch the second.
const DoubleEscapeMinLength = 200

// MaxLineLength bounds any single-line agent-authored field: a memory
// description, a claim reason, a mail subject. Each of them is rendered inline
// somewhere another agent is reading — a conflict message, a roster row, an
// index — so a paragraph in one costs every reader who did not write it.
const MaxLineLength = 500

// MaxDescriptionLength is the memory description's share of that limit, named
// separately because it is the one an agent meets most often.
const MaxDescriptionLength = MaxLineLength

// NormalizeText cleans up the unambiguous cases, in place of complaining.
//
// It runs to a FIXED POINT rather than making one pass, and that is not
// defensive coding \u2014 a single pass is provably wrong here. Stripping the BOM
// before trimming leaves the BOM in " \ufeffx", because it is not leading until
// the space has gone; trimming first leaves it in "\ufeff x" for the mirror
// reason; and either order leaves the second BOM in "\ufeff\ufeff". Each of
// those normalises to something that would normalise again to something else,
// so a caller validating what it holds and a store writing what it was given
// could disagree about the same string. A fuzz test found it in fifteen seconds.
//
// Termination is not in doubt: every step can only remove bytes, and the loop
// exits the moment one changes nothing.
func NormalizeText(s string) string {
	for {
		before := s
		// Written as an escape, never as the character: a literal BOM in this
		// file is a compile error, and the literal bidi overrides rejected below
		// would be invisible in the very source that rejects them.
		s = strings.TrimLeft(s, "\ufeff")
		// CRLF and lone CR both mean "line break" and nothing else. Left alone
		// they survive into every rendering as stray blank lines or ^M.
		if strings.ContainsRune(s, '\r') {
			s = strings.ReplaceAll(s, "\r\n", "\n")
			s = strings.ReplaceAll(s, "\r", "\n")
		}
		s = strings.TrimSpace(s)
		if s == before {
			return s
		}
	}
}

// ValidateBlock checks free-form multi-line text: a memory body, a mail body, a
// verification reason. Newlines and tabs are expected; everything in the rules
// above still applies.
//
// field names the input in the error, because an agent that gets one back has to
// know which of several it needs to fix.
func ValidateBlock(field, s string) error {
	if !utf8.ValidString(s) {
		return serr.E(serr.InvalidInput,
			"%s is not valid UTF-8. Send text, not raw bytes: whatever produced this has mangled the encoding, and the damage is already in the string", field)
	}
	if err := rejectControlChars(field, s); err != nil {
		return err
	}
	if err := rejectBidiOverrides(field, s); err != nil {
		return err
	}
	return rejectDoubleEscaped(field, s)
}

// ValidateLine checks text that has to stay on one line, because it is rendered
// inline somewhere: a conflict message, a roster row, an index.
//
// maxRunes of zero or less means no length limit, which is for the fields that
// are paths rather than prose — a long path is awkward, not wrong, and refusing
// one would lock an agent out of its own worktree over cosmetics.
func ValidateLine(field, s string, maxRunes int) error {
	if err := ValidateBlock(field, s); err != nil {
		return err
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return serr.E(serr.InvalidInput,
			"%s must be a single line, but it has a line break at character %d. It is rendered inline where other agents read it, so a second line breaks whatever is around it", field, i+1)
	}
	if maxRunes > 0 {
		if n := utf8.RuneCountInString(s); n > maxRunes {
			return serr.E(serr.InvalidInput,
				"%s is %d characters; the limit is %d. It is a one-line summary, not the content itself", field, n, maxRunes)
		}
	}
	return nil
}

// rejectControlChars refuses anything that is neither text nor a line break.
//
// Tab and newline are the only control characters with a meaning here. The rest
// arrive by accident and cause damage out of proportion to how they got in: NUL
// truncates the string in anything that reaches C, and ESC (0x1B) opens an ANSI
// sequence that will be interpreted by the terminal of the next agent or human
// who reads this memory — colours, cursor movement, or a line that erases
// itself. Memory text is displayed, so an escape sequence stored here is an
// escape sequence executed somewhere later.
func rejectControlChars(field, s string) error {
	for i, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return serr.E(serr.InvalidInput,
				"%s contains the control character %s at character %d. Only tabs and newlines are allowed — the rest do not survive being displayed, and %s in particular is how text ends up unreadable or rewriting someone's terminal",
				field, controlName(r), i+1, controlName(r))
		}
	}
	return nil
}

// rejectBidiOverrides refuses the directionality controls that let text display
// in an order different from the order it is stored in.
//
// This is the Trojan Source trick, and memories are exactly the wrong place to
// allow it: they are instructions that other agents read and act on, so text
// that renders as one thing and reads as another is a way to smuggle direction
// past review. No technical note needs these characters, so the honest trade is
// to refuse them outright.
func rejectBidiOverrides(field, s string) error {
	for i, r := range s {
		switch r {
		case '\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
			'\u2066', '\u2067', '\u2068', '\u2069':
			return serr.E(serr.InvalidInput,
				"%s contains the bidirectional override U+%04X at character %d. It makes text display in a different order than it is stored, so what a reader sees is not what is written",
				field, r, i+1)
		}
	}
	return nil
}

// rejectDoubleEscaped catches a body that was serialised twice.
//
// The signature is a long run of text with literal backslash-n in it and no
// actual line breaks at all. That is not prose about escape sequences — prose
// about escape sequences still has paragraphs. It is a document that was
// JSON-encoded on its way into a field that was going to be JSON-encoded
// anyway, and the result is a single unreadable line.
//
// It has to be refused rather than repaired. Unescaping means deciding that
// every "\n" in the text was a line break, and a body that genuinely discusses
// escape sequences would be silently rewritten into something its author never
// said. The agent that has the original is the one that can fix this correctly,
// and it is still holding it when the error comes back.
func rejectDoubleEscaped(field, s string) error {
	if len(s) < DoubleEscapeMinLength || strings.Contains(s, "\n") {
		return nil
	}
	if strings.Count(s, `\n`) < 2 {
		return nil
	}
	return serr.E(serr.InvalidInput,
		"%s is %d characters on a single line containing %d literal \\n sequences, so it was escaped twice on the way here and would be stored as one unreadable line. Send real line breaks, not the two characters backslash and n",
		field, len(s), strings.Count(s, `\n`))
}

// TextProblem is one stored memory that would not be accepted today.
type TextProblem struct {
	Key    string
	Reason string
}

// UnreadableMemories reports stored memories whose text breaks the rules above.
//
// It REPORTS and does not repair, which is the opposite of what doctor does for
// timestamps — and the difference is the point. Canonicalising a timestamp
// preserves the instant exactly; there is one right answer and no information is
// lost. Repairing a double-escaped body means deciding that every "\n" in it was
// meant to be a line break, which is a guess about what somebody meant to write.
// Guess wrong and the memory is silently rewritten into something its author
// never said, with nothing recording that it happened.
//
// So this hands back the list. An agent with the context to know what the memory
// was supposed to say can rewrite it under CAS, which is auditable, reversible
// and attributable in a way a doctor pass would not be.
func (d *DB) UnreadableMemories() ([]TextProblem, error) {
	rows, err := d.Query(`SELECT key, description, body FROM memories ORDER BY key`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read memories")
	}
	defer rows.Close()

	var out []TextProblem
	for rows.Next() {
		var key, desc, body string
		if err := rows.Scan(&key, &desc, &body); err != nil {
			return nil, serr.Internalf(err, "failed to read a memory")
		}
		if err := ValidateLine("description", desc, MaxDescriptionLength); err != nil {
			out = append(out, TextProblem{Key: key, Reason: message(err)})
			continue
		}
		if err := ValidateBlock("body", body); err != nil {
			out = append(out, TextProblem{Key: key, Reason: message(err)})
		}
	}
	return out, rows.Err()
}

// message is the actionable half of a serr, without the code prefix that
// doctor's own formatting would duplicate.
func message(err error) string {
	if e, ok := serr.As(err); ok {
		return e.Message
	}
	return err.Error()
}

// controlName names a control character, because its own representation is
// exactly the thing that cannot be printed in the error explaining it.
func controlName(r rune) string {
	switch r {
	case 0:
		return "NUL"
	case '\r':
		return "a carriage return"
	case 0x08:
		return "a backspace"
	case 0x1b:
		return "ESC (the start of an ANSI escape sequence)"
	case 0x7f:
		return "DEL"
	}
	return fmt.Sprintf("U+%04X", r)
}
