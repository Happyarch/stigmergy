// Package cli wires the stigmergy command tree. Commands stay thin; logic
// lives in the internal packages they call.
package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/xdg"
)

// Version is stamped at build time via -ldflags "-X ...cli.Version=v1.2.3".
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:           "stigmergy",
	Short:         "Shared memory, claims, and mailbox for multi-agent projects",
	Long:          "stigmergy coordinates Claude Code and Codex agents working in the same repository:\nshared project/global memories, file claims, and root-to-root negotiation.",
	Version:       Version,
	SilenceUsage:  true,
	SilenceErrors: false,
}

// globalDBPath is where the machine-wide memory database lives.
func globalDBPath() (string, error) { return xdg.GlobalDBPath() }

// Execute runs the CLI and exits non-zero on error.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(dbCmd)
	rootCmd.AddCommand(newDoctorCmd())
	rootCmd.AddCommand(newWatchCmd())
	rootCmd.AddCommand(newMCPCmd())
	rootCmd.AddCommand(newHookCmd())
	rootCmd.AddCommand(newInitCmd())
	rootCmd.AddCommand(newExploreCmd())
	rootCmd.AddCommand(newImportCmd())
}
