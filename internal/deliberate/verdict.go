package deliberate

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// ErrNoVerdict means the Adversary's output carried no verdict sentinel.
//
// The caller re-prompts once and then treats the round as FAIL. It is never a
// reason to pass.
var ErrNoVerdict = errors.New("no VERDICT line in the adversary's output")

// Finding is one thing wrong with the spec.
type Finding struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Failure  string `json:"failure"`
	Where    string `json:"where"`
}

// Verdict is the Adversary's answer to the only question that ends a run.
type Verdict struct {
	Pass     bool
	Findings []Finding
	// Raw is the adversary's whole turn, kept because a critique the parser
	// could not read is still the next Planner's problem to solve.
	Raw string
}

var (
	// The sentinel is the last non-empty line. It is what the driver branches
	// on, because a trailing line is the one thing models emit reliably; the
	// JSON is for the next Planner to read.
	verdictLine = regexp.MustCompile(`(?im)^\s*VERDICT:\s*(PASS|FAIL)\s*$`)
	// Fenced ```json … ``` block, non-greedy so a later fence does not swallow
	// the document.
	jsonFence = regexp.MustCompile("(?s)```json\\s*(.*?)```")
)

// ParseVerdict reads the Adversary's turn.
//
// Every ambiguity resolves to FAIL, and that is the single most important
// property in this package: a false FAIL costs one round, a false PASS ships a
// specification that nothing attacked. The whole system exists to make PASS mean
// something, so the parser must never guess it.
func ParseVerdict(out string) (Verdict, error) {
	v := Verdict{Raw: out}

	m := verdictLine.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return v, ErrNoVerdict
	}
	// Last sentinel wins: a model that restates the format ("reply with
	// VERDICT: PASS or VERDICT: FAIL") and then answers would otherwise be read
	// as whichever it mentioned first.
	said := strings.ToUpper(m[len(m)-1][1])

	v.Findings = parseFindings(out)

	switch {
	case said == "FAIL":
		// Findings or not, it said no. A FAIL with an empty list is a wasted
		// round, not a wrong one.
		v.Pass = false
	case len(v.Findings) > 0:
		// It said PASS and then listed what is wrong. The two statements
		// contradict; continuing costs a round, stopping costs the guarantee.
		v.Pass = false
	default:
		v.Pass = true
	}
	return v, nil
}

// parseFindings pulls findings out of the turn, tolerating both the documented
// object form and a bare array, because models produce both and the difference
// is not worth a round.
//
// The fenced ```json block is tried first: it is the documented form, and a
// model that fenced its answer meant the fenced text to be it. But the fence is
// the single most-dropped token — codex omits it in the Judge role and the
// driver then reported zero findings while a well-formed object sat in plain
// sight in the prose. Worse than a wrong count: with the findings unparsed, the
// contradiction guard in [ParseVerdict] (a PASS that also lists flaws) never
// fires, so an unfenced contradictory PASS would ship — the one outcome this
// package exists to prevent. So when no fence yields findings, fall back to
// scanning the raw turn for balanced JSON.
func parseFindings(out string) []Finding {
	for _, fence := range jsonFence.FindAllStringSubmatch(out, -1) {
		if f := findingsFromJSON(strings.TrimSpace(fence[1])); f != nil {
			return f
		}
	}
	// Unfenced fallback: take the LAST balanced object/array that carries
	// findings, mirroring the sentinel's "last one wins" — the real answer
	// trails the reasoning, and any JSON quoted earlier as an example does not.
	var found []Finding
	for _, cand := range jsonCandidates(out) {
		if f := findingsFromJSON(cand); f != nil {
			found = f
		}
	}
	return found
}

// findingsFromJSON reads one JSON document as either the documented
// {"findings":[…]} object or a bare […] array. It returns nil for anything else
// — including valid JSON with no findings key — so a caller can tell "not the
// findings" from "an empty findings list".
func findingsFromJSON(body string) []Finding {
	if body == "" {
		return nil
	}
	var obj struct {
		Verdict  string    `json:"verdict"`
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(body), &obj); err == nil && obj.Findings != nil {
		return obj.Findings
	}
	var arr []Finding
	if err := json.Unmarshal([]byte(body), &arr); err == nil && arr != nil {
		return arr
	}
	return nil
}

// jsonCandidates returns the balanced {…} and […] spans in s, in order, so the
// fallback can try the JSON a model emitted without the documented fence.
// json.Unmarshal is the real validator; this only has to find the spans worth
// handing it, so brackets are counted loosely (a '{' closed by a ']' still
// balances) and a candidate that is not valid JSON simply fails to parse.
func jsonCandidates(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' && s[i] != '[' {
			continue
		}
		if end := balancedEnd(s, i); end > i {
			out = append(out, s[i:end])
			i = end - 1 // resume after the span, not inside it
		}
	}
	return out
}

// balancedEnd returns the index just past the bracket that closes the one at
// start, or -1 if it never closes. String literals and their backslash escapes
// are honoured so a bracket inside a JSON string does not move the depth count.
func balancedEnd(s string, start int) int {
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}
