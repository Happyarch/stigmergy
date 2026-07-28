package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/importer"
	"github.com/happyarch/stigmergy/internal/project"
	"github.com/happyarch/stigmergy/internal/store"
)

func newImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import memories from an existing store",
	}
	cmd.AddCommand(newImportClaudeCmd())
	return cmd
}

func newImportClaudeCmd() *cobra.Command {
	var (
		source     string
		scope      string
		dryRun     bool
		reportPath string
	)
	cmd := &cobra.Command{
		Use:   "claude-memory",
		Short: "Import a Claude Code markdown memory directory into stigmergy",
		Long: "Copy an existing Claude Code memory directory into stigmergy's database.\n\n" +
			"The source files are never modified or deleted, and an existing memory is never\n" +
			"overwritten: a file whose content differs from a memory of the same name is reported\n" +
			"as a conflict for you to resolve. Re-running the import is safe — unchanged files are\n" +
			"skipped.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if scope != "project" && scope != "global" {
				return fmt.Errorf("--scope must be project or global, got %q", scope)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repo, err := gitx.ResolveWithFallback(cwd)
			if err != nil {
				return fmt.Errorf("%s is not inside a git repository", cwd)
			}

			if source == "" {
				dirs := importer.LegacyDirs(repo.WorktreeRoot)
				if len(dirs) == 0 {
					return fmt.Errorf("no Claude memory directory was found for this project — pass one with --source")
				}
				source = dirs[0]
			}

			db, err := openScope(scope, repo, dryRun)
			if err != nil {
				return err
			}
			defer db.Close()

			report, err := importer.Import(db, source, dryRun)
			if err != nil {
				return err
			}
			printReport(cmd.OutOrStdout(), report)

			if reportPath != "" {
				data, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(reportPath, append(data, '\n'), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "\nFull report written to %s\n", reportPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "memory directory to import (default: the detected Claude memory directory)")
	cmd.Flags().StringVar(&scope, "scope", "project", "scope to import into: project or global")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be imported without writing anything")
	cmd.Flags().StringVar(&reportPath, "report", "", "write a JSON report to this path")
	return cmd
}

func openScope(scope string, repo *gitx.Repo, dryRun bool) (*store.DB, error) {
	path := store.ProjectDBPath(repo.CommonDir)
	if scope == "global" {
		p, err := globalDBPath()
		if err != nil {
			return nil, err
		}
		path = p
	}

	// A dry run must not bring a database into existence. Opening for real
	// would create and migrate one, so a user asking "what would this do?" in a
	// project that has not adopted stigmergy would get a database as their
	// answer. If none exists yet, nothing has been imported yet either — a
	// scratch database gives exactly that answer and is discarded.
	if dryRun {
		if _, err := os.Stat(path); err != nil {
			scratch, err := os.MkdirTemp("", "stigmergy-dryrun-*")
			if err != nil {
				return nil, err
			}
			return store.OpenProject(scratch)
		}
	}

	if scope == "global" {
		return store.OpenGlobal(path)
	}
	return store.OpenProject(repo.CommonDir)
}

func printReport(out io.Writer, r *importer.Report) {
	if r.DryRun {
		fmt.Fprintf(out, "Dry run — nothing was written.\n\n")
	}
	fmt.Fprintf(out, "Imported %d, skipped %d, conflicts %d, invalid %d (from %s)\n",
		r.Imported, r.Skipped, r.Conflicts, r.Invalid, r.SourceDir)

	// Only the outcomes that need a human are worth printing in full. A wall of
	// "imported" lines buries the two files that actually need attention.
	for _, o := range r.Outcomes {
		if o.Status == "conflict" || o.Status == "invalid" {
			fmt.Fprintf(out, "\n  %s: %s\n    %s\n", o.Status, o.SourcePath, o.Detail)
		}
	}
	if r.Conflicts > 0 {
		fmt.Fprintf(out, "\nNothing was overwritten. Reconcile each conflict by hand: read the existing memory with\n"+
			"`memory_read`, compare it with the file, and write the merged version if the file is more current.\n")
	}
}

// maybeImportLegacyMemories is init's detect-and-prompt: finding an existing
// memory directory and silently ignoring it would leave the user's accumulated
// knowledge stranded, while importing it without asking would be presumptuous.
func maybeImportLegacyMemories(out io.Writer, in io.Reader, repo *gitx.Repo, noInput bool) error {
	dirs := importer.LegacyDirs(repo.WorktreeRoot)
	if len(dirs) == 0 {
		return nil
	}
	dir := dirs[0]
	n := importer.Count(dir)
	if n == 0 {
		return nil
	}

	fmt.Fprintf(out, "Found %d existing Claude memory file(s) in\n  %s\n\n", n, dir)

	if noInput || !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(out, "Import them with:\n  stigmergy import claude-memory --source %q\n\n", dir)
		return nil
	}

	fmt.Fprint(out, "Import them into this project's memory now? [y/N] ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
		fmt.Fprintf(out, "Skipped. Import later with:\n  stigmergy import claude-memory --source %q\n\n", dir)
		return nil
	}

	// Resolve the project (single-repo or multi-repo) and open its database.
	proj, err := project.ResolveWithFallback(repo.WorktreeRoot)
	if err != nil {
		return err
	}
	db, err := store.OpenProjectAt(proj.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	report, err := importer.Import(db, dir, false)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	printReport(out, report)
	fmt.Fprintf(out, "\nThe source files were not modified. Once you are satisfied the memories are in stigmergy,\n"+
		"you can remove the old directory yourself.\n\n")
	return nil
}
