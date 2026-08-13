package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/project"
	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
	"github.com/happyarch/stigmergy/internal/syncgit"
	"github.com/happyarch/stigmergy/internal/syncx"
	"github.com/happyarch/stigmergy/internal/xdg"
)

// Stage A of cross-machine memory sync (docs/sync-model.md). This file
// implements the primitives — export and import against a plain directory —
// plus the local bookkeeping around them: status, enable, share/unshare, and
// conflict resolution. It deliberately does NOT implement `sync init`, a bare
// `stigmergy sync`, or anything that shells out to git for a remote: those are
// Stage B (§8), and shipping them half-done would be worse than not shipping
// them. Every git subprocess call in this file (rootCommitFingerprint) is
// read-only, local, and only ever runs from a CLI command — never from
// internal/hooks, which D14 forbids from reaching a sync transport at all.
//
// docs/sync-model.md §6.3 (D9) is why none of this is reachable over MCP: a
// conflict needs a human, and an agent that can push can push private
// memories on a model's say-so.

func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Move memories between this developer's own machines",
		Long: "Cross-machine memory sync. `export` writes this\n" +
			"machine's syncable memories to a plain directory, and `import` reads another machine's\n" +
			"export back in. Move the directory between machines however you like (a USB stick, a\n" +
			"Syncthing folder, or a private git repository). `sync init --remote` configures the\n" +
			"git or directory transport; bare `sync` runs it. See docs/sync.md.",
	}
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		dry, _ := c.Flags().GetBool("dry-run")
		accept, _ := c.Flags().GetBool("accept-rollback")
		confirm, _ := c.Flags().GetBool("confirm")
		return runSync(c.OutOrStdout(), dry, accept, confirm)
	}
	cmd.AddCommand(newSyncExportCmd())
	cmd.AddCommand(newSyncImportCmd())
	cmd.AddCommand(newSyncStatusCmd())
	cmd.AddCommand(newSyncEnableCmd())
	cmd.AddCommand(newSyncShareCmd())
	cmd.AddCommand(newSyncUnshareCmd())
	cmd.AddCommand(newSyncResolveCmd())
	cmd.AddCommand(newSyncInitCmd())
	cmd.Flags().Bool("dry-run", false, "print the plan without changing a database or transport")
	cmd.Flags().Bool("accept-rollback", false, "allow a restored device identity to push")
	cmd.Flags().Bool("confirm", false, "confirm the first transmission of plaintext memories to this remote")
	return cmd
}

func newSyncInitCmd() *cobra.Command {
	var remote string
	cmd := &cobra.Command{Use: "init --remote <git-url|dir:path>", Short: "Configure the private sync transport", SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error { return runSyncInit(c.OutOrStdout(), remote) }}
	cmd.Flags().StringVar(&remote, "remote", "", "private git remote or dir:path")
	_ = cmd.MarkFlagRequired("remote")
	return cmd
}

func runSyncInit(out io.Writer, remote string) error {
	g, err := openSyncGlobal()
	if err != nil {
		return err
	}
	defer g.Close()
	if strings.TrimSpace(remote) == "" {
		return errors.New("--remote must not be empty")
	}
	if !strings.HasPrefix(remote, "dir:") {
		if err := refuseOriginRemote(remote); err != nil {
			return err
		}
	}
	if err := g.SetMeta(store.MetaSyncRemoteKey, remote); err != nil {
		return err
	}
	fmt.Fprintf(out, "sync transport configured: %s\n", remote)
	return nil
}

// runSync performs the Stage B wrapper around Stage A's directory primitives.
func runSync(out io.Writer, dryRun, acceptRollback, confirm bool) error {
	g, err := openSyncGlobal()
	if err != nil {
		return err
	}
	defer g.Close()
	remote := os.Getenv("STIGMERGY_SYNC_REMOTE")
	if remote == "" {
		remote = g.Meta(store.MetaSyncRemoteKey)
	}
	seq, _ := strconv.Atoi(g.Meta(store.MetaSyncSeqKey))
	if seq == 0 && !dryRun && !confirm {
		fmt.Fprintf(out, "first sync dry-run: %s will receive plaintext memories; review the remote and re-run with --confirm\n", remote)
		return nil
	}
	if remote == "" {
		return errors.New("sync has no remote — run `stigmergy sync init --remote …` first")
	}
	if !strings.HasPrefix(remote, "dir:") {
		if err := refuseOriginRemote(remote); err != nil {
			return err
		}
	}
	dir, cleanup, err := syncWorkingTree(remote)
	if err != nil {
		return err
	}
	defer cleanup()
	if !strings.HasPrefix(remote, "dir:") {
		r := syncgit.Runner{Dir: dir}
		_ = r.Fetch()
	}
	if dryRun {
		fmt.Fprintf(out, "sync dry-run against %s\n", remote)
		return nil
	}
	if err := refuseRollback(dir, g, acceptRollback); err != nil {
		return err
	}
	if err := runSyncImport(out, dir); err != nil {
		return err
	}
	if err := runSyncExport(out, dir); err != nil {
		return err
	}
	if err := runSyncKnownProjects(out, dir, g); err != nil {
		return err
	}
	device, err := g.LocalDeviceID()
	if err != nil {
		return err
	}
	seq++
	if err := writeDeviceSequence(dir, device, deviceLabel(g), seq); err != nil {
		return err
	}
	if err := g.SetMeta(store.MetaSyncSeqKey, strconv.Itoa(seq)); err != nil {
		return err
	}
	if strings.HasPrefix(remote, "dir:") {
		fmt.Fprintln(out, "sync complete (directory transport)")
		return nil
	}
	r := syncgit.Runner{Dir: dir}
	if err := r.AddAll(); err != nil {
		return err
	}
	if err := r.Commit("stigmergy sync"); err != nil {
		return err
	}
	if err := r.Push(); err != nil {
		if fetchErr := r.Fetch(); fetchErr != nil {
			return fmt.Errorf("sync push rejected and fetch failed: %w", fetchErr)
		}
		// The working copy is disposable transport state, so reset it to the
		// fetched commit rather than asking git to merge memory bodies. Stage A
		// then reconciles the two sides semantically and writes the retry commit.
		if resetErr := r.Run("reset", "--hard", "origin/main"); resetErr != nil {
			return fmt.Errorf("sync push rejected and transport reset failed: %w", resetErr)
		}
		if err := runSyncImport(out, dir); err != nil {
			return err
		}
		if err := runSyncExport(out, dir); err != nil {
			return err
		}
		if err := runSyncKnownProjects(out, dir, g); err != nil {
			return err
		}
		if err := r.AddAll(); err != nil {
			return err
		}
		if err := r.Commit("stigmergy sync retry"); err != nil {
			return err
		}
		if err := r.Push(); err != nil {
			return fmt.Errorf("sync push was rejected after one retry: %w", err)
		}
	}
	fmt.Fprintln(out, "sync complete")
	return nil
}

