# Memory change-evidence in stigmergy — model and implementation

## Context

Stigmergy's memory store holds curated, durable knowledge shared by every agent
in a repo. Nothing in it expresses age: `memory_search` returns no timestamp,
`memory_list` returns `updated_at` but sorts by key, and no query accepts a time
bound. An agent cannot ask "should I re-verify this?", and an upkeep routine has
nothing to prioritise by.

This document specifies what to build. Approaches that were tried and rejected —
including the original biological-decay proposal — are in **Appendix A**, because
the reasoning that killed them prevents specific regressions.

---

# Part I — The model

## 1. Ontology

| Entity | Meaning |
|---|---|
| **Memory** `m` | A durable record carrying a proposition `P(m)`. |
| **Referent** `R(m)` | The part of the world `P(m)` is about. |
| **Assertion** | An event in which an agent states that `P(m)` holds. |
| **Observer** | An agent reading `m` now, deciding: trust, verify, or ignore. |

**The schema does not store `R(m)`, and must not try.** A field asserting "this
memory is about the code" makes a claim about the world that can be wrong with
nothing detecting it. §4 gives the operational substitute.

## 2. The central relation, and what is actually measurable

> Staleness is a relation between a memory, its referent, and the interval since
> the memory was last **asserted**.

That is the right relation, and it is retained as the ontological ideal. The
critical caveat took two review rounds to surface:

> **stigmergy has never stored an assertion time.** `memories.updated_at` is
> *last mutation* — the last time any byte changed, for any reason.

A write is not proof that anyone verified anything (§7), so `updated_at` cannot
serve as assertion time.

**Neither stage measures the relation above.** This boundary is the point of the
section, and must not erode:

- **Stage 1** exposes a **last-update timestamp**. Not an age, not an assertion time.
- **Stage 2** measures change since an **operational comparison origin** — a base
  commit recorded by an explicit act. It is a comparison origin, *not* an
  assertion origin, and not the missing quantity above.
- **`memory_evidence_set` is deliberately not called an assertion or a
  verification.** It says only "compare against this from now on."

## 3. The datum is a vector, not a scalar

For a memory with a declared evidence policy, the datum is a **per-member
evidence vector**: for each declared member, an observation outcome and, where
measured, a change count and the base/head OIDs that produced it.

There is no `D(m)`. Prior volatility and corroboration history are *candidate
Stage 3 signals* (§16), not primitives of the implemented model — listing them
here as active quantities would repeat the §8 error one level up.

## 4. Evidence policy, coverage, and outcome

Three **orthogonal** things. An earlier draft collapsed them into one `regime`
field that could not express their differences.

**1. Evidence policy** — declared, operational, stored, CAS-versioned.
- *absent* (default, and the overwhelmingly common value): no change evidence;
  the last-update timestamp remains visible.
- *git*: an explicit member set, optional per-member literal/glob paths, and a
  base OID captured per member.

The git policy does **not** claim to name the referent. It means: *when assessing
this memory, observe changes in exactly these repositories and paths, from these
commits.* This makes the policy **inspectable** — the boundary is returned with
every result, so a reader can see what was and was not observed. It does **not**
make a wrong policy detectable; nothing here can tell that a declared scope was
the wrong scope.

**2. Coverage** — measured per evaluation, never cached. Applies only once a
policy exists:
- `complete` — every declared member measured
- `partial` — at least one measured and at least one not
- `unavailable` — **no** declared member measured

**3. Per-member outcome** — `measured`, `non_ancestor`, `missing_base`,
`shallow`, `missing_worktree`, `timeout`, `git_error`.

These are different axes; conflating them is how "2 of 3 measured" ends up
reported with the confidence of 3 of 3.

Measurement status must **not** be sticky — a cached label can lie. Consecutive
calls may legitimately differ, which is acceptable *because* coverage and
outcomes are surfaced alongside the numbers.

### Anchors are not self-enforcing

A too-narrow anchor undercounts silently and reads as plausibly clean. All of the
following are required:

