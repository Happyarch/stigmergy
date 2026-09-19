package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/hostcfg"
	"github.com/happyarch/stigmergy/internal/hosts"
	"github.com/happyarch/stigmergy/internal/project"
	"github.com/happyarch/stigmergy/internal/store"
	"github.com/happyarch/stigmergy/internal/xdg"
)

func newDoctorCmd() *cobra.Command {
	var gc bool
	var all bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the stigmergy installation and this project",
		Long: "Check that stigmergy is installed, that this project is configured, and that the\n" +
			"databases are readable. Run this first whenever something is not working.\n\n" +
			"With --all, check and upgrade every project this machine knows about instead of\n" +
			"just this one. Run it immediately after installing a stigmergy that carries a new\n" +
			"migration: until a project's database is upgraded, its claim-guard hook fails\n" +
			"closed and every edit in it is blocked.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				return runDoctorAll(cmd.OutOrStdout(), gc)
			}
			return runDoctor(cmd.OutOrStdout(), gc)
		},
	}
	cmd.Flags().BoolVar(&gc, "gc", false, "also prune old audit records and resolved mail")
	cmd.Flags().BoolVar(&all, "all", false, "check and upgrade every known project, not just this one")
	return cmd
}

// runDoctorAll upgrades every project the machine knows about, in one pass.
//
// This is the answer to the migration hazard described in the 0002 global
// migration: a new binary makes every out-of-date project fail closed at once,
// and the recovery used to be a manual walk through repositories that nothing
// listed. Here the whole sweep is one command, and it reports what it changed.
//
// It opens each project read-write, which is what applies the migration — the
// same thing a plain `doctor` does for the project it is standing in.
func runDoctorAll(out io.Writer, gc bool) error {
	d := &diag{out: out}

	globalPath, err := xdg.GlobalDBPath()
	if err != nil {
		d.fail("the global database path could not be resolved: %v", err)
		return finish(d, false, nil)
	}
	global, err := store.OpenGlobal(globalPath)
	if err != nil {
		d.fail("the global database could not be opened: %v", err)
		return finish(d, false, nil)
	}
	defer global.Close()

	projects, err := global.KnownProjects()
	if err != nil {
		d.fail("the project registry could not be read: %v", err)
		return finish(d, false, nil)
	}

	latest, _ := store.LatestVersion(store.Project)
	fmt.Fprintf(out, "Known projects (%d) — this stigmergy expects schema v%d\n", len(projects), latest)
	if len(projects) == 0 {
		d.warn("no projects are registered yet")
		d.note("A project registers itself when you run `stigmergy init` or `stigmergy doctor`")
		d.note("inside it, or when an agent opens it. Run `stigmergy doctor` once in each")
		d.note("project you already use, and they will be covered from then on.")
		return finish(d, false, nil)
	}

	var upgraded, missing int
	for _, kp := range projects {
		if _, err := os.Stat(kp.DBPath); err != nil {
			// A deleted clone is the ordinary case here, not a fault: say so, and
			// leave the row alone. Reaping it automatically would silently drop a
			// project that is merely on an unmounted disk.
			missing++
			d.warn("%s — database is gone (%s)", kp.Label, kp.DBPath)
			continue
		}
		before, err := projectSchemaVersion(kp.DBPath)
		if err != nil {
			d.fail("%s — could not be read: %v", kp.Label, err)
			continue
		}
		p, err := store.OpenProjectAt(kp.DBPath)
		if err != nil {
			d.fail("%s — could not be upgraded: %v", kp.Label, err)
			d.note("Every edit in this project is BLOCKED until this is fixed.")
			continue
		}
		after, _ := p.SchemaVersion()
		if gc {
			_, _ = p.ReapStaleRoots()
			_, _, _, _ = p.GC()
		}
		// Text that would be refused today is worth surfacing in the sweep too:
		// the memories most likely to be unreadable are in the projects nobody
		// has opened lately, which is exactly what this command is for.
		reportUnreadable(d, p, kp.Label)
		p.Close()

		switch {
		case after != latest:
			d.fail("%s — schema v%d, but this stigmergy expects v%d", kp.Label, after, latest)
			d.note("A newer stigmergy wrote this database. Upgrade, or edits stay blocked.")
		case before != after:
			upgraded++
			d.pass("%s — upgraded v%d to v%d", kp.Label, before, after)
		default:
			d.pass("%s — schema v%d", kp.Label, after)
		}
	}

	fmt.Fprintln(out)
	d.note("%d project(s) upgraded, %d missing, %d checked", upgraded, missing, len(projects))
	return finish(d, false, nil)
}

