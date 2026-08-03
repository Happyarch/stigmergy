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
		Use: "hook",
		// Not a host list: this one has been out of date twice already, and
		// nothing here depends on knowing which hosts exist.
		Short: "Host hook handlers (invoked by the agent hosts, not by hand)",
		Long: "Hook handlers read a host's JSON payload on stdin and write a decision on stdout.\n" +
			"They are wired up by `stigmergy init` and are not meant to be run directly.",
	}
	cmd.AddCommand(newClaimGuardCmd(), newRootGateCmd(), newSubagentStopCmd(),
		newSessionStartCmd(), newSessionEndCmd(),
		newMailGateCmd(), newMailNotifyCmd(), newHookDumpCmd())
	cmd.AddCommand(newCodexHookCmds()...)
	cmd.AddCommand(newAntigravityHookCmds()...)
	cmd.AddCommand(newOpenCodeHookCmds()...)
	return cmd
}

// newMailGateCmd delivers mail at the end of a turn, by refusing to let the turn
// end while there is undelivered mail.
//
// It is the answer to the mailbox's original defect: it was a pull channel with
// nothing to pull it. An agent had to think to call mailbox_inbox, and an agent
// deep in its own work does not — so messages sat unread, and the agent that sent
// one waited on an answer that was never coming. Reminding the agent in its
// instructions did not fix this, and could not: the instruction is read once, at
// the start, and the mail arrives later.
//
// Blocking the Stop is the only place a message can be put in front of an agent
// that is not asking for one. It costs the agent nothing when there is no mail —
// which is almost always — and when there is, it arrives at the natural moment:
// the agent has finished what it was doing and is about to walk away.
func newMailGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "mail-gate",
		Short:        "Stop: hand the agent its mail before it finishes the turn",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeClaude(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stigmergy: could not parse the hook payload: %v\n", err)
				return nil
			}
			// The agent is only still running because we blocked it last time.
			// Blocking again would trap it: it has been shown the mail, and what
			// it does about it is now its own business.
			if in.StopHookActive {
				return nil
			}
			hooks.Heartbeat("claude-code", in.SessionID, in.CWD)

			mail := hooks.CheckMail("claude-code", in.SessionID, in.CWD)
			text := hooks.MailText(mail)
			if text == "" {
				return nil
			}
			if err := json.NewEncoder(os.Stdout).Encode(hooks.NewStopBlock(text)); err != nil {
				// The mail was never written, so it stays undelivered and will
				// interrupt again. Marking first would consume it silently.
				return err
			}
			hooks.MarkDelivered("claude-code", in.SessionID, in.CWD, mail)
			return nil
		},
	}
}

// newMailNotifyCmd is the gentle half of delivery: it tells an agent what is
// waiting as its turn begins, so mail informs the work rather than interrupting
// it.
//
// It does not mark the mail delivered. A line of context an agent reads on its
// way to doing something else has been mentioned, not delivered, and if that were
// enough to satisfy the mailbox the Stop gate would never fire for it — which is
// exactly the failure this is meant to prevent. So the same message will still
// stop the turn at the end if the agent has not dealt with it, and this is only a
// courtesy: a chance to handle it while it is still cheap.
func newMailNotifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "mail-notify",
		Short:        "UserPromptSubmit: tell the agent what mail is waiting, and keep its claims alive",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeClaude(os.Stdin)
			if err != nil {
				return nil
			}
			// A prompt is proof of life: an agent that has been idle for an hour
			// while its user was away is not a crashed agent, and must not lose
			// its claims on the strength of a silence that was never its own.
			hooks.Heartbeat("claude-code", in.SessionID, in.CWD)

			text := hooks.MailText(hooks.CheckMail("claude-code", in.SessionID, in.CWD))
			if text == "" {
				return nil
			}
			return json.NewEncoder(os.Stdout).Encode(hooks.NewPromptContext(text))
		},
	}
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

			// An edit is the clearest proof of life there is, and this hook fires
			// on every one of them. That is what lets the root TTL be short enough
			// for a dead agent's claims to lapse in minutes rather than an hour:
			// a working agent proves it is alive by working.
			// Judged as whoever is actually editing. A peer inside the session has
			// its own root and its own claims, so guarding it under the session's
			// label would tell it that its neighbour's claims are its own — the
			// exact fail-open this hook exists to prevent — and would keep the
			// session's root alive on the strength of a peer's work.
			label := in.Label()
			hooks.Heartbeat("claude-code", label, in.CWD)

			d := hooks.Guard("claude-code", label, in.CWD, in.EditedPaths())
			if d.Allow {
				return nil // Say nothing, cost nothing.
			}
			hooks.AuditDenial("claude-code", label, in.CWD, d)
			return json.NewEncoder(os.Stdout).Encode(hooks.NewDeny(d.Reason))
		},
	}
}

// newRootGateCmd tells the MCP server which agent is calling it, and holds the
// line on the two things that still belong to the session alone.
//
// It used to do only the second half, and did it to everyone: any call carrying
// a hint of a subagent was denied, claims included. That was a guess standing in
// for an identity — the server could not tell one agent in a session from
// another, so the safe answer was to refuse them all. It made every peer agent
// in a shared session unable to claim a file, and unable to register a root that
// would have let it.
//
// The identity now exists (see store.AgentLabel and the caller-ticket table), so
// the refusal can be narrowed to what it was always really about. A peer holds
// its own claims, because two agents editing the same file is the failure this
// project exists to prevent and claims are how that is prevented. Memory and
// mail stay with the session root: a peer lives for a minute, cannot read a
// reply that arrives after it is gone, and a swarm of them writing memory
// concurrently is the incoherence stigmergy exists to prevent.
func newRootGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "root-gate",
		Short:        "PreToolUse: identify which agent is calling, and keep peers out of memory and mail",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeClaude(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stigmergy: could not parse the hook payload: %v\n", err)
				return nil
			}
			if !in.IsAgent() {
				return nil // The main thread is the session; nothing to say.
			}
			if reason := hooks.SessionOnlyTool(in.ToolName); reason != "" {
				return json.NewEncoder(os.Stdout).Encode(hooks.NewDeny(reason))
			}
			// Stamped only once the call is going ahead. A ticket for a call that
			// was just denied would sit there until it expired, and the next
			// unstamped call in this session would be attributed to whoever was
			// refused.
			hooks.StampCaller("claude-code", in.Label(), in.CWD, in.ToolUseID)
			return nil
		},
	}
}

// newSubagentStopCmd ends an agent's root the moment the agent does, so the
// files it claimed are free again immediately rather than at the TTL.
//
// SubagentStop carries agent_id and agent_type, the same pair PreToolUse does,
// which is what makes this possible at all: without them there would be no way
// to tell which of the session's agents had just finished.
func newSubagentStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "subagent-stop",
		Short:        "SubagentStop: end this agent's root and release its claims",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeClaude(os.Stdin)
			if err != nil || !in.IsAgent() {
				return nil
			}
			hooks.EndAgent("claude-code", in.Label(), in.CWD)
			return nil
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
