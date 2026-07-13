# stigmergy

Shared memory and coordination for multiple AI agents working in one git repository.

Claude Code and Codex each keep their own memory, per host and per session. Run two
of them on the same project and they diverge: they learn different things, forget
them separately, and overwrite each other's files without ever knowing the other was
there. stigmergy gives them one place to remember and one way to stay out of each
other's way.

*Stigmergy* is coordination through traces left in a shared environment — how ants
route around each other without a supervisor. That is the whole design: no daemon, no
scheduler, no central agent. Just a database the agents write to and read from, and
hooks that make them look before they leap.

> **stigmergy is cooperative, not a security boundary.** It coordinates agents that
> participate. A shell command, a stray script, or an agent without the hooks
> installed can still write a claimed file. Claims prevent accidents between
> cooperating agents; they defend against nothing.

## What an agent gets

**Memories** — durable notes in two scopes: *project* (in the repo's git directory,
shared by every worktree and every agent on it) and *global* (machine-wide). Full-text
search, and compare-and-swap writes, so two agents editing one memory get a visible
conflict instead of a silent overwrite.

**Claims** — an agent reserves a file or a directory subtree before working on it,
with a reason and a TTL. Another agent's edit inside that scope is then refused
(Claude Code) or warned about and halted after the fact (Codex — the hosts differ,
and stigmergy never pretends otherwise). Claims expire on their own, and a crashed
agent's claims die with it.

**Mailbox** — root-to-root messages, so a blocked agent can negotiate with the one
holding the claim instead of waiting or barging through.

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
stigmergy init      # configure Claude Code and Codex for this project
stigmergy doctor    # check it took
```

Then restart your agent sessions. From there it is the agents' job: `init` writes a
block into `CLAUDE.md` / `AGENTS.md` telling them to register at session start, search
memory before working, and claim before editing.

If you use Codex, trust the project when it prompts you — Codex loads project hooks
only for trusted projects, and until then none of this takes effect there.

## Documentation

| | |
|---|---|
| [docs/usage.md](docs/usage.md) | install, enable, the daily loop, full CLI reference, troubleshooting |
| [docs/architecture.md](docs/architecture.md) | the design and the reasons for it: schema, invariants, what a change must not break |
| [docs/mcp-tools.md](docs/mcp-tools.md) | reference for all 21 MCP tools: parameters, returns, error codes |
| [docs/hosts.md](docs/hosts.md) | what `init` writes, the hook contracts, and the Claude/Codex asymmetry |
| [docs/operations.md](docs/operations.md) | TTLs, housekeeping, backup, removal, failure modes |

## Status

v1. All milestones implemented and the test suite is green.

Both hosts' hook payloads are documented poorly or not at all, so some of what
stigmergy assumes about them is still assumed rather than observed. The one that
matters: whether a Claude subagent is distinguishable from a root in the hook
payload. If it is not, the subagent gate is advisory. Every such assumption is
written down where it is relied on, and each fails in the safe direction —
`stigmergy hook dump` is the instrument for settling them.

## Licence

LGPL-3.0-or-later. See [COPYING.LESSER](COPYING.LESSER) and [COPYING](COPYING).
