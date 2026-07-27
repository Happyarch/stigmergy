package cli

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/hooks"
)

// The Codex hooks are weaker than the Claude ones, and the asymmetry is
// structural rather than an oversight. Codex's PreToolUse cannot deny a tool
// call — the manual is explicit that only systemMessage is honored, and that
// returning continue:false there marks the hook failed and lets the call
// proceed. So a claim cannot be enforced before the write on Codex. What can be
// done is to warn loudly first, and then halt the turn after the edit lands, so
// the agent cannot keep building on a conflicting change.
//
// The other consequence: because a claim cannot be enforced here, these hooks
// fail OPEN. Blocking on a database error would cost the agent its work without
// buying any protection, since the write it was about to make could not have
// been stopped anyway.

func newCodexSessionStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "codex-session-start",
		Short:        "Codex SessionStart: tell the agent to register with stigmergy",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeCodex(os.Stdin)
			if err != nil {
				return nil
			}
			text := hooks.SessionStartText("codex", in.SessionID, in.CWD)
			if text == "" {
				return nil
			}
			text += "\nCodex cannot block an edit to a claimed file before it happens. If you write to one anyway, " +
				"the turn is halted after the fact and you will have to undo the change. Check claims yourself.\n"
			return json.NewEncoder(os.Stdout).Encode(hooks.NewCodexContext(text))
		},
	}
}

// newCodexMailNotifyCmd delivers mail on Codex, at the two moments Codex gives
// us that reliably reach the agent: the start of a turn, and after a tool call.
//
// Codex has a Stop event, but it cannot be used for this. Its Stop output is
// limited to continue/stopReason/systemMessage, and systemMessage is documented
// as a warning surfaced "in the UI or event stream" — to the human, that is, with
// no promise it lands in the agent's context. Claude's Stop hook can put text
// into the conversation and block; Codex's cannot. So on Codex the mailbox is
// delivered rather than enforced: the agent is told at the top of each turn and
// after each tool call, and the tool results carry the unread count as a backstop.
//
// Marking the mail delivered is therefore deliberate here and not merely
// mechanical: these are the channels we can see land, so a message announced
// through them has genuinely been announced.
func newCodexMailNotifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "codex-mail-notify",
		Short:        "Codex UserPromptSubmit/PostToolUse: hand the agent its mail, and keep its claims alive",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeCodex(os.Stdin)
			if err != nil {
				return nil
			}
			hooks.Heartbeat("codex", in.SessionID, in.CWD)

			mail := hooks.CheckMail("codex", in.SessionID, in.CWD)
			text := hooks.MailText(mail)
			if text == "" {
				return nil
			}
			if err := json.NewEncoder(os.Stdout).Encode(hooks.CodexWarning{SystemMessage: text}); err != nil {
				return err
			}
			hooks.MarkDelivered("codex", in.SessionID, in.CWD, mail)
			return nil
		},
	}
}

func newCodexClaimWarnCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "codex-claim-warn",
		Short:        "Codex PreToolUse: warn about an edit to a claimed path (Codex cannot block it)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeCodex(os.Stdin)
			if err != nil {
				return nil
			}
			// Every edit is proof this agent is alive, which is what lets the root
			// TTL stay short. Codex has no other regular heartbeat.
			hooks.Heartbeat("codex", in.SessionID, in.CWD)

			d := hooks.Guard("codex", in.SessionID, in.CWD, hooks.ExtractPaths(in))
			if d.Allow || len(d.Conflicts) == 0 {
				// Allowed, or we could not tell. Fail open: a warning we cannot
				// substantiate is noise, and we could not have blocked the
				// write regardless.
				return nil
			}
			return json.NewEncoder(os.Stdout).Encode(hooks.CodexWarning{
				SystemMessage: hooks.CodexWarnText(d),
			})
		},
	}
}

func newCodexClaimStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "codex-claim-stop",
		Short:        "Codex PostToolUse: halt the turn when an edit landed on a claimed path",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeCodex(os.Stdin)
			if err != nil {
				return nil
			}
			d := hooks.Guard("codex", in.SessionID, in.CWD, hooks.ExtractPaths(in))
			if d.Allow || len(d.Conflicts) == 0 {
				return nil
			}
			hooks.AuditCodexConflict(in.SessionID, in.CWD, d)
			return json.NewEncoder(os.Stdout).Encode(hooks.CodexHalt{
				Continue:      false,
				StopReason:    "stigmergy: edited a path claimed by another agent",
				SystemMessage: hooks.CodexHaltText(d),
			})
		},
	}
}

func newCodexHookCmds() []*cobra.Command {
	return []*cobra.Command{
		newCodexSessionStartCmd(),
		newCodexClaimWarnCmd(),
		newCodexClaimStopCmd(),
		newCodexMailNotifyCmd(),
	}
}
