package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/hooks"
	"github.com/happyarch/stigmergy/internal/xdg"
)

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Host hook handlers (invoked by Claude Code and Codex, not by hand)",
		Long: "Hook handlers read a host's JSON payload on stdin and write a decision on stdout.\n" +
			"They are wired up by `stigmergy init` and are not meant to be run directly.",
	}
	cmd.AddCommand(newClaimGuardCmd(), newRootGateCmd(), newSessionStartCmd(), newSessionEndCmd(), newHookDumpCmd())
	cmd.AddCommand(newCodexHookCmds()...)
	return cmd
}

// newClaimGuardCmd is the enforced path: Claude Code can deny a tool call
// before it runs, so a claimed file is genuinely protected.
func newClaimGuardCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "claim-guard",
		Short:        "PreToolUse: block edits to files claimed by another agent",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeClaude(os.Stdin)
			if err != nil {
				// A payload we cannot parse is a stigmergy problem, not the
				// agent's. Blocking every edit because our own parser broke
				// would be worse than the risk we are guarding against, and
				// the host tells us about it loudly on stderr.
				fmt.Fprintf(os.Stderr, "stigmergy: could not parse the hook payload: %v\n", err)
				return nil
			}

			d := hooks.Guard("claude-code", in.SessionID, in.CWD, in.EditedPaths())
			if d.Allow {
				return nil // Say nothing, cost nothing.
			}
			hooks.AuditDenial("claude-code", in.SessionID, in.CWD, d)
			return json.NewEncoder(os.Stdout).Encode(hooks.NewDeny(d.Reason))
		},
	}
}

// newRootGateCmd keeps subagents out of the tools that mutate shared state.
//
// Only a root may claim files, write memory, or send mail: a subagent's whole
// job is to explore and report back, and a swarm of them writing memory
// concurrently would produce exactly the incoherence stigmergy exists to
// prevent.
func newRootGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "root-gate",
		Short:        "PreToolUse: keep subagents out of the mutating stigmergy tools",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeClaude(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stigmergy: could not parse the hook payload: %v\n", err)
				return nil
			}
			isSub, field := in.SubagentEvidence()
			if !isSub {
				return nil
			}
			return json.NewEncoder(os.Stdout).Encode(hooks.NewDeny(fmt.Sprintf(
				"stigmergy: %s may only be called by a root session, and this call comes from a subagent (%s). "+
					"Report your findings to your root and let it record them: memories, claims and mail are the root's to write.",
				in.ToolName, field)))
		},
	}
}

func newSessionStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "session-start",
		Short:        "SessionStart: tell the agent to register with stigmergy",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeClaude(os.Stdin)
			if err != nil {
				return nil
			}
			text := hooks.SessionStartText("claude-code", in.SessionID, in.CWD)
			if text == "" {
				return nil // Not an adopted project: stay out of the way.
			}
			return json.NewEncoder(os.Stdout).Encode(hooks.NewSessionContext(text))
		},
	}
}

// newSessionEndCmd releases the session's claims at once, rather than making
// every other agent wait out the TTL for a session that is provably gone.
func newSessionEndCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "session-end",
		Short:        "SessionEnd: end this session's root and release its claims",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeClaude(os.Stdin)
			if err != nil {
				return nil
			}
			hooks.EndSession("claude-code", in.SessionID, in.CWD)
			return nil
		},
	}
}

// newHookDumpCmd captures a real host payload so its schema can be read rather
// than guessed at. It is how the open questions about subagent identification
// get settled — install it as a hook, trigger the event, read the file.
func newHookDumpCmd() *cobra.Command {
	var tag string
	cmd := &cobra.Command{
		Use:          "dump",
		Short:        "Append the raw hook payload on stdin to a probe log (for verifying host schemas)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			dir, err := xdg.StateDir()
			if err != nil {
				return err
			}
			path := filepath.Join(dir, "probe.jsonl")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()

			var payload any
			if err := json.Unmarshal(data, &payload); err != nil {
				payload = string(data)
			}
			rec := map[string]any{"tag": tag, "at": time.Now().UTC().Format(time.RFC3339), "payload": payload}
			if err := json.NewEncoder(f).Encode(rec); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "stigmergy: probe %q appended to %s\n", tag, path)
			return nil
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "probe", "label recorded alongside the payload")
	return cmd
}
