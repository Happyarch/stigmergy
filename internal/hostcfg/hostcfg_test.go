package hostcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstallClaudeInAnEmptyProject(t *testing.T) {
	wt := t.TempDir()
	if err := InstallClaude(wt); err != nil {
		t.Fatalf("InstallClaude: %v", err)
	}
	mcpPath, settingsPath, memoryPath := ClaudePaths(wt)

	var mcpCfg map[string]any
	if err := json.Unmarshal([]byte(read(t, mcpPath)), &mcpCfg); err != nil {
		t.Fatal(err)
	}
	srv := mcpCfg["mcpServers"].(map[string]any)["stigmergy"].(map[string]any)
	if srv["command"] != "stigmergy" {
		t.Fatalf("MCP server = %#v, want the PATH-resolved binary", srv)
	}

	settings := read(t, settingsPath)
	for _, want := range []string{"claim-guard", "root-gate", "session-start", "session-end", EditTools} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings.json does not install %q:\n%s", want, settings)
		}
	}
	// The instructions travel with the MCP server and the session-start hook
	// now. A CLAUDE.md conjured up by `init` would be a third copy that nothing
	// keeps current, so init must not create one.
	if _, err := os.Stat(memoryPath); !os.IsNotExist(err) {
		t.Errorf("install created CLAUDE.md; it should no longer write instruction files (err=%v)", err)
	}
}

// Claude Code's own auto memory must be switched off in an adopted project.
// Left on, it writes a second, divergent memory store in the very project
// stigmergy is supposed to be the single source of truth for — and it is not a
// permission-deniable tool, so this setting is the only mechanism that stops it.
func TestInstallClaudeDisablesAutoMemory(t *testing.T) {
	wt := t.TempDir()
	if err := InstallClaude(wt); err != nil {
		t.Fatal(err)
	}
	_, settingsPath, _ := ClaudePaths(wt)

	var settings map[string]any
	if err := json.Unmarshal([]byte(read(t, settingsPath)), &settings); err != nil {
		t.Fatal(err)
	}
	if v, ok := settings[AutoMemoryKey].(bool); !ok || v {
		t.Fatalf("%s = %#v, want false", AutoMemoryKey, settings[AutoMemoryKey])
	}

	// Removal takes back the value we wrote...
	if err := RemoveClaude(wt); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(settingsPath); err == nil {
		var after map[string]any
		if err := json.Unmarshal(data, &after); err != nil {
			t.Fatal(err)
		}
		if _, present := after[AutoMemoryKey]; present {
			t.Errorf("%s survived removal: %s", AutoMemoryKey, data)
		}
	}

	// ...but never reverts a user who turned auto memory off for their own
	// reasons into having it back on. If they have set it to true themselves,
	// that is their call and we leave it alone.
	if err := InstallClaude(wt); err != nil {
		t.Fatal(err)
	}
	settings2, err := ReadJSON(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	settings2[AutoMemoryKey] = true
	if err := WriteJSON(settingsPath, settings2); err != nil {
		t.Fatal(err)
	}
	if err := RemoveClaude(wt); err != nil {
		t.Fatal(err)
	}
	after, err := ReadJSON(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if after[AutoMemoryKey] != true {
		t.Errorf("removal clobbered the user's own %s=true: %#v", AutoMemoryKey, after)
	}
}

// The user's configuration was there first. Everything we do not own must
// survive install, uninstall, and reinstall untouched.
func TestInstallClaudePreservesExistingConfig(t *testing.T) {
	wt := t.TempDir()
	mcpPath, settingsPath, memoryPath := ClaudePaths(wt)

	writeFile(t, mcpPath, `{"mcpServers":{"other":{"command":"other-server"}}}`)
	writeFile(t, settingsPath, `{
	  "permissions": {"allow": ["Bash(ls:*)"]},
	  "hooks": {
	    "PreToolUse": [
	      {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-own-guard"}]}
	    ]
	  }
	}`)
	writeFile(t, memoryPath, "# My project\n\nSome instructions I wrote.\n")

	if err := InstallClaude(wt); err != nil {
		t.Fatalf("InstallClaude: %v", err)
	}

	var mcpCfg map[string]any
	if err := json.Unmarshal([]byte(read(t, mcpPath)), &mcpCfg); err != nil {
		t.Fatal(err)
	}
	servers := mcpCfg["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Error("install removed another MCP server")
	}
	if _, ok := servers["stigmergy"]; !ok {
		t.Error("install did not add the stigmergy server")
	}

	settings := read(t, settingsPath)
	if !strings.Contains(settings, "my-own-guard") {
		t.Error("install removed the user's own hook")
	}
	if !strings.Contains(settings, "Bash(ls:*)") {
		t.Error("install dropped unrelated settings")
	}
	if !strings.Contains(read(t, memoryPath), "Some instructions I wrote.") {
		t.Error("install overwrote the user's CLAUDE.md content")
	}

	// Uninstall must give back exactly the file we found.
	if err := RemoveClaude(wt); err != nil {
		t.Fatalf("RemoveClaude: %v", err)
	}
	settings = read(t, settingsPath)
	if strings.Contains(settings, "stigmergy") {
		t.Errorf("uninstall left stigmergy hooks behind:\n%s", settings)
	}
	if !strings.Contains(settings, "my-own-guard") || !strings.Contains(settings, "Bash(ls:*)") {
		t.Errorf("uninstall took the user's config with it:\n%s", settings)
	}
	memory := read(t, memoryPath)
	if strings.Contains(memory, "stigmergy") {
		t.Errorf("uninstall left the CLAUDE.md block behind:\n%s", memory)
	}
	if !strings.Contains(memory, "Some instructions I wrote.") {
		t.Error("uninstall removed the user's CLAUDE.md content")
	}
}

// Re-running init must not stack up duplicate hooks, or an edit would be
// guarded twice and the file would grow without bound.
func TestInstallClaudeIsIdempotent(t *testing.T) {
	wt := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := InstallClaude(wt); err != nil {
			t.Fatalf("InstallClaude #%d: %v", i+1, err)
		}
	}
	_, settingsPath, memoryPath := ClaudePaths(wt)

	if n := strings.Count(read(t, settingsPath), "hook claim-guard"); n != 1 {
		t.Fatalf("claim-guard is installed %d times, want 1", n)
	}
	if _, err := os.Stat(memoryPath); !os.IsNotExist(err) {
		t.Errorf("a second install created CLAUDE.md (err=%v)", err)
	}
}

// A settings file we cannot parse is far more likely to be one the user is
// midway through editing than garbage to discard.
func TestInstallRefusesToOverwriteInvalidJSON(t *testing.T) {
	wt := t.TempDir()
	_, settingsPath, _ := ClaudePaths(wt)
	writeFile(t, settingsPath, `{"hooks": [broken`)

	err := InstallClaude(wt)
	if err == nil {
		t.Fatal("install must refuse to proceed over invalid JSON")
	}
	if !strings.Contains(read(t, settingsPath), "broken") {
		t.Fatal("install overwrote a file it could not parse")
	}
}

func TestInstallCodexAppendsToTOMLWithoutRewritingIt(t *testing.T) {
	wt := t.TempDir()
	configPath, hooksPath, memoryPath := CodexPaths(wt)

	// A realistic existing config: comments and formatting that a TOML
	// round-trip would silently destroy.
	original := `# My Codex settings — hand-written, comments matter.
sandbox_mode = "workspace-write"
approval_policy = "never"

[sandbox_workspace_write]
network_access = true   # needed for the proxy
`
	writeFile(t, configPath, original)

	if err := InstallCodex(wt); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}

	got := read(t, configPath)
	if !strings.HasPrefix(got, original) {
		t.Fatalf("the user's TOML was rewritten rather than appended to:\n%s", got)
	}
	if !strings.Contains(got, "comments matter") || !strings.Contains(got, "# needed for the proxy") {
		t.Error("install destroyed the user's comments")
	}
	for _, want := range []string{"[mcp_servers.stigmergy]", "use_memories = false", TOMLBeginMarker, TOMLEndMarker} {
		if !strings.Contains(got, want) {
			t.Errorf("the managed block is missing %q", want)
		}
	}

	hooks := read(t, hooksPath)
	for _, want := range []string{"codex-session-start", "codex-claim-warn", "codex-claim-stop"} {
		if !strings.Contains(hooks, want) {
			t.Errorf("hooks.json does not install %q", want)
		}
	}
	if _, err := os.Stat(memoryPath); !os.IsNotExist(err) {
		t.Errorf("install created AGENTS.md; it should no longer write instruction files (err=%v)", err)
	}

	// Reinstalling replaces the block rather than stacking another one.
	if err := InstallCodex(wt); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(read(t, configPath), TOMLBeginMarker); n != 1 {
		t.Fatalf("config.toml has %d managed blocks, want 1", n)
	}

	// Uninstall returns the file to exactly what the user wrote.
	if err := RemoveCodex(wt); err != nil {
		t.Fatalf("RemoveCodex: %v", err)
	}
	if got := read(t, configPath); strings.TrimSpace(got) != strings.TrimSpace(original) {
		t.Fatalf("uninstall did not restore the original config:\n%q", got)
	}
}

