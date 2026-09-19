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
// a plugin API instead: JavaScript, loaded into opencode's own runtime. So
// stigmergy ships a plugin, and the plugin shells out.
//
// That makes this the one host where stigmergy ships code rather than configuration,
// which deserves suspicion. Two things keep it small. The plugin holds no
// policy: every decision is made by `stigmergy hook opencode-*` in Go, the same
// as everywhere else, and the plugin only carries payloads to it and acts on the
// answer. And it has no dependencies — no package.json, no install step, no
// import of the plugin package, whose define helper is an identity function —
// because a plugin is a plain object with an id and a setup function, and
// needing a toolchain to install stigmergy would be a worse cost than the file
// itself.
//
// The one thing the plugin decides is what only it can: whether a session is a
// subagent. opencode's tool payload names the session but not its parent, and
// the answer is a session lookup the plugin already holds a context for.

// OpenCodePluginDir is the directory stigmergy owns for opencode.
//
// `.opencode/plugins/` is auto-discovered: any .js or .ts file in it loads with
// no config entry. V2 discovers both `.opencode/plugin/` and
// `.opencode/plugins/`; the plural is the documented V2 location, so that is
// where new installs go and where the V1 file is migrated from.
func OpenCodePluginDir(worktree string) string {
	return filepath.Join(worktree, ".opencode", "plugins")
}

// OpenCodeLegacyPluginDir is where the V1 plugin lived. Install migrates the
// file out of it, remove takes it back out of both, and doctor accepts either.
func OpenCodeLegacyPluginDir(worktree string) string {
	return filepath.Join(worktree, ".opencode", "plugin")
}

// OpenCodeLegacyPluginPath is stigmergy's own file in the V1 directory — the
// only thing in there install or remove will touch.
func OpenCodeLegacyPluginPath(worktree string) string {
	return filepath.Join(OpenCodeLegacyPluginDir(worktree), "stigmergy.js")
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
	// The stigmergy V1 entry migrates in place. V1 kept the server at mcp.stigmergy
	// with an `enabled` flag; V2 nests servers under mcp.servers and inverts
	// the flag to `disabled`. Anyone else's V1 entries stay where they are:
	// V2 normalizes supported V1 fields in memory, so moving them would be
	// churn for no behavior change. A deliberately disabled server stays
	// disabled across a reinstall.
	disabled := false
	if mcp, ok := cfg["mcp"].(map[string]any); ok {
		if old, ok := mcp["stigmergy"].(map[string]any); ok {
			if enabled, ok := old["enabled"].(bool); ok && !enabled {
				disabled = true
			}
			delete(mcp, "stigmergy")
		}
		if servers, ok := mcp["servers"].(map[string]any); ok {
			if cur, ok := servers["stigmergy"].(map[string]any); ok {
				if d, ok := cur["disabled"].(bool); ok {
					disabled = d
				}
			}
		}
	}
	cfg.mapAt("mcp").mapAt("servers")["stigmergy"] = map[string]any{
		"type":     "local",
		"command":  []any{Binary, "mcp"},
		"disabled": disabled,
	}
	if err := WriteJSON(configPath, cfg); err != nil {
		return err
	}

	if err := os.MkdirAll(OpenCodePluginDir(worktree), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(pluginPath, []byte(openCodePluginJS()), 0o644); err != nil {
		return err
	}
	// Take back the V1 plugin file. V2 would still discover it beside the new
	// one, and a V1 plugin does not run on V2 — it would only fail at load.
	// Anything else in that directory is the user's and stays.
	_ = removeIfExists(OpenCodeLegacyPluginPath(worktree))
	_ = os.Remove(OpenCodeLegacyPluginDir(worktree))
	return nil
}

// RemoveOpenCode takes stigmergy back out, leaving the user's config intact.
func RemoveOpenCode(worktree string) error {
	configPath, pluginPath := OpenCodePaths(worktree)

	if cfg, err := ReadJSON(configPath); err == nil {
		if mcp, ok := cfg["mcp"].(map[string]any); ok {
			// Both shapes: a V1 entry never migrated, and the V2 one install
			// writes.
			delete(mcp, "stigmergy")
			if servers, ok := mcp["servers"].(map[string]any); ok {
				delete(servers, "stigmergy")
				if len(servers) == 0 {
					delete(mcp, "servers")
				}
			}
			if len(mcp) == 0 {
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
	_ = removeIfExists(OpenCodeLegacyPluginPath(worktree))
	// Take the directories back only if removal left them empty: a user with plugins
	// of their own keeps them.
	_ = os.Remove(OpenCodePluginDir(worktree))
	_ = os.Remove(OpenCodeLegacyPluginDir(worktree))
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

// openCodePluginJS is the plugin written into .opencode/plugins/stigmergy.js.
//
// Deliberately dependency-free and deliberately dumb: it decides nothing. Note
// that the prompt hook was chosen against a live session, not from the docs —
// the system-transform hook looks like the right home for the registration text
// and is not, because it also fires for opencode's internal title, summary and
// compaction agents with nothing in the payload to tell them apart.
func openCodePluginJS() string {
	editTools := "[" + quoteJSList(hosts.OpenCodeEditTools) + "]"
	return `// stigmergy — shared memory and coordination between agents in this repo.
//
// Written by ` + "`stigmergy init --host opencode`" + `. Do not edit: it is regenerated,
// and it holds no policy worth editing. Every decision is made by the stigmergy
// binary; this file carries payloads to it and does what it is told.
//
// Remove it with ` + "`stigmergy init --remove --host opencode`" + `.
//
// A V2 plugin (opencode 2.x): a plain object with an id and a setup function
// that registers hooks through the context. It imports nothing but
// node:child_process — the plugin package's define helper is an identity
// function, so there is no dependency to install and no toolchain needed.

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
`
}
