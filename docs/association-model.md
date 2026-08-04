# Associative & episodic memory in stigmergy — model and implementation

## Context

Stigmergy's memories are a flat, CAS-versioned key namespace per scope, retrieved
by FTS5/bm25 keyword match (`internal/store/memories.go`, `searchWith`). There are
**zero relations between memories**: no foreign key connects one memory to
another, there are no tags, and the key grammar (`^[a-z0-9][a-z0-9-]{0,127}$`,
`internal/store/memories.go`) forbids every separator character, so not even a
hierarchical naming convention is expressible. A memory is found only if it
shares *words* with the query — never because it shares *meaning* with the task.

The live database already shows agents faking the missing structure: "See also
`spongebob-restore-chain-hardlock-context`" written into a description, invisible
to tooling; episodic narratives ("cost a debugging round in `evidencePolicies`")
smuggled into semantic entries because there is nowhere else to put what
*happened*, as opposed to what *is true*.

This document specifies what to build: an explicit **association web** between
memories, **activation-driven exposure** of that web (links pushed into agent
context, never opt-in), and a curated **episodic layer** with provenance.
Approaches that were considered and rejected are in **Appendix A**, because the
reasoning that killed them prevents specific regressions.

**Read `docs/memory-model.md` first.** Its laws bind this design — particularly
§6 (evidence never mutates or removes), §8 (an enum nothing depends on is not a
free source of meaning), §11 (no portable unit), §12 (non-goals: no ranking, no
scalars, search order is bm25), and Appendix A.1/A.2 (decay and referent labels,
rejected). Nothing below relitigates them; everything below is filtered through
them.

---

# Part I — The model

## 1. The governing principle: the agent's weights are the general semantic network

Human semantic memory is an interrelated web: think of the Sharp SM83, arrive at
CISC, arrive at the x86 you are porting to. An LLM agent already *has* that web —
in its weights. Caesar→Rome→wine happens at inference time, free, current, and
better than anything a SQLite file could store.

Therefore stigmergy stores **only project-specialized associations**: bindings
the weights cannot contain because they are facts about *this* project. Not "TMs
teach moves" (the model knows Pokémon) but "in this repo, TM logic lives in
`tmhm.asm` and shares the move-effect table with the battle engine's dispatch."
The stored edge anchors project artifacts to each other; the agent supplies the
general-knowledge hop when it reads the edge's reason.

Consequences, all deliberate:

- **No general-knowledge nodes.** Concepts like "CISC" or "wine" never get
  entries just to be linked. If a concept matters to the project, it earns a
  memory the normal way, with a proposition about the project.