// runSyncKnownProjects gives bare sync the same machine-wide reach as doctor
// --all. The current project was handled by Stage A's public primitives above;
// repeating it here is harmless because import and export are idempotent.
func runSyncKnownProjects(out io.Writer, dir string, global *store.DB) error {
	projects, err := global.KnownProjects()
	if err != nil {
		return err
	}
	device, err := global.LocalDeviceID()
	if err != nil {
		return err
	}
	for _, known := range projects {
		p, err := store.OpenProjectAt(known.DBPath, store.NoMigrate())
		if err != nil {
			return fmt.Errorf("project %q: %w", known.Label, err)
		}
		if err := refuseOnSchemaSkew(p, store.Project); err != nil {
			p.Close()
			return err
		}
		name := p.Meta(store.MetaSyncProjectKey)
		if name == "" {
			p.Close()
			continue
		}
		pdir := filepath.Join(dir, "projects", name)
		_, err = importScope(p, "project", pdir, deviceLabel(global))
		if err == nil {
			_, err = exportScope(pdir, p, device)
		}
		if err == nil {
			err = writeProjectJSON(pdir, p)
		}
		p.Close()
		if err != nil {
			return fmt.Errorf("project %q: %w", name, err)
		}
		fmt.Fprintf(out, "project %q: synced\n", name)
	}
	return nil
}

func syncWorkingTree(remote string) (string, func(), error) {
	if strings.HasPrefix(remote, "dir:") {
		d := strings.TrimPrefix(remote, "dir:")
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", nil, err
		}
		return d, func() {}, nil
	}
	base := os.Getenv("STIGMERGY_SYNC_HOME")
	if base == "" {
		d, err := xdg.DataDir()
		if err != nil {
			return "", nil, err
		}
		base = filepath.Join(d, "sync")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", nil, err
	}
	d := filepath.Join(base, "transport")
	if _, err := os.Stat(filepath.Join(d, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := (syncgit.Runner{}).Clone(remote, d); err != nil {
			return "", nil, err
		}
	}
	return d, func() {}, nil
}

func refuseOriginRemote(remote string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	p, err := project.ResolveWithFallback(cwd)
	if err != nil || !p.Adopted() {
		return nil
	}
	r := syncgit.Runner{Dir: cwd}
	origin, err := r.RemoteURL("origin")
	if err == nil && origin == remote {
		return fmt.Errorf("sync remote is this project's origin (%s); refusing to send private memories there", remote)
	}
	return nil
}

// syncAgentKind is the free-text agent_kind sync's own writes are attributed
// under in the audit log — a human running a CLI command, not a host. There is
// no CHECK on this column (migration 0006 dropped it), and internal/importer's
// "" precedent already establishes that a non-host value is normal here.
const syncAgentKind = "sync"

// ---------------------------------------------------------------------------
// export
// ---------------------------------------------------------------------------

func newSyncExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <dir>",
		Short: "Write this machine's syncable memories to a directory tree",
		Long: "Write the global scope and, when run inside an enabled project, the project scope to\n" +
			"<dir>, in the format docs/sync-model.md §5 describes: one markdown file per memory, plus\n" +
			"links.jsonl and tombstones.jsonl. If <dir> already holds a tree from a previous export or\n" +
			"an import, this reconciles against it first (docs/sync-model.md §3.4) so a memory the other\n" +
			"machine has since changed is never silently overwritten — that key is left for `sync\n" +
			"resolve` instead.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncExport(cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

func runSyncExport(out io.Writer, dir string) error {
	if err := refuseIfSQLite(dir); err != nil {
		return err
	}
	if err := refuseOnNewerFormat(dir); err != nil {
		return err
	}

	global, err := openSyncGlobal()
	if err != nil {
		return err
	}
	defer global.Close()

	deviceID, err := global.LocalDeviceID()
	if err != nil {
		return err
	}
	label := deviceLabel(global)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeFormatFile(dir); err != nil {
		return err
	}
	if err := writeDeviceFile(dir, deviceID, label); err != nil {
		return err
	}

	gStats, err := exportScope(filepath.Join(dir, "global"), global, deviceID)
	if err != nil {
		return fmt.Errorf("global scope: %w", err)
	}
	fmt.Fprintf(out, "global: %s\n", gStats)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	proj, perr := project.ResolveWithFallback(cwd)
	if perr != nil || !proj.Adopted() {
		return nil
	}
	pdb, err := openSyncProject(proj)
	if err != nil {
		return err
	}
	defer pdb.Close()

	name := pdb.Meta(store.MetaSyncProjectKey)
	if name == "" {
		fmt.Fprintln(out, "project: not exported — run `stigmergy sync enable` first")
		return nil
	}
	pDir := filepath.Join(dir, "projects", name)
	if err := writeProjectJSON(pDir, pdb); err != nil {
		return err
	}
	pStats, err := exportScope(pDir, pdb, deviceID)
	if err != nil {
		return fmt.Errorf("project %q: %w", name, err)
	}
	fmt.Fprintf(out, "project %q: %s\n", name, pStats)
	return nil
}

// exportStats is a one-line summary of what one export pass did to one scope.
type exportStats struct {
	pushed, agreed, skippedPull, skippedConflict, deleted int
}

func (s exportStats) String() string {
	return fmt.Sprintf("%d pushed, %d agreed, %d left for import, %d conflicted (untouched), %d deleted",
		s.pushed, s.agreed, s.skippedPull, s.skippedConflict, s.deleted)
}

