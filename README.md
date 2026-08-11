# stigmergy

Shared memory and coordination for multiple AI agents working on one project — a
single git repository, or several that belong together.

Claude Code, Codex, Antigravity and opencode each keep their own memory, per host and
per session. Run two of them on the same project and they diverge: they learn
different things, forget them separately, and overwrite each other's files without
ever knowing the other was there. stigmergy gives them one place to remember and one
way to stay out of each other's way.

*Stigmergy* is coordination through traces left in a shared environment — how ants
route around each other without a supervisor. That is the whole design: no daemon, no
scheduler, no central agent. Just a database the agents write to and read from, and
hooks that make them look before they leap.

> **stigmergy is cooperative, not a security boundary.** It coordinates agents that
> participate. A shell command, a stray script, or an agent without the hooks
> installed can still write a claimed file. Claims prevent accidents between
> cooperating agents; they defend against nothing.

## What an agent gets

**Memories** — durable notes in two scopes: *project* (shared by every worktree,
every repository in the project, and every agent on them) and *global* (machine-wide). Full-text
search, and compare-and-swap writes, so two agents editing one memory get a visible
conflict instead of a silent overwrite. Both scopes live on this machine and travel
with neither `git push` nor `git clone`; [`stigmergy sync`](docs/sync.md) carries them
to your other machines, deliberately and by its own command.

**Claims** — an agent reserves a file or a directory subtree before working on it,
with a reason and a TTL. Another agent's edit inside that scope is then refused
outright (Claude Code, Antigravity, opencode) or warned about and halted after the
fact (Codex, whose hooks cannot deny a tool call — the hosts differ, and stigmergy
never pretends otherwise). Claims expire on their own, and a crashed agent's claims
die with it.

**Mailbox** — root-to-root messages, so a blocked agent can negotiate with the one
holding the claim instead of waiting or barging through. Mail is *delivered*, not left
to be found: on Claude Code and Antigravity an agent cannot end its turn while a
message it has never been shown is waiting. Codex and opencode have no hook that can
hold a turn open, so there it is put in front of the agent and can still be ignored —
which stigmergy tells the agent rather than implying a guarantee it cannot keep. And
it is addressed to whoever is actually there — claim
conflicts name the owner and say whether it is still alive, mail to a root that has
died is refused, and the refusal names the agents that are.

**Audit log** — who did what, kept 90 days.

## Install

Linux, Go 1.26+:

```sh
make build && sudo install -m755 bin/stigmergy /usr/local/bin/
```

It must be on `PATH` — the host configs invoke it by name, and a binary they cannot
find fails invisibly.

## Quickstart

```sh
cd your-repo
stigmergy init      # configure every supported host for this project
stigmergy doctor    # check it took
```

Then restart your agent sessions. From there it is the agents' job, and no file of
yours is involved: the rules travel with the MCP server, which every host shows its
model, and the session hooks tell each agent who it is and hand it its mail. `init`
writes configuration, not instructions — your `CLAUDE.md` and `AGENTS.md` are left
alone.

If you use Codex, trust the project when it prompts you — Codex loads project hooks
only for trusted projects, and until then none of this takes effect there.

## Several repositories, one project

Most projects are one repository and need none of this. When several are one
piece of work — a client and its service, with separate remotes, whose changes
cross between them — group them:

```
stigmergy project create --add ./naviamp --add ./naviamp-sidecar --name naviamp
```

They share one database, so memories, the roster and the mailbox reach across all
of them, and a claim in one blocks an agent editing it from another. Scopes are
spelled `repo:path`; a bare path means the repository you are in.

The repositories need share no parent directory and may sit on different disks.

```
stigmergy project list          every project on this machine
stigmergy project add <path>    add a repository to the project you are in
stigmergy project remove <r>    drop one; its claims are released
```

Start agent sessions **inside one of the member repositories**, never in a
directory above them — a parent is not a git repository and carries none of the
host configuration, so nothing there is coordinated. Choosing one member costs no
reach, because an edit is governed by the repository holding the file rather than
by where the session began:
[where to start an agent session](docs/usage.md#where-to-start-an-agent-session).

## After upgrading stigmergy

```
stigmergy doctor --all
```

A new binary carrying a schema migration blocks edits in every adopted project
until each database is upgraded — the claim guard fails closed when it cannot
verify claims. `--all` upgrades every project this machine knows about in one
pass. Run it immediately after installing.

## Documentation

| | |
|---|---|
| [docs/usage.md](docs/usage.md) | install, enable, the daily loop, full CLI reference, troubleshooting |
| [docs/architecture.md](docs/architecture.md) | the design and the reasons for it: schema, invariants, what a change must not break |
| [docs/mcp-tools.md](docs/mcp-tools.md) | reference for all 31 MCP tools: parameters, returns, error codes |
| [docs/hosts.md](docs/hosts.md) | what `init` writes, the hook contracts, and what each host can and cannot enforce |
| [docs/operations.md](docs/operations.md) | TTLs, housekeeping, backup, removal, failure modes |
| [docs/memory-model.md](docs/memory-model.md) | what a memory is for, and the change-evidence design: what it can and cannot tell you |
| [docs/association-model.md](docs/association-model.md) | links, priming and episodes: how memories reach each other, and why history is kept apart from state |
| [docs/sync.md](docs/sync.md) | `stigmergy sync` — carrying your memories between your own machines |
| [docs/sync-model.md](docs/sync-model.md) | the sync design: what travels and what must not, the merge rules, and every alternative rejected |
| [docs/deliberation.md](docs/deliberation.md) | `stigmergy deliberate` — the adversarial specification pipeline, a separate subsystem |
| [CONTRIBUTING.md](CONTRIBUTING.md) | building, testing, the invariants a change must not break, and how to ship a schema change safely |


## Licence

LGPL-3.0-or-later. See [COPYING.LESSER](COPYING.LESSER) and [COPYING](COPYING).
