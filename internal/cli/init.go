package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/hostcfg"
	"github.com/happyarch/stigmergy/internal/store"
	"github.com/happyarch/stigmergy/internal/xdg"
)

func newInitCmd() *cobra.Command {
	var (
		host    string
		remove  bool
		noInput bool
		purgeDB bool
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Enable stigmergy in this repository",
		Long: "Create the project database and register stigmergy with the agent hosts:\n" +
			"the MCP server, the enforcement hooks, and a block in the project's instruction file.\n\n" +
			"Existing configuration is merged, never overwritten, and `--remove` takes back exactly\n" +
			"what was added.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if host != "all" && host != "claude" && host != "codex" && host != "antigravity" {
				return fmt.Errorf("--host must be all, claude, codex, or antigravity, got %q", host)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repo, err := gitx.ResolveWithFallback(cwd)
			if err != nil {
				if errors.Is(err, gitx.ErrNotARepo) {
					return fmt.Errorf("%s is not inside a git repository — stigmergy coordinates per repository, so run this from one", cwd)
				}
				return err
			}
			out := cmd.OutOrStdout()

			if purgeDB && !remove {
				return fmt.Errorf("--purge-db only makes sense with --remove")
			}
			if remove {
				return runRemove(out, repo, host, purgeDB, yes)
			}
			if err := runInit(out, repo, host); err != nil {
				return err
			}
			return maybeImportLegacyMemories(out, os.Stdin, repo, noInput)
		},
	}
	cmd.Flags().StringVar(&host, "host", "all", "which hosts to configure: all, claude, codex, or antigravity")
	cmd.Flags().BoolVar(&remove, "remove", false, "remove stigmergy's configuration from this repository")
	cmd.Flags().BoolVar(&noInput, "no-input", false, "never prompt; print what to run instead")
	cmd.Flags().BoolVar(&purgeDB, "purge-db", false, "with --remove: also delete the project database and every memory in it")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --purge-db: skip the confirmation prompt")
	return cmd
}

func runInit(out io.Writer, repo *gitx.Repo, host string) error {
	// The database first: the hooks check for it to decide whether stigmergy is
	// active here, so a project is either fully enabled or not enabled at all.
	db, err := store.OpenProject(repo.CommonDir)
	if err != nil {
		return fmt.Errorf("could not create the project database: %w", err)
	}
	version, _ := db.SchemaVersion()
	db.Close()

	globalPath, err := xdg.GlobalDBPath()
	if err != nil {
		return err
	}
	gdb, err := store.OpenGlobal(globalPath)
	if err != nil {
		return fmt.Errorf("could not create the global database: %w", err)
	}
	gdb.Close()

	fmt.Fprintf(out, "stigmergy is enabled in %s\n\n", repo.WorktreeRoot)
	fmt.Fprintf(out, "  project database  %s (schema v%d)\n", store.ProjectDBPath(repo.CommonDir), version)
	fmt.Fprintf(out, "  global database   %s\n\n", globalPath)

	if host == "all" || host == "claude" {
		if err := hostcfg.InstallClaude(repo.WorktreeRoot); err != nil {
			return fmt.Errorf("could not configure Claude Code: %w", err)
		}
		mcpPath, settingsPath, memoryPath := hostcfg.ClaudePaths(repo.WorktreeRoot)
		fmt.Fprintln(out, "Claude Code")
		fmt.Fprintf(out, "  %s          MCP server\n", mcpPath)
		fmt.Fprintf(out, "  %s  hooks (claim enforcement)\n", settingsPath)
		fmt.Fprintf(out, "  %s         instructions\n\n", memoryPath)
	}

	if host == "all" || host == "codex" {
		if err := hostcfg.InstallCodex(repo.WorktreeRoot); err != nil {
			return fmt.Errorf("could not configure Codex: %w", err)
		}
		cfgPath, hooksPath, memoryPath := hostcfg.CodexPaths(repo.WorktreeRoot)
		fmt.Fprintln(out, "Codex")
		fmt.Fprintf(out, "  %s   MCP server\n", cfgPath)
		fmt.Fprintf(out, "  %s    hooks (claim warnings)\n", hooksPath)
		fmt.Fprintf(out, "  %s          instructions\n\n", memoryPath)
		fmt.Fprintln(out, "  Codex loads project config and hooks only for TRUSTED projects.")
		fmt.Fprintln(out, "  Start Codex here, trust the project when prompted, and review the hooks with /hooks.")
		fmt.Fprintln(out, "  Codex cannot block an edit before it lands: claims there are enforced by halting")
		fmt.Fprintln(out, "  the turn afterwards. See AGENTS.md.")
		fmt.Fprintln(out)
	}

	if host == "all" || host == "antigravity" {
		if err := hostcfg.InstallAntigravity(repo.WorktreeRoot); err != nil {
			return fmt.Errorf("could not configure Antigravity: %w", err)
		}
		pluginDir := hostcfg.AntigravityPluginDir(repo.WorktreeRoot)
		fmt.Fprintln(out, "Antigravity")
		fmt.Fprintf(out, "  %s\n", pluginDir)
		fmt.Fprintln(out, "  (plugin.json, mcp_config.json, hooks.json, rules/stigmergy.md)")
		fmt.Fprintln(out, "  Antigravity reads the plugin automatically from .agents/plugins/.")
		fmt.Fprintln(out, "  Your conversationId is the session_label for root_register — the pre-invocation hook tells you it.")
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, "Restart any running agent sessions so they pick up the new configuration.")
	fmt.Fprintln(out, "Check the installation with `stigmergy doctor`.")
	return nil
}

