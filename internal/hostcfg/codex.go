package hostcfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TOML marker delimiters. Codex's config is TOML, and there is no round-trip
// TOML encoder in the standard library that preserves comments, ordering and
// formatting. Decoding and re-encoding the user's config would silently reflow
// it and drop their comments — so we never do. We append a delimited block of
// our own text and only ever rewrite the text between these markers.
const (
	TOMLBeginMarker = "# >>> stigmergy managed block — do not edit; `stigmergy init` regenerates it >>>"
	TOMLEndMarker   = "# <<< stigmergy managed block <<<"
)

// CodexPaths are the files init touches for Codex.
func CodexPaths(worktree string) (config, hooksJSON, memory string) {
	return filepath.Join(worktree, ".codex", "config.toml"),
		filepath.Join(worktree, ".codex", "hooks.json"),
		filepath.Join(worktree, "AGENTS.md")
}

// codexBlock is the TOML stigmergy owns.
//
// use_memories=false is the point of the [memories] table here: Codex's own
// memory feature would be a second, divergent store competing with the shared
// one, which is the exact problem stigmergy exists to solve.
const codexBlock = `[mcp_servers.stigmergy]
command = "stigmergy"
args = ["mcp"]
startup_timeout_sec = 10.0
tool_timeout_sec = 60.0

[memories]
use_memories = false
`

// foreignServer matches an [mcp_servers.stigmergy] table declared outside our
// managed block — someone else's configuration for the same server name.
var foreignServer = regexp.MustCompile(`(?m)^\s*\[mcp_servers\.stigmergy\]`)

// InstallCodex registers the MCP server and hooks for Codex.
func InstallCodex(worktree string) error {
	configPath, hooksPath, memoryPath := CodexPaths(worktree)

	if err := writeTOMLBlock(configPath, codexBlock); err != nil {
		return err
	}
	if err := installCodexHooks(hooksPath); err != nil {
		return err
	}
	return WriteMarkerBlock(memoryPath, CodexBlurb())
}

// RemoveCodex takes stigmergy back out of the Codex configuration.
func RemoveCodex(worktree string) error {
	configPath, hooksPath, memoryPath := CodexPaths(worktree)

	if err := removeTOMLBlock(configPath); err != nil {
		return err
	}
	if hooks, err := ReadJSON(hooksPath); err == nil {
		setCodexHooks(hooks, false)
		if len(hooks) == 0 {
			_ = removeIfExists(hooksPath)
		} else if err := WriteJSON(hooksPath, hooks); err != nil {
			return err
		}
	}
	return RemoveMarkerBlock(memoryPath)
}

// writeTOMLBlock appends or replaces our block, byte for byte leaving the rest
// of the file as we found it.
func writeTOMLBlock(path, content string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text := string(existing)

	before, after, found := splitTOMLBlock(text)
	if !found && foreignServer.MatchString(text) {
		// Someone already configured an MCP server under our name, outside our
		// block. Overwriting it could disconnect a server we know nothing
		// about; there is no safe automatic answer, so stop and let a human
		// decide.
		return fmt.Errorf("%s already declares [mcp_servers.stigmergy] outside stigmergy's managed block — "+
			"remove or rename it, then re-run `stigmergy init`", path)
	}

	block := TOMLBeginMarker + "\n" + strings.TrimRight(content, "\n") + "\n" + TOMLEndMarker

	var out string
	switch {
	case found:
		out = before + block + after
	case strings.TrimSpace(text) == "":
		out = block + "\n"
	default:
		out = strings.TrimRight(text, "\n") + "\n\n" + block + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(out), 0o644)
}

func removeTOMLBlock(path string) error {
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	before, after, found := splitTOMLBlock(string(existing))
	if !found {
		return nil
	}
	rest := strings.TrimSpace(before + after)
	if rest == "" {
		return removeIfExists(path)
	}
	return writeFileAtomic(path, []byte(rest+"\n"), 0o644)
}

func splitTOMLBlock(s string) (before, after string, found bool) {
	i := strings.Index(s, TOMLBeginMarker)
	if i < 0 {
		return s, "", false
	}
	j := strings.Index(s[i:], TOMLEndMarker)
	if j < 0 {
		return s, "", false
	}
	end := i + j + len(TOMLEndMarker)
	return s[:i], s[end:], true
}

// codexHooks is what Codex gets. It is deliberately weaker than the Claude set,
// because Codex's PreToolUse cannot deny a call: the warning is advisory, and
// the PostToolUse handler is what actually enforces a claim — by halting the
// turn after the edit has already landed. Damage limitation, not prevention.
// The matchers follow the Codex manual: SessionStart matches on the start
// source, and the tool events match on tool name, where apply_patch is
// addressable as Edit|Write.
var codexHooks = []struct {
	event, matcher, command string
}{
	{"SessionStart", "startup|resume|clear|compact", Binary + " hook codex-session-start"},
	{"PreToolUse", "Edit|Write", Binary + " hook codex-claim-warn"},
	{"PostToolUse", "Edit|Write", Binary + " hook codex-claim-stop"},
}

func installCodexHooks(path string) error {
	hooks, err := ReadJSON(path)
	if err != nil {
		return err
	}
	setCodexHooks(hooks, true)
	return WriteJSON(path, hooks)
}

func setCodexHooks(cfg Object, install bool) {
	hooks := cfg.mapAt("hooks")
	for _, h := range codexHooks {
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
		for _, h := range codexHooks {
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
		delete(cfg, "hooks")
	}
}