// exportScope reconciles this database's syncable content against whatever is
// already on disk at scopeDir (empty, on a first export) and writes the
// result. It never overwrites a file this machine does not have the newest
// agreed content for — Reconcile decides that, not a blind directory dump.
func exportScope(scopeDir string, db *store.DB, deviceID string) (exportStats, error) {
	var stats exportStats

	mine, err := localSide(db)
	if err != nil {
		return stats, err
	}
	theirs, err := readSideFromTree(scopeDir)
	if err != nil {
		return stats, err
	}
	base, err := db.SyncBases()
	if err != nil {
		return stats, err
	}

	decisions := syncx.Reconcile(mine, theirs, base)

	memDir := filepath.Join(scopeDir, "memories")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		return stats, err
	}

	for _, dec := range decisions {
		switch dec.Action {
		case syncx.ActionPush, syncx.ActionAgree:
			if dec.Mine == nil {
				continue
			}
			b, err := syncx.MarshalMemory(*dec.Mine)
			if err != nil {
				return stats, err
			}
			if err := os.WriteFile(filepath.Join(memDir, dec.Key+".md"), b, 0o600); err != nil {
				return stats, err
			}
			if err := db.SetSyncBase(dec.Key, dec.Mine.Digest, deviceID); err != nil {
				return stats, err
			}
			if dec.Action == syncx.ActionPush {
				stats.pushed++
			} else {
				stats.agreed++
			}
		case syncx.ActionDeleteRemote:
			_ = os.Remove(filepath.Join(memDir, dec.Key+".md"))
			stats.deleted++
		case syncx.ActionPull, syncx.ActionDeleteLocal:
			// The remote's copy is the one that should survive; leave the tree
			// file exactly as it is so `sync import` sees it on this machine's
			// next run. Pushing here would be exactly the last-writer-wins
			// hazard docs/sync-model.md rejects (A.7).
			stats.skippedPull++
		case syncx.ActionConflict:
			// Neither side is written (D4). The tree keeps whatever it already
			// had; `sync resolve` is what settles this key.
			stats.skippedConflict++
		case syncx.ActionNone:
			// Nothing moved.
		}
	}

	links, err := db.ListLinks()
	if err != nil {
		return stats, err
	}
	if err := writeJSONL(filepath.Join(scopeDir, "links.jsonl"), wireLinksFrom(links)); err != nil {
		return stats, err
	}

	tombs, err := db.SyncTombstones()
	if err != nil {
		return stats, err
	}
	if err := writeJSONL(filepath.Join(scopeDir, "tombstones.jsonl"), tombs); err != nil {
		return stats, err
	}

	return stats, nil
}

// ---------------------------------------------------------------------------
// import
// ---------------------------------------------------------------------------

func newSyncImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <dir>",
		Short: "Apply another machine's export into this machine's databases",
		Long: "Read a tree written by `sync export` on another machine and reconcile it into this\n" +
			"machine's global scope and, when run inside an enabled project, the matching project\n" +
			"scope. A key that changed on both machines is never merged (docs/sync-model.md §3.5): it\n" +
			"is staged for `sync resolve` and left untouched here. This only ever writes to the local\n" +
			"databases — it never modifies <dir>. Run `sync export` afterwards to push what this run\n" +
			"decided in the other direction.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncImport(cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

func runSyncImport(out io.Writer, dir string) error {
	if err := refuseIfSQLite(dir); err != nil {
		return err
	}
	if err := refuseOnNewerFormat(dir); err != nil {
		return err
	}

	global, err := openSyncGlobal()
	if err != nil {
		return err
	}
	defer global.Close()
	label := deviceLabel(global)

	gStats, err := importScope(global, "global", filepath.Join(dir, "global"), label)
	if err != nil {
		return fmt.Errorf("global scope: %w", err)
	}
	fmt.Fprintf(out, "global: %s\n", gStats)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	proj, perr := project.ResolveWithFallback(cwd)
	if perr != nil || !proj.Adopted() {
		return nil
	}
	pdb, err := openSyncProject(proj)
	if err != nil {
		return err
	}
	defer pdb.Close()

	name := pdb.Meta(store.MetaSyncProjectKey)
	if name == "" {
		fmt.Fprintln(out, "project: not imported — run `stigmergy sync enable` first")
		return nil
	}
	pStats, err := importScope(pdb, "project", filepath.Join(dir, "projects", name), label)
	if err != nil {
		return fmt.Errorf("project %q: %w", name, err)
	}
	fmt.Fprintf(out, "project %q: %s\n", name, pStats)
	return nil
}

type importStats struct {
	pulled, agreed, conflicts, deletedLocal, linksCreated, linksDeleted, skipped int
	// linksReasonKept counts pairs both machines hold with different reasons.
	// The local reason wins and the remote's is discarded — see
	// applyImportLinks for why the §3.6 tie-break is not implemented in Stage
	// A. It is counted rather than passed over in silence: a run that quietly
	// drops one side's prose reads as a run that had nothing to drop, and the
	// remedy (unlink and relink with the reason wanted) needs the operator to
	// know there was a choice at all.
	linksReasonKept int
}

func (s importStats) String() string {
	out := fmt.Sprintf("%d pulled, %d agreed, %d conflicts staged, %d deleted locally, "+
		"%d link(s) created, %d link(s) deleted, %d skipped",
		s.pulled, s.agreed, s.conflicts, s.deletedLocal, s.linksCreated, s.linksDeleted, s.skipped)
	if s.linksReasonKept > 0 {
		out += fmt.Sprintf("\n  %d link(s) kept the local reason; the remote's differs "+
			"— unlink and relink to adopt it", s.linksReasonKept)
	}
	return out
}

func importScope(db *store.DB, scope, scopeDir, label string) (importStats, error) {
	var stats importStats

	deviceID, err := db.LocalDeviceID()
	if err != nil {
		return stats, err
	}
	mine, err := localSide(db)
	if err != nil {
		return stats, err
	}
	theirs, err := readSideFromTree(scopeDir)
	if err != nil {
		return stats, err
	}
	base, err := db.SyncBases()
	if err != nil {
		return stats, err
	}

	decisions := syncx.Reconcile(mine, theirs, base)
	for _, dec := range decisions {
		switch dec.Action {
		case syncx.ActionPull:
			if dec.Theirs == nil {
				continue
			}
			var expected *int
			if cur, err := db.ReadMemory(dec.Key); err == nil {
				v := cur.Version
				expected = &v
			} else if !errors.Is(err, store.ErrNotFound) {
				return stats, err
			}
			_, err := db.ImportMemory(store.MemoryImport{
				Key: dec.Theirs.Key, Type: dec.Theirs.Type, Description: dec.Theirs.Description,
				Body: dec.Theirs.Body, CreatedAt: dec.Theirs.CreatedAt, UpdatedAt: dec.Theirs.UpdatedAt,
				UpdatedBy: "sync:" + originLabel(dec.Theirs), ExpectedVersion: expected,
			}, syncAgentKind)
			if err != nil {
				if e, ok := serr.As(err); ok && e.Code == serr.CASConflict {
					// An agent wrote this key locally while the run was in
					// flight. Reported and left for the next run rather than
					// retried blind — a blind retry could overwrite exactly
					// the concurrent write CAS exists to protect.
					stats.skipped++
					continue
				}
				return stats, err
			}
			if err := db.SetSyncBase(dec.Key, dec.Theirs.Digest, deviceID); err != nil {
				return stats, err
			}
			stats.pulled++
		case syncx.ActionAgree:
			digest := ""
			if dec.Mine != nil {
				digest = dec.Mine.Digest
			} else if dec.Theirs != nil {
				digest = dec.Theirs.Digest
			}
			if digest == "" {
				continue
			}
			if err := db.SetSyncBase(dec.Key, digest, deviceID); err != nil {
				return stats, err
			}
			stats.agreed++
		case syncx.ActionConflict:
			if dec.Theirs == nil {
				// A tombstone/edit divergence: the remote deleted, this
				// machine edited. There is no remote MemoryRecord to stage —
				// keeping the local edit IS the resolution (§3.7: "never
				// silently destroy beats converge"), so nothing more to do.
				stats.conflicts++
				continue
			}
			if err := stageConflict(scope, dec.Key, dec.Theirs); err != nil {
				return stats, err
			}
			stats.conflicts++
		case syncx.ActionDeleteLocal:
			cur, err := db.ReadMemory(dec.Key)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return stats, err
			}
			if _, _, err := db.DeleteMemory(dec.Key, cur.Version, "sync:"+label, syncAgentKind); err != nil {
				if e, ok := serr.As(err); ok && e.Code == serr.CASConflict {
					stats.skipped++
					continue
				}
				return stats, err
			}
			stats.deletedLocal++
		case syncx.ActionPush, syncx.ActionDeleteRemote, syncx.ActionNone:
			// export's job, or nothing to do.
		}
	}

	links, err := readJSONL[wireLink](filepath.Join(scopeDir, "links.jsonl"))
	if err != nil {
		return stats, err
	}
	tombs, err := readJSONL[syncx.Tombstone](filepath.Join(scopeDir, "tombstones.jsonl"))
	if err != nil {
		return stats, err
	}
	created, deleted, skipped, reasonKept, err := applyImportLinks(db, links, tombs, label)
	if err != nil {
		return stats, err
	}
	stats.linksCreated, stats.linksDeleted = created, deleted
	stats.linksReasonKept = reasonKept
	stats.skipped += skipped

	return stats, nil
}