- Whole-member observation (zero declared paths) is the easy conservative
  default; paths are an optional precision refinement, never mandatory.
- Every result echoes the policy that produced it.
- Warn when a pattern matches nothing in the **union of the base and head trees**
  — tree semantics, not filesystem. A path present in base and deleted by head is
  *evidence*, not a bad anchor. Rename detection is explicitly best-effort; never
  promise to distinguish rename from delete/add.
- **"Zero changes within the declared scope" never becomes a freshness verdict.**
- `non_ancestor`, `missing_base` and `shallow` are outcomes, never coerced into a count.

## 5. What the evidence can and cannot support

Expected verification value decomposes as:

```
EV(verify) = P(P(m) no longer holds | evidence) × loss_if_relied_on − cost_to_verify
```

Change evidence bears on the **first factor only**. There is no impact term and
no verification-cost term anywhere in the system, and a repeatedly-affirmed fact
is often the *highest*-consequence one — so nothing here can be an expected value
or a priority ranking.

**Stages 1–2 compute change evidence. Nothing else.** No scalar, no bucket, no
ranking, no probability. Whether any ranking is defensible is a Stage 3 question,
answerable only once verification outcomes exist to calibrate against.

## 6. The durable invariant

> Evidence collection never mutates memory content and never removes a memory.

This preserves the commitment at `internal/store/gc.go:23` — memories are "the
state of the system, not its history, and nothing but an explicit delete should
ever remove them."

It is also the formal reason decay was rejected: decay fuses the *label*
transition with the *removal* transition. Split them and the useful half survives.

## 7. Verification outcomes must be explicit

An identical rewrite is not proof anyone verified anything; a changed body may be
a typo fix, formatting, or elaboration. Verification is *selected*, not randomly
sampled, so revision frequency is biased. Under sparse history a prior dominates
anyway.

Therefore verification is only ever recorded as an **explicit** reaffirm / revise
/ refute operation with an optional reason — never inferred from a write, and
never from byte equality. Stage 3.

This rule is why §15.2's baseline capture is a separate operation from
`memory_write`: inferring "the agent re-verified this" from an edit is the same
error, and there it would silently *erase evidence* rather than merely mis-rank it.

## 8. The type taxonomy does not encode the referent

Four live memories refute any mapping from `type`/scope to referent class:

| Memory | Type | Why the mapping fails |
|---|---|---|
| `deliberate-skeleton` | `reference` | Describes in-repo implementation state — "opaque" exactly where git evidence is most relevant. |
| `workflow-split-planning-vs-implementation` | `feedback` | An ongoing user workflow, not a static past event — would be excluded from scoring entirely. |
| `spongebob-restore-chain-hardlock-context` | `project`, global scope | Global treated as opaque, yet no `project`+opaque calibration exists. |
| `future-exportable-library` | `project` | An intent/decision; repo churn is weakly related to its invalidation. |

The generalisable lesson: **an enum nothing depends on is not a free source of
meaning.** `type` is never branched on anywhere in the codebase, which is
evidence it carries no enforced semantics — not headroom to give it some. Any new
*semantic* field inherits the same fate, which is why §4 stores an operational
policy configuring a measurable operation instead.

## 9. Aggregation is a policy, not the datum

Do not collapse the per-member vector. "Any member exceeds threshold" is an
*alert policy* layered on top and named as such; neither `max` nor `sum` is the
datum (Appendix A.4).

**Partial coverage is a downgrade.** A maximum over an observed subset is a lower
bound on the true maximum: it can trigger correctly but can never *clear*
correctly. Coverage is always surfaced, and no freshness-equivalent statement may
ever be issued from incomplete coverage.

## 10. Commit dates are not arrival times

`%ct` is the **committer date**, not the time a commit arrived in this repository.
Fast-forward, rebase and cherry-pick all land commits *after the captured
baseline* carrying committer dates *before* it, so any `--since=<t>` count
silently misses real change.

Accurate evidence therefore requires a stored **base OID** compared by
*reachability*, not by date. This is why Stage 2 needs a migration; the earlier
"no migration required" plan was buying a materially wrong estimator.

