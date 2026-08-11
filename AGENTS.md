# Writing conventions

This applies to every word committed to this repository: the README, everything
in `docs/`, and Go comments — including comments in tests.

**Write for a stranger.** A third-party user or a contributor who has never met
anyone on this project should be able to read any sentence here without
inferring who "we" are. That is not a style preference; it is what makes the
prose usable by the people it is actually for.

**Person.**

- **Never first person.** No `we`, `our`, `us`, `I`. The actor is `stigmergy`,
  or — better, because it is more precise — the specific thing doing the acting:
  the claim guard, the installer, `init`, `doctor`, the hook, this walker, the
  driver. "We block the edit" → "the guard blocks the edit". "our own parser
  broke" → "stigmergy's own parser broke".
- **Second person is for the reader, and only the reader.** In user-facing docs
  `you` is the person running the command. In agent-facing text `you` is the
  agent being addressed. Do not slide between the two inside one passage.
- **Third person for everything else** — the code, the hosts, the database, the
  other agents.

The two exceptions are genuine quotation: an example of something an agent would
say to another agent, and a named state or verdict being quoted back
(`"already yours"`). Those keep whatever person the quoted speaker used.

**When first person is hard to remove, the sentence is usually vague.** "we
cannot tell which" hides the subject; "the driver cannot tell which" names it,
and the reader learns something. Reach for the specific actor before reaching
for the passive.

Docs are load-bearing here — agents read them as instructions — so the same care
that goes into the code goes into the sentences. If the code and a document
disagree, the code is right and the document is a bug.
