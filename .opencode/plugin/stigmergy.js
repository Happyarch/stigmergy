// stigmergy — shared memory and coordination between agents in this repo.
//
// Written by `stigmergy init --host opencode`. Do not edit: it is regenerated,
// and it holds no policy worth editing. Every decision is made by the stigmergy
// binary; this file carries payloads to it and does what it is told.
//
// Remove it with `stigmergy init --remove --host opencode`.

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
// newPartID builds an id opencode will accept for a message part.
function newPartID() {
  const stamp = Date.now().toString(36)
  const tail = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2)
  return "prt_" + (stamp + tail).slice(0, 26).padEnd(26, "0")
}

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

export default async ({ client, directory, worktree }) => {
  // Whether a session has a parent is the entire subagent gate on this host, and
  // it is the one fact the tool payload does not carry. It cannot change for a
  // given session, so it is asked once and remembered — otherwise this is an
  // HTTP round-trip on every tool call.
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
      const res = await client.session.get({ path: { id: sessionID } })
      const info = res?.data ?? res
      answer = { parentID: info?.parentID ?? "", parentUnknown: false }
    } catch {
      answer = { parentUnknown: true }
    }
    parents.set(sessionID, answer)
    return answer
  }

  async function base(input) {
    return {
      tool: input?.tool ?? "",
      sessionID: input?.sessionID ?? "",
      callID: input?.callID ?? "",
      directory,
      worktree,
      ...(await parentOf(input?.sessionID)),
    }
  }

  return {
    // tool.execute.before is awaited before the tool runs, and a throw from it is
    // an unrecoverable defect: the tool never executes and the message reaches
    // the model verbatim. That is what lets this host block an edit and say who
    // holds the claim, rather than only failing.
    "tool.execute.before": async (input, output) => {
      const tool = input?.tool ?? ""
      const isEdit = EDIT_TOOLS.includes(tool)
      const isStigmergyTool = tool.startsWith(MCP_PREFIX)
      if (!isEdit && !isStigmergyTool) return

      const payload = { ...(await base(input)), args: output?.args ?? {} }
      const hook = isEdit ? "opencode-claim-guard" : "opencode-root-gate"
      const reply = call(hook, payload)
      if (reply?.deny) throw new Error(reply.reason || "stigmergy: refused")
    },

    // chat.message fires once per real user prompt — opencode's nearest thing to
    // a user-prompt-submit hook — and not for the internal agents. It is where
    // registration details and mail arrive.
    //
    // Mail is delivered here and nowhere else, because opencode has no end-of-turn
    // hook that can hold a turn open. So unlike Claude Code, an agent here can
    // finish while ignoring its mail, and stigmergy says so rather than implying
    // a guarantee it cannot keep.
    "chat.message": async (input, output) => {
      const reply = call("opencode-context", await base(input))
      const text = reply?.context
      if (!text) return
      output.parts.push({
        // opencode validates part ids: they must start with "prt", and it
        // rejects the whole message if they do not — the text is dropped and an
        // "invalid user part before save" is all that is left. Nothing documents
        // this; a live session refusing to save is what found it. Real ids are
        // prt_ plus 26 characters, ordered by time, so this matches that shape:
        // the clock keeps parts in order and the random tail keeps two in the
        // same millisecond apart.
        id: newPartID(),
        messageID: output.message?.id,
        sessionID: output.message?.sessionID ?? input?.sessionID,
        type: "text",
        text,
        synthetic: true,
      })
    },
  }
}
