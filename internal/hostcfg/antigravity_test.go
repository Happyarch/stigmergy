package hostcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAntigravityInAnEmptyProject(t *testing.T) {
	wt := t.TempDir()
	if err := InstallAntigravity(wt); err != nil {
		t.Fatalf("InstallAntigravity: %v", err)
	}
	pluginJSON, mcpConfig, hooksJSON := AntigravityPaths(wt)

	// The plugin marker is what makes Antigravity load the directory at all.
	var manifest map[string]any
	if err := json.Unmarshal([]byte(read(t, pluginJSON)), &manifest); err != nil {
		t.Fatalf("plugin.json is not valid JSON: %v", err)
	}
	if manifest["name"] != "stigmergy" {
		t.Errorf("plugin.json = %#v", manifest)
	}

	var mcpCfg map[string]any
	if err := json.Unmarshal([]byte(read(t, mcpConfig)), &mcpCfg); err != nil {
		t.Fatalf("mcp_config.json is not valid JSON: %v", err)
	}
	srv := mcpCfg["mcpServers"].(map[string]any)["stigmergy"].(map[string]any)
	if srv["command"] != "stigmergy" {
		t.Errorf("MCP server = %#v, want the PATH-resolved binary", srv)
	}

	hooks := read(t, hooksJSON)
	var hookCfg map[string]any
	if err := json.Unmarshal([]byte(hooks), &hookCfg); err != nil {
		t.Fatalf("hooks.json is not valid JSON: %v", err)
	}
	for _, want := range []string{
		"antigravity-claim-guard", "antigravity-root-gate",
		"antigravity-pre-invocation", "antigravity-stop",
	} {
		if !strings.Contains(hooks, want) {
			t.Errorf("hooks.json does not install %q:\n%s", want, hooks)
		}
	}
}

// PreToolUse nests its handlers under a matcher; PreInvocation and Stop take
// their handlers directly, and their matcher is ignored. Getting this backwards
// produces a file Antigravity reads without complaint and acts on incorrectly.
func TestAntigravityHooksMatchTheDocumentedSchema(t *testing.T) {
	wt := t.TempDir()
	if err := InstallAntigravity(wt); err != nil {
		t.Fatal(err)
	}
	_, _, hooksJSON := AntigravityPaths(wt)

	var cfg map[string]map[string]any
	if err := json.Unmarshal([]byte(read(t, hooksJSON)), &cfg); err != nil {
		t.Fatal(err)
	}

	guard := cfg["stigmergy-claim-guard"]["PreToolUse"].([]any)[0].(map[string]any)
	if _, ok := guard["matcher"].(string); !ok {
		t.Errorf("PreToolUse entry has no matcher: %#v", guard)
	}
	if _, ok := guard["hooks"].([]any); !ok {
		t.Errorf("PreToolUse entry does not nest its handlers under hooks: %#v", guard)
	}

	// The simpler shape: a handler directly under the event key.
	pre := cfg["stigmergy-pre-invocation"]["PreInvocation"].([]any)[0].(map[string]any)
	if pre["command"] == nil {
		t.Errorf("PreInvocation handler is not a bare command entry: %#v", pre)
	}
	if pre["hooks"] != nil {
		t.Errorf("PreInvocation must not nest handlers under hooks: %#v", pre)
	}
}

// The matcher may only name tools Antigravity actually has. An invented one
// matches nothing and quietly advertises coverage that does not exist.
func TestTheEditMatcherNamesOnlyRealWriteTools(t *testing.T) {
	documented := map[string]bool{
		"write_to_file":              true,
		"replace_file_content":       true,
		"multi_replace_file_content": true,
	}
	for _, tool := range strings.Split(antigravityEditTools, "|") {
		if !documented[tool] {
			t.Errorf("the matcher names %q, which is not a documented Antigravity tool", tool)
		}
	}
	for tool := range documented {
		if !strings.Contains(antigravityEditTools, tool) {
			t.Errorf("the matcher does not cover the write tool %q", tool)
		}
	}
}

// init owns the whole plugin directory, so removal is total — and must not
// touch anything else in .agents/.
func TestRemoveAntigravityTakesOnlyItsOwnPlugin(t *testing.T) {
	wt := t.TempDir()
	if err := InstallAntigravity(wt); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(wt, ".agents", "plugins", "someone-else", "plugin.json")
	writeFile(t, other, `{"name":"someone-else"}`)

	if err := RemoveAntigravity(wt); err != nil {
		t.Fatalf("RemoveAntigravity: %v", err)
	}
	if _, err := os.Stat(AntigravityPluginDir(wt)); !os.IsNotExist(err) {
		t.Error("the stigmergy plugin directory survived removal")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("removal took another tool's plugin with it")
	}
}

func TestRemoveAntigravityWhenItWasNeverInstalled(t *testing.T) {
	if err := RemoveAntigravity(t.TempDir()); err != nil {
		t.Errorf("removing a plugin that does not exist: %v", err)
	}
}

// Installing twice must converge rather than accumulate: init is how a project
// picks up a new hook, and it runs against projects that already have the old
// one.
func TestInstallAntigravityIsIdempotent(t *testing.T) {
	wt := t.TempDir()
	if err := InstallAntigravity(wt); err != nil {
		t.Fatal(err)
	}
	_, _, hooksJSON := AntigravityPaths(wt)
	firstHooks := read(t, hooksJSON)

	if err := InstallAntigravity(wt); err != nil {
		t.Fatal(err)
	}
	if got := read(t, hooksJSON); got != firstHooks {
		t.Error("a second init changed hooks.json")
	}
}