// applyImportLinks unions the remote's link set into the local one and
// propagates link tombstones — docs/sync-model.md §3.6.
//
// Stage A simplification, stated rather than hidden: when both sides hold a
// pair with a DIFFERENT reason, §3.6 specifies taking the earlier created_at
// (tie-broken by device id) and reporting the discarded reason. This function
// does not implement that tie-break — there is no store API to import a link
// with a caller-supplied created_at (CreateLink always stamps Now()), and
// adding one was judged out of scope for Stage A's memory-focused contract. A
// pair already present locally, whatever its reason, is left alone. Only a
// pair missing on one side (a true union case) or a tombstoned pair (a true
// delete case) is acted on here.
func applyImportLinks(db *store.DB, links []wireLink, tombs []syncx.Tombstone, label string) (created, deleted, skipped, reasonKept int, err error) {
	existing, err := db.ListLinks()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	have := make(map[string]store.Link, len(existing))
	for _, l := range existing {
		have[l.KeyA+" "+l.KeyB] = l
	}

	for _, t := range tombs {
		if t.Kind != "link" {
			continue
		}
		l, ok := have[t.Ident]
		if !ok {
			continue
		}
		if syncx.Digest(l.KeyA, l.KeyB, l.Reason, "") != t.Digest {
			// The local link changed since the digest this tombstone names —
			// delete/edit divergence, same rule as a memory (§3.7): reported,
			// not auto-resolved. Stage A has no `sync resolve` path for a
			// link, so this is surfaced only via the skipped count.
			skipped++
			continue
		}
		if _, err := db.DeleteLink(l.KeyA, l.KeyB, "sync:"+label, syncAgentKind); err != nil {
			return created, deleted, skipped, reasonKept, err
		}
		delete(have, t.Ident)
		deleted++
	}

	for _, wl := range links {
		ident := wl.KeyA + " " + wl.KeyB
		if local, ok := have[ident]; ok {
			// Both sides hold the pair. The reasons may still disagree, and
			// this is where §3.6's tie-break would go. Until it does, the local
			// reason stands and the divergence is counted so the run can say so.
			if local.Reason != wl.Reason {
				reasonKept++
			}
			continue
		}
		if _, err := db.ReadMemory(wl.KeyA); errors.Is(err, store.ErrNotFound) {
			skipped++
			continue
		} else if err != nil {
			return created, deleted, skipped, reasonKept, err
		}
		if _, err := db.ReadMemory(wl.KeyB); errors.Is(err, store.ErrNotFound) {
			skipped++
			continue
		} else if err != nil {
			return created, deleted, skipped, reasonKept, err
		}
		if _, err := db.CreateLink(wl.KeyA, wl.KeyB, wl.Reason, "sync:"+label, syncAgentKind); err != nil {
			if _, ok := serr.As(err); ok {
				// A duplicate race or the fan-effect cap: reported, not fatal.
				skipped++
				continue
			}
			return created, deleted, skipped, reasonKept, err
		}
		created++
	}
	return created, deleted, skipped, reasonKept, nil
}

// originLabel names the machine a pulled record came from, for the
// "sync:<device-label>" attribution (docs/sync-model.md §4.4). Stage A does
// not read the origin machine's devices/<id>.json for its human label — doing
// so only for this cosmetic purpose was judged not worth another file read
// per pulled record — so the device id itself stands in as its own label. It
// is still unambiguous and still names a device, not a root: exactly the
// property §4.4 asks for.
func originLabel(r *syncx.MemoryRecord) string {
	if r.OriginDevice != "" {
		return r.OriginDevice
	}
	return "unknown-device"
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func newSyncStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show this machine's sync bookkeeping and any staged conflicts",
		Long: "Stage A has no transport, so there is no remote to compare against and no \"pending\n" +
			"push/pull\" count in the sense a later stage will have — that arrives with Stage B's git\n" +
			"working copy, which remembers where the remote is. What this reports today: how many\n" +
			"memories have never been exported, how many tombstones this machine holds, and any\n" +
			"conflicts staged by the last `sync import`.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSyncStatus(cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSyncStatus(out io.Writer) error {
	global, err := openSyncGlobal()
	if err != nil {
		return err
	}
	defer global.Close()

	fmt.Fprintln(out, "global:")
	if err := printScopeStatus(out, global, "global"); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	proj, perr := project.ResolveWithFallback(cwd)
	if perr != nil || !proj.Adopted() {
		return nil
	}
	pdb, err := openSyncProject(proj)
	if err != nil {
		fmt.Fprintf(out, "\nproject: %v\n", err)
		return nil
	}
	defer pdb.Close()

	name := pdb.Meta(store.MetaSyncProjectKey)
	if name == "" {
		fmt.Fprintln(out, "\nproject: not enabled for sync — run `stigmergy sync enable`")
		return nil
	}
	fmt.Fprintf(out, "\nproject %q:\n", name)
	return printScopeStatus(out, pdb, "project")
}