// Another tool already configured under our name is not ours to overwrite.
func TestInstallCodexRefusesAForeignServerEntry(t *testing.T) {
	wt := t.TempDir()
	configPath, _, _ := CodexPaths(wt)
	writeFile(t, configPath, "[mcp_servers.stigmergy]\ncommand = \"something-else\"\n")

	err := InstallCodex(wt)
	if err == nil {
		t.Fatal("install must refuse when another [mcp_servers.stigmergy] exists outside the managed block")
	}
	if !strings.Contains(read(t, configPath), "something-else") {
		t.Fatal("install clobbered a foreign server entry")
	}
}

func TestMarkerBlockRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")

	if err := WriteMarkerBlock(path, "first"); err != nil {
		t.Fatal(err)
	}
	if !HasMarkerBlock(path) {
		t.Fatal("HasMarkerBlock is false right after writing one")
	}
	if err := WriteMarkerBlock(path, "second"); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("rewriting the block did not replace its content:\n%s", got)
	}

	if err := RemoveMarkerBlock(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a file containing only our block should be removed entirely")
	}
}

// A half-deleted block is a mess a human should look at, not something to
// swallow along with whatever text follows it.
func TestUnterminatedMarkerIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	content := "# Notes\n" + BeginMarker + "\nsomeone deleted the end marker\n\n## My other notes\n"
	writeFile(t, path, content)

	if HasMarkerBlock(path) {
		t.Fatal("an unterminated block must not count as a managed block")
	}
	if err := RemoveMarkerBlock(path); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != content {
		t.Fatal("remove must not touch a file with an unterminated marker")
	}
}
