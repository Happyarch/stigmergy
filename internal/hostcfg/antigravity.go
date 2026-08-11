package hostcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// antigravityEditTools is the PreToolUse matcher for file-writing tools.
//
// These three are the whole documented set, and each carries its target in
// toolCall.args.TargetFile — including multi_replace_file_content, whose
// several edits all land in one file. There is no edit_file: an earlier version
// of this matcher listed one, which matched nothing and advertised coverage
// that did not exist.
//
// run_command is deliberately absent, and it is the hole in this host as it is
// in Claude Code: an agent that shells out to sed can edit anything it likes.
// Claims are cooperative, and this is where that stops being an abstraction.
const antigravityEditTools = `write_to_file|replace_file_content|multi_replace_file_content`

// antigravityPluginName is the directory name used for the stigmergy plugin
// installed inside the Antigravity workspace plugins directory.
const antigravityPluginName = "stigmergy"

// AntigravityPluginDir returns the path to the stigmergy plugin directory
// that init installs inside the workspace.
func AntigravityPluginDir(worktree string) string {
	return filepath.Join(worktree, ".agents", "plugins", antigravityPluginName)
}

// AntigravityPaths returns the four file paths that InstallAntigravity writes.
func AntigravityPaths(worktree string) (pluginJSON, mcpConfig, hooksJSON string) {
	base := AntigravityPluginDir(worktree)
	return filepath.Join(base, "plugin.json"),
		filepath.Join(base, "mcp_config.json"),
		filepath.Join(base, "hooks.json")
}

// InstallAntigravity creates the stigmergy plugin for Antigravity inside the
// workspace's .agents/plugins/ directory.
//
// The plugin is self-contained: it has its own mcp_config.json and hooks.json,
// so it does not merge into or pollute any shared file (unlike the Claude Code
// host which shares .mcp.json). Removal is a single RemoveAll.
func InstallAntigravity(worktree string) error {
	base := AntigravityPluginDir(worktree)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}

	pluginJSON, mcpConfig, hooksJSON := AntigravityPaths(worktree)

	// plugin.json — the marker file that identifies the directory as a plugin.
	if err := writeFileAtomic(pluginJSON, []byte("{\"name\":\"stigmergy\"}\n"), 0o644); err != nil {
		return err
	}

	// mcp_config.json — registers the stigmergy MCP server with Antigravity.
	mcpData, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"stigmergy": map[string]any{
				"command": Binary,
				"args":    []string{"mcp"},
			},
		},
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(mcpConfig, append(mcpData, '\n'), 0o644); err != nil {
		return err
	}

	// hooks.json — all hook definitions for the plugin.
	//
	// There is no rules/stigmergy.md any more. It was always the weakest of the
	// three instruction files — Antigravity activates a rule in one of four modes
	// and the docs never said which applies to a rule that declares none, so it
	// may have reached nobody — and nothing load-bearing was allowed to rest on
	// it for exactly that reason. Now that the shared rules ride the MCP server's
	// instructions and the pre-invocation hook carries the rest, it has no job
	// left. See InstallClaude.
	return writeFileAtomic(hooksJSON, []byte(antigravityHooksJSON()), 0o644)
}

// RemoveAntigravity removes the stigmergy plugin directory entirely.
// It is safe to call when the plugin does not exist.
func RemoveAntigravity(worktree string) error {
	base := AntigravityPluginDir(worktree)
	if err := os.RemoveAll(base); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// antigravityHooksJSON returns the complete hooks.json content for the plugin.
// stigmergy owns this file entirely (it lives inside stigmergy's own plugin
// directory), so there is no merging logic — the installer writes the whole thing.
func antigravityHooksJSON() string {
	return `{
  "stigmergy-claim-guard": {
    "PreToolUse": [
      {
        "matcher": "` + antigravityEditTools + `",
        "hooks": [
          {
            "type": "command",
            "command": "` + CommandPrefix + ` antigravity-claim-guard",
            "timeout": 5
          }
        ]
      }
    ]
  },
  "stigmergy-root-gate": {
    "PreToolUse": [
      {
        "matcher": "` + RootGateTools + `",
        "hooks": [
          {
            "type": "command",
            "command": "` + CommandPrefix + ` antigravity-root-gate",
            "timeout": 5
          }
        ]
      }
    ]
  },
  "stigmergy-pre-invocation": {
    "PreInvocation": [
      {
        "type": "command",
        "command": "` + CommandPrefix + ` antigravity-pre-invocation",
        "timeout": 5
      }
    ]
  },
  "stigmergy-stop": {
    "Stop": [
      {
        "type": "command",
        "command": "` + CommandPrefix + ` antigravity-stop",
        "timeout": 10
      }
    ]
  }
}
`
}
