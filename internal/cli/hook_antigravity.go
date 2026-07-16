package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/hooks"
)

// newAntigravityHookCmds returns the hook handlers installed by
// `stigmergy init --host antigravity`. They are registered as named hooks in
// .agents/plugins/stigmergy/hooks.json and invoked by Antigravity at the
// corresponding points in its execution loop.
func newAntigravityHookCmds() []*cobra.Command {
	return []*cobra.Command{
		newAntigravityClaimGuardCmd(),
		newAntigravityRootGateCmd(),
		newAntigravityPreInvocationCmd(),
		newAntigravityStopCmd(),
	}
}

// newAntigravityClaimGuardCmd is the enforced path: Antigravity PreToolUse
// can deny a tool call before it runs, so a claimed file is genuinely
// protected — same enforcement level as Claude Code.
func newAntigravityClaimGuardCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "antigravity-claim-guard",
		Short:        "PreToolUse: block edits to files claimed by another agent (Antigravity)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeAntigravityPreToolUse(os.Stdin)
			if err != nil {
				// A payload we cannot parse is a stigmergy problem, not the
				// agent's. Blocking every edit because our own parser broke
				// would be worse than the risk we are guarding against.
				fmt.Fprintf(os.Stderr, "stigmergy: could not parse the hook payload: %v\n", err)
				return nil
			}

			// The workspace that holds the file being edited, which is not
			// necessarily the first one mounted: Antigravity reports every
			// workspace the user has open, and the repository this edit belongs
			// to is the one containing the path we are about to guard.
			edits := in.EditedPaths()
			cwd := in.CWD()
			if len(edits) > 0 {
				cwd = in.CWDFor(edits[0])
			}

			// An edit is the clearest proof of life there is. Heartbeating
			// here keeps the root alive with a short TTL even during long
			// stretches of editing without explicit MCP calls.
			hooks.Heartbeat("antigravity", in.ConversationID, cwd)

			d := hooks.Guard("antigravity", in.ConversationID, cwd, edits)
			if d.Allow {
				// Say nothing, cost nothing. Antigravity documents `decision`
				// as required, but the only value that would let this edit
				// through is "allow", and "allow" means auto-approved: it would
				// suppress the user's own permission prompt for every write in
				// the workspace. Staying silent risks a redundant prompt; the
				// alternative risks approving edits on the user's behalf.
				return nil
			}
			hooks.AuditDenial("antigravity", in.ConversationID, cwd, d)
			return json.NewEncoder(os.Stdout).Encode(hooks.NewAntigravityDeny(d.Reason))
		},
	}
}

// subagentCaveat is appended to the registration instructions on Antigravity,
// because those instructions reach subagents too and would otherwise recruit
// every one of them as a root.
//
// On Claude Code the root gate makes this unnecessary: a subagent's payload says
// it is a subagent, so the mutating tools are simply denied to it. Antigravity
// sends nothing of the kind — a subagent is just another conversation — so the
// only lever left is to tell the agent what stigmergy cannot see for itself, and
// to say why, because an instruction an agent understands the reason for is one
// it can apply to a case this text did not anticipate.
const subagentCaveat = "\nIf you are a subagent — invoked by another agent rather than by the user — " +
	"do not register, do not claim, and do not write memory. Report what you found to the agent that " +
	"invoked you and let it record the durable parts. stigmergy cannot tell your conversation from your " +
	"root's, so this one is on you: a swarm of subagents registering as roots is a swarm of agents that " +
	"can each block the others' edits, and that is the failure this whole tool exists to prevent.\n"

// newAntigravityRootGateCmd keeps subagents out of the tools that mutate
// shared state. Only a root may claim files, write memory, or send mail.
//
// On Antigravity this is currently inert and must be understood as such: no
// documented payload field identifies a subagent, so SubagentEvidence never
// fires and this gate never denies. It is kept because it costs nothing, is
// wired up correctly, and starts working the day the payload grows a field that
// names a parent. Until then the boundary on this host is subagentCaveat, which
// is advice. See docs/hosts.md.
func newAntigravityRootGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "antigravity-root-gate",
		Short:        "PreToolUse: keep subagents out of the mutating stigmergy tools (Antigravity)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeAntigravityPreToolUse(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stigmergy: could not parse the hook payload: %v\n", err)
				return nil
			}
			isSub, field := in.SubagentEvidence()
			if !isSub {
				return nil
			}
			return json.NewEncoder(os.Stdout).Encode(hooks.NewAntigravityDeny(fmt.Sprintf(
				"stigmergy: %s may only be called by a root session, and this call comes from a subagent (%s). "+
					"Report your findings to your root and let it record them: memories, claims and mail are the root's to write.",
				in.ToolCall.Name, field)))
		},
	}
}