## 11. Commit counts are not portable

A squash-merge repo yields roughly an order of magnitude fewer commits than a
micro-commit repo for identical change. Rejected as universal units: lines
changed (formatting and generated code dominate), distinct commit-days
(reintroduces wall-clock and release cadence), path anchors (improve relevance,
not granularity), `--first-parent` (a different estimator that fails when the base
is an ancestor through a non-first parent).

**There is no universal normalisation from git history alone.** Stage 2 therefore
reports raw components and defines no thresholds. Labelling constants "initial
calibration" does not cure a non-portable unit.

## 12. Non-goals

- **Predict truth.** Never.
- **Delete or mutate anything.** §6.
- **Emit any scalar, bucket or ranking before Stage 3.** §5.
- **Reorder search by time.** The last-update timestamp is reported alongside a
  hit, never folded into rank.
- **Say "fresh".** The strongest available statement is *"no observed changes
  within the declared policy, at this coverage."*

---

# Part II — Implementation

## 13. What exists

Confirmed constraints:

- `internal/project` imports `internal/store`, so **`store` cannot import
  `project`** — a cycle, not a preference. Evidence computation is therefore
  driven from `internal/mcpserver`, which already holds `s.proj` and the entries.
- `memories_fts` is external-content FTS5 whose triggers enumerate columns
  literally, with `snippet()` addressing body by column *index*. Adding
  `m.updated_at` to the search SELECT does not change FTS column indices; the
  virtual table's column list must not change.
- `internal/gitx` is a filesystem-walk resolver whose single subprocess is a
  carved-out fallback carrying a "never on the hook path" warning in two files,
  and it collapses "not a repo" / "git missing" / "git failed" into
  `ErrNotARepo` (gitx.go:109,113) — which cannot express §4's outcomes.
- **Runtime foreign keys are ON**: every DSN carries `_pragma=foreign_keys(1)`
  (`internal/store/open.go:148-151`) over a single-connection pool.
  `applyPending` disables them on a pinned connection during migration and
  restores `PRAGMA foreign_keys = ON` afterwards (`migrate.go:127-132`).
  Declaring FKs while enforcement is off is fine; runtime cascades are live.
- The `repos` primary key is exactly `repo_id` (`0007_multi_repo.sql:35-39`).

## 14. Stage 1 — time visibility (no git, no migration)

**1. `SearchHit` carries its last-update timestamp.**
`internal/store/memories.go:50-57` gains `UpdatedAt`; `searchWith`
(memories.go:157-185) adds `m.updated_at` to SELECT and scan. Order stays `bm25`,
limit stays `MaxSearchHits`. It is a *last-update timestamp* — not an age, not an
assertion time — and the field docs must say so (§2).

**2. Query surface**, following the existing struct-parameter convention
(`store.MemoryWrite`, `store.Registration`):

```go
type MemoryQuery struct {
    UpdatedSince  string // inclusive
    UpdatedBefore string // inclusive
    OrderBy       string // "" | "key" (default) | "recent"
}
func (d *DB) QueryMemories(q MemoryQuery) ([]IndexEntry, error)
```

`ListMemories()` becomes a wrapper so existing callers are untouched.
`"recent"` orders by last-update descending with a `key` tie-break — memories
written in one call share a timestamp, and an unstable order makes a paging
upkeep routine skip entries.

**3. Canonicalise on every read path.** The live database contains non-canonical
timestamps (global `machine-navi31-hard-locks` holds `2026-07-25T22:40:57.901Z` —
three fractional digits, not nine). Every projection that returns a timestamp
canonicalises it after `Scan`:

| Path | Columns |
|---|---|
| `scanMemory` | `created_at`, `updated_at` |
| `scanIndex` / `QueryMemories` / `SuggestSimilar` | `updated_at` |
| `searchWith` / `SearchHit` | `updated_at` |