func runRemove(out io.Writer, repo *gitx.Repo, host string, purgeDB, yes bool) error {
	if host == "all" || host == "claude" {
		if err := hostcfg.RemoveClaude(repo.WorktreeRoot); err != nil {
			return fmt.Errorf("could not remove the Claude Code configuration: %w", err)
		}
		fmt.Fprintln(out, "Removed stigmergy from the Claude Code configuration.")
	}
	if host == "all" || host == "codex" {
		if err := hostcfg.RemoveCodex(repo.WorktreeRoot); err != nil {
			return fmt.Errorf("could not remove the Codex configuration: %w", err)
		}
		fmt.Fprintln(out, "Removed stigmergy from the Codex configuration.")
	}
	if host == "all" || host == "antigravity" {
		if err := hostcfg.RemoveAntigravity(repo.WorktreeRoot); err != nil {
			return fmt.Errorf("could not remove the Antigravity configuration: %w", err)
		}
		fmt.Fprintln(out, "Removed stigmergy from the Antigravity configuration.")
	}

	dbPath := store.ProjectDBPath(repo.CommonDir)
	if !purgeDB {
		// Disabling stigmergy and destroying the project's accumulated memory
		// are different intentions, and only one of them is reversible.
		fmt.Fprintf(out, "\nThe project database was kept: %s\n", dbPath)
		fmt.Fprintln(out, "It holds this project's memories. Re-running `stigmergy init` picks them up again.")
		fmt.Fprintln(out, "To delete them for good: `stigmergy init --remove --purge-db`.")
		return nil
	}

	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintln(out, "\nThere is no project database to delete.")
		return nil
	}
	if err := confirmPurge(out, os.Stdin, dbPath, repo.CommonDir, yes); err != nil {
		return err
	}
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not delete %s: %w", p, err)
		}
	}
	fmt.Fprintf(out, "\nDeleted %s and every memory, claim and message in it.\n", dbPath)
	fmt.Fprintln(out, "Global memories were not touched.")
	return nil
}

// confirmPurge is the one genuinely destructive path in stigmergy, so it says
// exactly what will be lost — with counts, because "delete the database?" and
// "delete 47 memories?" are different questions — and refuses to guess when it
// cannot ask.
func confirmPurge(out io.Writer, in *os.File, dbPath, commonDir string, yes bool) error {
	memories := 0
	if db, err := store.OpenProject(commonDir, store.ReadOnly()); err == nil {
		_ = db.QueryRow(`SELECT count(*) FROM memories`).Scan(&memories)
		db.Close()
	}

	if yes {
		return nil
	}
	if !term.IsTerminal(int(in.Fd())) {
		return fmt.Errorf("refusing to delete %d memories without confirmation — re-run with --yes if you mean it", memories)
	}

	fmt.Fprintf(out, "\nThis will permanently delete %s,\nincluding %d project memories, and every claim and message in this repository.\n",
		dbPath, memories)
	fmt.Fprint(out, "This cannot be undone. Type 'delete' to confirm: ")

	answer, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(answer) != "delete" {
		return fmt.Errorf("not confirmed; nothing was deleted")
	}
	return nil
}
