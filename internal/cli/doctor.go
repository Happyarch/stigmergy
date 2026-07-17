package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/hostcfg"
	"github.com/happyarch/stigmergy/internal/hosts"
	"github.com/happyarch/stigmergy/internal/store"
	"github.com/happyarch/stigmergy/internal/xdg"
)

func newDoctorCmd() *cobra.Command {
	var gc bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the stigmergy installation and this project",
		Long: "Check that stigmergy is installed, that this project is configured, and that the\n" +
			"databases are readable. Run this first whenever something is not working.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.OutOrStdout(), gc)
		},
	}
	cmd.Flags().BoolVar(&gc, "gc", false, "also prune old audit records and resolved mail")
	return cmd
}

type diag struct {
	out    io.Writer
	failed bool
}

func (d *diag) pass(format string, a ...any) { fmt.Fprintf(d.out, "PASS  "+format+"\n", a...) }
func (d *diag) warn(format string, a ...any) { fmt.Fprintf(d.out, "WARN  "+format+"\n", a...) }
func (d *diag) fail(format string, a ...any) {
	d.failed = true
	fmt.Fprintf(d.out, "FAIL  "+format+"\n", a...)
}
func (d *diag) note(format string, a ...any) { fmt.Fprintf(d.out, "      "+format+"\n", a...) }

func runDoctor(out io.Writer, gc bool) error {
	d := &diag{out: out}

	fmt.Fprintln(out, "Installation")
	if exe, err := os.Executable(); err == nil {
		d.pass("binary: %s (version %s)", exe, Version)
	} else {
		d.warn("the binary's own path could not be determined: %v", err)
	}
	// Host configs invoke the bare command name, so a binary that is not on
	// PATH is invisible to the very hosts it is meant to serve — the MCP server
	// simply fails to start, and the hooks silently never run.
	if path, err := exec.LookPath(hostcfg.Binary); err == nil {
		d.pass("stigmergy is on PATH: %s", path)
	} else {
		d.fail("stigmergy is not on PATH")
		d.note("Host configs invoke `stigmergy` by name. Until it is on PATH, the MCP server")
		d.note("will not start and the claim hooks will not run.")
	}
	if _, err := exec.LookPath("git"); err == nil {
		d.pass("git is on PATH")
	} else {
		d.fail("git is not on PATH — stigmergy locates a project by its git repository")
	}
	if _, err := exec.LookPath("codex"); err == nil {
		d.pass("codex is on PATH (stigmergy explore is available)")
	} else {
		d.warn("codex is not on PATH — `stigmergy explore` will not work (Claude Code is unaffected)")
	}

	fmt.Fprintln(out, "\nGlobal memory")
	globalPath, err := xdg.GlobalDBPath()
	if err != nil {
		d.fail("the global database path could not be resolved: %v", err)
	} else if g, err := store.OpenGlobal(globalPath); err != nil {
		d.fail("the global database could not be opened: %v", err)
	} else {
		defer g.Close()
		v, _ := g.SchemaVersion()
		d.pass("global database: %s (schema v%d)", g.Path, v)
		if err := g.ProbeFTS5(); err != nil {
			d.fail("FTS5 is unavailable, so memory search cannot work: %v", err)
		} else {
			d.pass("FTS5 is available")
		}
		if n, err := countMemories(g); err == nil {
			d.note("%d global memories", n)
		}
	}

	fmt.Fprintln(out, "\nThis project")
	cwd, _ := os.Getwd()
	repo, err := gitx.ResolveWithFallback(cwd)
	if err != nil {
		d.warn("not inside a git repository — project checks skipped")
		return finish(d, gc, nil)
	}

	d.pass("worktree: %s", repo.WorktreeRoot)
	d.note("shared database dir: %s", repo.CommonDir)

	dbPath := store.ProjectDBPath(repo.CommonDir)
	if _, err := os.Stat(dbPath); err != nil {
		d.warn("stigmergy is not enabled here — run `stigmergy init` to enable it")
		return finish(d, gc, nil)
	}

	p, err := store.OpenProject(repo.CommonDir)
	if err != nil {
		d.fail("the project database could not be opened: %v", err)
		d.note("Every edit is currently BLOCKED in Claude Code: the claim guard fails closed")
		d.note("when it cannot verify claims. Fix or remove %s", dbPath)
		return finish(d, gc, nil)
	}
	defer p.Close()

	v, _ := p.SchemaVersion()
	latest, _ := store.LatestVersion(store.Project)
	if v != latest {
		d.fail("the project database is at schema v%d but this stigmergy expects v%d", v, latest)
		d.note("A newer stigmergy wrote this database. Upgrade, or edits will stay blocked.")
	} else {
		d.pass("project database: %s (schema v%d)", p.Path, v)
	}
	if n, err := countMemories(p); err == nil {
		d.note("%d project memories", n)
	}
	if active, err := p.ActiveClaims(""); err == nil {
		if len(active) == 0 {
			d.pass("no claims are currently held")
		} else {
			d.pass("%d active claim(s):", len(active))
			for _, c := range active {
				scope := c.ScopePath
				if c.Recursive {
					scope += "/**"
				}
				d.note("%s — %s (%s, expires %s)", scope, c.Reason, c.RootID, c.ExpiresAt)
			}
		}
	}

	checkHostConfig(d, repo.WorktreeRoot)

	fmt.Fprintln(out, "\nScope")
	d.warn("stigmergy is cooperative, not a security boundary")
	d.note("It coordinates agents that participate. A shell command, a stray script, or an")
	d.note("agent without the hooks installed can still write a claimed file. Claims prevent")
	d.note("accidents between cooperating agents; they do not defend against anything.")

	return finish(d, gc, p)
}