// projectSchemaVersion reads a project's schema version without migrating it, so
// the sweep can report what it actually changed rather than only where it ended
// up. NoMigrate rather than ReadOnly: a read-only open cannot see WAL frames
// that have not been checkpointed, and reporting a stale version here would
// invent upgrades that never happened.
func projectSchemaVersion(dbPath string) (int, error) {
	p, err := store.OpenProjectAt(dbPath, store.NoMigrate())
	if err != nil {
		return 0, err
	}
	defer p.Close()
	return p.SchemaVersion()
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
	// Held past this block so the project section can register itself below.
	var global *store.DB
	globalPath, err := xdg.GlobalDBPath()
	if err != nil {
		d.fail("the global database path could not be resolved: %v", err)
	} else if g, err := store.OpenGlobal(globalPath); err != nil {
		d.fail("the global database could not be opened: %v", err)
	} else {
		defer g.Close()
		global = g
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
		repairTimestamps(d, g, "global")
		reportUnreadable(d, g, "global")
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

	proj, err := project.ResolveWithFallback(cwd)
	if err != nil {
		d.fail("this repository's project could not be resolved: %v", err)
		return finish(d, gc, nil)
	}
	dbPath := proj.DBPath
	if !proj.Adopted() {
		if proj.MultiRepo() {
			d.fail("this repository points at project %s, whose database is missing (%s)",
				proj.ID, dbPath)
			d.note("Every edit here is BLOCKED: the claim guard fails closed when it cannot")
			d.note("verify claims. Restore the database, or remove the pointer at %s",
				project.PointerPath(repo.CommonDir))
			return finish(d, gc, nil)
		}
		d.warn("stigmergy is not enabled here — run `stigmergy init` to enable it")
		return finish(d, gc, nil)
	}
	if proj.MultiRepo() {
		d.pass("project %s (shared by several repositories)", proj.ID)
		// A stray per-repository database beside a pointer is the exact state the
		// init guard exists to prevent, so say so if one turns up anyway.
		if _, err := os.Stat(store.ProjectDBPath(repo.CommonDir)); err == nil {
			d.warn("a stray per-repository database sits beside the pointer: %s",
				store.ProjectDBPath(repo.CommonDir))
			d.note("It is NOT the one in use and nothing reads it. Move it aside to avoid confusion.")
		}
	}

	p, err := store.OpenProjectAt(dbPath)
	if err != nil {
		d.fail("the project database could not be opened: %v", err)
		d.note("Every edit is currently BLOCKED in Claude Code: the claim guard fails closed")
		d.note("when it cannot verify claims. Fix or remove %s", dbPath)
		return finish(d, gc, nil)
	}
	defer p.Close()

	// Adopt any claim still carrying the pre-0007 empty repo id, and make sure
	// this repository has a row of its own. Doctor is one of the few places that
	// may write schema-shaped data, and it is where an already-adopted project
	// picks this up without anyone having to re-run init.
	// Single-repository projects only — see EnsureSelfRepo. A member of a
	// multi-repo project is already registered, and running it there would
	// re-attribute any legacy claim to whichever member doctor was run from.
	if !proj.MultiRepo() {
		if err := p.EnsureSelfRepo(project.SlugFor(repo.WorktreeRoot), repo.CommonDir, repo.WorktreeRoot); err != nil {
			d.warn("this repository could not be recorded: %v", err)
		}
	}

	// Running doctor here is proof this project is real and in use, so record it.
	// Best-effort: a registry write must never turn a diagnostic into a failure.
	if global != nil {
		if err := global.RememberProject(p.Path, p.Label(repo.WorktreeRoot)); err != nil {
			d.warn("this project could not be added to the registry: %v", err)
			d.note("`stigmergy doctor --all` will not reach it until this succeeds.")
		}
	}

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
	repairTimestamps(d, p, "project")
	reportUnreadable(d, p, "project")
	if active, err := p.ActiveClaims(""); err == nil {
		if len(active) == 0 {
			d.pass("no claims are currently held")
		} else {
			d.pass("%d active claim(s):", len(active))
			for _, c := range active {
				scope := c.Qualified()
				if c.Recursive {
					scope += "/**"
				}
				d.note("%s — %s (%s, expires %s)", scope, c.Reason, c.RootID, c.ExpiresAt)
			}
		}
	}

	// The member table, and a host check per member.
	//
	// Checking only the repository you happen to be standing in is how a project
	// ends up half-wired: one member coordinating, another silently not, and no
	// way to tell from inside the working one.
	if err := proj.Load(p); err != nil {
		d.warn("the project's repositories could not be read: %v", err)
	}
	if len(proj.Members) > 1 {
		d.pass("%d repositories in this project:", len(proj.Members))
		for _, m := range proj.Members {
			here := ""
			if m.ID == proj.SelfID {
				here = "  <- you are here"
			}
			if _, err := os.Stat(m.WorktreeRoot); err != nil {
				d.fail("  %-22s %s  MISSING", m.ID, m.WorktreeRoot)
				d.note("  Its claims cannot be resolved. Restore it, or")
				d.note("  `stigmergy project remove %s` to release them.", m.ID)
				continue
			}
			d.note("  %-22s %s%s", m.ID, m.WorktreeRoot, here)
		}
		// The pointer and the roster have to agree, or a claim is attributed to a
		// repository that does not exist as far as the database is concerned.
		if proj.SelfID != "" && proj.Member(proj.SelfID) == nil {
			d.fail("this repository calls itself %q, which the project does not list", proj.SelfID)
			d.note("Re-add it: `stigmergy project add %s`", repo.WorktreeRoot)
		}
		anyCodex := false
		for _, m := range proj.Members {
			anyCodex = checkHostConfig(d, m.ID+": ", m.WorktreeRoot) || anyCodex
		}
		codexTrustNote(d, anyCodex)
	} else {
		codexTrustNote(d, checkHostConfig(d, "", repo.WorktreeRoot))
	}

	fmt.Fprintln(out, "\nScope")
	d.warn("stigmergy is cooperative, not a security boundary")
	d.note("It coordinates agents that participate. A shell command, a stray script, or an")
	d.note("agent without the hooks installed can still write a claimed file. Claims prevent")
	d.note("accidents between cooperating agents; they do not defend against anything.")

	return finish(d, gc, p)
}

// codexTrustNote says the one thing about Codex that is true of the run rather
// than of a repository, and says it at most once.
//
// It used to be printed from inside checkHostConfig under `label == ""`, which is
// only ever true in the single-repository branch — so no multi-repo project ever
// saw it, however many of its members were wired for Codex.
func codexTrustNote(d *diag, codex bool) {
	if !codex {
		return
	}
	d.note("Codex loads project hooks only for TRUSTED projects: trust this project in Codex")
	d.note("and review the hooks with /hooks, or none of this takes effect there.")
}

// checkHostConfig verifies the project is actually wired up. A database with no
// host configuration is the quiet failure mode: everything looks fine, and no
// agent is ever told stigmergy exists.
//
// It reports whether Codex is configured here, for the once-per-run note its
// caller prints.
func checkHostConfig(d *diag, label, worktree string) bool {
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
	// A V1 install that was never re-initialized is migrated here rather than
	// only reported: the plugin file is stigmergy's own (regenerated on every
	// init), and InstallOpenCode merges the config without touching the user's
	// keys. A V1 plugin does not run on opencode V2 at all, so "configured"
	// without migration is the quiet failure this check exists to prevent.
	// This is the same standing doctor already claims for EnsureSelfRepo
	// above: a diagnostic that heals what it owns without a re-run of init.
	opencode := fileContains(openCodeConfig, "stigmergy") &&
		(fileContains(openCodePlugin, "stigmergy hook") ||
			fileContains(hostcfg.OpenCodeLegacyPluginPath(worktree), "stigmergy hook"))
	if opencode {
		if migrated, err := migrateOpenCodeIfStale(worktree); err != nil {
			d.warn("%sopencode install could not be migrated: %v", label, err)
			d.note("Run `stigmergy init --host opencode` in %s.", worktree)
		} else if migrated {
			d.pass("%sopencode install migrated to the current shape", label)
		}
	}

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

	// Every line is prefixed with which repository it is about, once a project
	// has more than one. Without it a two-member project printed two identical
	// verdicts, and a project with one member misconfigured printed a PASS and a
	// FAIL with nothing saying which was which.
	switch len(configured) {
	case 0:
		d.fail("%sno host is configured — the database exists but no agent will use it", label)
		d.note("Run `stigmergy init` in %s.", worktree)
	case 1:
		d.pass("%s%s is configured", label, configured[0])
	default:
		d.pass("%s%s are configured", label, joinWords(configured))
	}
	for _, h := range hosts.All() {
		if !detected[h.Kind] && len(configured) > 0 {
			d.warn("%s%s is not configured — run `stigmergy init --host %s` there if you use it",
				label, h.Name, h.Flag)
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
	return codex
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

// migrateOpenCodeIfStale rewrites a stale opencode install in place and
// reports whether anything changed. InstallOpenCode is idempotent — a current
// install comes back byte-identical — so the snapshot comparison is what
// distinguishes "migrated" from "already current". It runs only when
// stigmergy markers are already present; doctor never enables a host the user
// did not ask for.
func migrateOpenCodeIfStale(worktree string) (bool, error) {
	configPath, pluginPath := hostcfg.OpenCodePaths(worktree)
	before := snapshotOpenCode(configPath, pluginPath, hostcfg.OpenCodeLegacyPluginPath(worktree))
	if err := hostcfg.InstallOpenCode(worktree); err != nil {
		return false, err
	}
	after := snapshotOpenCode(configPath, pluginPath, hostcfg.OpenCodeLegacyPluginPath(worktree))
	return !before.equal(after), nil
}

// openCodeSnapshot is the bytes doctor compares to tell a migration from a
// no-op. Missing files snapshot as nil, which compares unequal to any install
// output — but the caller only runs the install when markers are present, so
// a nil here means a half-written install, which is exactly what migrates.
type openCodeSnapshot struct {
	config []byte
	plugin []byte
	legacy []byte
}

func snapshotOpenCode(configPath, pluginPath, legacyPath string) openCodeSnapshot {
	return openCodeSnapshot{
		config: readIfPresent(configPath),
		plugin: readIfPresent(pluginPath),
		legacy: readIfPresent(legacyPath),
	}
}

func (s openCodeSnapshot) equal(o openCodeSnapshot) bool {
	return bytes.Equal(s.config, o.config) &&
		bytes.Equal(s.plugin, o.plugin) &&
		bytes.Equal(s.legacy, o.legacy)
}

func readIfPresent(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
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

// repairTimestamps canonicalises stored memory timestamps, in whichever scope it
// is handed.
//
// It runs unconditionally, not under --gc: --gc means retention pruning, which
// deletes things, and this deletes nothing and changes no content. It is a
// lossless rewrite of how an instant is spelled, and every read path already
// tolerates the un-repaired form — so a database that never sees doctor still
// sorts correctly. This only makes the common case clean.
//
// A timestamp nobody can parse is a failure rather than a warning: it cannot be
// ordered, and the memory it belongs to will error on every read until a human
// decides what the right instant was. There is nothing safe to guess.
// reportUnreadable names memories whose text would be refused today.
//
// A warning and not a failure: these are already stored, they were accepted by
// the rules in force when they were written, and most are still perfectly
// legible to a person. The one that is not — a whole document collapsed onto a
// single line because it was escaped twice — needs an agent that knows what it
// was meant to say, not a repair pass that guesses.
func reportUnreadable(d *diag, db *store.DB, scope string) {
	problems, err := db.UnreadableMemories()
	if err != nil {
		d.warn("%s memories could not be checked for unreadable text: %v", scope, err)
		return
	}
	if len(problems) == 0 {
		return
	}
	d.warn("%d %s memory/memories contain text that would be refused today:", len(problems), scope)
	for _, p := range problems {
		d.note("%s — %s", p.Key, p.Reason)
	}
	d.note("They are readable enough to fix: read each one and write it back with")
	d.note("memory_write under its current version. Nothing rewrites them automatically,")
	d.note("because un-escaping a body means guessing what its author meant.")
}

func repairTimestamps(d *diag, db *store.DB, scope string) {
	rep, err := db.RepairMemoryTimestamps()
	if err != nil {
		d.warn("%s memory timestamps could not be repaired: %v", scope, err)
		return
	}
	if rep.Repaired > 0 {
		d.pass("%d %s memory timestamp(s) rewritten in canonical form", rep.Repaired, scope)
	}
	// The same fixed-width requirement applies to every timestamp compared in
	// SQL, and two of them decide whether a claim still binds and whether a root
	// is alive. Reported as a count: these are the system's own bookkeeping
	// rather than anything an agent wrote.
	if other, err := db.RepairStampColumns(); err != nil {
		d.warn("%s bookkeeping timestamps could not be repaired: %v", scope, err)
	} else if other.Repaired > 0 {
		d.pass("%d %s claim/root/mail timestamp(s) rewritten in canonical form", other.Repaired, scope)
	}
	if len(rep.Bad) > 0 {
		d.fail("%d %s memory timestamp(s) are unreadable: %s", len(rep.Bad), scope, strings.Join(rep.Bad, ", "))
		d.note("Reading these memories fails. They were left untouched — no instant can be")
		d.note("guessed safely. Fix them by hand in the database, or delete and rewrite them.")
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
		audit, mail, episodes, err := project.GC()
		if err != nil {
			d.warn("old records could not be pruned: %v", err)
		} else {
			d.pass("%d old audit record(s), %d resolved message(s) and %d stale episode(s) pruned", audit, mail, episodes)
		}
	}
	if d.failed {
		return fmt.Errorf("doctor found problems")
	}
	return nil
}