func printScopeStatus(out io.Writer, db *store.DB, scope string) error {
	mems, err := db.SyncableMemories()
	if err != nil {
		return err
	}
	bases, err := db.SyncBases()
	if err != nil {
		return err
	}
	tombs, err := db.SyncTombstones()
	if err != nil {
		return err
	}
	neverExported := 0
	for _, m := range mems {
		if _, ok := bases[m.Key]; !ok {
			neverExported++
		}
	}
	fmt.Fprintf(out, "  %d syncable memor%s, %d never exported, %d tombstone%s recorded\n",
		len(mems), plural(len(mems), "y", "ies"), neverExported, len(tombs), plural(len(tombs), "", "s"))

	conflicts, err := stagedConflicts(scope)
	if err != nil {
		return err
	}
	if len(conflicts) == 0 {
		fmt.Fprintln(out, "  no staged conflicts")
		return nil
	}
	fmt.Fprintf(out, "  %d staged conflict%s:\n", len(conflicts), plural(len(conflicts), "", "s"))
	for _, key := range conflicts {
		mine, err := db.ReadMemory(key)
		mineDesc := "(missing locally)"
		if err == nil {
			mineDesc = mine.Description
		}
		theirs, terr := readStagedConflict(scope, key)
		theirsDesc := "(unreadable)"
		if terr == nil {
			theirsDesc = theirs.Description
		}
		fmt.Fprintf(out, "    %s\n      mine:   %s\n      theirs: %s\n", key, mineDesc, theirsDesc)

		// Two descriptions are often identical while the bodies are not — an
		// edit that does not touch the description is the ordinary case — so
		// naming the two bodies apart is what actually tells the operator what
		// is in dispute.
		//
		// stigmergy prints no diff of its own, deliberately (D16). The remote
		// copy is already staged as a file, the operator has a diff tool that
		// is better than any built here, and `--edit` is the guided path for
		// anyone who does not want to run one. What is owed here is the two
		// digests — so a body-only change is visible as such — and the path.
		if err == nil && terr == nil {
			mineDigest := syncx.Digest(mine.Key, mine.Type, mine.Description, mine.Body)
			if mineDigest == theirs.Digest {
				fmt.Fprintln(out, "      (identical content; resolving either way is safe)")
			} else {
				fmt.Fprintf(out, "      digests: mine %s, theirs %s\n",
					shortDigest(mineDigest), shortDigest(theirs.Digest))
			}
		}
		if path, perr := stagedConflictPath(scope, key); perr == nil {
			fmt.Fprintf(out, "      theirs staged at: %s\n", path)
		}
		fmt.Fprintf(out, "      resolve: stigmergy sync resolve %s --mine|--theirs|--edit --scope %s\n",
			key, scope)
	}
	return nil
}

// ---------------------------------------------------------------------------
// enable
// ---------------------------------------------------------------------------

func newSyncEnableCmd() *cobra.Command {
	var as string
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Declare this project's sync identity (once per project, per machine)",
		Long: "Propose a join name from a fingerprint over every root commit in this repository's\n" +
			"history, or accept one explicitly with --as. Running this on a second machine with the\n" +
			"same repository history proposes the same name, so in the common case nothing has to be\n" +
			"typed twice (docs/sync-model.md §4.2). Required before `sync export`/`import` will do\n" +
			"anything for this project's scope.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSyncEnable(cmd.OutOrStdout(), as)
		},
	}
	cmd.Flags().StringVar(&as, "as", "", "the project's sync name (required for a shallow clone or a multi-repository project)")
	return cmd
}

func runSyncEnable(out io.Writer, as string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	proj, err := project.ResolveWithFallback(cwd)
	if err != nil {
		return fmt.Errorf("%s is not inside a git repository", cwd)
	}
	pdb, err := openSyncProject(proj)
	if err != nil {
		return err
	}
	defer pdb.Close()

	global, err := openSyncGlobal()
	if err != nil {
		return err
	}
	defer global.Close()

	if proj.MultiRepo() && as == "" {
		return fmt.Errorf("this project spans several repositories, so it must declare its own name — " +
			"pass --as <name>; a fingerprint over one member's history would not describe the whole project")
	}

	name := as
	fingerprint := ""
	if proj.Repo != nil && isShallowClone(proj.Repo.CommonDir) {
		if name == "" {
			return fmt.Errorf("this is a shallow clone, so a root-commit fingerprint cannot be derived reliably — pass --as <name>")
		}
	} else if proj.Repo != nil {
		fp, ferr := rootCommitFingerprint(proj.Repo.WorktreeRoot)
		if ferr != nil && name == "" {
			return fmt.Errorf("could not compute a project fingerprint (%v) — pass --as <name>", ferr)
		}
		fingerprint = fp
		if name == "" {
			name = shortFingerprint(fp)
		}
	}
	if name == "" {
		return fmt.Errorf("pass --as <name>: no fingerprint could be derived for this repository")
	}

	if err := pdb.SetMeta(store.MetaSyncProjectKey, name); err != nil {
		return err
	}
	if fingerprint != "" {
		if err := pdb.SetMeta(store.MetaSyncFingerprintKey, fingerprint); err != nil {
			return err
		}
	}
	// Adopt the machine-wide device id into this project, so its tombstones and
	// exported provenance agree with every other synced project on this
	// machine from now on — see store.LocalDeviceID's doc comment.
	deviceID, err := global.LocalDeviceID()
	if err != nil {
		return err
	}
	if err := pdb.SetMeta(store.MetaSyncDeviceIDKey, deviceID); err != nil {
		return err
	}

	fmt.Fprintf(out, "project enabled for sync as %q\n", name)
	if fingerprint != "" {
		fmt.Fprintf(out, "fingerprint: %s\n", fingerprint)
		if as == "" {
			fmt.Fprintf(out, "run the same command on the other machine — it derives the same name.\n"+
				"to use a name you would rather read, pass --as <name> on every machine.\n")
		}
	}
	return nil
}

// shortFingerprint trims the join name to a legible prefix.
//
// The name is a directory in the sync tree and appears in every line of output,
// so a full 64-character hash makes a human-facing path unreadable for no gain.
// The prefix keeps the property that actually matters — both machines derive it
// from the same root commits, so they agree with nobody typing anything — while
// the FULL fingerprint is still what `project.json` records and what a mismatch
// is detected against (§4.2). The name identifies; the fingerprint proves.
//
// Derived from the fingerprint rather than from the directory name because a
// clone's directory is a local accident: the same repository checked out as
// ~/work/stig on one machine and ~/code/stigmergy on the other must still land
// on one name.
func shortFingerprint(fp string) string {
	const n = 12
	if len(fp) <= n {
		return fp
	}
	return fp[:n]
}

