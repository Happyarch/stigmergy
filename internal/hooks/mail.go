package hooks

import (
	"fmt"
	"strings"
	"time"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/store"
)

// heartbeatBudget caps how long a hook will spend refreshing the root's
// liveness. Like the audit write, this is bookkeeping the agent is waiting on,
// so it gets a budget rather than a promise: a missed heartbeat costs a little
// of the TTL, while a slow one costs the agent its turn.
const heartbeatBudget = 100 * time.Millisecond

// Heartbeat keeps a working agent alive without the agent having to think about
// it.
//
// This is what pays for the short root TTL. Liveness used to depend on an agent
// choosing to call a stigmergy tool, which meant an agent heads-down in a long
// stretch of editing looked exactly like an agent that had crashed — so the TTL
// had to be generous enough to cover the longest plausible silence, and a genuine
// crash then held its claims for that whole hour.
//
// Hooks break that trade. They fire on the agent's own activity — every edit,
// every turn — so a working agent is continuously proving it is alive as a side
// effect of working, and silence starts to mean what it says. The TTL can then be
// set to how long a dead agent may go on blocking others, which is a much shorter
// number.
//
// Best-effort throughout: an unregistered session has no claims to keep alive,
// and a heartbeat that cannot be written must never cost the agent its edit.
func Heartbeat(agentKind, sessionLabel, cwd string) {
	if sessionLabel == "" {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		db, _, ok := openProject(cwd)
		if !ok {
			return
		}
		defer db.Close()
		_ = db.HeartbeatSession(agentKind, sessionLabel)
	}()

	select {
	case <-done:
	case <-time.After(heartbeatBudget):
	}
}

// Mail is what a delivery hook found waiting for an agent.
type Mail struct {
	// Pending is mail the agent has never been shown. Reporting it marks it
	// delivered, so it interrupts exactly once.
	Pending []store.Delivery
	// Stalled are conversations where this agent is waiting on a reply from a
	// root that is gone. Nobody will ever answer these, and the agent needs to be
	// told so — it is otherwise indistinguishable from being politely ignored.
	Stalled []store.ThreadSummary
}

func (m Mail) empty() bool { return len(m.Pending) == 0 && len(m.Stalled) == 0 }

// CheckMail finds what an agent needs to hear about, and — if claim is true —
// records the pending messages as delivered.
//
// The claim flag is the difference between announcing and reminding. The Stop
// hook claims: it has interrupted the agent, the mail is now in its context, and
// interrupting a second time for the same message would be nagging rather than
// delivering. A reminder at the top of a turn does not claim, because a message
// mentioned in passing to an agent that is about to do something else has not
// really been delivered at all.
func CheckMail(agentKind, sessionLabel, cwd string, claim bool) Mail {
	if sessionLabel == "" {
		return Mail{}
	}
	db, _, ok := openProject(cwd)
	if !ok {
		return Mail{}
	}
	defer db.Close()

	root, err := db.RootBySession(agentKind, sessionLabel)
	if err != nil {
		// Not registered: no root, so no mailbox. The session-start text is
		// already telling this agent to register, and saying it again here would
		// not help.
		return Mail{}
	}

	var m Mail
	if pending, err := db.PendingMail(root.RootID); err == nil {
		m.Pending = pending
	}
	if threads, err := db.Threads(root.RootID, store.ThreadOpen); err == nil {
		for _, t := range threads {
			if t.Stalled() {
				m.Stalled = append(m.Stalled, t)
			}
		}
	}

	if claim && len(m.Pending) > 0 {
		ids := make([]int64, 0, len(m.Pending))
		for _, p := range m.Pending {
			ids = append(ids, p.ID)
		}
		// If this write fails the message stays unnotified and will interrupt
		// again next turn. Annoying, and strictly better than the alternative:
		// marking mail delivered that the agent never saw.
		_ = db.MarkNotified(root.RootID, ids)
	}
	return m
}

// MailText renders mail as the agent will read it: what arrived, who from,
// whether they can still be answered, and what to do about it.
//
// The reply instruction carries the sender's root id because that is the
// question this whole path exists to answer. An agent replying from memory —
// from an id it saw in a conflict twenty minutes ago, or in a note it wrote to
// itself — is how mail ends up addressed to a root that has since died. The id
// is put in the message so it never has to be remembered.
func MailText(m Mail) string {
	if m.empty() {
		return ""
	}
	var sb strings.Builder

	if n := len(m.Pending); n > 0 {
		fmt.Fprintf(&sb, "stigmergy: %s waiting for you.\n\n", plural(n, "message", "messages"))
		for _, p := range m.Pending {
			fmt.Fprintf(&sb, "  from %s (%s, %s), thread %d\n", p.FromRoot, p.FromKind, p.FromLiveness, p.ThreadID)
			fmt.Fprintf(&sb, "  subject: %s\n", p.Subject)
			fmt.Fprintf(&sb, "  %s\n\n", indent(p.Body))
		}
		sb.WriteString("Another agent is blocked on you, or is about to be. Deal with this before you carry on:\n" +
			"  - reply with mailbox_send(to_root=…, thread_id=…), using the ids above;\n" +
			"  - if you agreed to give up a path, release the claim (claim_release) — saying yes is not enough;\n" +
			"  - close the negotiation with mailbox_resolve when it is settled;\n" +
			"  - mailbox_mark_read acknowledges mail you have handled.\n" +
			"If a message needs no action, mark it read and say why in your reply — silence reads as neglect.\n")
	}

	if n := len(m.Stalled); n > 0 {
		if len(m.Pending) > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "stigmergy: %s waiting on an agent that is gone.\n\n", plural(n, "conversation", "conversations"))
		for _, t := range m.Stalled {
			fmt.Fprintf(&sb, "  thread %d with %s — %s\n    last message: %q (you)\n",
				t.ID, t.With, t.WithLiveness, t.Subject)
		}
		sb.WriteString("\nNo answer is coming, and the claims that root held have lapsed with it: " +
			"the paths you were asking about are free. Stop waiting — take the claim you need and get on with it, " +
			"and close the thread with mailbox_resolve(abandoned=true).\n")
	}
	return sb.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// indent keeps a multi-line message body visibly part of the message it belongs
// to, rather than running into the instructions that follow it.
func indent(body string) string {
	return strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n  ")
}

// openProject opens the project database for a delivery hook, or reports that
// there is nothing here to open.
//
// Writable, because delivery has to write: a heartbeat, a notified_at stamp.
// Quiet, because a database this binary does not recognise is left alone — no
// delivery, no heartbeat, no complaint. The agent is not silently stranded by
// that, because the claim guard checks the same version and fails closed loudly,
// with the one message that matters ("run stigmergy doctor"), rather than a
// mailbox quietly doing nothing.
//
// The resolution, the version gate and the never-migrate rule all live in
// resolveProject now; this is the delivery-shaped view of it.
func openProject(cwd string) (*store.DB, *gitx.Repo, bool) {
	p, ok := openQuietly(cwd, writable)
	if !ok {
		return nil, nil, false
	}
	return p.DB, p.Repo, true
}