// newAntigravityPreInvocationCmd fires before every model call. It serves two
// purposes:
//
//  1. On the first invocation (InvocationNum == 0), it injects the
//     session-start registration instructions, replacing the dedicated
//     SessionStart event that Antigravity does not have.
//
//  2. On every invocation, it delivers pending mail as an ephemeral message
//     before the model sees the context. This is the gentle half of delivery:
//     it informs the turn without interrupting it. The Stop hook enforces.
//
// The first of those is sharper than it looks. On Antigravity a subagent is a
// separate conversation with its own conversationId, so this hook fires for it
// too, with InvocationNum == 0 — and the registration text hands it its own id
// and tells it to register as a root. Nothing in the payload distinguishes it
// from the real root, so the instruction cannot be withheld from it. It can only
// be addressed to it, which is what subagentCaveat does: a request, not an
// enforcement. The agent knows it is a subagent; stigmergy does not.
func newAntigravityPreInvocationCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "antigravity-pre-invocation",
		Short:        "PreInvocation: inject registration instructions and mail notification (Antigravity)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeAntigravityPreInvocation(os.Stdin)
			if err != nil {
				return nil
			}
			hooks.Heartbeat("antigravity", in.ConversationID, in.CWD())

			var parts []string

			// Session start: inject registration instructions on the first
			// model call of the session.
			if in.InvocationNum == 0 {
				if text := hooks.SessionStartText("antigravity", in.ConversationID, in.CWD()); text != "" {
					parts = append(parts, text+subagentCaveat)
				}
			}

			// Mail notify: non-claiming check so the same message still
			// interrupts at Stop if the agent does not act on it here.
			mailText := hooks.MailText(hooks.CheckMail("antigravity", in.ConversationID, in.CWD(), false))
			if mailText != "" {
				parts = append(parts, mailText)
			}

			if len(parts) == 0 {
				return nil
			}
			return json.NewEncoder(os.Stdout).Encode(
				hooks.NewAntigravityInject(strings.Join(parts, "\n")))
		},
	}
}

// newAntigravityStopCmd is the enforced half of mail delivery. It fires when
// the execution loop terminates and re-enters the loop (decision: "continue")
// if there is undelivered mail, injecting the mail text as a system message
// the agent cannot miss.
//
// FullyIdle governs deregistration, not delivery. It reports whether the
// agent's background tasks have finished, and an earlier version of this hook
// read it as Claude Code's stop_hook_active and returned early — allowing the
// stop, and letting an agent with a running background task walk away from its
// mail entirely. It is not that flag: the loop is terminating either way, so
// this is the last moment the message can be put in front of the agent, busy or
// not. What FullyIdle does decide is whether the session is over: an agent with
// tasks still running has not finished, and its claims must not be released out
// from under them.
//
// The loop guard Claude Code needs is not needed here, for a different reason
// than it looks. CheckMail marks what it returns as delivered, so the mail that
// caused a block cannot cause a second one. Only genuinely new mail arriving
// during the continuation blocks again, and that is delivery working, not a
// trap.
func newAntigravityStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "antigravity-stop",
		Short:        "Stop: deliver pending mail and end the session (Antigravity)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.DecodeAntigravityStop(os.Stdin)
			if err != nil {
				return nil
			}
			hooks.Heartbeat("antigravity", in.ConversationID, in.CWD())

			mail := hooks.CheckMail("antigravity", in.ConversationID, in.CWD(), true)
			if text := hooks.MailText(mail); text != "" {
				return json.NewEncoder(os.Stdout).Encode(
					hooks.NewAntigravityStopBlock(text))
			}

			// No mail, and nothing still running: the session is over, so free
			// its claims now rather than making every other agent wait out the
			// TTL for an agent that has provably finished.
			if in.FullyIdle {
				hooks.EndSession("antigravity", in.ConversationID, in.CWD())
			}
			return nil
		},
	}
}