// isShallowClone checks the marker docs/sync-model.md §4.2 names: a `.git` is
// a FILE in a linked worktree, which is why CommonDir (not the worktree) is
// what gets checked — the same reason internal/drift checks the common dir.
func isShallowClone(commonDir string) bool {
	_, err := os.Stat(filepath.Join(commonDir, "shallow"))
	return err == nil
}

// rootCommitFingerprint hashes every root commit's OID, sorted, exactly as
// docs/sync-model.md §4.2 specifies. Read-only, local, no network — this is a
// CLI command, never the hook path, so a git subprocess is fine here the same
// way it is in internal/gitx's fallback and internal/explore.
func rootCommitFingerprint(worktree string) (string, error) {
	out, err := runGit(worktree, "rev-list", "--max-parents=0", "--all")
	if err != nil {
		return "", err
	}
	roots := strings.Fields(out)
	if len(roots) == 0 {
		return "", fmt.Errorf("no root commit was found")
	}
	slices.Sort(roots)
	h := sha256.New()
	for _, r := range roots {
		h.Write([]byte(r))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

// ---------------------------------------------------------------------------
// share / unshare
// ---------------------------------------------------------------------------

func newSyncShareCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "share <key>",
		Short: "Opt one memory into sync",
		Long: "Project memories sync by default; this is mainly for the global scope, where a memory\n" +
			"does not sync unless told to (docs/sync-model.md §6.4, D6) — a global memory may be a fact\n" +
			"about this physical machine, and only a human knows which.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncPolicy(cmd.OutOrStdout(), scope, args[0], "include")
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "global", "project or global")
	return cmd
}

func newSyncUnshareCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "unshare <key>",
		Short: "Opt one memory out of sync",
		Args:  cobra.ExactArgs(1),
		Long: "Mainly for the project scope, where a memory syncs by default unless told not to — this\n" +
			"is how one memory is kept off the wire even though its project as a whole is enabled.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncPolicy(cmd.OutOrStdout(), scope, args[0], "exclude")
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "project", "project or global")
	return cmd
}

func runSyncPolicy(out io.Writer, scope, key, mode string) error {
	db, closeFn, err := openScopeDB(scope)
	if err != nil {
		return err
	}
	defer closeFn()
	if _, err := db.ReadMemory(key); err != nil {
		return err
	}
	if err := db.SetSyncPolicy(key, mode); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %q: %sd for sync\n", scope, key, mode)
	return nil
}

// ---------------------------------------------------------------------------
// resolve
// ---------------------------------------------------------------------------

func newSyncResolveCmd() *cobra.Command {
	var scope string
	var mine, theirs, edit bool
	cmd := &cobra.Command{
		Use:   "resolve <key>",
		Short: "Settle a memory `sync import` staged as a conflict",
		Long: "Nothing is ever auto-merged (docs/sync-model.md §3.5): a key both machines changed is\n" +
			"staged, not written, and stays that way until this runs. --mine keeps the local body as-is;\n" +
			"--theirs replaces it with the remote's; --edit opens $EDITOR on a diff3-style draft with both\n" +
			"bodies, for a human to merge by hand. Whichever wins, the result is written as an ordinary\n" +
			"CAS update and the base advances, so the conflict does not recur on the next sync.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncResolve(cmd.OutOrStdout(), args[0], scope, mine, theirs, edit)
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "project", "project or global")
	cmd.Flags().BoolVar(&mine, "mine", false, "keep the local body")
	cmd.Flags().BoolVar(&theirs, "theirs", false, "adopt the remote's body")
	cmd.Flags().BoolVar(&edit, "edit", false, "open $EDITOR on both bodies and write the merged result")
	return cmd
}

func runSyncResolve(out io.Writer, key, scope string, mine, theirs, edit bool) error {
	picked := 0
	for _, b := range []bool{mine, theirs, edit} {
		if b {
			picked++
		}
	}
	if picked != 1 {
		return fmt.Errorf("pass exactly one of --mine, --theirs, or --edit")
	}

	db, closeFn, err := openScopeDB(scope)
	if err != nil {
		return err
	}
	defer closeFn()

	staged, err := readStagedConflict(scope, key)
	if err != nil {
		return err
	}
	cur, err := db.ReadMemory(key)
	if err != nil {
		return fmt.Errorf("%q could not be read locally: %w", key, err)
	}

	typ, desc, body := cur.Type, cur.Description, cur.Body
	switch {
	case theirs:
		typ, desc, body = staged.Type, staged.Description, staged.Body
	case edit:
		merged, err := editDraft(cur.Body, staged.Body)
		if err != nil {
			return err
		}
		body = merged
	}

	label := deviceLabel(db)
	expected := cur.Version
	res, err := db.WriteMemory(store.MemoryWrite{
		Key: key, Type: typ, Description: desc, Body: body,
		UpdatedBy: "sync:" + label, ExpectedVersion: &expected,
	}, syncAgentKind)
	if err != nil {
		return err
	}

	deviceID, err := db.LocalDeviceID()
	if err != nil {
		return err
	}
	digest := syncx.Digest(key, typ, desc, body)
	if err := db.SetSyncBase(key, digest, deviceID); err != nil {
		return err
	}
	if err := clearStagedConflict(scope, key); err != nil {
		return err
	}

	fmt.Fprintf(out, "%q resolved at version %d; the next `sync export` will push it\n", key, res.Memory.Version)
	return nil
}

// editDraft opens $EDITOR on a diff3-style draft of both bodies and returns
// the human's merged result. This is an OFFER confirmed by a human, which
// docs/sync-model.md §3.5 (and A.6) is explicit is a different thing from an
// automatic text merge — nothing here decides which lines win.
func editDraft(mine, theirs string) (string, error) {
	tmp, err := os.CreateTemp("", "stigmergy-sync-resolve-*.md")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	draft := "<<<<<<< mine\n" + mine + "\n=======\n" + theirs + "\n>>>>>>> theirs\n"
	if _, err := tmp.WriteString(draft); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, tmpPath)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("$EDITOR exited with an error: %w", err)
	}

	b, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", err
	}
	edited := strings.TrimRight(string(b), "\n")
	if strings.TrimSpace(edited) == "" {
		return "", fmt.Errorf("the edited body is empty; resolution aborted")
	}
	if strings.Contains(edited, "<<<<<<< mine") || strings.Contains(edited, "=======") || strings.Contains(edited, ">>>>>>> theirs") {
		return "", fmt.Errorf("conflict markers are still present in the edited body; remove them and run resolve again")
	}
	return edited, nil
}

// ---------------------------------------------------------------------------
// shared plumbing: opening databases, the wire tree, conflict staging
// ---------------------------------------------------------------------------

