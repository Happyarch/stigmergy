package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/explore"
	"github.com/happyarch/stigmergy/internal/gitx"
)

func newExploreCmd() *cobra.Command {
	var (
		worktree string
		timeout  time.Duration
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "explore PROMPT",
		Short: "Run a read-only exploration in a confined Codex process",
		Long: "Run an exploration that cannot write the tree and cannot see stigmergy's tools.\n\n" +
			"Use this instead of a native Codex subagent: native subagents inherit your sandbox and\n" +
			"permissions, so they are not a read-only boundary. The explorer reports back to you, and\n" +
			"you decide what to record — memories and claims stay the root's to write.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if worktree == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				repo, err := gitx.ResolveWithFallback(cwd)
				if err != nil {
					if errors.Is(err, gitx.ErrNotARepo) {
						return fmt.Errorf("%s is not inside a git repository", cwd)
					}
					return err
				}
				worktree = repo.WorktreeRoot
			}

			err := explore.Run(cmd.Context(), explore.Request{
				Prompt: args[0], Worktree: worktree, Timeout: timeout, JSON: asJSON,
			}, os.Stdout, os.Stderr)

			// The explorer's exit code is the result of the exploration, and it
			// belongs to the caller: a script driving several of these needs to
			// know which ones failed.
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				os.Exit(exitErr.ExitCode())
			}
			return err
		},
	}
	cmd.Flags().StringVar(&worktree, "worktree", "", "directory to explore (default: the current repository)")
	cmd.Flags().DurationVar(&timeout, "timeout", explore.DefaultTimeout, "give up after this long")
	cmd.Flags().BoolVar(&asJSON, "json", false, "stream Codex's JSON events instead of plain output")
	return cmd
}
