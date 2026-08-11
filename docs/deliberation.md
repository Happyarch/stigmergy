# Deliberation

`stigmergy deliberate` turns a rough idea into a specification that three agents
have failed to break.

This document is the design. Like [architecture.md](architecture.md) it is mostly
*decisions*, not mechanisms — the mechanism here is small, and every interesting
part of it is a choice that looks arbitrary until you know what it is buying.

Companion documents: [architecture.md](architecture.md) (what stigmergy is made
of), [usage.md](usage.md) (driving it), [hosts.md](hosts.md) (the host contract),
[mcp-tools.md](mcp-tools.md) (the tool reference).

Status: **partly implemented**. `internal/deliberate/` is a walking skeleton of
this document — the round loop, the rotation, the sandbox, the adapters and the
verdict parser all run. INTAKE, `--resume-run`, PAUSED and the stigmergy
integration in [§7](#7-what-stigmergy-does-here) are not built.

The host behaviour in [§6](#6-harness-adapters) was probed against live processes
on 2026-07-16 — codex 0.144.5, claude 2.1.212, agy 1.0.10 — and where a claim is
marked *unverified* it means nobody has run it. [§9](#9-what-is-assumed) collects
those so they get settled rather than inherited, and it is worth reading before
trusting anything here, because it is also the tally of how often the vendors' own
documentation turned out to be wrong.

The premise in [§1](#1-the-problem) is no longer an argument. Given one seed and
one variable — who holds the Adversary role — the same pipeline produced:

| adversary | outcome |
|---|---|
| the mind that wrote the spec | **PASS, zero findings, 46 characters** |
| a different mind | **FAIL, with real findings** |

The rubber stamp is [§1](#1-the-problem) reproduced on demand. The different mind
found an unborn branch in a fresh `git init` — a real edge case the authoring model
had certified as flawless seconds earlier.

**Requires Linux and `bubblewrap`.** Not a recommendation — a precondition, checked
at startup, with no fallback ([§6.4](#64-bwrap-is-the-boundary)). Workers are
confined by the kernel rather than by their hosts' permission flags, and bwrap is
Linux-only. A macOS path is deliberately absent rather than sketched: nobody here
can run it, and an untested confinement is worse than an admitted gap.

---

## 1. The problem

Ask one model to write a spec, then ask it to review its own spec, and it will
approve it. This is not a
prompting failure that a sterner instruction fixes: the model is being asked
whether the most plausible continuation of its own reasoning is plausible, and of
course it is. The reviewer and the author share every prior, so they share every
blind spot. That is an echo chamber, and the failure is silent — you get a
confident document with a hole in it, and no signal that the hole exists.

Two agents do not fix it either, if they are the same model. They fail in
correlated ways, and the correlation is invisible from inside.

`deliberate` attacks this from three directions at once:

**Different minds.** Three agents, ideally three different harnesses and three
different models. Their blind spots are uncorrelated, so what one cannot see
another can.

**Different jobs.** An agent that must *attack* a document behaves differently
from the same agent asked to *review* it, because the role sets what counts as
success. A reviewer succeeds by finding nothing. An adversary succeeds by finding
something.

**Rotation.** No agent holds a role twice in a row ([§3.4](#34-shift-left)). The
mind that resolves a teardown is never the mind that wrote it.

### Why Socratic and adversarial compose

The Socratic Guide is the part people leave out, and it is the part that makes the
Adversary worth paying for.

An adversary attacking an *unjustified* spec finds the same thing every time:
unstated assumptions. It fills its critique with "why is this three and not five?"
and "what happens if the input is empty?" — questions about intent that the author
simply never wrote down. That critique is cheap to produce and nearly worthless,
because the answers existed all along; they were just never on the page.

So the Guide goes first, and asks only *why*. It is deliberately incapable of
answering: it proposes nothing, it fixes nothing, it does not know more than the
document. It just makes the Planner **say out loud, in its own words, why the spec
is the way it is** — and a Planner forced to justify a decision it cannot justify
will change the decision. That is the whole trick. The Guide is a cheap pass
(small model, few tokens) that converts the spec from a set of assertions into a
set of *defended* assertions.

Only then is the Adversary's expensive attention worth spending, because now every
question it asks is a real one. It cannot spend its budget on "why three?" — round
2's spec says why three. It has to find something that is actually wrong.

The order is load-bearing. Interrogate before attacking, or you pay adversary
prices for questions a teacher could have asked.

---

## 2. The roles

Each role is an agent given a role prompt, a payload ([§5](#5-the-payload)), and
nothing else. Roles are *positions*, not agents: they rotate.

There are three agents and four positions, because the last one is not a fourth
mind. The Guide, having interrogated the spec, returns at the end of the round as
the **Judge** ([§2.4](#24-the-judge)) — the one role that neither wrote the spec
nor attacked it, and so the only one with no case to win. The Adversary files
findings; the Judge rules on them. See [§2.4](#24-the-judge) for why the verdict
had to be taken away from the Adversary.

### 2.1 The Planner

Owns the specification. It is the only role that writes one.

Two moves, depending on where the pipeline is:

- **Draft** — turn the seed and the intent brief into a first spec.
- **Revise** — answer the Guide's questions *inside the document*, and resolve the
  previous round's teardown.

Prompt guidelines, and the reason for each:

> You are the Planner. You own the specification and you are the only role that
> writes one.
>
> **Output the complete specification and nothing else.** No preamble, no "here is
> the revised spec", no notes to the reader about what you changed. Your entire
> response is the document, in markdown.
>
> Answer the Guide's questions **in the specification itself**, not in a reply to
> the Guide. If a question asks why something is the way it is, the answer belongs
> in the document as a stated rationale — because the question exposed that a
> reader could not tell. A question you can only answer in a cover letter is a
> question the spec still fails to answer.
>
> If a question has no good answer, **change the decision.** That is what the
> question was for.
>
> Address every item in the teardown. You may reject one — the Adversary is not
> always right — but a rejected item must be answered *in the document*, with the
> reasoning that makes it not a flaw. Silence is not rejection; an unaddressed item
> will simply be raised again by an agent that now believes you did not read it.
>
> Do not ask the Guide or the Adversary anything. They will not answer, and there
> is no turn in which they could.

The "output only the document" rule is not tidiness. The Planner's output *is* the
next payload ([§5](#5-the-payload)); anything else in it is noise that every
downstream role pays context for, forever.

### 2.2 The Socratic Guide

Asks why. Answers nothing.

This is the cheap role — small model, low effort ([§8](#8-cli-surface)) — and it
is cheap *because* its job is genuinely small. It is not reviewing. It is not
looking for bugs. It is finding the places where the document asserts without
justifying, and pointing at them with a question.

> You are the Socratic Guide. You are a teacher. Your student has written a
> specification and can defend it — you are going to find out whether that is true.
>
> **Ask open-ended questions about why. That is your entire output.** Number them.
> Five to eight is right; more than that and you are reviewing, not teaching.
>
> **Never supply an answer, never hint at one, and do not work one out privately.**
> Not in the question, not after it, not as "have you considered X?" — that is you
> proposing X. The moment your question contains its own answer, the Planner agrees
> with you instead of thinking, and you have taught it nothing. "Why does the
> retry limit stop at three?" teaches. "Why three, when exponential backoff would
> handle bursts better?" just hands over your opinion wearing a question mark.
>
> A good question makes the Planner say something the document does not currently
> say. If the document already answers your question, it is not a question — cut
> it.
>
> **You are not the Adversary.** Do not hunt for flaws, edge cases, or bugs; that
> is someone else's job and they are better resourced for it. You are asking about
> *intent and scope*: why this boundary, why this default, why is this in and that
> out, who is this for, what does "fast" mean here.
>
> You may not rewrite the specification, and you may not suggest an edit.
>
> Output: a numbered list of questions. Nothing else.

"Do not work one out privately" is in there deliberately, and it is doing real
work: a model that has reasoned its way to an answer leaks it in the framing of the
question, every time. The instruction is also what makes the role cheap — a Guide
that is not solving anything does not need a large model or a reasoning budget.

### 2.3 The Adversary

Attacks the revised spec. It no longer decides whether the run ends — it files a
docket for the Judge ([§2.4](#24-the-judge)).

> You are the Adversary. The specification in front of you is being presented as
> finished. It is not, and your job is to prove it.
>
> Attack it. Look for: logical flaws and internal contradictions; edge cases and
> failure modes the spec does not survive; unstated assumptions holding up load;
> structural weak points where the design will not take the weight it is being
> given; requirements that are stated but unimplementable as described; scope that
> has quietly grown or quietly vanished.
>
> **Name the flaw. Do not design the fix.** A one-line hint at direction is fine
> when the flaw is otherwise unclear, but do not write the replacement text and do
> not specify the solution. Someone else — someone who has not spent this turn
> committed to a theory of what is wrong — is going to resolve this, and they need
> to see the problem, not your answer to it. If you hand them a patch, they will
> apply it, and the design will be yours by default rather than by argument.
>
> Every item needs a **concrete failure**: the input, state, or scenario under
> which the spec produces the wrong outcome. "This section is vague" is not a
> finding. "If two runs share an output path, §4 does not say which claim wins, and
> the second driver blocks forever" is.
>
> **You do not decide whether the specification passes.** A separate Judge will
> weigh your findings and rule. That frees you: you are not gambling a round on a
> pass/fail call, so do not soften a real flaw to seem fair and do not decide for
> yourself that something is too minor to mention. Report every flaw you can stand
> behind with a concrete failure. Do not pad the list — an invented flaw, or a
> matter of taste dressed up as a failure, wastes the Judge's turn and buries your
> real findings. If you find nothing, return an empty list; the Judge reads that as
> a finding of its own.

Naming the flaw without designing the fix is the same rule as shift-left
([§3.4](#34-shift-left)), applied inside a single turn rather than across rounds.
An Adversary that prescribes the fix has made the next Planner its typist.

The Adversary used to issue the verdict itself, and that was a mistake caught in
live runs. A mind told to *attack* will always find something to say, and when the
same mind then scores its own findings, everything it said becomes grounds to
fail. Real runs failed on nitpicks — a hardening no one asked for, a scenario the
intent had ruled out — and the Planner spent whole rounds chasing ghosts. Finding
and judging are different jobs; the fix was to give them to different minds.

### 2.4 The Judge

Reads the Adversary's docket and rules: PASS if nothing on it would really hurt,
FAIL with the survivors if something would. This is the only turn that ends a run.

The Judge is the Guide's agent, resumed — the same mind that opened the round by
asking *why*, returning to close it. That is not a cost-saving accident, it is the
point: of the three agents in the round, the Guide is the only one that neither
wrote the spec nor attacked it, so it is the only one that can weigh the teardown
without a case to win. The Planner would defend its document; the Adversary would
defend its findings; the Guide is disinterested in both.

> You are the Judge. You did not write this specification, and you did not attack
> it. You have read it once already — you are the same mind that questioned it as
> the Guide — but you own neither the plan nor the teardown, and that is exactly
> why the ruling is yours: you are the only role in the room with no case to win.
>
> A finding earns a FAIL only if **you** can name the concrete harm — the input or
> state under which the spec, as written, produces a wrong or unacceptable outcome
> against the intent. Walk the failure through the spec and see whether it really
> fires. Uphold the ones that do.
>
> **Dismiss the rest, and be willing to.** An adversary under orders to attack will
> always find something to say, and much of it is true of every spec ever written:
> a matter of taste, a hardening no one asked for, a scenario the intent puts out
> of scope. A finding is not real merely because it was filed or is technically
> accurate. The question is never "is this imperfect"; it is "does this break, in a
> way that matters here". Fitness for the intent is the bar, not perfection.
>
> [FAIL carries forward only the upheld findings, each restated with the harm you
> confirmed — this list, not the Adversary's, is what the Planner answers next.
> PASS if none survive.]

The two errors the Judge can make are not symmetric in *where* they hurt, but both
are named to it explicitly, because naming only one is how you bias the role. Wave
through a real flaw and the pipeline ships a hole with a certificate on it — the
failure the whole design exists to prevent. Uphold a nitpick and you block a spec
that was ready and send the Planner chasing a ghost — the failure *this role*
exists to prevent. There is deliberately no default verdict for a tie: a thumb on
either scale brings back one of the two failures, so the Judge is told to reason
each finding to a conclusion rather than fall back on a rule.

Note the one asymmetry that *does* survive, and lives in the parser, not the
prompt ([§5.1](#51-the-verdict-protocol)): an *unreadable* verdict is a FAIL. That
is about parse ambiguity, not judgement — a garbled ruling is not a licence to
ship — and it does not push the Judge's actual reasoning toward FAIL.

Only the upheld findings travel onward. The nitpicks the Judge dismissed are gone:
they never reach the next Planner, so no round is ever spent answering them. This
is the mechanism that fixes the failure [§2.3](#23-the-adversary) describes.

---

## 3. The state machine

This mirrors `novel-llm-planning.svg`, transition for transition. The one addition
is INTAKE, which the diagram does not have and [§3.1](#31-intake) argues for.

```
                    START
                      │
                   INTAKE ──────── the human. once, before anything is spent.
                      │            (skipped by --no-interview)
                    INIT ───────── seed, intent brief, max_rounds, agents
                      │
        ┌──────────► DRAFT ─────── Planner  (roles[0])
        │             │ spec
        │        INTERROGATE ───── Guide    (roles[1])
        │             │ questions
        │           REVISE ─────── Planner  (roles[0])
        │             │ revised spec
        │          TEARDOWN ────── Adversary(roles[2])
        │             │ critique (findings, no verdict)
        │         ADJUDICATE ───── Judge    (roles[1], the Guide resumed)
        │             │ verdict + upheld findings
        │             ▼
        │      ┌─────────────┐ PASS
        │      │  Verdict?   ├──────────────────► END: validated
        │      └──────┬──────┘
        │             │ FAIL
        │             ▼
        │      ┌─────────────┐ round >= max_rounds
        │      │ Max rounds? ├──────────────────► END: unvalidated
        │      └──────┬──────┘
        │             │ no
        │        SHIFT LEFT + round++
        └─────────────┘
```

Any step may also exit to **PAUSED** ([§3.5](#35-paused)) or **FAILED**
([§10](#10-failure-modes)). Both are resumable; neither loses a round of work.

### 3.1 INTAKE

The seed is a sentence. The Guide's questions are only worth asking if the Planner
can answer them from something real — and on round 1 there is nothing real, so it
answers from invention. It guesses an audience, guesses a scale, guesses what
"fast" meant, and writes those guesses down as decisions. Every later round then
faithfully hardens the guesses. The Adversary cannot catch it: nothing in the
document is *inconsistent*, it is just about a different problem than the one the
user has.

So before anything is spent, the human is interviewed exactly once:

1. One cheap agent call turns the seed into clarifying questions — the same
   discipline as the Guide's, aimed at the human: what is in scope, what is out,
   who is this for, what does done look like, what must it not do.
2. **The driver asks them.** Plain prompts on the TTY, one at a time, readline. No
   model is in this loop — the questions are already written, and a model sitting
   between the human and the transcript is a chance to paraphrase them wrong.
   Empty answer skips a question.
3. Answers plus seed are compiled into `intent.md`.

`intent.md` is **immutable for the run** and travels in *every* payload
([§5](#5-the-payload)). It is the run's ground truth: the Planner answers *from*
it, and the Guide and Adversary audit the spec *against* it. It is the only thing
in the payload that no agent wrote.

`--no-interview` skips this for a user who arrives with a brief already written
(`--seed brief.md`). The brief becomes `intent.md` verbatim.

### 3.2 The steps

| step | role | in | out |
|---|---|---|---|
| DRAFT | Planner | intent + seed (+ previous critique, if round > 1) | `spec.vN.md` |
| INTERROGATE | Guide | intent + spec | `questions.vN.md` |
| REVISE | Planner | intent + spec + questions | `spec.vN.md` (replaced) |
| TEARDOWN | Adversary | intent + revised spec | `critique.vN.json` (findings, no verdict) |
| ADJUDICATE | Judge | intent + revised spec + critique | verdict + upheld findings |

ADJUDICATE is the Guide's agent resumed, not a fourth agent ([§2.4](#24-the-judge)).

On round 1, DRAFT writes from nothing. On every later round DRAFT ingests the
previous round's critique — which is now the Judge's *upheld* findings, not the
Adversary's raw docket, so a dismissed nitpick never reaches the pen. This is the
diagram's *Critique & New Map* edge: the ruling is not consumed by the agent that
made it, but by whoever holds the pen next.

Each step is one agent turn. A round is five turns; the Guide's agent takes two of
them (INTERROGATE and ADJUDICATE) and the two expensive roles take the other
three.

### 3.3 Termination

Exactly two ways out, both from the diagram:

- **PASS** — the Judge upheld none of the Adversary's findings, whether because
  there were none or because none would hurt. Output is labeled `validated`.
- **max_rounds** — the round counter hit the ceiling with an upheld critique
  outstanding. Output is labeled **`unvalidated — max rounds reached`**, and the
  Judge's surviving findings ship next to it.

The label is not decoration. A spec that ran out of budget with known flaws in it
is a *different artifact* from one that survived an attack, and a person who
returns to it in a month cannot tell the two apart from the prose. So the driver
says so, in the file, at the top. Do not "clean this up" — the whole value of the
run is the distinction.

There is no third exit. In particular the Planner cannot declare itself done; the
only role that can end the run is the one whose job is to prevent it.

### 3.4 Shift left

On FAIL, roles rotate **left**: `[A, B, C] → [B, C, A]`.

| | round N | round N+1 |
|---|---|---|
| Planner | A | **B** (was Guide) |
| Guide | B | **C** (was Adversary) |
| Adversary | C | **A** (was Planner) |

Left, and never right, and the reason is the whole architecture:

**Shift right would make the Adversary the next Planner.** It would resolve its own
teardown — implement its own critique, agree with itself about what was wrong and
about what fixes it, and the round would produce nothing that a single agent
talking to itself could not have produced. The teardown would stop being an
*argument* and become a to-do list. Every guarantee in this document comes from a
critique being resolved by a mind that did not write it.

Shift-left gets that, and gets two second-order properties nearly for free:

- **The new Planner (B) is the old Guide — and the old Judge.** It has read the
  spec closely enough to interrogate it, and it arrives *informed but uncommitted*:
  it has spent its turns asking about this document and ruling on the attack, and
  zero turns defending the text. If anything the Judge turn sharpens the handoff —
  B has just decided, finding by finding, which of the Adversary's points are worth
  fixing, and it is now the one that fixes them. It is still the best-prepared agent
  in the room that owes the current words nothing.
- **The new Guide (C) is the old Adversary.** It interrogates B's resolution of
  C's own critique — which is exactly the right question to be asking, asked by the
  one agent that knows precisely what it meant.

The rotation is an **offset, not a mutation of the array.** `agents` is written
once at INIT and never reordered; `rotation` is an integer, and
`role(i) = agents[(rotation + i) % len(agents)]`. Shift-left is `rotation++`.

This matters more than it looks. Each agent carries a *session*
([§6](#6-harness-adapters)) keyed by its position in `agents`; if the driver
reordered the array it would have to move the session refs in lockstep, and the
first off-by-one would silently hand agent B's transcript to agent C. Nothing would
crash. The run would just quietly stop being three minds.

### 3.5 PAUSED

Sometimes the Guide or the Adversary hits a question no agent can answer — a real
product decision, a constraint only the user knows. It tags the item `[NEEDS-USER]`
rather than guessing, because a guess here is exactly the failure INTAKE exists to
prevent, arriving one round later.

The driver then writes the open questions to the run directory, pauses, and exits
with instructions. The user answers in `user-answers.md` and runs
`stigmergy deliberate --resume-run <id>`; the answers are appended to the intent
brief, and the run continues from the step it paused on with no work lost — the
state is journaled anyway ([§4](#4-state)), so pause costs nothing that a crash
would not have already cost.

Off by default (`--ask-user` enables it), because a pipeline that stops to ask
questions is not the unattended tool most runs want.

---

## 4. State

### 4.1 Where it lives

```
<git-common-dir>/deliberate/<run-id>/
├── state.json          the state machine, below
├── journal.jsonl       one line per completed step, append-only
├── intent.md           immutable after INTAKE
├── user-answers.md     only when PAUSED
├── seed.md             as given
└── rounds/
    ├── 1/
    │   ├── spec.v1.md
    │   ├── questions.v1.md
    │   ├── critique.v1.json
    │   └── raw/                stdout of each turn, verbatim
    └── 2/…
```

**SUPERSEDED — runs now live in `$XDG_STATE_HOME/stigmergy/deliberate/<run-id>/`.**
A deliberation may span several repositories, so there is no single repository to
put the run in, and choosing one would make that repository special in a design
that is otherwise symmetric. XDG *state* rather than data: this is transient run
output, which is the distinction `internal/xdg` already draws.

Two consequences worth having in front of you. The old path joined `.git`
literally, which is a *file* rather than a directory in a linked worktree — that
latent bug left with it. And the tmpfs that hides the run from the workers must
now be applied **after** the `$HOME` overlay, because XDG state is inside `$HOME`:
applied before, the overlay restores the real directory through its lower layer
and every worker can read every other worker's turns, silently, with nothing in
the argv looking wrong. `TestTheRunDirectoryUnderHomeIsMaskedAfterTheHomeOverlay`
is what notices, and it was verified by deliberately mis-ordering the mounts.

The original reasoning, kept because the constraint it names still holds:

The **git common dir**, for the same reason `stigmergy.sqlite3` is there
(architecture.md §3): linked worktrees of one repository share it, so a run is
visible from every worktree of the repo it belongs to, and separate clones stay
separate. It is inside `.git/`, so it is not committed and not pushed — the
run's internals are working state, not an artifact. The only thing that lands in
the tree is the final spec, at `--out`, at END.

The run directory needs protecting from the very agents it records, and being
under `.git/` is not enough on its own. A worker stands at the repository root
([§6.3](#63-workers-run-in-a-shadow-of-the-repository)) with `.git/deliberate/` in
plain sight, and left readable it is an **out-of-band channel that quietly defeats
the payload contract** ([§5](#5-the-payload)): the Adversary could read the Guide's
questions, and a Planner could read the critique history this design deliberately
withholds — which is precisely the conformity gradient the rotation exists to
prevent. Nothing in the prompts invites that. The point is that it must not be
possible.

So the sandbox mounts a **tmpfs over `.git/deliberate/`**. Workers see an empty
directory; the driver, which is not sandboxed, goes on writing the real one.
Verified both ways: without the tmpfs a worker can `cat` another slot's turn; with
it, the directory is empty and the file does not exist.

The workers' writes need no home at all — they are shadowed and discarded
([§6.3](#63-workers-run-in-a-shadow-of-the-repository)), so there is no cache
directory, no checkout, and nothing on disk for a killed run to leak.

Deliberately **not** in the database. This is bulk text with no relational
questions asked of it, written by one process, read by a human debugging a run.
Files are the right shape, and `cat` is the right tool. The database is for things
other agents must see; a run in progress is nobody else's business
([§7](#7-what-stigmergy-does-here) is where it becomes theirs).

### 4.2 `state.json`

```jsonc
{
  "schema": 1,
  "run_id": "d-3f9a1c7b",
  "created_at": "2026-07-16T22:14:03.000000000Z",   // store.TimeLayout, always

  "seed_path": "seed.md",
  "out_path": "docs/specs/thing.md",                 // repo-relative
  "max_rounds": 3,

  "agents": [                                        // written once at INIT
    {"slot": 0, "host": "claude-code", "model": null,           "session": {"id": "0f9c…", "kind": "claude-code"}},
    {"slot": 1, "host": "codex",       "model": "gpt-5.1-codex", "session": {"id": "01JC…", "kind": "codex"}},
    {"slot": 2, "host": "antigravity", "model": null,           "session": null}
  ],

  "round": 2,
  "rotation": 1,                                     // roles = agents[(rotation + i) % n]
  "step": "teardown",                                // next step to run
  "status": "running",                               // running | paused | done | failed

  "artifacts": {
    "intent":   {"path": "intent.md",                     "sha256": "…"},
    "spec":     {"path": "rounds/2/spec.v2.md",           "sha256": "…"},
    "questions":{"path": "rounds/2/questions.v2.md",      "sha256": "…"},
    "critique": {"path": "rounds/1/critique.v1.json",     "sha256": "…"}
  },

  "outcome": null                                    // validated | unvalidated | null
}
```

`state.json` is written **after** the step's artifact is durable, and written
atomically — temp file, fsync, rename. The ordering is the recovery rule: if the
driver dies between the two, the artifact exists but `state` still points at the
step that produced it, so resume re-runs one step and overwrites a file it already
had. That costs one turn. The other order would advance `step` past an artifact
that was never written, and the run would proceed on a missing file — so the cost
is paid in the direction where the failure is *visible and bounded*, which is the
same instinct as architecture.md §5.8.

`journal.jsonl` is the audit trail of the run itself: one line per completed step
with the role, agent slot, host, duration, token counts if the host reports them,
and the artifact hash. It is what you read when a run produces something strange
and you need to know which agent said what, and when. Nothing reads it back; it is
for people.

### 4.3 Resume

`--resume-run <id>` reads `state.json` and continues from `step`. Because sessions
([§6](#6-harness-adapters)) live in the *hosts'* own storage and only their ids are
in `state.json`, resume also restores each agent's memory of the run — the files it
read, the reasoning it did. A resumed run is not a re-run.

If a session id no longer resolves (host storage cleared, session expired), the
driver falls back to a **fresh invocation with the full payload** and records the
fallback in the journal. This is safe, and it is safe by construction rather than
by luck: the payload is self-contained by design ([§5](#5-the-payload)), so a
session is an *optimization*, never a correctness dependency. The agent loses its
transcript and re-reads what it needs. The round still completes.

---

## 5. The payload

Every role, every turn, receives exactly three things:

```
┌─ INTENT ──────── intent.md, verbatim. Immutable. The user's words.
├─ SPEC ────────── the current specification, in full.
└─ CRITIQUE ────── the most recent critique only. Absent on round 1.
```

Plus its role prompt ([§2](#2-the-roles)) and the questions, at REVISE.

**Only the most recent critique.** Not the history, not the previous rounds'
specs, not the transcript of who said what. This is the rule the whole payload
design turns on, and it is not only about context windows:

- The current spec already *contains* the resolution of every earlier critique —
  that is what REVISE did. Shipping round 1's teardown alongside round 3's spec
  is shipping a list of problems that no longer exist, and an agent that reads it
  will dutifully re-litigate them.
- Old critiques are a **conformity gradient**. An Adversary that can see two
  previous Adversaries' findings will anchor on them: it will grade the spec
  against their theory of what matters instead of forming its own. That is the
  echo chamber, rebuilt inside the payload after all the trouble taken to keep it
  out of the roster.

The agents' own sessions do carry history ([§6](#6-harness-adapters)) — an agent
in slot 0 remembers being the Planner two rounds ago. That is a *feature* and it is
why the sessions are worth keeping: it is memory of *its own* reasoning, which is
not conformity. What it never gets is the other agents' history handed to it as
authority.

Payload assembly is one function with one shape, host-independent. Adapters
([§6](#6-harness-adapters)) decide how to *deliver* a payload; nothing in an
adapter decides what is *in* one.

**Delivery is a bound file, not argv.** The payload is written into the run
directory and `--ro-bind` mounted into the sandbox at `/tmp/stigmergy-payload.md`;
argv carries only a fixed sentence pointing at it.

This is the licence above being spent. Linux caps a *single* argv element at
`MAX_ARG_STRLEN` — 32 pages, 131072 bytes — and the payload crossed **two**
`execve` boundaries, since the driver execs bwrap and bwrap execs the host. A
spec that grew past the cap killed the turn with `E2BIG`, and a failing run grows
its spec every round: it marched into the wall exactly when it could least afford
to.

One mechanism for all four hosts, rather than stdin for the three that support
it. agy's `--print`/`--prompt` are Go-style *valued* flags — the prompt IS the
flag's value, with no positional slot and no documented stdin path — so a file
was required there regardless, and two delivery paths across four adapters is
strictly worse than one. It is also more private than argv, which is
world-readable through `/proc`: each turn gets its own bwrap with its own `/tmp`
tmpfs, so one worker cannot see another's brief even in principle.

Always used, never size-switched. A "spill to a file only when it is large"
branch would be exercised only on long runs — which are the ones already in
trouble, and the ones you can least afford to lose to an untested path.

The bind goes after the `/tmp` tmpfs, or the tmpfs masks it, and under `/tmp`
specifically so neither the repository overlays nor the `$HOME` overlay can
shadow it later.

### 5.1 The verdict protocol

Two turns write into this protocol, and they write different halves of it.

The **Adversary** produces the findings, and nothing that ends the run:

```jsonc
{
  "findings": [
    {
      "category": "edge-case",    // logic | edge-case | structure | assumption | scope
      "severity": "high",         // high | medium | low
      "summary": "one line",
      "failure": "the concrete input/state → wrong outcome",
      "where": "§4.2"
    }
  ]
}
```

An empty `findings` array is the Adversary saying it found nothing. There is no
`VERDICT` line in its output — issuing the verdict is not its job
([§2.4](#24-the-judge)).

The **Judge** produces the machine-readable answer to the one question that ends
the run — does the spec pass? — in the *same* shape, carrying forward only the
findings it upheld:

```jsonc
{
  "verdict": "FAIL",              // PASS | FAIL
  "findings": [ /* only the upheld ones, each restated with the confirmed harm */ ]
}
```

Fenced as ```json, followed by a final line `VERDICT: PASS` or `VERDICT: FAIL`.
Belt and braces on purpose: the sentinel is what the driver actually branches on
(a trailing line is the one thing models reliably emit), and the JSON is what the
next Planner reads. Where a host can enforce a schema natively, use it — codex
takes `--output-schema <file>` ([§6](#6-harness-adapters)) — but never *rely* on
it, because three of the four hosts cannot. The parser (`verdict.go`) is a single
function: it reads a `VERDICT` line and a findings block wherever they appear, so
it does not care that the Adversary and the Judge use overlapping shapes — it is
only ever pointed at the Judge's turn.

Parsing rules for the Judge's turn, and each is chosen for its failure direction:

- **`verdict: FAIL` with zero findings → FAIL.** A round is spent. Annoying, not
  wrong: the Judge said the spec is not done but named nothing — treated as a live
  critique rather than a pass.
- **`verdict: PASS` with findings → FAIL**, and the findings are kept. The two
  statements contradict; the run continues, because continuing costs a round and
  stopping costs the guarantee.
- **Unparseable → one re-prompt** ("your last reply had no verdict; rule now,
  reasoning then JSON then the VERDICT line"), on the same session so it can see
  what it wrote. Still unparseable → **FAIL**, and the raw output is kept as the
  critique body.

Every *parse* ambiguity resolves to FAIL. This is deliberate and it is the single
most important line in this document: **a false FAIL costs one round; a false PASS
ships a spec whose flaws nobody ruled on.** The entire system exists to produce the
guarantee that PASS means something, and a parser that guesses PASS on malformed
output would sell that guarantee for one round of compute. Fail-closed toward
scrutiny, always.

Note the seam between this rule and the Judge's prompt. The *parser* fails closed —
a garbled ruling is never read as PASS. The *Judge* does not: it is told to clear
nitpicks, because the whole reason the verdict moved off the Adversary
([§2.4](#24-the-judge)) was that a fail-closed *judgement* rejected everything. The
parser guards against unreadable output; the Judge guards against unready specs.
Different failures, different turns, and it matters that they are not confused: bias
the Judge toward FAIL and you have rebuilt the trigger-happy Adversary inside the
role that was supposed to restrain it.

---

## 6. Harness adapters

An adapter's whole job:

```go
// Start begins a session and runs the first turn.
Start(ctx, role Role, payload string) (out string, s SessionRef, err error)

// Resume runs a turn on an existing session.
Resume(ctx, s SessionRef, payload string) (out string, err error)

// Capabilities is what this host can honestly do.
Capabilities() Caps
```

Nothing else. Adapters do not know about rounds, roles, rotation, or verdicts —
they turn a payload into text. Everything else is the driver's.

### 6.1 What the hosts actually do

Verified against installed CLIs, 2026-07-16. This table is the reason the
architecture is what it is, so it says what was checked rather than what the docs
promise:

| | Claude Code 2.1.212 | Codex 0.144.5 | opencode 1.17.20 | Antigravity (agy) 1.0.10 |
|---|---|---|---|---|
| headless | `-p` | `codex exec <prompt>` | `opencode run <msg>` | `-p` / `--print` |
| resume | `--resume <id>` | `codex exec resume <id> <prompt>` | `--session <id>` | `--conversation <id>` |
| session id | **pre-assign** `--session-id <uuid>` ✔ | **`thread.started` event** ✔ | `opencode session list` | **diff `conversations/`** ✔ |
| resume, wrapped | ✔ | ✔ | ✗ | ✗ |
| structured out | `--output-format json` | `--json`, `-o <file>`, `--output-schema <file>` | `--format json` | *none found* |
| model | `--model`, `--effort` | `-m` | `-m`, `--variant` | `--model` |
| never prompts | `--permission-mode acceptEdits` ✔ | `-c approval_policy="never"` ✔ | `--auto` | `--dangerously-skip-permissions` ✔ |
| workspace scoping | inherits cwd ✔ | `-C <dir>` / cwd ✔ | `--dir <dir>` | **`--add-dir` — cwd is ignored** ✔ |
| effort | `--effort low` | `-c model_reasoning_effort=` | `--variant` | **baked into the model name** |

What matters is the last two rows: each host must be pointable at a **directory**
and tellable not to ask. Confinement is deliberately absent from this table —
workers live in a per-process overlay
([§6.3](#63-workers-run-in-a-shadow-of-the-repository)) — but see
[§6.2](#62-a-worker-must-never-be-asked-anything) for what each host claims about
staying put, and how often that claim was false.

**Antigravity's CLI diverges from the other three and the divergence is silent.**
Two traps, both found by running it:

- **`--print` takes the prompt as its value.** It is not a boolean like Claude's
  `-p`; `--prompt` is documented as an alias for it, which is the tell. So
  `agy -p --sandbox … "PROMPT"` feeds the *string* `--sandbox` in as the prompt and
  the real prompt is dropped. The agent then answers a question nobody asked, and
  nothing errors. Three probes for this document were invalidated this way before
  the agent itself pointed it out: *"The user has provided the string
  `--dangerously-skip-permissions`. This looks like a command line flag…"*. It is a
  Go `flag` binary — the help header says `Usage of agy:` — so flags must precede
  positionals and take values in Go's style.
- **cwd is ignored.** Claude and Codex both work in the shell's working directory.
  agy does not: relative paths land in `~/.gemini/antigravity-cli/scratch/`,
  verified by finding `probe.txt` there after agy reported success. The workspace
  must be handed over with `--add-dir`, which works — and which is a *declaration*,
  not a boundary: agy still writes outside it freely.

**Model names are a third trap.** `agy models` is the authority; effort is part of
the name (`Gemini 3.5 Flash (Low)`), there is no `--effort`. And the catalog is not
what you would guess: Flash is 3.5, **Pro is 3.1** — there is no Gemini 3.5 Pro.
Codex is worse: it does not validate model names locally at all. `codex exec -m
gpt-4.4` prints `model: gpt-4.4` and proceeds to fail at the API. The real catalog
runs `gpt-5` … `gpt-5.6-sol`; the cheap one is `gpt-5.4-mini`.

Note also that agy serves `Claude Sonnet 4.6` and `Claude Opus 4.6`. Selecting one
of those while Claude Code is also in the rotation would rebuild the echo chamber
([§1](#1-the-problem)) through the back door: two slots, two harnesses, one model
family, correlated blind spots. The driver should warn.

✔ = exercised against a live process, not read off a help string. The rest is help
text and the manual, which have both already been wrong once here — see the
`--ask-for-approval` trap below.

**Claude pre-assigns its session id**, which is the cleanest of the four and the
one to prefer: the driver mints a UUID at INIT, passes `--session-id`, and never
has to parse anything out of the output to know what it is resuming. There is no
window in which the driver has run a turn but does not know how to get back to it.

**Codex must not be given `--ephemeral`** — the flag `explore` correctly uses
([internal/explore/codexexec.go](../internal/explore/codexexec.go)) for exactly the
opposite reason. `explore` wants no session state to survive; `deliberate` wants
nothing but. Same binary, opposite need, and a copied flag list would silently make
every codex turn a fresh mind with a warm-sounding name.

**Codex's session id arrives in the first event.** `--json` opens with
`{"type":"thread.started","thread_id":"019f6dd8-…"}` before the turn starts, and
that `thread_id` is what `codex exec resume <id>` takes. Resume re-emits the same
`thread.started` id, so the driver can assert it reconnected to the thread it meant
to rather than assume it.

**`codex exec resume` does not inherit the session's model, and only warns:**

```
This session was recorded with model `gpt-5.4-mini` but is resuming with
`gpt-5.6-sol`. Consider switching back…
```

…and then it proceeds. Omit `-m` on a resume and the agent silently becomes
whatever `config.toml` defaults to — on the author's machine, the most expensive
model available. Here that is not a billing accident but a correctness one: the
guarantee in [§1](#1-the-problem) is that three *stable, different* minds hold the
roles, and an agent that changes model on turn two is not the mind that took the
role on turn one. **Pass `-m` on every invocation, resume included**, and read that
warning as a bug in the driver rather than as advice.

`codex exec resume` does accept `-m` (checked, rather than assumed — its help is
long enough to truncate at exactly the wrong place), and passing it removes the
warning and holds the model. Verified in a `CODEX_HOME` whose default was a third
model neither side of the test used, so that the result could distinguish "resume
falls back to the config default" from "resume inherits the last session's model":
without `-m` it took the config default; with `-m` the resumed turn was clean. The
first version of this test ran against a config whose default *happened* to be the
expected answer and therefore proved nothing — a control matters here more than
usual, because both hypotheses predict the same string.

**And the config default is not stable.** Codex's interactive TUI **rewrites
`config.toml`**: changing the model in a live session edits `model` (and
`model_reasoning_effort`) on disk, permanently. So a driver that relies on the
file's defaults is reading a value the user can change from another terminal,
mid-run, without knowing they have done it. Three independent reasons, then, all
pointing the same way: resume ignores the session's model, the file can move under
you, and a wrong model is a *silent* correctness failure rather than a loud one.
**Pass `-m` and the effort explicitly on every invocation and never read the
defaults.**

**The manual does not mention any of this.** *Resume a non-interactive session* is
ten lines demonstrating `--last`, and the words "model" and "resume" never meet in
it. So this is not a documented gotcha anyone can look up — it is discoverable only
by resuming a session with a non-default model and reading a warning that arrives
after the switch has already happened. Which is the argument for
[§9](#9-what-is-assumed) in one sentence: the documentation is not merely wrong
here, it is absent, and the failure it fails to mention is silent.

**Antigravity can resume; the id must be taken from the filesystem.** `--print`
does persist a conversation — each run creates
`~/.gemini/antigravity-cli/conversations/<uuid>.db`, confirmed by four probe runs
leaving four DBs with matching mtimes — and `--conversation <uuid>` takes that id.
Nothing prints it, so the driver **snapshots that directory before the first turn
and diffs after**. Deterministic for a sequential pipeline, and unlike "pick the
newest file" it does not lose a race with a human running agy at the same time.
`history.jsonl` also records `conversationId`, but only for interactive sessions —
`--print` never touches it, so it is not the channel.

**`--ask-for-approval never` does not exist on `codex exec`**, and this is the
cautionary tale of this section. It is rejected outright — `error: unexpected
argument '--ask-for-approval' found` — because the flag is top-level only: it
belongs to the interactive TUI, where there is a human to ask. `explore.Args`
passed it for months and `stigmergy explore` was therefore dead on arrival against
this codex version, every single time, failing in argument parsing before the model
was ever reached. Nobody noticed, because a flag list is not the kind of thing
anyone tests.

The equivalent that works is `-c approval_policy="never"` — a real key whose value
codex validates (`untrusted|on-failure|on-request|granular|never`). Note the
quoting: `-c` values are parsed as TOML, so a bare `never` is not the string. And
note the asymmetry that makes this genuinely dangerous: a bad *value* fails loudly,
but a **bad key is accepted in silence** unless `--strict-config` is passed. Every
confinement flag this driver adds must be exercised against a live codex, because
"it ran without complaining" is not evidence that the flag did anything.

`codex exec` also **defaults to a read-only sandbox** (manual, *Non-interactive
mode → Permissions and safety*), so `--sandbox read-only` is belt-and-braces
rather than the thing holding the line. Pass it anyway; a default is someone
else's decision to change.

**opencode has no *sandbox*, but it does have permissions**, which is better for
this purpose. See [§6.2](#62-a-worker-must-never-be-asked-anything).

**Antigravity resumes by `--conversation <id>` but offers no documented way to
learn the id.** `--continue` (most recent) is not usable here: three agents are
interleaved on one machine and "most recent" is a race, so it would resume whoever
went last, which is a different agent. Until id capture is settled, the honest
implementation is to run Antigravity **sessionless** — every turn a fresh
invocation with the full payload — which works ([§4.3](#43-resume)) and costs
context rebuild. This is the first thing to fix, and the fix is `stigmergy hook
dump`-style empiricism, not reading the docs.

### 6.2 A worker must never be asked anything

There is nobody to ask. This is the failure mode that kills unattended pipelines,
and it does not announce itself as a permission problem — it announces itself as a
run that stopped.

Every one of these hosts will, by default, stop and ask a human before it does
something it considers consequential. In a headless run that prompt goes to a
terminal nobody is reading. **Antigravity is the clearest case: `--print-timeout`
defaults to 5 minutes, so an unattended agy worker asked for permission simply
waits, then dies** — and the driver sees a timeout, retries, and waits five minutes
again. Twelve turns of that is an afternoon spent discovering that a dialog box was
open.

So every turn must be pre-authorized: nothing may ever be asked. Every host offers
a flag that does exactly that by auto-approving everything, and on a normal
repository reaching for it would be indefensible — you would be trading a hang for
an agent with a free hand in your source.

That trade is only bad because of an assumption worth naming: that the worker is
pointed at *your repository*. It is not
([§6.3](#63-workers-run-in-a-shadow-of-the-repository)) — it is pointed at a disposable checkout of
HEAD, where there is nothing to lose and nothing to protect. Once that is true,
auto-approve stops being a concession and becomes simply correct, and the question
collapses from "how do I let it act without letting it write?" to "how do I stop it
asking?" — which every host can answer:

| host | never prompts | escape blocked? | verified |
|---|---|---|---|
| Codex | `-c approval_policy="never"` | ✅ `read-only file system` | ✔ live |
| Claude Code | `--permission-mode acceptEdits` | ✅ `permission denied` | ✔ live |
| Antigravity | `--dangerously-skip-permissions` | ❌ **escapes** | ✔ live |
| opencode | `--auto` | ? | ✗ |

That third column is the whole table, and it was produced by **asking each host to
escape** — write a file inside its workspace, then write one into the real
repository — rather than by reading what each promises. Two of the three refused at
the harness layer, with no model judgment involved. Antigravity did it.

**Codex has the cleanest semantics.** `approval_policy="never"` does not mean
"approve everything" — it means *never ask*, and an execution failure is returned to
the model instead of a prompt. Deny and don't-ask are one setting.

**opencode can be finer-grained if wanted.** It has no sandbox — my first draft
therefore wrote it off as unconfinable, which was wrong. It has a `permission`
config with values `ask | allow | deny` (`Literals(["ask","allow","deny"])`, read
out of the v1.17.20 binary) over these categories:

```
bash  edit  read  glob  grep  webfetch  websearch  external_directory
lsp   task  skill  todowrite  plan_enter  plan_exit  question  doom_loop
```

`--auto` only auto-replies to a prompt that was going to be shown (`case
"permission.asked": if (mode === "auto") permission.reply({reply: "once"})`), so a
denied category is never silently approved by it. Under the overlay none of this is
*needed* — a worker's writes are shadowed wherever it aims them — but
`external_directory: "deny"` is cheap and narrows what it tries in the first place.

A useful bonus from the `opencode-plugin-contract` research: denying `stigmergy_*`
removes the server's tools **and** silently drops its instructions from the system
prompt. A footgun for a normal agent; exactly right for a worker, which must not be
told to register as a root.

**None of these flags is the boundary.** [§6.4](#64-bwrap-is-the-boundary) is. The
table above is about *not hanging*; what a host can be talked into not writing is
recorded below for completeness and because it is genuinely useful — but nothing in
this design rests on it, and after the tally in [§9](#9-what-is-assumed) nothing
should.

**Claude wants `acceptEdits`, and the near-miss here is instructive.**
`--permission-mode` accepts `acceptEdits | auto | bypassPermissions | manual |
dontAsk | plan`. The obvious pick for "never prompt" is `bypassPermissions` — it
says so in the name — and it **escapes**: it wrote into the real repository from
inside the workspace it was given, because Claude Code has no sandbox and bypass
means bypass, the
working directory included. `acceptEdits` auto-accepts edits inside the workspace
and returns `permission denied` for anything outside it, without prompting and
without hanging. Same never-prompt property; opposite blast radius. Guessing from
the flag name would have picked the one that fails.

`auto` (a classifier decides) very likely also holds, but `acceptEdits` is a rule
rather than a judgment, and for twelve unattended turns a rule is worth more than a
classifier that is usually right. Nothing is gained by the classifier here.

**Antigravity escapes, and no flag fixes it.** Its only non-interactive posture is
`--dangerously-skip-permissions`, which auto-approves everything with no filesystem
boundary at all. Asked to write outside its workspace, it did, immediately.
`--sandbox` is not the answer either — it is a real sandbox, but it relocates the
agent into agy's own scratch directory rather than confining it where you put it.

This is **not a model-quality problem and will not be fixed by a better model.**
The agent did exactly what it was asked; the harness permitted it. A stronger model
follows the instruction *more* reliably, not less. Codex and Claude refused the
identical request at the harness layer without consulting the model at all. This is
a property of the CLI, and the only fixes are external: wrap agy in `bwrap` (or
equivalent) so the kernel enforces what the CLI will not, or leave it out of the
rotation.

The first was chosen, and not only for agy: **every host launches under `bwrap`**
([§6.4](#64-bwrap-is-the-boundary)), because the tally in [§9](#9-what-is-assumed)
says the difference between agy and the others is one of degree. Antigravity is
simply the host that proves why the boundary cannot live in a flag.

### 6.3 Workers run in a shadow of the repository

A worker runs at the **real repository path** and may write anything, anywhere.
None of it is real:

```
bwrap --ro-bind / /  --dev-bind /dev /dev  --proc /proc  --tmpfs /tmp
      --ro-bind <brief> /tmp/stigmergy-payload.md      this turn's brief (§5)
      --overlay-src <repo>  --tmp-overlay <repo>      EACH member repo, shadowed
      --overlay-src $HOME   --tmp-overlay $HOME       every tool cache, shadowed
      --bind <host-state> <host-state>                sessions: real, on top
      --chdir <repo>  --  <host command …>
```

`--tmp-overlay` mounts overlayfs with **writes going to an invisible tmpfs**. The
worker sees an ordinary writable filesystem, edits files, runs builds, and when the
process exits every trace of it is gone. What survives is only what is bound back
on top — the host's own state directory, without which session resume dies.

**Isolation is per process, not per directory.** Three agents share one path and
still cannot see each other's writes, because each invocation gets its own upper
layer. The payload contract ([§5](#5-the-payload)) therefore holds by construction
rather than by keeping the agents in separate rooms.

#### Why this and not a worktree

An earlier version of this document gave each agent slot a throwaway
`git worktree add --detach … HEAD`. It was replaced, and the reasons are worth
keeping because each one is a thing that was actually tried:

- **Tools did not work.** Under a plain `--ro-bind / /` a worker can *run* a
  compiler but cannot cache anything: `GOCACHE`, `~/.cargo` and pip's
  site-packages are all read-only, so real builds fail. Verified: `touch
  $(go env GOCACHE)/probe` → `Read-only file system`. Overlaying `$HOME` makes
  every one of them writable at once, and — the part that matters for maintenance
  — with no per-tool list to keep current as tools change.
- **The worker could not see the truth.** A worktree is checked out at HEAD, so
  uncommitted work and ignored files are invisible. That was written up as an
  accepted cost until it met a real task: this repository keeps its project skills
  under `.claude/skills/`, and an agent planning against HEAD would silently lose
  any skill that had not been committed — along with 44K of the user's own notes.
  The overlay shows the working tree as it is.
- **There was nothing to clean up.** No checkout, no `git worktree remove`, no
  prune, and no orphans. That last one was not hypothetical: a killed run left
  three registered worktrees on disk, and `git worktree prune` **does not collect
  them** — prune only forgets worktrees whose directories have vanished. Every
  ctrl-c leaked three full checkouts. The overlay has nothing on disk to leak.

The clean-tree question dissolves too. A worktree at HEAD demanded a committed
tree to be useful, and the obvious cheaper design — "let workers loose in the repo
but refuse to start unless `git status` is clean" — never worked at all, because
**`git status --porcelain` does not report ignored files and ignored files are not
in HEAD**. The gate would pass while unrecoverable work sat in the working
directory. The overlay needs no gate: nothing it does is recoverable *because
nothing it does is real*.

#### Verified

Same prompt, one variable:

| | plain `--ro-bind` | with overlays |
|---|---|---|
| `GOCACHE` writable | ✗ read-only | ✅ |
| `go build -a` | cannot cache | ✅ exit 0 |
| sees uncommitted work | ✗ (worktree at HEAD) | ✅ |
| sees ignored files / skills | ✗ | ✅ |
| leaks into the real repo | — | ✅ none |

A live three-agent round then ran with the repository at 42 dirty entries before
and 42 after: the workers built, wrote and reasoned inside a copy that was never
there.

#### The costs, stated

- **`--tmp-overlay /` does not work.** overlayfs refuses the root
  (`Invalid argument`), so the overlays must name real subdirectories — the repo
  and `$HOME`. A tool that writes somewhere else entirely still meets a read-only
  filesystem. That is a discoverable gap, not a silent one: it fails loudly at the
  write.
- **The upper layer is RAM.** tmpfs, so a worker that writes gigabytes can exhaust
  it where a worktree would have spilled to disk. Planning does not, but a task
  that produces large artifacts could.
- **Scratch does not survive a turn.** Each invocation gets a fresh layer, so a
  file an agent wrote in one turn is gone in the next; only the transcript carries
  over, via session resume ([§4.3](#43-resume)). This is mostly a feature — an
  agent cannot leave notes for its future self outside the payload — but it is a
  real difference from a worktree that persisted across a slot's turns.
- **Order is load-bearing.** bwrap applies operations in sequence and a later
  mount masks an earlier one. `--tmpfs /tmp` must come before anything stigmergy
  puts under `/tmp`, and a `--bind` that must survive must come *after* the overlay
  covering it. Both were real failures; both are pinned by tests, because neither
  is visible to a compiler and both fail conditionally.

### 6.4 bwrap is the boundary

Every worker runs under [bubblewrap](https://github.com/containers/bubblewrap).
Everything is read-only except the two things it must write: its worktree, and its
own host state.

```
bwrap --ro-bind / /                       everything, read-only
      --bind   <worktree>  <worktree>     its workspace
      --bind   <host-state> <host-state>  ~/.codex, ~/.claude, ~/.gemini …
      --dev-bind /dev /dev  --proc /proc  --tmpfs /tmp
      --chdir  <worktree>
      <host command …>
```

**This is a hard dependency, not a hardening option.** No bwrap, no run — the same
way `explore` refuses when codex is absent. `stigmergy deliberate` is Linux-only
and says so, because bwrap is Linux-only and a macOS path nobody can run is a
promise nobody can keep. That is the same instinct as "local filesystems only" in
architecture.md §3: scope to what can be tested, and be plain about the edge.

#### Why the kernel and not the flags

Read [§9](#9-what-is-assumed) and count. Over the course of specifying this
document the host layer was wrong five times, and every time it was wrong in the
direction of *looking fine*:

| what the docs promised | reality |
|---|---|
| `codex exec --ask-for-approval never` | flag does not exist; hard argument error |
| opencode's `permission.ask` hook | documented, never implemented |
| `--permission-mode bypassPermissions` | escapes the workspace entirely |
| `agy -p <flags> "PROMPT"` | `-p` eats the next flag *as* the prompt |
| `-c any_unknown_key=x` | accepted in silence, does nothing |

Every one of those was found by running a process, and none by reading. A design
whose safety rests on that layer being right is a design betting on the one thing
this project keeps disproving. The kernel has not been wrong once.

So: **the flags handle "don't ask"; the kernel handles "don't reach".** Those are
now two different mechanisms, which is what stops a mistake in the first from
becoming a breach in the second. `bypassPermissions` would still be a bad choice —
but under bwrap it is a bad choice that cannot hurt anybody, and that is the whole
point of putting the boundary somewhere the host does not control.

#### Verified

The boundary itself, same prompt and model and flags, wrapper as the only variable:

| | agy alone | agy under bwrap |
|---|---|---|
| writes inside its worktree | ✅ | ✅ still works |
| escapes to the real repo | ❌ **yes, immediately** | ✅ **blocked** |

agy kept its auth, its network and its scratch dir under the wrap. That is what
takes Antigravity from "cannot be used unattended" to "usable like any other host",
without trusting it an inch.

Then the intersection that actually decides whether this design runs — **the
sandbox and session resume together**, since resume is the entire transport
([§4.3](#43-resume)) and it needs a writable host-state dir *inside* the wrap:

| under bwrap | codex 0.144.5 | claude 2.1.212 | agy 1.0.10 |
|---|---|---|---|
| runs at all | ✅ | ✅ | ✅ |
| session established | ✅ `thread_id` | ✅ pre-assigned uuid | ✅ conversation `.db` |
| **resumes with context intact** | ✅ quoted its prior turn | ✅ quoted its prior turn | untested |
| prompt cache survives | ✅ 11392/27105 | — | — |
| state dir bound | `~/.codex` | `~/.claude` + `~/.claude.json` | `~/.gemini` |

Two of three are proven end to end under the exact configuration this document
specifies. Nothing here was inherited from the first draft's assumptions; every ✅
is a process that ran.

#### What this costs

Per-host knowledge does not disappear; it changes into a kind that can be checked.
Instead of "what does this host's permission mode actually mean" — unanswerable
except by experiment, and wrong in the docs — each adapter declares **which
directories its host writes to**, which is discoverable, stable across versions,
and fails loudly rather than silently when wrong. A host denied its config dir does
not quietly lose its boundary; it fails to start.

The one real risk is a host that needs a path nobody predicted, and it announces
itself as an immediate crash rather than as a silent escape. That is the correct
direction for this failure to point.

### 6.5 The driver owns every artifact

Workers may write in their worktree; nothing they write there is ever *read* by the
pipeline. The artifact of a turn is its **stdout**, and the driver persists it
([§4](#4-state)). A spec that exists only as a file in a worker's worktree does not
exist.

This is not belt-and-braces on top of §6.3. It is what keeps the state machine
honest: `state.json`, the journal and the artifact hashes are the truth only if
there is exactly one writer. If a Planner could edit `--out` directly, resume would
replay against a file the driver never recorded, and the Adversary would review
something other than the payload it was handed.

So the worktree is where a worker is *allowed* to make a mess, not where it is
expected to produce anything. The prompt says output the document
([§2.1](#21-the-planner)); the worktree only means that an agent which ignores that
and starts implementing the spec — which is what coding agents are trained to do,
and the single most likely misbehaviour here — costs the run a turn instead of
costing you your afternoon.

**The driver still claims `--out`** ([§7](#7-what-stigmergy-does-here)), and it is
worth being precise about who that defends against, because it is not the workers:
they are in another directory and cannot see it. It defends against *your own
interactive agents* editing the spec while a run is producing it. That was always
its real job.

### 6.6 Timeouts

Per turn, defaulting to `explore.DefaultTimeout`'s 10 minutes, with the same
reasoning: a worker that never returns must not hold up the run forever. Antigravity
has its own `--print-timeout` (default 5m) which the driver sets to match rather
than fight. Timeout → the step FAILs → the run FAILs and is resumable
([§10](#10-failure-modes)). It does not silently skip a role; a round with a missing
Adversary is not a round.

---

## 7. What stigmergy does here

`deliberate` is a stigmergy subcommand, and the fit is not incidental — the
pipeline *is* stigmergy's thesis. Three agents that never talk to each other
coordinate entirely through traces left in a shared environment: a spec, a
question list, a critique. No agent addresses another. Each reads what the last one
left and acts on it. That is the definition of the word, and it is why this belongs
here rather than in a script that happens to call four CLIs.

What the driver actually uses:

**Registers as a root**, `agent_kind: "stigmergy-deliberate"`, `session_label` =
run id. This is possible because migration
[`0006_drop_agent_kind_check.sql`](../internal/store/migrations/project/0006_drop_agent_kind_check.sql)
removed the closed set; a driver is not a host and would not have fit before. Root
registration *resumes* (architecture.md §5.6), so `--resume-run` reconnects to the
run's own claims rather than stranding them — which is exactly the property that
migration made available to non-hosts.

**Claims `--out`**, non-recursive, for the run's duration, reason "deliberating a
spec: run d-3f9a1c7b". Now a human's Claude session that tries to edit the spec
mid-run is blocked and *told who and why* — and can negotiate over the mailbox,
because the driver's root is in the roster like everyone else's.

**Heartbeats between steps.** Turns are minutes long and the root TTL is 15
(architecture.md §5.1.1), so a driver that only heartbeat at round boundaries could
lapse mid-round — and a lapsed root that comes back **loses its claims** (§5.1.1,
and it does this on purpose). The output file would be unclaimed for part of every
round, silently. Heartbeat every step; steps are minutes, the TTL is fifteen.

**Audits each step** — `deliberate_step` with the round, role, host, and verdict.
The audit log is the only durable record that a spec in the tree came out of a
run at all.

**Writes one memory at END**: key `spec-<slug>`, type `project`, body naming the
output path, the outcome (validated / unvalidated), the agents that produced it,
and the surviving critique if any. This is the part that outlives the run
directory, and it is what a future agent finds via `memory_search` when it goes to
change the spec — including, crucially, "this shipped unvalidated with three known
holes, here they are."

**Releases the claim.** Always, including on FAIL and PAUSED, because a paused run
may sit for days and a claim held by a paused run is a lock with nobody behind it.
`--resume-run` re-acquires — and if it cannot, that is a genuine conflict a human
must see, not something to work around.

---

## 8. CLI surface

```
stigmergy deliberate --seed <text|file> --out <path> [flags]

  --seed <text|@file>     the idea. required.
  --out <path>            where the final spec lands. required.
  --agents <list>         comma-separated hosts, in rotation order.
                          default: every configured host, up to 3.
  --max-rounds <n>        default 3.

  --guide-model <m>       the cheap role. e.g. --guide-model claude:haiku-4.5
  --planner-model <m>
  --adversary-model <m>

  --no-interview          skip INTAKE; --seed is already the brief.
  --ask-user              allow [NEEDS-USER] pauses (§3.5).
  --yes                   skip the cost confirmation.
  --resume-run <id>       continue a paused, failed, or killed run.
  --timeout <dur>         per turn. default 10m.
```

### 8.1 A human starts this, and is told what it costs

The trigger is a person typing the command. Not an agent, not a hook, not another
model deciding a spec needs deliberating. This is a deliberate constraint on an
expensive tool: a round is four full agent turns, the default is three rounds,
and the ceiling is **eighteen turns across three harnesses** (six per round: five roles, plus the judge's one re-prompt when its verdict will not parse) — most of them on large
models reading a repository. That is not a thing to discover afterwards.

So before the first agent is invoked, the driver prints what it is about to do and
waits:

```
stigmergy deliberate — 3 agents, up to 3 rounds

  planner/guide/adversary rotate over:
    1. claude-code   (claude-opus-4-8)
    2. codex         (gpt-5.1-codex)
    3. antigravity   (gemini-3-pro)     sessionless — full payload each turn

  each agent works at the real repository path, in a bwrap overlay whose
  writes go nowhere. your working tree is not touched and need not be clean.

  up to 12 agent turns. output → docs/specs/thing.md
  press enter to start, or ctrl-c.
```

`--yes` skips it for scripted runs. The preview names the *degraded* modes
(sessionless, unconfined, same-model) because those are exactly what a person would
want to know before spending eighteen turns, and exactly what is easiest to not
notice.

`--max-rounds` defaults to **3** and not 5, for the same reason. Three rounds is
enough for the rotation to put each agent in each role once — which is the point of
the design, and the natural stopping place.

Fewer than three distinct harnesses is allowed and **warned about, loudly**: with
one harness in all three slots, rotation still separates the *roles* and the
sessions, which is worth something — but the three minds are one mind, their blind
spots are perfectly correlated, and the central defense of this document is gone.
It runs. It is not the same tool.

---

## 9. What is assumed

Collected here so it can be settled rather than inherited. This section is a
to-do list, not a disclaimer.

The pattern in what follows is worth naming, because it held every single time:
**everything settled here was settled by running a process, and several of the
answers are the opposite of what the help text implied.** `--ask-for-approval never`
did not exist; `bypassPermissions` escaped; `-p` on agy ate the prompt. Nothing in
this section was resolved by reading.

**Settled**, by running the thing rather than reading about it:

1. ~~Codex session id capture.~~ `thread.started` carries `thread_id`, first
   event, before the turn. Resume re-emits the same id.
2. ~~Whether resume preserves context and hits the prompt cache.~~ **Both, and
   this is the finding the whole transport rests on.** A `codex exec` turn
   reported `input_tokens: 12773, cached_input_tokens: 9984`. Resuming that thread
   and asking it to quote my previous message, it quoted it correctly —
   `input_tokens: 25574, cached_input_tokens: 19968`. So a resumed non-interactive
   turn is *not* a cold start: the transcript is intact and ~78% of the input was
   cached. The worry that drove the original design question — that headless mode
   would re-read every file each turn because it cannot cache like a live session
   — does not hold for codex.

   The honest caveat: those two turns were seconds apart, and in a real run an
   agent's next turn is three turns and several minutes later, which may exceed the
   cache TTL. **A cache miss is a cost, not a correctness problem** — the
   transcript still survives, so the agent still does not re-read the repository,
   which is the larger saving. This is why §4.3 makes sessions an optimization and
   never a dependency.

**Open:**

3. **Antigravity session id.** Whether `--print` reveals a conversation id at all.
   If not, Antigravity is sessionless forever and pays full context rebuild every
   turn — which works, but should be a known cost, not a surprise.
4. **opencode session id from `run`.** `opencode session list` exists; whether
   `--format json` names the session inline is unchecked. Listing is a race if two
   runs overlap, so inline is worth confirming.
5. **Whether Claude and opencode resume cache the way codex does.** Assumed from
   the codex result, which is exactly the kind of assumption this section exists to
   stop people making. Measure each.
6. **agy resume, end to end.** The conversation `.db` exists and `--conversation`
   takes an id; nobody has round-tripped one and confirmed the context survives.
   If it does not, agy is sessionless and pays a context rebuild every turn —
   which works ([§4.3](#43-resume)) and is a cost, not a blocker.
7. **opencode, end to end.** The one host never launched for this document.
   `--dir`, `--auto`, the `permission` deny-list and `~/.local/share/opencode` are
   all read off the binary rather than run. Probe it the same way: ask it to
   escape. Deferring it is legitimate — a rotation of codex, claude and agy is
   three different minds already.
8. **Worktree lifecycle under failure.** `git worktree remove` on a crashed run
   leaves admin entries needing `git worktree prune`. The driver should prune on
   start and reuse an existing slot worktree on `--resume-run`, but the
   interaction between a resumed session's transcript paths and a re-created
   worktree at the same path is untested.
9. **Whether the Guide's cheap model is cheap enough to matter.** The role is
   sized for a small model. If a small model asks bad questions, the economics
   argument in §1 changes shape.

Retired by worktree isolation, and recorded because the reasoning is worth
keeping: an earlier draft made workers **read-only**, which was expressible on two
hosts out of four, impossible on Antigravity, and bought protection against damage
that `git` already bounds. It also would have forced a clean working tree —
[§6.3](#63-workers-run-in-a-shadow-of-the-repository) explains why that check would not even have
worked.

---

## 10. Failure modes

| what happens | what the driver does |
|---|---|
| worker exits non-zero | one retry on the same session; then FAIL the run, resumable at that step |
| worker times out | no retry (it will time out again). FAIL, resumable |
| worker blocks on a permission prompt | must be impossible ([§6.2](#62-a-worker-must-never-be-asked-anything)). If a turn ever times out with no output at all, suspect this first — it presents as silence, not as a permission error |
| session id does not resolve | fresh invocation with the full payload; journal the fallback (§4.3) |
| unparseable verdict | one re-prompt; then FAIL the round, keep raw output (§5.1) |
| driver killed | resume re-runs at most one step (§4.2) |
| `bwrap` absent or unusable | refuse to start, and say why. There is no unwrapped fallback: it would be the one configuration nobody tested, entered automatically at the moment the safety net failed ([§6.4](#64-bwrap-is-the-boundary)) |
| a host crashes under bwrap (missing state dir) | refuse that host, name the path. Loud and immediate, which is the point — the alternative failure is a silent escape |
| `git worktree add` fails (disk, lock, pruned entry) | prune and retry once, then refuse to start. A worker without its worktree must never fall back to the real repo — that is the whole boundary ([§6.3](#63-workers-run-in-a-shadow-of-the-repository)) |
| run killed, worktrees left behind | `git worktree prune` on next start; `--resume-run` reuses the slot's path so resumed sessions' transcripts still resolve |
| claim on `--out` refused at start | refuse to start, name the holder. Someone is editing the target |
| claim lost mid-run (root lapsed) | re-acquire at the next heartbeat; if refused, PAUSE and tell the user |
| `--out` modified during the run | the claim exists to prevent this; if it happened anyway, the writer was not cooperating (architecture.md §1). Driver overwrites at END and says so |
| fewer than 3 distinct harnesses | run, warn loudly (§8.1) |

Every FAIL is resumable, because every step is journaled before `state.json`
advances. A run that dies eleven turns in has lost eleven turns of *nothing*.

---

## 11. Future work

**Implementation shape**, following the repo's structural rules (architecture.md
§6 — `cli` stays thin, logic lives in packages):

```
internal/deliberate/
  deliberate.go   the state machine. no I/O beyond the run dir
  payload.go      payload assembly + role prompts (§2, §5)
  verdict.go      the verdict parser (§5.1). pure, table-tested — this is the
                  file where a bug ships an unattacked spec
  state.go        state.json, journal, atomic write + resume
  sandbox.go      the bwrap argv (§6.3, §6.4): overlays, masks and binds, in
                  the one order that works. every worker goes through here;
                  there is no code path that launches a host unwrapped
  adapter.go      the interface (§6)
  host_claude.go  host_codex.go  host_opencode.go  host_antigravity.go
internal/cli/deliberate.go   cobra wiring only
```

`verdict.go` deserves the same isolation `internal/claims/overlap.go` has, and for
the same reason: it is a pure decision the whole guarantee rests on, so it must be
testable with no process, no host, and no network in sight.

Later, and not now:

- **N > 3 agents.** The rotation is already `% len(agents)`; the pipeline is not.
  Four agents want two Adversaries or two Guides, and which is a real question.
- **Parallel adversaries.** Fan out the teardown across every non-Planner agent and
  merge. Strictly better attacks; strictly worse cost; breaks the baton-pass
  premise that makes the driver sequential and simple.
- **Mailbox mode.** Live interactive agents deliberating over `mailbox_send`, with
  a human as one of the three. This is where stigmergy's mailbox stops being
  overkill for this problem — but it needs a scheduler, which stigmergy does not
  have and should not grow (architecture.md §5.1).
- **`--from-spec`.** Run the pipeline against a document that already exists, with
  no Planner draft. The Adversary is useful on its own.