- **Edges are same-scope only.** With general concepts out of the picture, the
  motivation for project↔global edges collapses; and mechanically, project and
  global are two SQLite files that cannot share a transaction or a foreign key
  (see `PromoteMemory`'s ordering comment in `internal/store/memories.go`).
  Global memories may link to global memories; project to project; never across.

## 2. Psychology, imported through this repo's filters

Each phenomenon is imported **with its operational translation and its filter** —
what was kept, what was cut, and why. The cuts are as load-bearing as the keeps.

| Phenomenon | Operational translation | Filter applied |
|---|---|---|
| **Spreading activation** (Collins & Loftus 1975): retrieval follows associative links outward from cues | Search returns FTS hits **plus each hit's linked neighbors, labeled as neighbors**; reading a memory surfaces its neighbors | Real activation is weighted and decays with distance. No portable unit exists for edge strength (memory-model §11), so: unweighted edges, exactly one hop, a labeled second tier **never folded into bm25 order** (§12) |
| **Encoding specificity** (Tulving): a cue retrieves a memory only when it matches the encoding context | Every edge carries a **mandatory free-text reason** — the context that makes the hop meaningful | A bare edge ("A relates to B") is useless to a future reader; the reason is the payload, not metadata |
| **Fan effect** (ACT-R): the more associations a node has, the weaker each one's pull | Hard cap on links per memory; bounded surfacing caps; link inflation named as a failure mode in agent instructions | We do not model the effect quantitatively; we bound the structure so it cannot dominate context |
| **Priming**: activating a concept lowers the retrieval threshold of its neighbors | The agent's *current activity* is a cue: touching file F primes memories whose evidence paths cover F, and one hop of their neighbors — delivered as an end-of-turn note | Only files the session actually claimed; once per (root, memory); bounded note size. Priming routes attention; it asserts nothing |
| **Reconsolidation**: a retrieved memory becomes labile — retrieval is the moment of update | The priming note tells the agent to `memory_verify` what it confirmed and update or unlink what it invalidated | The web routes attention to the verification machinery that already exists (memory-model §16); nothing here records a verification implicitly (§7) |
| **Testing effect**: re-encountering material is what strengthens memory; for an LLM, "remembering" literally means being in the context window | **Push, not pull**: links surface unconditionally on read and search (no opt-in flag); priming notes inline neighbor *descriptions*, not just keys; delivery rides the hook path that already makes mail unskippable | Exposure is bounded (descriptions, never bodies; capped counts) so push cannot become flooding |
| **Episodic vs semantic memory** (Tulving 1972; Renoult et al. 2019): events-in-context vs knowledge-out-of-context, distinct but interdependent | An `episodes` table (what happened: actor, session, time) distinct from `memories` (what is true), with provenance links so a semantic claim can cite the episodes that ground it | Prior art (Zep/Graphiti, AriGraph) uses LLM pipelines to extract semantic facts from episodes. **Stigmergy contains no LLM** — the agents are the intelligence. Semanticization is an explicit agent act: read episodes → write the distilled memory → link provenance |
| **Consolidation**: episodic experience is a scaffold; what generalizes is distilled into semantic knowledge, and the scaffold fades | Episodes are **prunable history**: GC may reclaim old episodes that ground no memory and sit in no chain. Distilled-and-cited history is provenance and stays | This is consolidation as *retention policy*, not the rejected biological decay (memory-model A.1): semantic content never fades, access frequency strengthens nothing, and removal never touches `memories` |
| **Base-level activation / decay** (use strengthens, disuse fades) | **Not imported. At all.** | Memory-model A.1's reasoning stands: access frequency measures FTS phrasing, not value; an LLM cannot notice an absence. Do not re-import decay through the back door as edge weights, edge aging, or link-use counters |

## 3. Edges: explicit, untyped, symmetric, reasoned facts

An edge is an assertion an agent makes deliberately: *these two memories are
associated, and here is why*. Like every other agent-authored fact in stigmergy
it is attributed, timestamped, audited, and explicitly created and deleted.

**What branches on an edge** — the memory-model §8 test ("an enum nothing
depends on is not a free source of meaning"), answered before the schema exists:

1. `memory_read` and `memory_search` surface neighbors unconditionally (§5).
2. Priming walks edges to build the end-of-turn note (§4 of Part II).

A **wrong** edge therefore produces a visibly irrelevant neighbor stub and a
visibly irrelevant nudge — which any agent can fix with `memory_unlink`. A
**missing** edge produces exactly the doc rot the project suffers today. That
asymmetry is the detectability story, and it is why edges need no verification
machinery of their own.

Deliberate design cuts:

- **Untyped.** One relation kind. A typed vocabulary (`supersedes`, `part-of`,
  `refutes`…) is only defensible if something branches on each type from day
  one; otherwise it is the `type` column's failure repeated. The mandatory
  reason carries the semantics, and the reading agent interprets it.
- **Symmetric.** Association is bidirectional in every psychological model this
  imports. Stored once in canonical order, queried both directions.
- **No weights, no decay, no use-counters** (§2 filters).
- **No CAS versions on link operations.** An edge is cheap, attributed, and
  repairable by unlink; demanding `expected_version` for both endpoints would
  tax exactly the behavior we want more of. A duplicate link *is* rejected, and
  the rejection carries the existing edge so the agent can merge reasons
  (unlink + relink) rather than silently losing either.
- **Nothing machine-generated is ever stored.** The create-path `similar[]`
  suggestions (already returned by `memory_write`) are the transient
  link-candidate list; an agent may act on them with `memory_link`. Stored
  auto-similarity is Appendix A.2 of the memory model — an unauditable semantic
  claim — plus a model dependency a static Go binary must not carry.

## 4. Episodes: history, not state

`internal/store/gc.go` states the doctrine: memories are "the state of the
system, not its history." Episodes are the other half, finally first-class:
**history, not state**. "Session N tried approach X; it failed because Y" is
actor-bound, time-bound, and true forever *as a record of what happened* — even
if the reasoning it records turns out wrong.

- **Immutable.** No update tool exists, ever. A wrong episode is corrected by a
  **new** episode linked `corrects`; an investigation that resumes is linked
  `continues`. What was believed stays written; what we now believe is a later
  record. (This is the episodic analogue of never inferring a verification from
  a write.)
- **Successors always surface.** Anything that returns an episode returns its
  successor chain unconditionally — superseded reasoning is never read without
  its correction. This is the §5 push principle applied to episodes.
- **Provenance.** An episode can ground one or more memories (`episode_memory`),
  giving semantic claims citations: *this* is where that lesson came from.
  Semanticization is the agent act of reading episodes and writing the distilled
  memory, then linking provenance.
- **Prunable.** GC may reclaim episodes that are old **and** ground no memory
  **and** sit in no chain (either side). Everything else stays. Consolidation as
  retention policy (§2).
- **Project scope only.** Episodes are session-bound events in a repo; the
  global DB has no roots and no sessions. A machine-wide lesson worth keeping
  is, by definition, already semantic — promote a memory.

## 5. Push, not pull

An association that sits in a table until an agent volunteers a tool call might
as well not exist: for an LLM agent, remembering means *being in the context
window*. So exposure is unconditional at moments of activation:

- `memory_read` always returns the link list — neighbor key, description, and
  the edge's reason. No `include_links` flag exists to not set.
- `memory_search` hits carry bounded neighbor stubs, labeled `linked`.
- Priming notes inline neighbor descriptions — reading the note *is* reading
  the association.
- Delivery rides the hook path that already makes mail unskippable.

The fan effect (§2) is the counterweight: all exposure is bounded — one hop,
capped counts, descriptions never bodies.

## 6. Non-goals

- **Never reorder search by link structure.** bm25 stands; neighbors are a
  labeled second tier. (memory-model §12 extended.)
- **Never auto-create an edge** — not from co-access, not from FTS similarity,
  not from evidence-path overlap. Suggestions may be *shown*; only agents write.
- **Never infer a verification** from a priming note being delivered, read, or
  acted on. `memory_verify` remains the only assertion record.
- **Never cross-scope edges.** (§1.)
- **Never general-knowledge nodes.** (§1.)
- **Never weights, decay, or use-strengthening.** (§2.)
- **Never touch `memories` or `memories_fts`.** Everything here is satellite
  tables. The FTS virtual table's triggers enumerate columns literally and
  `snippet()` addresses body by column index; the schema of `memories` is frozen
  to this design.

---

# Part II — Implementation

## 7. What exists — constraints confirmed in code

The implementing agent should verify each of these before relying on it; line
numbers drift, symbols do not.

- **FTS law**: `memories_fts` is external-content FTS5; its three triggers
  (`memories_ai/_ad/_au`) enumerate columns literally; `snippet()` addresses
  `body` by column index 2 (`searchWith`, `internal/store/memories.go`). Adding
  columns to `memories` is forbidden by this design; satellite tables only.
- **Foreign keys are ON at runtime**: every DSN carries `_pragma=foreign_keys(1)`
  (`internal/store/open.go`), single-connection pool, so `ON DELETE CASCADE` is
  live. Migrations run with FKs off on a pinned connection (`migrate.go`).
- **CAS**: the whole matrix lives in `casCheck` (`internal/store/memories.go`),
  shared by write/delete/promote. Link/episode operations do **not** use it (§3).
- **Timestamps**: store in `store.Now()` form; canonicalise after every Scan via
  the `CanonicalStamp`/`canonicalStamps` helpers; filter and sort **in Go**,
  never in SQL over raw text (memory-model §14.4). Applies to every new
  projection below.
- **Key grammar**: `^[a-z0-9][a-z0-9-]{0,127}$` (`ValidateKey`). Total order on
  keys is therefore well-defined byte order — the canonical-pair trick in §8
  depends on it.
- **Text rules**: single-line fields via `ValidateLine`, multi-line via
  `ValidateBlock` (`internal/store/text.go`; see the `memory-text-rules`
  project memory). Edge reasons are single-line; episode bodies are blocks.
- **Search**: `MaxSearchHits = 20` per scope; project hits always precede global
  (`SearchScopes`, `internal/store/search.go`); `FTSQuery` quotes tokens and
  ANDs them (`internal/store/fts.go`).
- **Mailbox delivery pattern**: `CheckMail` / text build / `MarkDelivered`
  with `notified_at` ensuring exactly-one interruption
  (`internal/store/mail.go`, `internal/hooks/mail.go`, `internal/cli/hook.go`
  Stop path). Priming clones this shape.
- **Hook-path law**: `internal/hooks` never spawns git and never imports
  `internal/drift` — pinned by a transitive-import test. Priming must be pure
  SQLite reads.
- **Roots & claims**: `RootsOfSession` (`internal/store/roots.go`) finds the
  session's roots; active claims carry `RepoID` + repo-relative `ScopePath` +
  `Recursive` (`internal/store/claims.go`); claim-before-edit is already the
  mandated workflow, so claims are the durable record of what a session touched.
- **Evidence tables**: `memory_evidence_policy/_member/_path`
  (`migrations/project/0008_memory_evidence.sql`, `internal/store/evidence.go`).
  Zero `_path` rows = whole-member observation. `EvidencePolicies` shows the
  batched one-query + assemble-in-Go pattern to copy for `NeighborsOf`.
- **Response-wrapper precedent**: `MemoryListEntry{ store.IndexEntry; … }`
  embedding in `internal/mcpserver/tools_memory.go` — store types stay pure,
  mcpserver wraps. Same trick for search hits.
- **`memory_write` create path** already returns `similar[]` (top-3 FTS-OR
  near-duplicates via `SuggestSimilar`) — reuse as the link-candidate list;
  no new machinery.
- **Instructions**: `internal/mcpserver/instructions.go` — the mandatory
  priority block must stay ≤512 chars (unit-test enforced); new text goes in
  `extended()` only.
- **Migrations**: latest were `project/0010_caller_tickets.sql` and
  `global/0002_known_projects.sql` before this design's own Stages A–C added
  `project/0011–0013` and `global/0003` (§8.1, §9.1, §10.1) — now the actual
  latest. Every new migration must state its undo in
  `TestRestoringIndexesOnADatabaseThatAlreadyLostThem`
  (`internal/store/migrate_test.go`), and a migration failure fails every hook
  closed machine-wide until rebuild (see the
  `shipping-a-migration-locks-the-repo` memory) — land schema and code together.
- **Enforcement plumbing**: `RootGateTools` regex (`internal/hostcfg/claude.go`)
  routes calls through the root-gate hook; the `sessionOnly` map
  (`internal/hooks/claude.go`) keeps designated tools with the session root.
- **Audit**: `memory_*` audit actions in `internal/store/audit.go` callers;
  follow the existing naming (`memory_link`, `memory_unlink`,
  `episode_record`).
- **Known inherited gap**: a project adopted purely through `context_open` has
  an empty `repos` table (see the `memory-change-evidence-design` memory), so
  evidence-based priming silently has nothing to join against until
  `stigmergy doctor` runs. Same fix path as evidence; do not redesign around it.

## 8. Stage A — the association web

### 8.1 Schema — `migrations/project/0011_memory_links.sql` and `migrations/global/0003_memory_links.sql`

Identical DDL in both scopes (links are same-scope; each DB carries its own
table). Follow 0008's comment style: the file explains why untyped, why
symmetric, why no weights (cite this doc).

```sql
CREATE TABLE memory_links (
  key_a      TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  key_b      TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  reason     TEXT NOT NULL,     -- the encoding context; never empty
  created_by TEXT NOT NULL,
  agent_kind TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (key_a, key_b),
  CHECK (key_a < key_b)
);
CREATE INDEX idx_memory_links_b ON memory_links(key_b);
```

`CHECK (key_a < key_b)` is total under the key grammar and makes symmetry
structural: one row per pair, self-links impossible, the PK prefix serves
`key_a` lookups and the index serves `key_b`. No `ON UPDATE CASCADE` (same
reasoning as 0008: no rename operation exists; drop the promise rather than
half-keep it). Add `DROP TABLE memory_links` (and the index) to the undo test
for **both** scopes.

### 8.2 Store — new `internal/store/links.go`

Constants — one place, explicitly tunable, never load-bearing semantics:

| Constant | Value | Meaning |
|---|---|---|
| `MaxLinksPerMemory` | 16 | hard cap at create; the error names the fan effect and suggests pruning or an intermediate memory |
| `MaxNeighborsSurfaced` | 8 | cap on `memory_read`'s link list (with total reported) |
| `MaxSearchNeighbors` | 4 | neighbor stubs per search hit |
| `MaxPrimingMemories` | 6 | memories per priming note (Stage B) |

API:

```go
type Neighbor struct {
    Key         string `json:"key"`
    Description string `json:"description"` // joined from memories in the same query
    Reason      string `json:"reason"`
    LinkedBy    string `json:"linked_by"`
    LinkedAt    string `json:"linked_at"`
}

func (d *DB) CreateLink(a, b, reason, actor, agentKind string) (Link, error)
func (d *DB) DeleteLink(a, b, actor, agentKind string) (bool, error)
func (d *DB) NeighborsOf(keys []string) (map[string][]Neighbor, error)
func (d *DB) LinkCounts(keys []string) (map[string]int, error)
```

`CreateLink`: `ValidateKey` both; reject `a == b`; `ValidateLine` the reason;
pre-check both memories exist so the error is a helpful `InvalidInput` naming
the missing key rather than a bare FK failure; canonicalise order; **a
duplicate pair is an error carrying the existing edge** (the conflict-carrying
pattern CAS errors use — idempotent create would silently discard a differing
reason; the agent merges by unlink + relink); enforce `MaxLinksPerMemory` for
both endpoints inside the transaction; audit `memory_link` with the pair as
target and the reason in detail.

`DeleteLink`: absent pair returns `(false, nil)`, not an error; audit
`memory_unlink` with the severed reason in detail.

`NeighborsOf`: one batched query (`key_a IN (…) OR key_b IN (…)`) joined to
`memories` for descriptions, assembled in Go (the `EvidencePolicies`
precedent); timestamps canonicalised.

### 8.3 MCP surface — `internal/mcpserver/tools_memory.go`, `server.go`

New tools (register after `memory_promote`):

- **`memory_link(scope, key, other_key, reason)`** → the stored edge.
  Description: *"Associate two memories in the same scope, with the reason the
  association matters. Links are shown to every agent that reads or finds
  either memory. Record project-specific connections your general knowledge
  cannot infer."*
- **`memory_unlink(scope, key, other_key)`** → `{removed: bool}`.

Changed outputs:

- **`memory_read`**: gains `links: []Neighbor` — **always present when the
  memory is found, empty array when none** (omission is indistinguishable from
  not-asked; same rule as evidence's `not_configured`), capped at
  `MaxNeighborsSurfaced` with `links_total` when truncated.
- **`memory_search`**: hits become `SearchHitEntry{ store.SearchHit;
  Linked []Neighbor }` (wrapper in mcpserver; store type stays pure). One
  batched `NeighborsOf` per scope after search; ≤ `MaxSearchNeighbors` stubs
  per hit; the field is named `linked` and **bm25 order is untouched**.
- **`memory_list`**: entries gain `link_count` (via `LinkCounts`; no bodies, no
  reasons — a structure signal, not an exposure path).
- **`memory_write`** (create path): unchanged schema; the instructions text now
  frames the existing `similar[]` as link candidates.
- **`memory_delete`**: read neighbor keys inside the delete transaction; append
  `severed_links=[…]` to the audit detail and return a note naming them —
  "severed", never "lost".
- **`memory_promote`**: links are not copied (same-scope only). When the source
  has links, extend the existing not-copied note: *"Links are project-local and
  were not copied; re-link the global copy against global memories if the
  associations hold there."*

### 8.4 Plumbing, instructions, docs

- `RootGateTools` (`internal/hostcfg/claude.go`): add `memory_link|memory_unlink`
  to the alternation. **Note**: installed `settings.json` blocks are stale until
  `stigmergy init`/`doctor` re-runs per repo — until then a peer's link call is
  attributed to the session root. Attribution-only stakes; say it in release
  notes.
- `sessionOnly` (`internal/hooks/claude.go`): add both tools, reason "memories
  outlive you" (matches the existing memory_write entry style).
- `instructions.go` `extended()` Memories block, three additions: link
  semantics ("record connections your weights can't know; every link needs its
  why"); the fan-effect warning ("a memory linked to everything primes nothing —
  prefer a new intermediate memory over a hub"); "the `similar` list on create
  is your link-candidate list."
- `docs/mcp-tools.md`: a Links subsection under Memories (tools, output changes,
  the push-not-pull rule). `docs/architecture.md`: schema section gains
  `memory_links`.

## 9. Stage B — priming (doc-rot / code-rot nudges)

All pure SQLite on the hook path (§7 hook-path law).

### 9.1 The cue chain

1. **Cue set** = active claims of this session's roots (`RootsOfSession` →
   active claims). Claims are what the session declared it was touching.
2. **Claim → memory**: memories whose evidence policy overlaps a claimed scope,
   in the claim's repo. Zero-path policies (whole-repo, the common default)
   match any claim in that repo. Literal paths reuse the claim-overlap algebra
   (`internal/claims`); glob patterns get a small Go matcher with git-`:(glob)`
   semantics (`*` never crosses `/`, `**` does), unit-tested against the same
   cases `internal/drift`'s pathspec tests use — but implemented in a package
   hooks may import (NOT `internal/drift`).
3. **One hop**: `NeighborsOf` over the primed set; neighbors join the note,
   labeled as neighbors with their edge reasons.
4. **Dedup** — `migrations/project/0012_priming_delivered.sql`:

```sql
CREATE TABLE priming_delivered (
  root_id TEXT NOT NULL REFERENCES roots(root_id) ON DELETE CASCADE,
  key     TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  at      TEXT NOT NULL,
  PRIMARY KEY (root_id, key)
);
```

Once per (root, memory) — a session hears about a memory exactly once. Mark
**after** the note text is composed, never before (the `notified_at`
philosophy: a crash between mark and delivery must not eat the note).

### 9.2 Delivery

Extend the existing Stop mail-gate path (`internal/cli/hook.go`): after
`CheckMail`, compute priming; if either is non-empty, emit **one** StopBlock
(the `stop_hook_active` guard already prevents loops), then `MarkDelivered` +
mark priming. New `internal/hooks/priming.go` mirroring `mail.go`'s shape
(`CheckPriming` / `PrimingText` / `MarkPrimed`).

Note format: ≤ `MaxPrimingMemories` memories; each as `key — description`
(descriptions inline: forced exposure), neighbors indented under the primed
memory with the edge reason; `"…and N more (memory_list)"` on overflow; closing
instruction, verbatim:

> You changed files these memories are about. Check the cross-referenced
> docs/code before finishing; memory_verify what you confirmed, update or
> unlink what you invalidated.

Claude first (Stop). Codex/opencode receive the same text on their advisory
channels (UserPromptSubmit/PostToolUse) as a follow-up change. Edit-time
(PostToolUse) promotion on Claude only after the noise budget is proven live —
delivery cadence is a constant to tune, not a redesign.

## 10. Stage C — episodes

### 10.1 Schema — `migrations/project/0013_episodes.sql` (project scope only; §4)

```sql
CREATE TABLE episodes (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  title      TEXT NOT NULL,          -- ValidateLine
  body       TEXT NOT NULL,          -- ValidateBlock; immutable once written
  actor      TEXT NOT NULL,
  agent_kind TEXT NOT NULL,
  at         TEXT NOT NULL
);
CREATE TABLE episode_memory (        -- provenance: this episode grounds that memory
  episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  key        TEXT NOT NULL REFERENCES memories(key) ON DELETE CASCADE,
  note       TEXT NOT NULL,
  PRIMARY KEY (episode_id, key)
);
CREATE TABLE episode_links (         -- correction/continuation chains
  episode_id   INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  successor_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('corrects','continues')),
  PRIMARY KEY (episode_id, successor_id),
  CHECK (episode_id != successor_id)
);
CREATE VIRTUAL TABLE episodes_fts USING fts5(
  title, body, content='episodes', content_rowid='id',
  tokenize='porter unicode61'
);
-- plus insert/delete/update triggers cloned from the memories_fts pattern.
-- A NEW virtual table: memories_fts and its triggers are untouched.
```

### 10.2 Tools (session-root-only: add to `sessionOnly` and `RootGateTools`)

- **`episode_record(title, body, memory_keys?, note?, corrects_episode_id?,
  continues_episode_id?)`** — record, ground, and chain in one call. Validates
  referenced memories/episodes exist; audit `episode_record`.
- **`episode_read(id)`** — body + provenance (linked memories with notes) +
  the successor chain, **always** (§4: superseded reasoning is never read
  without its correction).
- **`episode_list(since?, before?, query?, limit?)`** — recent-first; `query`
  runs `episodes_fts` through the existing `FTSQuery` quoting; time bounds
  RFC3339 inclusive, filtered in Go (canonical-stamp rule).
- **`memory_read`** additionally surfaces provenance citations for memories
  that have them: latest 5 of `{episode_id, title, at}` plus a total.
- **`memory_search` stays memories-only.** Same reason search never got time
  filters: an extra result class interacting with `MaxSearchHits` truncates
  differently than an agent expects. `episode_list(query)` is the episodic
  search surface.

### 10.3 GC — `internal/store/gc.go`

`EpisodeRetention = 180 * 24 * time.Hour`. Under `--gc`, prune episodes where
**all** hold: older than retention; no `episode_memory` rows; no `episode_links`
row on either side. Report the count in doctor's `--gc` output. The doctrine
comment: distilled-and-cited history is provenance and stays; undistilled,
unchained residue is the compactable scaffold. `memories` are never touched by
this (memory-model §6 intact).

### 10.4 Instructions

`extended()` gains an Episodes block: what an episode is (what happened, not
what is true); record failures and dead ends — they are the episodes future
agents need most; correct by chaining, never by rewriting; distill lessons into
memories and link provenance; session roots record episodes for work their
peers report to them.

## 11. Open risks, stated

1. **`RootGateTools` staleness** until per-repo `init`/`doctor` re-run
   (attribution only).
2. **Whole-repo evidence policies over-prime**: in this repo ~10 memories watch
   everything, so a session's first Stop note lists up to the cap. Per-root
   dedup makes it once per session. If still noisy: restrict priming to
   policies with declared paths first, and only then reconsider — a constant,
   not a redesign.
3. **Link spam / hub memories**: caps and instructions are the only defense,
   and that is deliberate — a wrong edge is visible and deletable (§3). Do not
   "fix" link quality with weights; that is the A.1/§11 error.
4. **The caps are guesses** (16/8/4/6, 180d). Keep them named constants in one
   place; tune from live use; never let them harden into semantics.
5. **Empty `repos` table** for context_open-only projects mutes priming (§7,
   last bullet). Fix path is doctor, as with evidence.

## 12. Verification

**Store (Stage A)**
- Link created via either argument order → one canonical row; reading from
  either endpoint returns it.
- Self-link rejected; duplicate rejected **with the existing edge in the error**.
- Cap: 17th link on a memory fails naming the fan effect.
- Reason: empty and multi-line rejected (`ValidateLine` rules).
- `memory_delete` cascade removes edges; audit detail and response note name
  severed neighbors. Assert `PRAGMA foreign_keys` is ON in the test before
  relying on the cascade (the evidence-work lesson: consistency tests don't
  prove enforcement).
- `NeighborsOf` batched over N keys: both directions, descriptions joined,
  canonical timestamps.
- `memory_promote` leaves links on the source and returns the not-copied note.

**Migrations**
- Undo statements for project 0011/0012/0013 and global 0003 in
  `TestRestoringIndexesOnADatabaseThatAlreadyLostThem`; idempotent replay of
  the full ladder in both scopes.

**FTS isolation**
- Memories search snippet still highlights `body` (existing column-index guard
  stays green). `episodes_fts` triggers stay consistent through insert/delete.

**MCP surface**
- `memory_read` returns `links: []` (present, empty) for an unlinked memory;
  truncation reports `links_total`.
- `memory_search` output: bm25 order identical with and without links present;
  `linked` stubs capped at 4.
- `memory_link` on global scope works against global memories; cross-scope
  attempts have no expressible input shape (scope applies to both keys) — the
  doc records the rejection.

**Hooks (Stage B)**
- Fixture DB (root + claim + evidence policy + link): priming note matches
  expected text, neighbor included with reason; second Stop emits nothing
  (dedup); mail + priming compose into one StopBlock; crash-safety: marking
  happens after composition.
- Glob matcher: `*` does not cross `/`, `**` does; literal/claim overlap reuses
  the claims algebra tests.
- `internal/hooks` still does not import `internal/drift` (imports test).

**Episodes (Stage C)**
- No update path exists (nothing to test but its absence — grep the tool
  surface).
- `episode_read` of a corrected episode returns the successor chain.
- GC: old + unground + unchained pruned; old but cited kept; old but chained
  kept; count reported.

**Instructions**
- The ≤512-char priority-block test still passes (only `extended()` grew).

**End to end**
- `go build ./... && go vet ./... && go test ./...`
- Against the live MCP server in this repo: link two real memories; read shows
  the link from both endpoints; search shows `linked` stubs; unlink; delete a
  scratch memory and see the severed-links note. Record an episode, correct it
  with a second, read the first and see the successor. Hold a claim over a
  path watched by a real evidence policy, end the turn, receive the priming
  note exactly once; end another turn, receive nothing.

## 13. Where this lives

`docs/association-model.md`, sibling of `docs/memory-model.md`. Part I is the
standing justification for why edges have no types and no weights, why episodes
are immutable and prunable, and why exposure is unconditional. Anyone proposing
weights, decay, auto-links, or an episode-edit tool reads Appendix A first.

---

# Appendix A — Rejected approaches

Kept because each prevents a specific, likely regression. None is current design.

**A.1 Embeddings / vector similarity as the association mechanism.** True
semantic recall, but: a model dependency inside a static pure-Go binary
(`modernc.org/sqlite`, no cgo); and an *unauditable* stored semantic claim —
memory-model A.2's error, automated. The agent's weights already provide
general semantics at read time (§1). If someday wanted, it is a separate
suggest-only layer, never stored truth.

**A.2 Auto-created edges from co-access ("Hebbian" linking).** Memories read in
the same session correlate through workflow and FTS phrasing, not meaning —
memory-model A.1's access-frequency critique, one level up. Co-access could
someday *suggest*; only agents write.

**A.3 Typed relation vocabulary** (`supersedes`, `refutes`, `part-of`, …).
Nothing branches on the types on day one, so it is the `type` column's failure
(memory-model §8) with more enum values. The mandatory free-text reason carries
richer semantics than any enum, and the reading agent — an LLM — interprets it.

**A.4 Edge weights / strength / decay.** No portable unit (memory-model §11);
agent-supplied weights are uncalibrated guesses; use-based strengthening
re-imports A.1's rejected decay. The fan effect is handled structurally (caps),
not numerically.

**A.5 Cross-scope (project↔global) edges.** Two SQLite files; no shared
transaction, no cross-DB foreign key (see `PromoteMemory`'s ordering comment) —
every dangling-edge scenario would need bespoke repair. The weights principle
(§1) removes the motivation: general knowledge needs no nodes, so the examples
that seemed to need cross-scope links dissolve.

**A.6 Folding link proximity into search ranking.** Memory-model §12: search
order is bm25, full stop. Linked neighbors are a labeled second tier. A memory
must never rank higher because it is popular in the graph — that is PageRank
for a store whose whole doctrine is that nothing here measures importance.

**A.7 An `include_links` opt-in flag on read/search.** An association an agent
must ask for will not be asked for; the testing-effect principle (§2, §5) is
push, not pull. Cost is bounded by the surfacing caps, not by a flag.

**A.8 Editable or deletable-in-place episodes.** An episode records what
happened, including reasoning later found wrong — that wrongness is itself
history (the correction chain preserves both). An edit tool re-introduces the
inference-from-mutation ambiguity the memory model spent Stage 3 eliminating:
with immutable episodes plus `corrects` links, the record of belief over time
is explicit.

**A.9 Episodes in the global scope.** Episodes are session-bound; the global DB
has no roots or sessions to bind to. A machine-wide lesson is already semantic:
promote a memory. (Recorded so the asymmetry with `memories` reads as a
decision, not an oversight.)

**A.10 Deriving episodes from the audit log.** Raw tool-call rows are not
narratives; the audit log is GC-pruned history with its own retention; and an
auto-derived episode is an unattributed assertion nobody made. Episodes are
explicit acts, like every other agent-authored fact in stigmergy.
