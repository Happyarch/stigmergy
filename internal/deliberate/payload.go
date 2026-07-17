package deliberate

import (
	"fmt"
	"strings"
)

// Role is a position in the pipeline, not an agent. Roles rotate.
type Role int

const (
	Planner Role = iota
	Guide
	Adversary
)

func (r Role) String() string {
	switch r {
	case Planner:
		return "planner"
	case Guide:
		return "guide"
	default:
		return "adversary"
	}
}

// Payload is what a role receives. Exactly this, every turn, and nothing else.
//
// The critique is the MOST RECENT one only — never the history. Not just for
// context economy: old critiques are a conformity gradient. An Adversary that can
// read two previous Adversaries' findings grades the spec against their theory of
// what matters instead of forming its own, which rebuilds the echo chamber inside
// the payload after all the trouble taken to keep it out of the roster. The
// current spec already contains the resolution of every earlier critique — that is
// what REVISE did.
type Payload struct {
	Intent    string // the human's words. immutable for the run.
	Spec      string // the current specification, in full
	Questions string // guide -> planner, within a round
	Critique  string // the most recent teardown only
}

const plannerPrompt = `You are the Planner. You own the specification and you are the only role that writes one.

Output the complete specification and nothing else. No preamble, no "here is the revised spec", no notes about what you changed. Your entire response is the document, in markdown.

Answer the Guide's questions IN the specification itself, not in a reply to the Guide. If a question asks why something is the way it is, the answer belongs in the document as a stated rationale — because the question proves a reader could not tell. A question you can only answer in a cover letter is a question the spec still fails to answer.

If a question has no good answer, change the decision. That is what the question was for.

Address every item in the teardown. You may reject one — the Adversary is not always right — but a rejected item must be answered in the document, with the reasoning that makes it not a flaw. Silence is not rejection: an unaddressed item will simply be raised again by an agent that now believes you did not read it.

Do not ask the Guide or the Adversary anything. They will not answer, and there is no turn in which they could.`

const guidePrompt = `You are the Socratic Guide. You are a teacher. Your student has written a specification and can defend it — you are going to find out whether that is true.

Ask open-ended questions about WHY. That is your entire output. Number them. Five to eight is right; more than that and you are reviewing, not teaching.

Never supply an answer, never hint at one, and do not work one out privately. Not in the question, not after it, not as "have you considered X?" — that is you proposing X. The moment your question contains its own answer, the Planner agrees with you instead of thinking, and you have taught it nothing. "Why does the retry limit stop at three?" teaches. "Why three, when exponential backoff would handle bursts better?" just hands over your opinion wearing a question mark.

A good question makes the Planner say something the document does not currently say. If the document already answers your question, it is not a question — cut it.

You are NOT the Adversary. Do not hunt for flaws, edge cases, or bugs; that is someone else's job and they are better resourced for it. You are asking about intent and scope: why this boundary, why this default, why is this in and that out, who is this for, what does "fast" mean here.

You may not rewrite the specification, and you may not suggest an edit.

Output: a numbered list of questions. Nothing else.`

const adversaryPrompt = `You are the Adversary. The specification in front of you is being presented as finished. It is not, and your job is to prove it.

Attack it. Look for: logical flaws and internal contradictions; edge cases and failure modes the spec does not survive; unstated assumptions holding up load; structural weak points where the design will not take the weight it is given; requirements stated but unimplementable as described; scope that has quietly grown or quietly vanished.

Name the flaw. Do NOT design the fix. A one-line hint at direction is fine when the flaw is otherwise unclear, but do not write the replacement text and do not specify the solution. Someone else — someone who has not spent this turn committed to a theory of what is wrong — is going to resolve this, and they need to see the problem, not your answer to it. Hand them a patch and the design becomes yours by default rather than by argument.

Every item needs a concrete failure: the input, state, or scenario under which the spec produces the wrong outcome. "This section is vague" is not a finding. "If two runs share an output path, §4 does not say which claim wins, and the second driver blocks forever" is.

PASS is real, and it is rare. Issue it only if you genuinely cannot find a flaw after trying to. Do not pass to be agreeable, and do not pass because the spec is good — good specs have flaws. But do not manufacture a finding either: an invented flaw costs a full round to disprove. If you have nothing, say so honestly.

Output, in this order and nothing else:

` + "```json" + `
{"verdict":"FAIL","findings":[{"category":"logic|edge-case|structure|assumption|scope","severity":"high|medium|low","summary":"one line","failure":"the concrete input/state -> wrong outcome","where":"section"}]}
` + "```" + `

then a final line, exactly:

VERDICT: FAIL

(or VERDICT: PASS with an empty findings list, if it truly survives.)`

// Prompt assembles a turn. The role instruction leads, the payload follows, and
// the payload is host-independent: adapters decide how to deliver a payload,
// never what is in one.
func Prompt(r Role, p Payload) string {
	var b strings.Builder

	switch r {
	case Planner:
		b.WriteString(plannerPrompt)
	case Guide:
		b.WriteString(guidePrompt)
	case Adversary:
		b.WriteString(adversaryPrompt)
	}
	b.WriteString("\n\n---\n\n")

	section(&b, "INTENT (the user's words; the fixed target — audit the spec against this, and do not contradict it)", p.Intent)
	section(&b, "SPECIFICATION", p.Spec)
	section(&b, "QUESTIONS FROM THE GUIDE (answer these inside the document)", p.Questions)
	section(&b, "MOST RECENT TEARDOWN (resolve these)", p.Critique)

	if r == Planner && p.Spec == "" {
		b.WriteString("\nThere is no specification yet. Write the first one from the intent above.\n")
	}
	return b.String()
}

func section(b *strings.Builder, title, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	fmt.Fprintf(b, "## %s\n\n%s\n\n", title, strings.TrimSpace(body))
}
