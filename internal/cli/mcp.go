package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/mcpserver"
	"github.com/happyarch/stigmergy/internal/xdg"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the stigmergy MCP server on stdio",
		Long: "Run the stigmergy MCP server over stdio. This is what host configurations invoke; " +
			"it is not meant to be run by hand.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			globalPath, err := xdg.GlobalDBPath()
			if err != nil {
				return err
			}
			// stdout is the protocol channel: nothing but MCP frames may go there.
			cmd.SetOut(os.Stderr)

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return mcpserver.Run(ctx, Version, globalPath)
		},
	}
}
