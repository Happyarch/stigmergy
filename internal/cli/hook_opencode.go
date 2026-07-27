package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/hooks"
)

// The opencode hooks.
//
// opencode has no hook system in the sense the other three hosts mean it: there
// is no config file that names a command to run. It has a plugin API in
// TypeScript, loaded into its own runtime. So `stigmergy init --host opencode`
// writes a small plugin that shells out to these subcommands, and the shape
// below is the same as everywhere else — decode, call the shared seam, encode —
// with the plugin doing the translation.
//
// The difference from the other hosts is that these speak a payload we defined,
// so there is nothing to guess. What was guessed, and then checked against a
// live session, is in the opencode-plugin-contract memory.
const openCodeKind = "opencode"

func newOpenCodeHookCmds() []*cobra.Command {
	return []*cobra.Command{
		newOpenCodeClaimGuardCmd(),
		newOpenCodeRootGateCmd(),
		newOpenCodeContextCmd(),
	}
}

// newOpenCodeClaimGuardCmd blocks an edit to a file another agent has claimed.
//
// This host can genuinely block. The plugin throws when we say deny, opencode
// treats a throw from tool.execute.before as an unrecoverable defect, and the
// tool never runs — the trigger is awaited before the tool executes and nothing
// catches it. The reason reaches the model verbatim, so unlike Codex the agent
// is both stopped and told who to talk to.
func newOpenCodeClaimGuardCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "opencode-claim-guard",
		Short:        "tool.execute.before: block edits to files claimed by another agent",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeOpenCode(os.Stdin)
			if err != nil {
				// Our parser breaking must not stop the user's work. Allow, and
				// say so on stderr where opencode surfaces it.
				fmt.Fprintf(os.Stderr, "stigmergy: could not parse the hook payload: %v\n", err)
				return allowOpenCode()
			}
			// Every tool call is proof of life, not just edits: this hook sees
			// them all, which is what keeps a 15-minute root TTL affordable.
			hooks.Heartbeat(openCodeKind, in.SessionID, in.CWD())

			if !in.IsEdit() {
				return allowOpenCode()
			}
			d := hooks.Guard(openCodeKind, in.SessionID, in.CWD(), in.EditedPaths())
			if d.Allow {
				return allowOpenCode()
			}
			hooks.AuditDenial(openCodeKind, in.SessionID, in.CWD(), d)
			return json.NewEncoder(os.Stdout).Encode(hooks.NewOpenCodeDeny(d.Reason))
		},
	}
}

// newOpenCodeRootGateCmd keeps subagents out of the tools only a root may call.
//
// This gate actually works here, which is worth saying because it does not on
// two of the other hosts. Codex gives us nothing to identify a subagent, and an
// Antigravity subagent is a separate conversation with no recorded parent — so
// on both, the rule survives only because the agent keeps it. opencode records
// a parentID on the session, and the plugin resolves it before calling us.
func newOpenCodeRootGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "opencode-root-gate",
		Short:        "tool.execute.before: keep subagents out of the root-only tools",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeOpenCode(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stigmergy: could not parse the hook payload: %v\n", err)
				return allowOpenCode()
			}
			if !in.IsSubagent() {
				return allowOpenCode()
			}
			return json.NewEncoder(os.Stdout).Encode(hooks.NewOpenCodeDeny(
				"This is a subagent session (its parent is " + in.ParentID + "), and only a root " +
					"may claim files, write memory, or send mail. Report what you found to the agent " +
					"that invoked you and let it record what lasts."))
		},
	}
}

// newOpenCodeContextCmd hands the agent its registration details and its mail.
//
// It is the session-start hook and the mail notifier at once, because opencode
// gives us one place that fires at the right moment: chat.message, once per real
// user prompt. That is the UserPromptSubmit analogue, and it deliberately does
// not fire for the internal title, summary or compaction agents.
//
// The obvious alternative was experimental.chat.system.transform, which reaches
// every request and carries the sessionID. It is a trap: it fires for those
// internal agents too, with nothing in the payload to tell them apart — a live
// probe caught stigmergy's text being pushed into the title generator's prompt —
// and the system prompt is cache-sensitive, so volatile text there would cost a
// cache miss every turn.
//
// Unlike Claude's session-start, this can fire many times in a session, so it
// says the registration part only until the root is actually registered. The
// database already knows; there is no reason to repeat ourselves at an agent
// that has done as it was asked.
func newOpenCodeContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "opencode-context",
		Short:        "chat.message: hand the agent its registration details and any mail",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeOpenCode(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stigmergy: could not parse the hook payload: %v\n", err)
				return json.NewEncoder(os.Stdout).Encode(hooks.OpenCodeContext{})
			}
			hooks.Heartbeat(openCodeKind, in.SessionID, in.CWD())

			// A subagent must not be told to register: it would register as a
			// root of its own, and then a swarm of roots blocks each other's
			// edits — the failure stigmergy exists to prevent. It gets its mail
			// checked by nobody and needs nothing else.
			if in.IsSubagent() {
				return json.NewEncoder(os.Stdout).Encode(hooks.OpenCodeContext{})
			}

			// Once the agent has registered, it needs its mail and nothing else.
			// SessionStartText carries the mail itself, so asking for both would
			// deliver it twice.
			var text string
			var mail hooks.Mail
			if hooks.Registered(openCodeKind, in.SessionID, in.CWD()) {
				mail = hooks.CheckMail(openCodeKind, in.SessionID, in.CWD())
				text = hooks.MailText(mail)
			} else {
				text = hooks.SessionStartText(openCodeKind, in.SessionID, in.CWD())
			}
			if err := json.NewEncoder(os.Stdout).Encode(hooks.OpenCodeContext{Context: text}); err != nil {
				return err
			}
			hooks.MarkDelivered(openCodeKind, in.SessionID, in.CWD(), mail)
			return nil
		},
	}
}

// allowOpenCode says yes explicitly. See hooks.NewOpenCodeAllow for why silence
// will not do here.
func allowOpenCode() error {
	return json.NewEncoder(os.Stdout).Encode(hooks.NewOpenCodeAllow())
}
