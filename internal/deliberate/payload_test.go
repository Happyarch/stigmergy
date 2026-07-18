package deliberate

import "strings"

import "testing"

// The verdict moved off the Adversary and onto the Judge. These assertions lock
// that split in place: the Adversary must stop issuing a verdict, and the Judge —
// the Guide's agent resumed — must receive the teardown framed as a docket to rule
// on, not a to-do list to resolve.
func TestJudgeAndAdversaryWiring(t *testing.T) {
	if got := Judge.String(); got != "judge" {
		t.Fatalf("Judge.String() = %q, want %q", got, "judge")
	}

	adv := Prompt(Adversary, Payload{Intent: "i", Spec: "s"})
	if strings.Contains(adv, "VERDICT:") {
		t.Error("the Adversary must not be told to emit a VERDICT line; the Judge rules")
	}
	if !strings.Contains(adv, "The Judge writes that.") {
		t.Error("the Adversary prompt should hand the verdict to the Judge")
	}

	critique := "the adversary's findings go here"
	j := Prompt(Judge, Payload{Intent: "i", Spec: "s", Critique: critique})
	if !strings.Contains(j, "You are the Judge.") {
		t.Error("Prompt(Judge, ...) must lead with the judge prompt")
	}
	if !strings.Contains(j, critique) {
		t.Error("the Judge must receive the Adversary's critique in its payload")
	}
	if !strings.Contains(j, "rule on each finding") {
		t.Error("the Judge's critique section must frame the teardown as a docket to rule on")
	}
	if strings.Contains(j, "resolve these") {
		t.Error("the Judge rules on the teardown; it does not resolve it like the Planner")
	}

	// The re-prompt path threads its correction through Note; it must surface.
	withNote := Prompt(Judge, Payload{Note: "your last reply had no verdict"})
	if !strings.Contains(withNote, "your last reply had no verdict") {
		t.Error("a Note must render into the prompt (the malformed-verdict re-prompt depends on it)")
	}
}
