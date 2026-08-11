# Contributing

stigmergy is small, and most of it is decisions rather than mechanism. The code is
easy to change; the hard part is knowing which changes quietly break a guarantee
somebody else is relying on. This document is about that part.

## Getting set up

Linux, Go 1.26 or newer. There is no cgo, no code generation, and nothing to install
before building.

```sh
make build     # -> bin/stigmergy
make test      # go test ./... -count=1
make check     # test + gofmt + go vet — run this before you push
```

Some tests skip themselves when a tool is missing rather than failing: `git` for
anything touching a repository, `bwrap` for the sandbox, `codex` for the explorer. A
green run on a machine without them has covered less than you think, and the skip
lines say which.

The text rules have a fuzz target, with its findings committed as seeds:

```sh
go test ./internal/store/ -fuzz FuzzTextRules
```

To try your build against a real agent, install it and run `stigmergy init` in a
scratch repository. Read [Shipping a schema change](#shipping-a-schema-change) first
if your branch adds a migration — installing one carelessly stops every agent on your
machine from editing anything.

## Where things live

[docs/architecture.md §6](docs/architecture.md#6-package-map) has a line on every
package. The two structural rules worth repeating: `internal/cli` is cobra wiring
only, with the logic in the packages it calls, and `internal/claims` never grows a
database dependency — the overlap rule the whole enforcement layer rests on has to be
testable with no SQLite in sight.

## Before you change anything

Read [§5 of architecture.md](docs/architecture.md#5-invariants). Those are the
properties the tests are protecting, each with the failure it prevents, and most of
them were written after something went wrong. A change that violates one is not
necessarily wrong — but it needs to argue with the reasoning rather than pass it by.

Three that catch people most often:

- **A claim is only alive while its root is** — expiry is folded into the query
  predicate, not maintained by a background sweep. Do not add a `status` column that
  something has to remember to update.
- **A claim that has stopped binding never comes back** without re-running the same
  overlap check that grants one. Three separate bugs were this shape.
- **Failure directions are chosen per failure.** The claim guard allows on an
  unparseable payload and denies on an unreadable database. Both are deliberate, and
  they point opposite ways on purpose.

The hosts differ in what they can enforce, and stigmergy never papers over it: a
message that tells a Codex agent its edit was "blocked" is a lie, because on Codex the
edit has already landed. If you touch agent-facing text, keep the per-host wording
per-host — `internal/hosts` renders it from one declaration for exactly this reason.

## Shipping a schema change

A migration is the one change that can stop everybody's work, including yours, on
every repository on the machine. The hook that guards edits refuses to run against a
database whose schema does not match its own binary — deliberately, since a hook that
migrated the database on the fly would do it from inside another agent's edit. So the
moment a new migration is applied, every older binary fails closed.

The order that works:

1. Write the migration.
2. Build and install the binary, so what is on `PATH` expects the new version.
3. `stigmergy doctor` — a read-write open, which applies the migration.
4. `stigmergy doctor --all` for every other project on the machine, and tell anyone
   working in them.

Between 2 and 3 every edit in every adopted repository is blocked, so do them back to
back. `stigmergy doctor` is also the diagnosis when someone hits this: it prints both
versions and says plainly that edits are blocked.

Prefer a new table, a new column, or a convention that needs no schema change at all,
over rebuilding an existing table. SQLite can only change a constraint by rebuilding,
a rebuild silently takes the table's indexes with it, and that has cost us the roots
indexes once already. The migration replay test will also make you state how to undo
your migration — add that line when you write it, not when the test goes red.

A new MCP tool is a second, separate hazard the steps above do not cover: an already
running `stigmergy mcp` process keeps serving whatever tool set it started with, even
after you rebuild the binary and `doctor` migrates the database out from under it — it
does not re-read `server.go`'s registration on its own. New tools only become callable
once the host opens a fresh connection (restart the session, or however your host
reconnects its MCP servers); that is not something a hook or a script can trigger for
you. The reads and writes an old process already knew about keep working fine across
the migration — the mismatch check on `internal/hooks/project.go`'s hook path is what
fails closed on a version gap, not the MCP server's own tool calls — so this is a "new
capability isn't there yet" surprise, not a broken one, but it is worth expecting
before you go looking for why `memory_link` isn't in the list.

## Probing a host

Every host's hook payloads are documented poorly, not at all, or wrongly. Do not
reason about them; capture them.

```sh
stigmergy hook dump --tag whatever    # wire it up as a hook; appends raw payloads
```

For the MCP side, where there is no hook to point anywhere, wrap the server in a
two-line script that `tee`s its input and name that as the server command in a
throwaway config. [docs/hosts.md](docs/hosts.md#probing-the-hosts) has both recipes.
That method is how the agent identity fields were found — none of them are documented
anywhere, and one of them the vendor's own docs deny exists.

Record what you learn in `docs/VERIFY.md`, which tracks every assumption stigmergy
makes about a host, how it was checked, and what is still open. That file is a local
working note rather than part of the repository — it is gitignored, alongside
`docs/TODO.md` and `docs/plan.md`, so a fresh clone will not have one and starting it
is not a mistake.
"Confirmed" there means observed against the real thing; "harness-confirmed" means
stigmergy's own side behaves correctly against a synthetic payload, which is a weaker claim and must
stay distinguishable from the first.

## Tests

Prefer a test that states the property in its name and fails with the reason, not the
symptom. `TestPeersInOneSessionBlockEachOther` tells the next person what broke;
`TestGuard3` does not.

Where a rule applies to many entry points, test it as a matrix rather than a list —
`internal/store/sanitation_test.go` runs every hostile payload against every field
that accepts agent-written text, so adding a field without validation fails
immediately instead of going untested because nobody wrote its case.

Tests that assert a *current* limitation should say so in the name (there is one
ending `_KnownGap`). If you close the gap, delete the test and say why in the commit
— do not weaken it into passing.

## Documentation

The files in `docs/` are for people: what stigmergy does, how to drive it, and why it
works the way it does. They are not a place to paste code comments. If an explanation
only makes sense with the source open beside it, it belongs in the source.

- **README** — what this is and how to start.
- **usage.md** — driving it: the daily loop, the CLI, troubleshooting.
- **hosts.md** — what `init` writes for each host, and what each host can enforce.
- **mcp-tools.md** — the tool reference agents and integrators need.
- **architecture.md** — the design and the reasons; this is the one written for
  contributors, and where naming internals is appropriate.
- **operations.md**, **memory-model.md**, **association-model.md**,
  **deliberation.md** — running it, what a memory is for, how memories reach each
  other, and the deliberation subsystem.
- **sync.md** / **sync-model.md** — carrying memories between one developer's own
  machines, and the design behind it. The model document's §0 is a table of every
  decision with the alternative it rejected; read it before proposing a transport, a
  merge rule, or anything that would put a column on `memories`.

If you change behaviour, change the document that describes it in the same commit.
The failure mode here is specific and has bitten this project: three copies of the
same advice drifted apart until two of them contradicted each other, and an agent that
read the wrong one stopped checking claims. That is why agent-facing instructions are
rendered from one declaration in `internal/hosts` rather than written per host, and
why `init` no longer writes anything into `CLAUDE.md`.

## Commits

One change per commit, with a subject that says what changed and a body that says
what it fixes or why it is worth doing. `git log` in this repository is a reasonable
model: the commits explaining the failure they prevent are the ones people go back and
read.

## Scope, and the one thing to keep in mind

**stigmergy is cooperative. It is not a security boundary.** It coordinates agents
that participate; a shell command, a stray script, or an agent with no hooks installed
can write a claimed file and nothing will stop it. Claims prevent accidents between
cooperating agents, and that is the whole promise.

Everything follows from taking that seriously — it is why identity can be
model-supplied, why enforcement lives in advisory hooks, and why the complexity budget
goes on being useful rather than airtight. A change that starts treating stigmergy as
a security mechanism is a change in the project's scope, not a bug fix; make that case
explicitly, or leave it.

## Licence

LGPL-3.0-or-later. By contributing you agree your work is licensed under it.
