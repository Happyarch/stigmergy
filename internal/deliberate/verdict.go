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

// parseFindings pulls findings out of the fenced JSON, tolerating both the
// documented object form and a bare array, because models produce both and the
// difference is not worth a round.
func parseFindings(out string) []Finding {
	for _, fence := range jsonFence.FindAllStringSubmatch(out, -1) {
		body := strings.TrimSpace(fence[1])
		if body == "" {
			continue
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
	}
	return nil
}
