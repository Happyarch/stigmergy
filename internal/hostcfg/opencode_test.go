package hostcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/hosts"
)

func TestInstallOpenCodeInAnEmptyProject(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatalf("InstallOpenCode: %v", err)
	}
	configPath, pluginPath := OpenCodePaths(wt)

	var cfg struct {
		Schema string `json:"$schema"`
		MCP    map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			Enabled bool     `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(read(t, configPath)), &cfg); err != nil {
		t.Fatalf("opencode.json is not valid JSON: %v", err)
	}
	srv, ok := cfg.MCP["stigmergy"]
	if !ok {
		t.Fatal("opencode.json does not register the stigmergy MCP server")
	}
	// A local server is a command opencode spawns. Getting "type" wrong makes it
	// try to fetch stigmergy over HTTP, which fails in a way that looks like a
	// network problem rather than a config one.
	if srv.Type != "local" {
		t.Errorf("MCP server type = %q, want \"local\"", srv.Type)
	}
	if len(srv.Command) < 2 || srv.Command[0] != Binary || srv.Command[1] != "mcp" {
		t.Errorf("MCP command = %v, want [%s mcp]", srv.Command, Binary)
	}

	// The MCP registration is what carries the shared rules to the agent: opencode
	// puts every connected server's instructions in the system prompt. Without it
	// the plugin still guards edits, but nobody is ever told why.
	if !strings.Contains(read(t, pluginPath), "stigmergy hook") {
		t.Error("the plugin does not call `stigmergy hook`")
	}
}

// The plugin must load with nothing installed. opencode auto-discovers .js in
// .opencode/plugin/, but only a plugin that needs no dependencies can be written
// by a Go binary and just work — anything importing @opencode-ai/plugin needs an
// npm install that stigmergy has no business running.
func TestTheOpenCodePluginHasNoDependencies(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	_, pluginPath := OpenCodePaths(wt)
	js := read(t, pluginPath)

	if strings.Contains(js, "@opencode-ai/") {
		t.Error("the plugin imports from @opencode-ai — that needs an npm install we do not do")
	}
	for _, unwanted := range []string{"package.json", "bun install", "npm install"} {
		if strings.Contains(js, unwanted) {
			t.Errorf("the plugin mentions %q; it is supposed to need no toolchain", unwanted)
		}
	}
	if _, err := os.Stat(filepath.Join(wt, ".opencode", "package.json")); !os.IsNotExist(err) {
		t.Errorf("install created a package.json (err=%v)", err)
	}
}

// Every tool that writes to the tree must reach the claim guard. A tool the
// plugin does not forward is a hole the Go side can never see, and no test of
// the tools it does forward would show it.
func TestThePluginForwardsEveryEditTool(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	_, pluginPath := OpenCodePaths(wt)
	js := read(t, pluginPath)

	for _, tool := range hosts.OpenCodeEditTools {
		if !strings.Contains(js, `"`+tool+`"`) {
			t.Errorf("the plugin does not name the %q tool, so edits through it are unguarded", tool)
		}
	}
	// opencode's MCP tools are server-name prefixed; Claude's matcher shape does
	// not transfer, and getting this wrong silently disables the root gate.
	if !strings.Contains(js, hosts.OpenCodeMCPPrefix) {
		t.Errorf("the plugin does not match stigmergy's own tools by the %q prefix", hosts.OpenCodeMCPPrefix)
	}
	if strings.Contains(js, "mcp__stigmergy__") {
		t.Error("the plugin uses Claude's MCP tool naming, which opencode does not use")
	}
}

// A throw from tool.execute.before is what blocks an edit, and it is also what
// would break the agent if stigmergy itself fell over. The plugin must throw
// only on a deny.
func TestThePluginOnlyThrowsOnADeny(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	_, pluginPath := OpenCodePaths(wt)
	js := read(t, pluginPath)

	// Count statements, not the word: the comments discuss throwing at length,
	// and a test that reads prose is measuring the wrong thing.
	var throws int
	for _, line := range strings.Split(js, "\n") {
		code, _, _ := strings.Cut(strings.TrimSpace(line), "//")
		if strings.Contains(code, "throw ") {
			throws++
		}
	}
	if throws != 1 {
		t.Errorf("the plugin throws %d times, want exactly 1 (only a deny may block a tool call)", throws)
	}
	if !strings.Contains(js, "if (reply?.deny) throw") {
		t.Error("the throw is not guarded by a deny; a stigmergy failure would block the agent's work")
	}
}

// opencode rejects a message part whose id does not start with "prt", and it
// rejects the whole message with it: the text never reaches the model and the
// only trace is an "invalid user part before save" in a log nobody is reading.
// It is undocumented, and it cost a live session to find, so it is pinned here.
func TestThePluginBuildsAcceptablePartIDs(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	_, pluginPath := OpenCodePaths(wt)
	js := read(t, pluginPath)

	if !strings.Contains(js, `"prt_"`) {
		t.Error("the plugin does not build prt_ part ids; opencode will drop the registration text")
	}
	if strings.Contains(js, `"stigmergy-" + Date.now()`) {
		t.Error("the plugin is back to inventing its own part id shape, which opencode rejects")
	}
}

func TestInstallOpenCodeKeepsTheUsersConfig(t *testing.T) {
	wt := t.TempDir()
	configPath, _ := OpenCodePaths(wt)
	writeFile(t, configPath, `{"$schema":"https://opencode.ai/config.json","model":"lmstudio/qwen","mcp":{"mine":{"type":"local","command":["my-server"]}}}`)

	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(read(t, configPath)), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["model"] != "lmstudio/qwen" {
		t.Error("install dropped the user's model setting")
	}
	mcp, _ := cfg["mcp"].(map[string]any)
	if _, ok := mcp["mine"]; !ok {
		t.Error("install dropped the user's own MCP server")
	}
	if _, ok := mcp["stigmergy"]; !ok {
		t.Error("install did not add stigmergy")
	}
}

func TestRemoveOpenCodeLeavesTheUsersConfigBehind(t *testing.T) {
	wt := t.TempDir()
	configPath, pluginPath := OpenCodePaths(wt)
	writeFile(t, configPath, `{"model":"lmstudio/qwen","mcp":{"mine":{"type":"local","command":["my-server"]}}}`)

	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOpenCode(wt); err != nil {
		t.Fatalf("RemoveOpenCode: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(read(t, configPath)), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["model"] != "lmstudio/qwen" {
		t.Error("remove took the user's model setting with it")
	}
	mcp, _ := cfg["mcp"].(map[string]any)
	if _, ok := mcp["stigmergy"]; ok {
		t.Error("remove left the stigmergy MCP server behind")
	}
	if _, ok := mcp["mine"]; !ok {
		t.Error("remove took the user's own MCP server with it")
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Errorf("remove left the plugin behind (err=%v)", err)
	}
}

// A plugin directory of the user's own must survive. Removal takes back what we
// put there and nothing else.
func TestRemoveOpenCodeKeepsTheUsersOwnPlugins(t *testing.T) {
	wt := t.TempDir()
	mine := filepath.Join(OpenCodePluginDir(wt), "mine.js")

	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mine, "export default async () => ({})\n")

	if err := RemoveOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("remove deleted the user's own plugin: %v", err)
	}
}

func TestInstallOpenCodeIsIdempotent(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	configPath, pluginPath := OpenCodePaths(wt)
	firstConfig, firstPlugin := read(t, configPath), read(t, pluginPath)

	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	if got := read(t, configPath); got != firstConfig {
		t.Errorf("a second init changed opencode.json:\n%s", got)
	}
	if got := read(t, pluginPath); got != firstPlugin {
		t.Errorf("a second init changed the plugin:\n%s", got)
	}
}

// init must not write an instruction file for opencode either. opencode reads
// AGENTS.md, and AGENTS.md is commonly the same inode as CLAUDE.md — which is
// how two hosts came to overwrite each other's instructions with contradictory
// text. The rules ride the MCP server instead.
func TestInstallOpenCodeWritesNoInstructionFile(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(wt, name)); !os.IsNotExist(err) {
			t.Errorf("install created %s (err=%v)", name, err)
		}
	}
}
