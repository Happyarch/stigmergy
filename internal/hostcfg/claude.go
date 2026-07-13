package hostcfg

import (
	"path/filepath"
	"strings"
)

// CommandPrefix identifies the hook entries stigmergy owns. Everything the
// installer writes into a shared hooks array starts with it, so removal can be
// exact: we take back what we put in, and nothing else.
const CommandPrefix = "stigmergy hook"

// Binary is the command written into host configs. It is deliberately bare and
// PATH-resolved rather than an absolute path: the config lands in the user's
// repository, where an absolute path from one machine is wrong on every other.
const Binary = "stigmergy"

// RootGateTools is the matcher for tools only a root may call. Subagents that
// try are denied: memories, claims and mail are the root's to write, and a
// swarm of subagents writing them concurrently produces exactly the incoherence
// stigmergy exists to prevent.
const RootGateTools = `mcp__stigmergy__(root_register|root_heartbeat|root_deregister|memory_write|memory_promote|memory_delete|claim_acquire|claim_renew|claim_release|mailbox_.*)`

// EditTools is the matcher for the tools that write files.
const EditTools = `Edit|Write|NotebookEdit`

// AutoMemoryKey is Claude Code's own "auto memory" switch, which we turn off in
// any project that adopts stigmergy.
//
// Auto memory is on by default and writes markdown to
// ~/.claude/projects/<slug>/memory/. Left running alongside stigmergy it would be
// a second, divergent memory store in the same project — precisely the problem
// this tool exists to end. It is not a permission-deniable tool, so this setting
// is the only way to stop it; telling the agent not to use it in CLAUDE.md is
// advice, and advice is not a mechanism.
//
// Import the old memories first (`stigmergy import claude-memory`), because
// disabling auto memory does not delete them — it just stops Claude reading and
// writing them, and anything left behind is stranded.
const AutoMemoryKey = "autoMemoryEnabled"

// claudeHooks is what init installs, and what remove takes back out.
var claudeHooks = []struct {
	event, matcher, command string
}{
	{"SessionStart", "", Binary + " hook session-start"},
	{"PreToolUse", EditTools, Binary + " hook claim-guard"},
	{"PreToolUse", RootGateTools, Binary + " hook root-gate"},
	{"SessionEnd", "", Binary + " hook session-end"},
}

// ClaudePaths are the files init touches for Claude Code.
func ClaudePaths(worktree string) (mcpJSON, settings, memory string) {
	return filepath.Join(worktree, ".mcp.json"),
		filepath.Join(worktree, ".claude", "settings.json"),
		filepath.Join(worktree, "CLAUDE.md")
}

// InstallClaude registers the MCP server and hooks in a project, merging into
// whatever is already there.
func InstallClaude(worktree string) error {
	mcpPath, settingsPath, memoryPath := ClaudePaths(worktree)

	mcpCfg, err := ReadJSON(mcpPath)
	if err != nil {
		return err
	}
	mcpCfg.mapAt("mcpServers")["stigmergy"] = map[string]any{
		"command": Binary,
		"args":    []any{"mcp"},
	}
	if err := WriteJSON(mcpPath, mcpCfg); err != nil {
		return err
	}

	settings, err := ReadJSON(settingsPath)
	if err != nil {
		return err
	}
	setClaudeHooks(settings, true)
	settings[AutoMemoryKey] = false
	if err := WriteJSON(settingsPath, settings); err != nil {
		return err
	}

	return WriteMarkerBlock(memoryPath, ClaudeBlurb())
}

// RemoveClaude takes stigmergy back out, leaving the user's own configuration
// intact.
func RemoveClaude(worktree string) error {
	mcpPath, settingsPath, memoryPath := ClaudePaths(worktree)

	if mcpCfg, err := ReadJSON(mcpPath); err == nil {
		if servers, ok := mcpCfg["mcpServers"].(map[string]any); ok {
			delete(servers, "stigmergy")
			if len(servers) == 0 {
				delete(mcpCfg, "mcpServers")
			}
		}
		if len(mcpCfg) == 0 {
			_ = removeIfExists(mcpPath)
		} else if err := WriteJSON(mcpPath, mcpCfg); err != nil {
			return err
		}
	}

	if settings, err := ReadJSON(settingsPath); err == nil {
		setClaudeHooks(settings, false)
		// Take back only the value we wrote. If the user has since set it to
		// true, that is their decision and not ours to revert; deleting the key
		// outright would silently re-enable auto memory for someone who had
		// turned it off on purpose.
		if v, ok := settings[AutoMemoryKey].(bool); ok && !v {
			delete(settings, AutoMemoryKey)
		}
		if len(settings) == 0 {
			_ = removeIfExists(settingsPath)
		} else if err := WriteJSON(settingsPath, settings); err != nil {
			return err
		}
	}

	return RemoveMarkerBlock(memoryPath)
}

// setClaudeHooks rewrites stigmergy's hook entries, in place.
//
// It always strips our entries first, then re-adds them when installing. That
// makes install idempotent and upgrades safe: an entry whose matcher or command
// changed between versions is replaced rather than duplicated, and a stale one
// that no longer exists is dropped instead of lingering forever.
func setClaudeHooks(settings Object, install bool) {
	hooks := settings.mapAt("hooks")

	for _, h := range claudeHooks {
		entries := hooks.arrayAt(h.event)
		kept := make([]any, 0, len(entries))
		for _, e := range entries {
			if !isStigmergyEntry(e) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(hooks, h.event)
		} else {
			hooks[h.event] = kept
		}
	}

	if install {
		for _, h := range claudeHooks {
			entry := map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": h.command}},
			}
			if h.matcher != "" {
				entry["matcher"] = h.matcher
			}
			hooks[h.event] = append(hooks.arrayAt(h.event), entry)
		}
	}

	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
}

// isStigmergyEntry reports whether a hook entry is one of ours, by looking at
// the command it runs. Matching on the command — not on position, and not on a
// marker we would have to store elsewhere — is what lets removal be exact even
// if the user has reordered or reformatted the file.
func isStigmergyEntry(entry any) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	inner, ok := obj["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range inner {
		hook, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, ok := hook["command"].(string); ok && strings.HasPrefix(strings.TrimSpace(cmd), CommandPrefix) {
			return true
		}
	}
	return false
}