// refuseOnSchemaSkew is docs/sync-model.md §7.1: sync never migrates
// anything, so a database whose schema differs from what this binary expects
// is refused rather than silently upgraded (D8) — the same refusal the claim
// guard hook already gives, reached from a command that can afford to explain
// it in full.
func refuseOnSchemaSkew(db *store.DB, kind store.Kind) error {
	version, err := db.SchemaVersion()
	if err != nil {
		return fmt.Errorf("the %s database schema could not be read: %w", kind, err)
	}
	latest, err := store.LatestVersion(kind)
	if err != nil {
		return err
	}
	if version != latest {
		return fmt.Errorf("the %s database is at schema version %d but this stigmergy expects %d — run `stigmergy doctor`",
			kind, version, latest)
	}
	return nil
}

// openSyncGlobal opens the global database the way every sync command needs
// it: never migrating (sync never migrates anything, D8) and refusing on
// schema skew (§7.1) rather than silently upgrading a database another
// command should have.
func openSyncGlobal() (*store.DB, error) {
	path, err := globalDBPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no global stigmergy database found — run `stigmergy doctor` first")
	}
	g, err := store.OpenGlobal(path, store.NoMigrate())
	if err != nil {
		return nil, err
	}
	if err := refuseOnSchemaSkew(g, store.Global); err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}

// openSyncProject mirrors openSyncGlobal for the project scope.
func openSyncProject(proj *project.Project) (*store.DB, error) {
	if !proj.Adopted() {
		return nil, fmt.Errorf("stigmergy has not been set up in this repository yet — run `stigmergy doctor` first")
	}
	p, err := store.OpenProjectAt(proj.DBPath, store.NoMigrate())
	if err != nil {
		return nil, err
	}
	if err := refuseOnSchemaSkew(p, store.Project); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// openScopeDB opens whichever scope a --scope flag named, from the current
// working directory, for the commands (share/unshare/resolve) that operate on
// one memory rather than a whole export/import pass.
func openScopeDB(scope string) (db *store.DB, closeFn func(), err error) {
	switch scope {
	case "global":
		g, err := openSyncGlobal()
		if err != nil {
			return nil, nil, err
		}
		return g, func() { g.Close() }, nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return nil, nil, err
		}
		proj, err := project.ResolveWithFallback(cwd)
		if err != nil {
			return nil, nil, fmt.Errorf("%s is not inside a git repository", cwd)
		}
		p, err := openSyncProject(proj)
		if err != nil {
			return nil, nil, err
		}
		return p, func() { p.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("--scope must be project or global, got %q", scope)
	}
}

// deviceLabel is the human-editable name that appears in "sync:<label>"
// attribution and in devices/<id>.json — docs/sync-model.md §4.1. It defaults
// to the hostname and is cached in meta on first use, the same lazy pattern
// LocalDeviceID uses for the id itself.
func deviceLabel(db *store.DB) string {
	if v := db.Meta(store.MetaSyncDeviceLabelKey); v != "" {
		return v
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		h = "unknown-device"
	}
	_ = db.SetMeta(store.MetaSyncDeviceLabelKey, h)
	return h
}

// localSide projects a database's syncable content into the shape Reconcile
// takes: every syncable memory as a MemoryRecord (§3.3's synced fields plus
// this machine's own provenance), and every memory-kind tombstone.
func localSide(db *store.DB) (syncx.Side, error) {
	side := syncx.Side{Memories: map[string]syncx.MemoryRecord{}, Tombstones: map[string]syncx.Tombstone{}}

	deviceID, err := db.LocalDeviceID()
	if err != nil {
		return side, err
	}
	mems, err := db.SyncableMemories()
	if err != nil {
		return side, err
	}
	for _, m := range mems {
		side.Memories[m.Key] = syncx.MemoryRecord{
			Key: m.Key, Type: m.Type, Description: m.Description,
			Digest:          syncx.Digest(m.Key, m.Type, m.Description, m.Body),
			CreatedAt:       m.CreatedAt,
			UpdatedAt:       m.UpdatedAt,
			OriginDevice:    deviceID,
			OriginUpdatedBy: m.UpdatedBy,
			OriginVersion:   m.Version,
			Body:            m.Body,
		}
	}

	tombs, err := db.SyncTombstones()
	if err != nil {
		return side, err
	}
	for _, t := range tombs {
		if t.Kind == "memory" {
			side.Tombstones[t.Ident] = t
		}
	}
	return side, nil
}

// readSideFromTree is localSide's mirror for a directory written by `sync
// export` — on another machine, or by this one on a previous run.
func readSideFromTree(scopeDir string) (syncx.Side, error) {
	side := syncx.Side{Memories: map[string]syncx.MemoryRecord{}, Tombstones: map[string]syncx.Tombstone{}}

	memDir := filepath.Join(scopeDir, "memories")
	entries, err := os.ReadDir(memDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return side, nil
		}
		return side, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(memDir, e.Name()))
		if err != nil {
			return side, err
		}
		r, err := syncx.UnmarshalMemory(b)
		if err != nil {
			return side, fmt.Errorf("%s: %w", filepath.Join(memDir, e.Name()), err)
		}
		side.Memories[r.Key] = r
	}

	tombs, err := readJSONL[syncx.Tombstone](filepath.Join(scopeDir, "tombstones.jsonl"))
	if err != nil {
		return side, err
	}
	for _, t := range tombs {
		if t.Kind == "memory" {
			side.Tombstones[t.Ident] = t
		}
	}
	return side, nil
}

// wireLink is links.jsonl's shape (docs/sync-model.md §5): store.Link already
// carries json tags in this exact shape, but a dedicated type here keeps the
// wire format decoupled from internal/store's own struct, the way
// syncx.MemoryRecord is already decoupled from store.Memory.
type wireLink struct {
	KeyA      string `json:"key_a"`
	KeyB      string `json:"key_b"`
	Reason    string `json:"reason"`
	CreatedBy string `json:"created_by"`
	AgentKind string `json:"agent_kind"`
	CreatedAt string `json:"created_at"`
}

func wireLinksFrom(links []store.Link) []wireLink {
	out := make([]wireLink, len(links))
	for i, l := range links {
		out[i] = wireLink{
			KeyA: l.KeyA, KeyB: l.KeyB, Reason: l.Reason,
			CreatedBy: l.CreatedBy, AgentKind: l.AgentKind, CreatedAt: l.CreatedAt,
		}
	}
	return out
}

// wireProject is project.json (docs/sync-model.md §5): the fingerprint and
// the project schema version the writer was at. The receiver does not gate on
// the version matching (D8) — it is provenance for a human to read, never a
// gate sync itself enforces.
type wireProject struct {
	Fingerprint   string `json:"fingerprint"`
	SchemaVersion int    `json:"schema_version"`
}