Parseable values (RFC3339 / RFC3339Nano) are returned in `store.Stamp` form.
An **unparseable** value is a read **error** — doctor treats it as corruption
(§14.5) and Stage 1 cannot order it. Search ranking is unaffected: `bm25` stands,
and only the returned timestamp is canonicalised.

**4. Filter and sort in Go — permanently, for both scopes.** `WHERE updated_at >= ?`
and `ORDER BY updated_at` operate on raw text and would exclude or misorder
non-canonical rows *before* Go ever sees them. Tolerant parsing does not fix
this, because SQLite has already decided.

`ListMemories` already fetches every row with no LIMIT and memory sets are small,
so fetching candidates and filtering/sorting in Go costs nothing real.
**Doctor repair may make the clean case common, but it must never become a
correctness precondition** — a database that has not been repaired must still
sort correctly.

**5. Repair in doctor, unconditionally, in both scopes.** Plain `doctor` already
migrates and backfills via `EnsureSelfRepo`, so a lossless canonicalisation of
*parseable* timestamps runs unconditionally, across **project and global**
memories, and reports the count. Unparseable values are reported, left untouched,
and are a doctor **failure**. Do **not** overload `--gc`, which means retention
pruning.

This is deliberately *not* done in a migration: the known bad row is in the
**global** database, which a project migration cannot reach, and robust
RFC3339/RFC3339Nano parsing with UTC and nanosecond normalisation is not
something static embedded SQL does well.

**6. Validation.** Reject a malformed bound with `serr.InvalidInput` rather than
returning a silently wrong set; validate `updated_since <= updated_before`;
reject unknown `order_by`; document that `updated_before` is **inclusive**.

**7. Tool + docs.** `MemoryListInput` (`tools_memory.go:92`) gains the three
fields; the Memories block in `instructions.go:34-40` and `docs/mcp-tools.md` each
gain a line. `memory_search` does **not** get time filters — a filter interacting
with `MaxSearchHits` truncates differently than an agent expects.

## 15. Stage 2 — evidence substrate (migration)

**No scalar. No aggregation. No verdicts.** Raw evidence only.

### 15.1 Schema — `migrations/project/0008_memory_evidence.sql`

Project scope only; git evidence is undefined for global memories and the option
is **rejected** there rather than silently ignored.

```sql
CREATE TABLE memory_evidence_policy (
  key        TEXT PRIMARY KEY REFERENCES memories(key) ON DELETE CASCADE,
  version    INTEGER NOT NULL DEFAULT 1,
  updated_by TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE memory_evidence_member (
  key         TEXT NOT NULL REFERENCES memory_evidence_policy(key) ON DELETE CASCADE,
  repo_id     TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
  base_oid    TEXT NOT NULL,
  captured_at TEXT NOT NULL,
  PRIMARY KEY (key, repo_id)
);
CREATE TABLE memory_evidence_path (
  key     TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  kind    TEXT NOT NULL CHECK (kind IN ('literal','glob')),
  pattern TEXT NOT NULL,
  PRIMARY KEY (key, repo_id, kind, pattern),
  FOREIGN KEY (key, repo_id) REFERENCES memory_evidence_member(key, repo_id) ON DELETE CASCADE
);
```

- **Zero child paths means whole-member observation** — the conservative default.
- **No `ON UPDATE CASCADE` anywhere.** Declaring it only on the policy table
  would cascade one level and then fail at the next; no memory-key rename
  operation exists, so the whole promise is dropped rather than half-kept.
- Delimited TEXT was rejected: git filenames may legally contain newlines, and it
  cannot distinguish literal from glob, validate, or deduplicate.
- The `repos` FK matters: `project remove` deletes repos, and a departed member's
  evidence must disappear rather than petrify as a permanent `missing_worktree` row.
- `memories` itself is untouched, so `memories_fts` and its triggers are not involved.
- This migration performs **no timestamp canonicalisation** — see §14.5.

### 15.2 Operations — capture is explicit, and CAS'd

`memory_write` **never** creates, modifies or re-captures an evidence policy.