// checkHostConfig verifies the project is actually wired up. A database with no
// host configuration is the quiet failure mode: everything looks fine, and no
// agent is ever told stigmergy exists.
func checkHostConfig(d *diag, worktree string) {
	mcpPath, settingsPath, claudeMemory := hostcfg.ClaudePaths(worktree)
	codexConfig, codexHooks, codexMemory := hostcfg.CodexPaths(worktree)

	antigravityMCP, antigravityHooks := antigravityConfigPaths(worktree)

	claude := fileContains(mcpPath, "stigmergy") && fileContains(settingsPath, "stigmergy hook")
	codex := fileContains(codexConfig, "mcp_servers.stigmergy") && fileContains(codexHooks, "stigmergy hook")
	// The same standard the other two are held to: a directory proves nothing,
	// and a half-written plugin that reports "configured" is worse than one that
	// reports nothing, because it is the answer someone stops investigating at.
	antigravity := fileContains(antigravityMCP, "stigmergy") && fileContains(antigravityHooks, "stigmergy hook")

	openCodeConfig, openCodePlugin := hostcfg.OpenCodePaths(worktree)
	opencode := fileContains(openCodeConfig, "stigmergy") && fileContains(openCodePlugin, "stigmergy hook")

	// Keyed by agent_kind and walked in registry order, so a host that exists but
	// is missing here shows up as a blank row rather than as nothing at all. The
	// old version was a hand-written list of three, and a fourth host could have
	// been fully installed and still reported "not configured" by never appearing.
	detected := map[string]bool{
		"claude-code": claude,
		"codex":       codex,
		"antigravity": antigravity,
		"opencode":    opencode,
	}

	var configured []string
	for _, h := range hosts.All() {
		if detected[h.Kind] {
			configured = append(configured, h.Name)
		}
	}

	switch len(configured) {
	case 0:
		d.fail("no host is configured — the database exists but no agent will use it")
		d.note("Run `stigmergy init`.")
	case 1:
		d.pass("%s is configured", configured[0])
	default:
		d.pass("%s are configured", joinWords(configured))
	}
	for _, h := range hosts.All() {
		if !detected[h.Kind] && len(configured) > 0 {
			d.warn("%s is not configured here — run `stigmergy init --host %s` if you use it", h.Name, h.Flag)
		}
	}

	// A stigmergy block used to be written into these files, and the check here
	// used to be the other way round: it warned when one was MISSING. Now the
	// instructions travel with the MCP server and the session-start hook, and a
	// block left behind is a liability rather than a help — it is a frozen copy
	// of what stigmergy said on the day it was written, and the agent has no way
	// to know it is out of date. Nothing breaks if it stays, so this reports
	// rather than deletes: the file is the user's, and `init --remove` is the
	// thing that takes stigmergy's text back out.
	for _, f := range []struct{ name, path string }{
		{"CLAUDE.md", claudeMemory},
		{"AGENTS.md", codexMemory},
	} {
		if hostcfg.HasMarkerBlock(f.path) {
			d.warn("%s still has a stigmergy block — it is no longer written or updated", f.name)
			d.note("The instructions now come from the MCP server and the session-start hook, so the")
			d.note("block is a stale copy that may contradict them. Delete it, or run")
			d.note("`stigmergy init --remove` and then `stigmergy init` to take it out cleanly.")
		}
	}
	// Two memory systems in one project is the divergence stigmergy exists to
	// end, and it is silent: both stores look healthy while drifting apart.
	if claude && !autoMemoryDisabled(settingsPath) {
		d.fail("Claude Code's auto memory is still enabled here")
		d.note("It writes a second, divergent memory store alongside stigmergy. Import anything")
		d.note("worth keeping (`stigmergy import claude-memory`), then re-run `stigmergy init`,")
		d.note("which sets \"autoMemoryEnabled\": false in .claude/settings.json.")
	}
	if codex {
		d.note("Codex loads project hooks only for TRUSTED projects: trust this project in Codex")
		d.note("and review the hooks with /hooks, or none of this takes effect there.")
	}
}

