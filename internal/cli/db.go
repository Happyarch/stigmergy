package cli

import (
	"fmt"
	"os"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/store"
	"github.com/happyarch/stigmergy/internal/xdg"
	"github.com/spf13/cobra"
)

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Low-level database operations (debugging)",
}

var dbPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the project and global database paths",
	RunE: func(cmd *cobra.Command, args []string) error {
		globalPath, err := xdg.GlobalDBPath()
		if err != nil {
			return err
		}
		fmt.Printf("global:  %s\n", globalPath)
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		repo, err := gitx.ResolveWithFallback(cwd)
		if err != nil {
			fmt.Println("project: (not inside a git worktree)")
			return nil
		}
		fmt.Printf("project: %s\n", store.ProjectDBPath(repo.CommonDir))
		fmt.Printf("worktree: %s\n", repo.WorktreeRoot)
		return nil
	},
}

var dbMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Create or migrate the global DB and, when inside a repo, the project DB",
	RunE: func(cmd *cobra.Command, args []string) error {
		globalPath, err := xdg.GlobalDBPath()
		if err != nil {
			return err
		}
		g, err := store.OpenGlobal(globalPath)
		if err != nil {
			return err
		}
		defer g.Close()
		gv, err := g.SchemaVersion()
		if err != nil {
			return err
		}
		fmt.Printf("global:  %s (schema v%d)\n", g.Path, gv)

		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		repo, err := gitx.ResolveWithFallback(cwd)
		if err != nil {
			fmt.Println("project: skipped (not inside a git worktree)")
			return nil
		}
		p, err := store.OpenProject(repo.CommonDir)
		if err != nil {
			return err
		}
		defer p.Close()
		pv, err := p.SchemaVersion()
		if err != nil {
			return err
		}
		fmt.Printf("project: %s (schema v%d)\n", p.Path, pv)
		return nil
	},
}

func init() {
	dbCmd.AddCommand(dbPathCmd)
	dbCmd.AddCommand(dbMigrateCmd)
}