| Operation | `expected_memory_version` | `expected_policy_version` |
|---|---|---|
| `memory_evidence_set` | **always required** | omitted only when no policy exists; integer for replacement/recapture |
| `memory_evidence_clear` | **required** | **required** |

`clear` checks the memory version for the same reason `set` does: otherwise an
agent holding an older proposition can strip evidence after a concurrent
rewrite. Both return a CAS conflict carrying current memory *and* policy context,
and both return the resulting policy version. New audit actions
`evidence_policy_set` / `evidence_policy_clear`, matching the existing
`memory_*` rows.

**Atomicity.** Resolve and capture every declared member's HEAD *first*; if any
initial capture fails, fail the whole operation without touching stored policy.
Only then open **one** transaction, re-check both CAS versions, and replace the
parent, member and path rows together. Partial coverage is an evaluation state
that presupposes a valid policy — it is never a valid half-created baseline.

**Input validation.** Members must be non-empty, unique, and registered in
`repos`. Patterns must be non-empty, repo-relative, normalised, free of NUL, and
unable to escape via absolute paths or `..` components.

**Rationale** (§7): if an ordinary edit re-captured the baseline, a typo fix would
silently erase accumulated evidence. Keeping a stale baseline across an edit
**over**-reports change, which is visible and conservative; resetting it
**under**-reports, which is invisible. `memory_write` therefore notes *"evidence
baseline unchanged; recapture explicitly if this edit reasserts the proposition"*
— but **only on an update to a memory that actually has a policy**, otherwise it
is noise on every write in the system.

### 15.3 Promotion

`memory_promote` is **allowed and unchanged in effect**. Evidence policy is
auxiliary, project-local observation configuration — not part of `P(m)`.

- It copies **content only**.
- The project evidence policy **remains on the source**.
- The global target receives no git policy, which is correct: git evidence is
  undefined there.
- When the source has a policy, the response carries a structured note:
  *"Content promoted. Git evidence is project-only and was not copied; the source
  policy remains on project:&lt;key&gt;."*

The wording is "not copied", never "lost".

### 15.4 Evaluation

Reachability-based, so §10's date problem cannot arise. Per declared member:

```
git -C <worktree> rev-parse --verify <base>^{commit}        # missing_base if this fails
git -C <worktree> rev-parse --verify HEAD
git -C <worktree> merge-base --is-ancestor <base> HEAD      # non-zero → non_ancestor
git -C <worktree> rev-list --count --full-history <base>..HEAD -- <compiled pathspecs>
```

The explicit base-object check is what makes `missing_base` distinguishable:
`merge-base` exit 1 means *not an ancestor*, while an invalid or absent object
produces a different failure, and the two must not be collapsed.

- Patterns are **compiled, never passed raw**: literal → `:(top,literal)<path>`,
  glob → `:(top,glob)<pattern>`. A leading colon in user input would otherwise
  enable pathspec magic including exclusions, and bare `*` crosses directory
  separators in ordinary pathspecs, which surprises agents.
- `--full-history` is pinned so git does not prune an entire parent history
  merely because a merge is TREESAME to one parent.
- The component is named **"git full-history path-limited commit count"**, not
  "commits touching paths", and the **count mode is returned in the record**.
  Under path limiting, rev-list applies history simplification: a trivial merge
  may be omitted while its constituent commits count, and a conflict-resolution
  merge touching the paths may itself count. That is documented git behaviour,
  not a stable integration-event unit (§11).
- `merge-base` cannot distinguish force-push from branch switch, so report
  `non_ancestor` and list causes as **possibilities**, never as detected facts.

### 15.5 The response contract

`EvidenceInfo` is the MCP contract and is pinned here, not left to implementation.

```jsonc
{
  "configured": false,          // policy state, INDEPENDENT of coverage
  "state": "not_configured",    // not_configured | evaluated
  "policy_version": 3,          // when configured
  "measured_at": "…",
  "coverage": "complete",       // complete | partial | unavailable — only when configured
  "members": [
    { "repo": "app",
      "outcome": "measured",    // measured | non_ancestor | missing_base | shallow |
                                // missing_worktree | timeout | git_error
      "base_oid": "…", "head_oid": "…",
      "count": 212,
      "count_mode": "git full-history path-limited commit count",
      "paths": [ {"kind": "glob", "pattern": "internal/**"} ],   // echoed boundary
      "warnings": ["pattern \"docs/x\" matched nothing in base or head"] }
  ]
}
```