// autoMemoryDisabled reports whether Claude Code's native memory is switched off
// in this project's settings. A missing file or a missing key means it is ON:
// auto memory is enabled by default, so silence is not consent.
func autoMemoryDisabled(settingsPath string) bool {
	settings, err := hostcfg.ReadJSON(settingsPath)
	if err != nil {
		return false
	}
	enabled, ok := settings[hostcfg.AutoMemoryKey].(bool)
	return ok && !enabled
}

func fileContains(path, needle string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), needle)
}

// antigravityConfigPaths returns the two files that decide whether the
// Antigravity plugin is really installed: the MCP server registration and the
// hooks. The plugin directory existing says only that something once ran.
func antigravityConfigPaths(worktree string) (mcpConfig, hooksJSON string) {
	_, mcp, hooks := hostcfg.AntigravityPaths(worktree)
	return mcp, hooks
}

func joinWords(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	case 2:
		return words[0] + " and " + words[1]
	default:
		return strings.Join(words[:len(words)-1], ", ") + ", and " + words[len(words)-1]
	}
}

func countMemories(db *store.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM memories`).Scan(&n)
	return n, err
}

func finish(d *diag, gc bool, project *store.DB) error {
	if gc && project != nil {
		fmt.Fprintln(d.out, "\nHousekeeping")
		roots, err := project.ReapStaleRoots()
		if err != nil {
			d.warn("stale roots could not be reaped: %v", err)
		} else {
			d.pass("%d long-silent root(s) closed", roots)
		}
		audit, mail, err := project.GC()
		if err != nil {
			d.warn("old records could not be pruned: %v", err)
		} else {
			d.pass("%d old audit record(s) and %d resolved message(s) pruned", audit, mail)
		}
	}
	if d.failed {
		return fmt.Errorf("doctor found problems")
	}
	return nil
}