func writeProjectJSON(dir string, db *store.DB) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	version, err := db.SchemaVersion()
	if err != nil {
		return err
	}
	w := wireProject{Fingerprint: db.Meta(store.MetaSyncFingerprintKey), SchemaVersion: version}
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "project.json"), append(b, '\n'), 0o600)
}

// wireDevice is one devices/<id>.json entry. Stage A writes only the label —
// seq, last push, and the schema versions written are Stage B/C fields, load-
// bearing only once rollback detection (docs/sync-model.md §7.5) exists to
// read them.
type wireDevice struct {
	Label string `json:"label"`
	Seq   int    `json:"seq,omitempty"`
}

func writeDeviceFile(dir, deviceID, label string) error {
	devDir := filepath.Join(dir, "devices")
	if err := os.MkdirAll(devDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(wireDevice{Label: label}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(devDir, deviceID+".json"), append(b, '\n'), 0o600)
}

func writeDeviceSequence(dir, deviceID, label string, seq int) error {
	devDir := filepath.Join(dir, "devices")
	if err := os.MkdirAll(devDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(wireDevice{Label: label, Seq: seq}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(devDir, deviceID+".json"), append(b, '\n'), 0o600)
}

// refuseRollback detects the only dangerous restored-backup path: an old copy
// of this device identity attempting to publish a sequence the transport has
// already seen. A normal stale machine pulls first and is harmless.
func refuseRollback(dir string, db *store.DB, accept bool) error {
	id, err := db.LocalDeviceID()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, "devices", id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var remote wireDevice
	if err := json.Unmarshal(b, &remote); err != nil {
		return fmt.Errorf("read remote device record: %w", err)
	}
	local, _ := strconv.Atoi(db.Meta(store.MetaSyncSeqKey))
	if remote.Seq > 0 && local > 0 && local+1 <= remote.Seq && !accept {
		return fmt.Errorf("refusing rollback for device %s: local sequence %d is not newer than remote sequence %d; this can mean a restored backup or two machines cloned from one image (use --accept-rollback or `sync adopt-identity --new`)", id, local, remote.Seq)
	}
	return nil
}

func writeFormatFile(dir string) error {
	return os.WriteFile(filepath.Join(dir, "FORMAT"), []byte(strconv.Itoa(syncx.FormatVersion)+"\n"), 0o600)
}

// refuseOnNewerFormat is docs/sync-model.md §5: a reader finding a FORMAT
// version higher than this binary's refuses the entire run rather than
// guessing at fields it has never seen. A tree with no FORMAT file at all
// (nothing has ever exported here) is not a refusal — export creates it, and
// import treats it as an empty tree.
func refuseOnNewerFormat(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, "FORMAT"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("%s does not contain a single integer", filepath.Join(dir, "FORMAT"))
	}
	if v > syncx.FormatVersion {
		return fmt.Errorf("%s was written by a wire format version %d newer than this stigmergy understands (%d) — "+
			"install a newer stigmergy before syncing against it", dir, v, syncx.FormatVersion)
	}
	return nil
}

// refuseIfSQLite is docs/sync-model.md §7.12 and Appendix A.1: nothing in a
// sync tree may be a SQLite database file, because somebody will eventually
// point a file syncer at stigmergy.sqlite3 directly, and WAL journalling over
// a single-connection pool means a file syncer cannot copy the main file and
// its -wal/-shm companions atomically. What lands on the other end is a
// database whose WAL does not belong to it — and the failure does not present
// as a sync failure, it presents as the claim guard failing closed somewhere
// the user is not standing.
func refuseIfSQLite(dir string) error {
	var found string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		switch {
		case strings.HasSuffix(name, ".sqlite3"), strings.HasSuffix(name, ".sqlite"), strings.HasSuffix(name, ".db"),
			strings.HasSuffix(name, "-wal"), strings.HasSuffix(name, "-shm"):
			found = path
		}
		return nil
	})
	if found == "" {
		return nil
	}
	return fmt.Errorf("%s contains a database file (%s) — stigmergy sync refuses to run against it.\n\n"+
		"Never point a file syncer, or `sync export`/`import`, at stigmergy.sqlite3 itself: WAL journalling\n"+
		"and a single-connection pool mean the main file and its -wal/-shm companions cannot be copied\n"+
		"atomically, and what lands on the other machine is a database whose WAL does not belong to it.\n"+
		"The failure will not look like a sync failure — it will look like the claim guard failing closed\n"+
		"in a repository nobody touched. Point sync at a plain export directory instead; see docs/sync.md.",
		dir, found)
}

// ---------------------------------------------------------------------------
// conflict staging: $XDG_STATE_HOME/stigmergy/sync/conflicts/<scope>/<key>.theirs.md
// ---------------------------------------------------------------------------

func conflictsDir(scope string) (string, error) {
	state, err := xdg.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "sync", "conflicts", scope), nil
}

func stageConflict(scope, key string, theirs *syncx.MemoryRecord) error {
	dir, err := conflictsDir(scope)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := syncx.MarshalMemory(*theirs)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, key+".theirs.md"), b, 0o600)
}

func stagedConflicts(scope string) ([]string, error) {
	dir, err := conflictsDir(scope)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".theirs.md") {
			out = append(out, strings.TrimSuffix(e.Name(), ".theirs.md"))
		}
	}
	slices.Sort(out)
	return out, nil
}

// stagedConflictPath is where the remote's copy of a conflicted memory waits
// for a human. Reported by `sync status` so the operator can point their own
// diff tool at it (D16).
func stagedConflictPath(scope, key string) (string, error) {
	dir, err := conflictsDir(scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, key+".theirs.md"), nil
}

// shortDigest trims a digest for display. Enough to tell two apart at a glance,
// which is all a status line needs; nothing compares these.
func shortDigest(d string) string {
	const n = 19 // "sha256:" plus 12 hex characters
	if len(d) <= n {
		return d
	}
	return d[:n] + "…"
}

func readStagedConflict(scope, key string) (*syncx.MemoryRecord, error) {
	dir, err := conflictsDir(scope)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, key+".theirs.md")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no staged conflict for %q in scope %q — run `stigmergy sync import` first", key, scope)
	}
	if err != nil {
		return nil, err
	}
	r, err := syncx.UnmarshalMemory(b)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func clearStagedConflict(scope, key string) error {
	dir, err := conflictsDir(scope)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, key+".theirs.md"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ---------------------------------------------------------------------------
// small generic helpers
// ---------------------------------------------------------------------------

func writeJSONL[T any](path string, items []T) error {
	var buf bytes.Buffer
	for _, it := range items {
		b, err := json.Marshal(it)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

func plural(n int, singular, pluralSuffix string) string {
	if n == 1 {
		return singular
	}
	return pluralSuffix
}