**With `include_drift: true`, a memory with no policy must still return an
evidence record** with `configured: false` / `state: "not_configured"`. It must
**not** be omitted under `omitempty`: omission is indistinguishable from "the
caller never asked for drift", which is precisely the ambiguity the flag exists
to remove. `coverage` is meaningless without a policy and is absent in that state.

The two operations' input/output shapes are pinned alongside it, including
CAS-conflict payloads (current memory version, current policy version) and the
resulting policy version on success.

**Docs.** Stage 2 gets the same treatment Stage 1 does: the Memories block in
`instructions.go` and the tool reference in `docs/mcp-tools.md` both describe
`include_drift`, both operations, and — critically — the fact that the output is
change evidence rather than a freshness verdict.

### 15.6 Mechanics

- **New package `internal/drift`** — not `gitx` (§13). It takes already-resolved
  worktree paths; it never calls `gitx.Resolve` itself.
- Response wrapper in `tools_memory.go`, since `store.IndexEntry` must stay pure:
  `MemoryListEntry{ store.IndexEntry; Evidence *EvidenceInfo }` — embedding
  promotes the existing JSON fields, so the change is purely additive.
- **Opt-in** `include_drift` on `memory_list` — *not* `include_freshness`, which
  would promise a verdict the model does not emit. The last-update timestamp
  stays unconditional from Stage 1; only the git work is gated. Rejected outright
  for global scope rather than silently ignored.
- One `context.WithTimeout` **derived from the MCP request context** for the whole
  phase, with members loaded **concurrently** so one wedged member cannot consume
  the entire budget. `exec.LookPath("git")` once, not M failed spawns.
- Shallow detection at **`CommonDir/shallow`** — `.git` is a *file* in a linked
  worktree. `project.Member` already carries `CommonDir`.

## 16. Stage 3 — deferred, explicitly

Explicit `reaffirm` / `revise` / `refute` operations recording real outcomes
against Stage 2 evidence. Only once that history exists can anyone ask whether
repo-relative thresholds or an upkeep ranking are defensible; prior volatility
and corroboration history are candidate signals at that point, not before.
§5's missing impact and cost terms may well remain agent judgment rather than schema.

The intended end-state lifecycle — assert → accumulate evidence → reaffirm /
revise / refute — is **hypothetical** until then, and no part of Stages 1–2
presumes it.

## 17. Verification

**Stage 1**
- `QueryMemories`: each bound, both orderings, malformed bound → `InvalidInput`
  (not a silently empty result), inverted range rejected, unknown `order_by` rejected.
- **A non-canonical timestamp row sorts and filters correctly with no repair
  having run** — the specific bug §14.4 exists for, and the one a SQL-side
  implementation gets wrong. Cover both project and global scope.
- Every read path returns canonical form: `scanMemory`, `scanIndex`,
  `QueryMemories`, `SuggestSimilar`, `searchWith`. An unparseable value errors.
- `doctor` canonicalises parseable rows in **both** scopes and reports; an
  unparseable row fails.
- `SearchMemories` returns populated `UpdatedAt` **and** the `snippet` highlight
  still lands on `body`, proving the FTS column index was untouched.

**Stage 2** — `internal/drift` against real git fixtures (`t.TempDir()` + real
commits), no `exec` mocking, matching how `internal/gitx` and `internal/hooks`
already test git:
- **A rebase/cherry-pick fixture whose committer dates predate the base OID** —
  the case §10 exists for.
- Merge semantics: normal merge of path changes, conflict-resolution merge,
  squash, and change-then-revert — asserting the documented count mode, not an
  assumed one.
- Pathspec: a pattern containing a leading colon is treated literally; `*` does
  not cross directory separators under `:(top,glob)`; `..` and absolute patterns
  are rejected at input.
