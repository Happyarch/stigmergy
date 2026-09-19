package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/hostcfg"
	"github.com/happyarch/stigmergy/internal/store"
)

// An unreadable timestamp is a FAILURE, not a warning: every read of that memory
// errors until someone decides what the instant was, and doctor's exit status is
// what tells a script something needs a human.
func TestRepairTimestampsReportsAndFailsCorrectly(t *testing.T) {
	newDB := func(t *testing.T) *store.DB {
		t.Helper()
		db, err := store.OpenGlobal(filepath.Join(t.TempDir(), "global.sqlite3"))
		if err != nil {
			t.Fatalf("OpenGlobal: %v", err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	put := func(t *testing.T, db *store.DB, key, raw string) {
		t.Helper()
		if _, err := db.WriteMemory(store.MemoryWrite{
			Key: key, Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
		}, "claude-code"); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := db.Exec(`UPDATE memories SET updated_at = ? WHERE key = ?`, raw, key); err != nil {
			t.Fatalf("planting raw timestamp: %v", err)
		}
	}

	t.Run("a repairable row passes and is counted", func(t *testing.T) {
		db := newDB(t)
		put(t, db, "short-form", "2026-03-01T00:00:00.5Z")

		var buf bytes.Buffer
		d := &diag{out: &buf}
		repairTimestamps(d, db, "global")

		if d.failed {
			t.Error("a repairable timestamp must not fail doctor")
		}
		if !strings.Contains(buf.String(), "1 global memory timestamp(s) rewritten") {
			t.Errorf("output does not report the repair:\n%s", buf.String())
		}
	})

	t.Run("an unreadable row fails and is named", func(t *testing.T) {
		db := newDB(t)
		put(t, db, "corrupt-row", "whenever")

		var buf bytes.Buffer
		d := &diag{out: &buf}
		repairTimestamps(d, db, "global")

		if !d.failed {
			t.Error("an unreadable timestamp must fail doctor")
		}
		if !strings.Contains(buf.String(), "corrupt-row") {
			t.Errorf("output does not name the bad key:\n%s", buf.String())
		}
	})

	t.Run("a clean database says nothing", func(t *testing.T) {
		db := newDB(t)
		if _, err := db.WriteMemory(store.MemoryWrite{
			Key: "fine", Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
		}, "claude-code"); err != nil {
			t.Fatalf("write: %v", err)
		}

		var buf bytes.Buffer
		d := &diag{out: &buf}
		repairTimestamps(d, db, "global")

		if d.failed || buf.Len() != 0 {
			t.Errorf("a clean database produced output:\n%s", buf.String())
		}
	})
}

// A project initialized under opencode V1 must not need a manual re-run of
// init: doctor migrates the legacy plugin and the V1 config shape in place.
// A V1 plugin does not run on opencode V2 at all, so reporting "configured"
// without migrating would be the quiet failure the host check exists to
// prevent.
func TestCheckHostConfigMigratesAStaleOpenCodeInstall(t *testing.T) {
	wt := t.TempDir()
	configPath, _ := hostcfg.OpenCodePaths(wt)
	writeHostcfgFile(t, configPath,
		`{"$schema":"https://opencode.ai/config.json","model":"lmstudio/qwen",`+
			`"mcp":{"stigmergy":{"type":"local","command":["stigmergy","mcp"],"enabled":true}}}`)
	legacy := hostcfg.OpenCodeLegacyPluginPath(wt)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	// V1 shape: string-keyed hooks and no setup function. The marker comment
	// is what the check keys on, same as a real V1 install.
	writeHostcfgFile(t, legacy,
		"// stigmergy hook\n// tool.execute.before\n// chat.message\nexport default async () => ({})\n")

	var buf bytes.Buffer
	d := &diag{out: &buf}
	checkHostConfig(d, "", wt)

	if d.failed {
		t.Errorf("a migrated install must not fail:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "migrated to the current shape") {
		t.Errorf("doctor did not report the migration:\n%s", buf.String())
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the legacy plugin is still in place (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".opencode", "plugins", "stigmergy.js")); err != nil {
		t.Errorf("the V2 plugin was not written: %v", err)
	}

	// A second run is a no-op: InstallOpenCode is idempotent, so there is
	// nothing left to migrate and nothing more to say.
	buf.Reset()
	d = &diag{out: &buf}
	checkHostConfig(d, "", wt)
	if strings.Contains(buf.String(), "migrated to the current shape") {
		t.Errorf("a current install reported a migration:\n%s", buf.String())
	}
}

// Doctor never enables a host the user did not ask for: a worktree with no
// opencode markers gets no plugin and no config, whatever else it reports.
func TestCheckHostConfigLeavesUnconfiguredOpenCodeAlone(t *testing.T) {
	wt := t.TempDir()

	var buf bytes.Buffer
	d := &diag{out: &buf}
	checkHostConfig(d, "", wt)

	configPath, pluginPath := hostcfg.OpenCodePaths(wt)
	for _, p := range []string{configPath, pluginPath, hostcfg.OpenCodeLegacyPluginPath(wt)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("doctor created %s in a project without opencode (err=%v)", p, err)
		}
	}
}

func writeHostcfgFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
