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
		MCP    struct {
			Servers map[string]struct {
				Type     string   `json:"type"`
				Command  []string `json:"command"`
				Disabled bool     `json:"disabled"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(read(t, configPath)), &cfg); err != nil {
		t.Fatalf("opencode.json is not valid JSON: %v", err)
	}
	srv, ok := cfg.MCP.Servers["stigmergy"]
	if !ok {
		t.Fatal("opencode.json does not register the stigmergy MCP server under mcp.servers")
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
	if srv.Disabled {
		t.Error("a fresh install registers the server disabled")
	}

	// V2 inverts the flag: `disabled`, not `enabled`. A V1 `enabled` key is
	// still read, but writing it would mean two sources of truth for the same
	// bit the next reader has to reconcile.
	var raw map[string]any
	if err := json.Unmarshal([]byte(read(t, configPath)), &raw); err != nil {
		t.Fatal(err)
	}
	mcp, _ := raw["mcp"].(map[string]any)
	if _, ok := mcp["stigmergy"]; ok {
		t.Error("opencode.json keeps a V1 mcp.stigmergy entry beside mcp.servers")
	}
	servers, _ := mcp["servers"].(map[string]any)
	entry, _ := servers["stigmergy"].(map[string]any)
	if _, ok := entry["enabled"]; ok {
		t.Error("the V2 entry uses a V1 `enabled` flag; V2 wants `disabled`")
	}

	// The MCP registration is what carries the shared rules to the agent: opencode
	// puts every connected server's instructions in the system prompt. Without it
	// the plugin still guards edits, but nobody is ever told why.
	if !strings.Contains(read(t, pluginPath), "stigmergy hook") {
		t.Error("the plugin does not call `stigmergy hook`")
	}
}

// The plugin must load with nothing installed. opencode auto-discovers .js in
// .opencode/plugins/, but only a plugin that needs no dependencies can be written
// by a Go binary and just work — anything importing the plugin package needs an
// install step that stigmergy has no business running. Its define helper is an
// identity function, so a plain object with an id and a setup function loads
// exactly the same with no import at all.
func TestTheOpenCodePluginHasNoDependencies(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	_, pluginPath := OpenCodePaths(wt)
	js := read(t, pluginPath)

	if strings.Contains(js, "@opencode-ai/") {
		t.Error("the plugin imports from @opencode-ai — that needs an install step the installer never runs")
	}
	if strings.Contains(js, "@opencode/plugin") {
		t.Error("the plugin imports the plugin package — that needs an install step the installer never runs")
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

// The plugin is a V2 plugin: a plain object with an id and a setup function
// that registers hooks through the context. A V1 plugin — a function returning
// a hook map — does not run on V2 at all.
func TestThePluginIsAV2Plugin(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	_, pluginPath := OpenCodePaths(wt)
	js := read(t, pluginPath)

	for _, want := range []string{
		`id: "stigmergy"`,
		"async setup(ctx)",
		`ctx.tool.hook("execute.before"`,
		`ctx.session.hook("prompt"`,
		"ctx.session.get",
		"ctx.location",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the plugin does not contain %q; it is not a working V2 plugin", want)
		}
	}
	for _, gone := range []string{
		"chat.message",
		"client.session.get",
		"output.parts",
		"prt_",
		"newPartID",
		"tool.execute.before",
	} {
		if strings.Contains(js, gone) {
			t.Errorf("the plugin still contains %q; that is the V1 API and it does not run on V2", gone)
		}
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

// A throw from execute.before is what blocks an edit, and it is also what
// would break the agent if stigmergy itself fell over. The plugin must throw
// only on a deny — and only there: the prompt hook mutates text and never
// throws at all.
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

// V2 has no message parts for a plugin to push: the prompt hook mutates the
// prompt text in place. Anything still building part ids is a V1 plugin that
// will not run — and a part id opencode rejects used to drop the whole message
// in silence, so this is pinned from the other side now.
func TestThePluginExtendsThePromptText(t *testing.T) {
	wt := t.TempDir()
	if err := InstallOpenCode(wt); err != nil {
		t.Fatal(err)
	}
	_, pluginPath := OpenCodePaths(wt)
	js := read(t, pluginPath)

	if !strings.Contains(js, "event.prompt.text") {
		t.Error("the plugin does not extend the prompt text; registration and mail go nowhere")
	}
	for _, gone := range []string{`"prt_"`, "messageID", "synthetic", "output.parts"} {
		if strings.Contains(js, gone) {
			t.Errorf("the plugin still contains %q; V2 has no parts to build", gone)
		}
	}
}

// An existing V1 install migrates in place: the stigmergy server moves from mcp.stigmergy
// (enabled) to mcp.servers.stigmergy (disabled), and the V1 plugin file is taken
// back so V2 does not discover it beside the new one. Anyone else's entries and
// files stay exactly where they are.
func TestInstallOpenCodeMigratesAV1Install(t *testing.T) {
	wt := t.TempDir()
	configPath, _ := OpenCodePaths(wt)
	writeFile(t, configPath, `{"$schema":"https://opencode.ai/config.json","model":"lmstudio/qwen","mcp":{"mine":{"type":"local","command":["my-server"]},"stigmergy":{"type":"local","command":["stigmergy","mcp"],"enabled":true}}}`)
	legacy := OpenCodeLegacyPluginPath(wt)
	writeFile(t, legacy, "export default async () => ({})\n")
	mine := filepath.Join(OpenCodeLegacyPluginDir(wt), "mine.js")
	writeFile(t, mine, "export default async () => ({})\n")

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
	if _, ok := mcp["stigmergy"]; ok {
		t.Error("install left the V1 mcp.stigmergy entry behind")
	}
	servers, _ := mcp["servers"].(map[string]any)
	entry, _ := servers["stigmergy"].(map[string]any)
	if entry == nil {
		t.Fatal("install did not write the V2 mcp.servers.stigmergy entry")
	}
	if disabled, _ := entry["disabled"].(bool); disabled {
		t.Error("migration disabled a server the user had enabled")
	}
	if _, ok := entry["enabled"]; ok {
		t.Error("the migrated entry kept its V1 `enabled` flag")
	}
	if _, ok := mcp["mine"]; !ok {
		t.Error("install dropped the user's own MCP server")
	}

	_, pluginPath := OpenCodePaths(wt)
	if !strings.Contains(read(t, pluginPath), "stigmergy hook") {
		t.Error("install did not write the V2 plugin")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("install left the V1 plugin file behind (err=%v)", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("install deleted the user's own V1-dir plugin: %v", err)
	}
}

// A server the user deliberately disabled stays disabled across a reinstall,
// in either shape.
func TestInstallOpenCodeKeepsADisabledServerDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"v1 shape", `{"mcp":{"stigmergy":{"type":"local","command":["stigmergy","mcp"],"enabled":false}}}`},
		{"v2 shape", `{"mcp":{"servers":{"stigmergy":{"type":"local","command":["stigmergy","mcp"],"disabled":true}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wt := t.TempDir()
			configPath, _ := OpenCodePaths(wt)
			writeFile(t, configPath, tc.body)
			if err := InstallOpenCode(wt); err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if err := json.Unmarshal([]byte(read(t, configPath)), &cfg); err != nil {
				t.Fatal(err)
			}
			mcp, _ := cfg["mcp"].(map[string]any)
			servers, _ := mcp["servers"].(map[string]any)
			entry, _ := servers["stigmergy"].(map[string]any)
			if disabled, _ := entry["disabled"].(bool); !disabled {
				t.Error("reinstall re-enabled a server the user had disabled")
			}
		})
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
	servers, _ := mcp["servers"].(map[string]any)
	if _, ok := servers["stigmergy"]; !ok {
		t.Error("install did not add stigmergy under mcp.servers")
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
		t.Error("remove left the V1 stigmergy MCP server behind")
	}
	if servers, ok := mcp["servers"].(map[string]any); ok {
		if _, ok := servers["stigmergy"]; ok {
			t.Error("remove left the V2 stigmergy MCP server behind")
		}
	}
	if _, ok := mcp["mine"]; !ok {
		t.Error("remove took the user's own MCP server with it")
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Errorf("remove left the plugin behind (err=%v)", err)
	}
}

// A plugin directory of the user's own must survive. Removal takes back what
// install put there and nothing else.
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