- Force-push / branch switch → `non_ancestor`; a deleted base object →
  `missing_base`; the two are distinguished.
- Shallow clone detected via `CommonDir/shallow`, including from a linked worktree.
- Coverage: one of three members failing → `partial`; **all** members failing →
  `unavailable`.
- Deadline exhaustion → `partial`, already-loaded members retained.
- `include_drift: true` on a memory with **no** policy returns
  `configured: false` / `not_configured`, never an omitted field.
- CAS matrix: `set` without `expected_memory_version` rejected; `set` with a stale
  `expected_policy_version` rejected; `clear` requires both; conflict payloads
  carry current versions.
- Atomicity: a member whose HEAD cannot be resolved leaves stored policy
  completely unchanged.
- `memory_write` leaves an existing baseline unchanged, and emits the baseline
  note only when a policy exists.
- `memory_evidence_set` rejected on global scope.
- **Cascades actually fire**: assert `PRAGMA foreign_keys` is ON at runtime, then
  delete a memory and a repo and confirm evidence rows vanish. The existing
  `foreign_key_check` test proves consistency, not that enforcement is enabled.
- `memory_promote` copies content and returns the not-copied note (§15.3).

**End to end**
- `go build ./... && go vet ./... && go test ./...`
- Drive the real MCP server against this repo: `memory_list` with
  `order_by:"recent"`, then `include_drift:true` on a memory given a policy, and
  confirm the reported boundary matches the declared policy.
- No hook path regression: `internal/hooks` must not import `internal/drift`.

## 18. Where this lives

`docs/memory-model.md`. Part I is the standing justification for why there are no
thresholds, why nothing is ever deleted, and why the schema stores an operational
policy rather than a referent.

---

# Appendix A — Rejected approaches

Kept because each prevents a specific, likely regression. None is current design.

**A.1 Biological decay** (memories fade since last access; use reinforces).
Access frequency measures FTS phrasing, not value — generic wording is retrieved
more often. An LLM cannot notice an absence, so wrongly forgetting costs far more
than wrongly keeping. And decay fuses the label transition with the removal
transition (§6).

**A.2 A `referent` field** (`repo | external | user | historical`). Would
recreate `type`'s failure with higher stakes: a second semantic label, supplied
by the same agents, with nothing depending on it. Replaced by the operational
evidence policy (§4), which configures a measurable operation and returns its own
boundary.

**A.3 Mapping `type`/scope onto a referent partition.** Refuted by four live
memories (§8).

**A.4 Collapsing multi-repo evidence to `sum`, then to `max`.** `sum` scales with
member count, so no threshold ports across projects. `max` assumed each memory
refers to exactly one unknown member, and can *clear* a memory whose exposure is
distributed across members. Neither is the datum (§9).

**A.5 "Expected value of re-verifying" as the emitted quantity.** The system has
neither the impact term nor the cost term, so it cannot compute an expected value
(§5). Renaming it "verification pressure" was still a relabel: Stages 1–2 compute
change evidence and nothing else.

**A.6 Date-windowed drift** (`--since`, `--since-as-filter`, sorted commit
timestamps, binary search per memory). Committer dates are not arrival times
(§10). Reachability from a stored base OID replaced the entire mechanism and is
both correct and smaller.

**A.7 Global commit thresholds** (`≥50` elevated, `≥200` suspect). The unit is
not portable across merge styles (§11).

**A.8 Re-capturing the baseline on `memory_write`.** A typo fix would silently
erase accumulated evidence — the §7 inference error, in the one place where it
destroys data rather than mis-ranking it (§15.2).

**A.9 Tolerate-on-read alone for non-canonical timestamps.** SQLite filters and
orders on raw text before Go can canonicalise (§14.4).

**A.10 Canonicalising timestamps in migration 0008.** The known bad row is in the
**global** database, which a project migration cannot reach; and robust
RFC3339/RFC3339Nano normalisation is not a job for static embedded SQL (§14.5).
