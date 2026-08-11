package hostcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/happyarch/stigmergy/internal/hosts"
)

// opencode is configured differently from the other three hosts, because it is
// the only one with no way to call a program on a hook.
//
// Claude Code, Codex and Antigravity all read a JSON or TOML file that names a
// command; stigmergy writes the file and the host runs the binary. opencode has
// a plugin API instead: TypeScript or JavaScript, loaded into opencode's own
// runtime. So stigmergy ships a plugin, and the plugin shells out.
//
// That makes this the one host where stigmergy ships code rather than configuration,
// which deserves suspicion. Two things keep it small. The plugin holds no
// policy: every decision is made by `stigmergy hook opencode-*` in Go, the same
// as everywhere else, and the plugin only carries payloads to it and acts on the
// answer. And it has no dependencies — no package.json, no npm install, no
// import from @opencode-ai/plugin — because a plugin is just a function that
// returns an object, and needing a toolchain to install stigmergy would be a
// worse cost than the file itself.
//
// The one thing the plugin decides is what only it can: whether a session is a
// subagent. opencode's tool payload names the session but not its parent, and
// the answer is an HTTP call to the opencode server that the plugin already
// holds a client for.

// OpenCodePluginDir is the directory stigmergy owns for opencode.
//
// `.opencode/plugin/` is auto-discovered: any .js or .ts file in it loads with
// no config entry. (`.opencode/plugins/` also works — opencode accepts both —
// but one of them has to be chosen and this is the one its own reference doc
// uses.)
func OpenCodePluginDir(worktree string) string {
	return filepath.Join(worktree, ".opencode", "plugin")
}

// OpenCodePaths are the files init touches for opencode.
func OpenCodePaths(worktree string) (config, plugin string) {
	return filepath.Join(worktree, "opencode.json"),
		filepath.Join(OpenCodePluginDir(worktree), "stigmergy.js")
}

// InstallOpenCode registers the MCP server and installs the hook plugin.
func InstallOpenCode(worktree string) error {
	configPath, pluginPath := OpenCodePaths(worktree)

	// opencode.json is the user's file and may hold their model, agents and
	// permissions, so this merges into it rather than owning it — the same
	// treatment .mcp.json gets for Claude.
	cfg, err := ReadJSON(configPath)
	if err != nil {
		return err
	}
	if _, ok := cfg["$schema"]; !ok {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}
	cfg.mapAt("mcp")["stigmergy"] = map[string]any{
		"type":    "local",
		"command": []any{Binary, "mcp"},
		"enabled": true,
	}
	if err := WriteJSON(configPath, cfg); err != nil {
		return err
	}

	if err := os.MkdirAll(OpenCodePluginDir(worktree), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(pluginPath, []byte(openCodePluginJS()), 0o644)
}

// RemoveOpenCode takes stigmergy back out, leaving the user's config intact.
func RemoveOpenCode(worktree string) error {
	configPath, pluginPath := OpenCodePaths(worktree)

	if cfg, err := ReadJSON(configPath); err == nil {
		if servers, ok := cfg["mcp"].(map[string]any); ok {
			delete(servers, "stigmergy")
			if len(servers) == 0 {
				delete(cfg, "mcp")
			}
		}
		// A file holding nothing but the schema line is one the installer created. Anything
		// else is the user's and stays.
		if len(cfg) == 0 || (len(cfg) == 1 && cfg["$schema"] != nil) {
			_ = removeIfExists(configPath)
		} else if err := WriteJSON(configPath, cfg); err != nil {
			return err
		}
	}

	if err := removeIfExists(pluginPath); err != nil {
		return err
	}
	// Take the directories back only if removal left them empty: a user with plugins
	// of their own keeps them.
	_ = os.Remove(OpenCodePluginDir(worktree))
	_ = os.Remove(filepath.Join(worktree, ".opencode"))
	return nil
}

// jsString renders a Go string as a JavaScript literal. JSON string syntax is a
// subset of JavaScript's, so the encoder is the right quoter.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// Unreachable for a string, and a panic here would be a build-time
		// certainty rather than a runtime surprise.
		return `""`
	}
	return string(b)
}

func quoteJSList(items []string) string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, jsString(s))
	}
	return strings.Join(out, ", ")
}

// openCodePluginJS is the plugin written into .opencode/plugin/stigmergy.js.
//
// Deliberately dependency-free and deliberately dumb: it decides nothing. Note
// that the hooks it uses were chosen against a live session, not from the docs —
// experimental.chat.system.transform looks like the right home for the
// registration text and is not, because it also fires for opencode's internal
// title, summary and compaction agents with nothing in the payload to tell them
// apart.
func openCodePluginJS() string {
	editTools := "[" + quoteJSList(hosts.OpenCodeEditTools) + "]"
	return `// stigmergy — shared memory and coordination between agents in this repo.
//
// Written by ` + "`stigmergy init --host opencode`" + `. Do not edit: it is regenerated,
// and it holds no policy worth editing. Every decision is made by the stigmergy
// binary; this file carries payloads to it and does what it is told.
//
// Remove it with ` + "`stigmergy init --remove --host opencode`" + `.

import { spawnSync } from "node:child_process"

const BINARY = ` + jsString(Binary) + `
const EDIT_TOOLS = ` + editTools + `
const MCP_PREFIX = ` + jsString(hosts.OpenCodeMCPPrefix) + `

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
`
}
