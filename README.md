# stigmergy

Shared memory and coordination for multiple AI agents working in one git repository.

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

**Memories** — durable notes in two scopes: *project* (in the repo's git directory,
shared by every worktree and every agent on it) and *global* (machine-wide). Full-text
search, and compare-and-swap writes, so two agents editing one memory get a visible
conflict instead of a silent overwrite.

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

## Documentation

| | |
|---|---|
| [docs/usage.md](docs/usage.md) | install, enable, the daily loop, full CLI reference, troubleshooting |
| [docs/architecture.md](docs/architecture.md) | the design and the reasons for it: schema, invariants, what a change must not break |
| [docs/mcp-tools.md](docs/mcp-tools.md) | reference for all 21 MCP tools: parameters, returns, error codes |
| [docs/hosts.md](docs/hosts.md) | what `init` writes, the hook contracts, and what each host can and cannot enforce |
| [docs/operations.md](docs/operations.md) | TTLs, housekeeping, backup, removal, failure modes |

## Status

v1. All milestones implemented and the test suite is green.

Every host's hook payloads are documented poorly, not at all, or wrongly, so some of
what stigmergy assumes about them is still assumed rather than observed. The one that
matters: whether a subagent is distinguishable from a root. It is on Claude Code and
on opencode (which records a parent on the session); it is not on Codex or
Antigravity, where the subagent rule is advisory and says so. Every such assumption is
written down where it is relied on, and each fails in the safe direction —
`stigmergy hook dump` is the instrument for settling them.

opencode is the sharpest example of why that matters. Its published documentation omits
the field the claim guard depends on, and describes a hook that does not exist; what
stigmergy does there was read out of the binary and then checked against a live
session.

## Licence

LGPL-3.0-or-later. See [COPYING.LESSER](COPYING.LESSER) and [COPYING](COPYING).
