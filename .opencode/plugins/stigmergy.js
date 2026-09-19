// stigmergy — shared memory and coordination between agents in this repo.
//
// Written by `stigmergy init --host opencode`. Do not edit: it is regenerated,
// and it holds no policy worth editing. Every decision is made by the stigmergy
// binary; this file carries payloads to it and does what it is told.
//
// Remove it with `stigmergy init --remove --host opencode`.
//
// A V2 plugin (opencode 2.x): a plain object with an id and a setup function
// that registers hooks through the context. It imports nothing but
// node:child_process — the plugin package's define helper is an identity
// function, so there is no dependency to install and no toolchain needed.

import { spawnSync } from "node:child_process"

const BINARY = "stigmergy"
const EDIT_TOOLS = ["edit", "write", "apply_patch"]
const MCP_PREFIX = "stigmergy_"

// call runs a stigmergy hook and returns its JSON reply, or null.
//
// Every failure is a null: a missing binary, a crash, a timeout, unparseable
// output. stigmergy is cooperative tooling, and a coordination layer that breaks
// the agent when it is itself broken is worse than one that is absent. The one
// thing this must never do is throw, because a throw here blocks the tool call.
function call(hook, payload) {
  try {
    const r = spawnSync(BINARY, ["hook", hook], {
      input: JSON.stringify(payload),
      encoding: "utf8",
      timeout: 5000,
    })
    if (r.error || r.status !== 0 || !r.stdout) return null
    return JSON.parse(r.stdout)
  } catch {
    return null
  }
}

export default {
  id: "stigmergy",
  async setup(ctx) {
    // Deliberation workers stand the plugin down. They run under bwrap with a
    // private server, their writes evaporate with the overlay, and an ephemeral
    // session holds no claims of its own — enforcing the real tree's claims
    // against it would stall the worker on files it does not hold. The driver
    // sets this variable; nothing else should.
    if (process.env.STIGMERGY_UNGUARDED) return

    // The plugin instance's location, not any one session's: the same closure
    // the V1 plugin took as arguments. directory is where the agent works;
    // the project's canonical root is what the store keys on.
    const directory = ctx.location.directory
    const worktree = ctx.location.project.canonical

    // Whether a session has a parent is the entire subagent gate on this host, and
    // it is the one fact the tool payload does not carry. It cannot change for a
    // given session, so it is asked once and remembered — otherwise this is a
    // lookup on every tool call.
    //
    // "Could not ask" is reported as such rather than guessed at. Treating an
    // unknown session as a subagent would block a real root from claiming or
    // writing memory, which breaks stigmergy outright; the other way costs a
    // subagent that should not have registered.
    const parents = new Map()
    async function parentOf(sessionID) {
      if (!sessionID) return { parentUnknown: true }
      if (parents.has(sessionID)) return parents.get(sessionID)
      let answer
      try {
        const res = await ctx.session.get({ sessionID })
        const info = res?.data ?? res
        answer = { parentID: info?.parentID ?? "", parentUnknown: false }
      } catch {
        answer = { parentUnknown: true }
      }
      parents.set(sessionID, answer)
      return answer
    }

    // execute.before runs before the tool executes, and a throw from it stops
    // the call outright: the tool never runs and the message reaches the model
    // verbatim. That is what lets this host block an edit and say who holds
    // the claim, rather than only failing. A permission-evaluate hook would
    // not do: it reviews allow/ask decisions after the rules, and an explicit
    // deny never reaches it.
    await ctx.tool.hook("execute.before", async (event) => {
      const tool = event.tool ?? ""
      const isEdit = EDIT_TOOLS.includes(tool)
      const isStigmergyTool = typeof tool === "string" && tool.startsWith(MCP_PREFIX)
      if (!isEdit && !isStigmergyTool) return

      // The Go side wants an object for args. A non-object input has no paths
      // in it, so it arrives empty rather than as a decode error.
      const input = event.input
      const args = input && typeof input === "object" && !Array.isArray(input) ? input : {}
      const payload = {
        tool,
        sessionID: event.sessionID ?? "",
        callID: event.id ?? "",
        directory,
        worktree,
        ...(await parentOf(event.sessionID)),
        args,
      }
      const hook = isEdit ? "opencode-claim-guard" : "opencode-root-gate"
      const reply = call(hook, payload)
      if (reply?.deny) throw new Error(reply.reason || "stigmergy: refused")
    })

    // prompt runs once per admitted user prompt, before the model is called —
    // the nearest thing to a user-prompt-submit hook — and not for the
    // internal agents, which never submit prompts. Registration details and
    // mail arrive by extending the prompt text in place.
    //
    // Mail is delivered here and nowhere else, because nothing in V2 can hold
    // a turn open at its end: event subscriptions observe but cannot block. So
    // unlike Claude Code, an agent here can finish while ignoring its mail, and
    // stigmergy says so rather than implying a guarantee it cannot keep.
    await ctx.session.hook("prompt", async (event) => {
      const reply = call("opencode-context", {
        tool: "",
        sessionID: event.sessionID ?? "",
        callID: "",
        directory,
        worktree,
        ...(await parentOf(event.sessionID)),
        args: {},
      })
      const text = reply?.context
      if (!text) return
      event.prompt.text = event.prompt.text ? event.prompt.text + "\n\n" + text : text
    })
  },
}
