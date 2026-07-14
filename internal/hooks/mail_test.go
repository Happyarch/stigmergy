package hooks

import (
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/store"
)

// register puts a root in the fixture's project without giving it a claim.
func (f *fixture) register(t *testing.T, kind, sessionLabel string) string {
	t.Helper()
	root, _, err := f.db.RegisterRoot(store.Registration{
		RootID: ids.NewRootID(), AgentKind: kind,
		Worktree: f.worktree, SessionLabel: sessionLabel,
	})
	if err != nil {
		t.Fatal(err)
	}
	return root.RootID
}

// TestMailIsHandedToTheAgentExactlyOnce is the contract the Stop hook rests on.
// Deliver every message — an agent that is never told has not ignored its mail,
// it has been failed by the mailbox — but deliver each one only once, or the Stop
// gate becomes a trap the agent can never get out of.
func TestMailIsHandedToTheAgentExactlyOnce(t *testing.T) {
	f := newFixture(t)
	sender := f.register(t, "codex", "sess-sender")
	f.register(t, "claude-code", "sess-recipient")

	recipient, err := f.db.RootBySession("claude-code", "sess-recipient")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SendMessage(store.SendRequest{
		FromRoot: sender, ToRoot: recipient.RootID,
		Subject: "src/main.go", Body: "I am blocked on your claim — may I have it?",
	}); err != nil {
		t.Fatal(err)
	}

	// A reminder at the top of a turn mentions the mail without claiming it: an
	// agent on its way to do something else has been told, not delivered to.
	reminded := CheckMail("claude-code", "sess-recipient", f.worktree, false)
	if len(reminded.Pending) != 1 {
		t.Fatalf("prompt-time reminder found %d messages, want 1", len(reminded.Pending))
	}

	// The Stop gate claims it: the agent has been interrupted and handed the text.
	delivered := CheckMail("claude-code", "sess-recipient", f.worktree, true)
	if len(delivered.Pending) != 1 {
		t.Fatalf("Stop gate found %d messages, want 1 — the reminder must not have consumed it", len(delivered.Pending))
	}

	text := MailText(delivered)
	for _, want := range []string{sender, "src/main.go", "mailbox_send", "mailbox_resolve"} {
		if !strings.Contains(text, want) {
			t.Errorf("delivered text omits %q, so the agent has to remember it:\n%s", want, text)
		}
	}

	// And never again. Blocking the same turn-end forever would leave the agent
	// unable to finish at all.
	if again := CheckMail("claude-code", "sess-recipient", f.worktree, true); len(again.Pending) != 0 {
		t.Fatalf("the same message was delivered twice: %d pending", len(again.Pending))
	}
	if MailText(CheckMail("claude-code", "sess-recipient", f.worktree, false)) != "" {
		t.Error("a delivered message is still being announced")
	}
}

// TestNoMailCostsNothing: the overwhelmingly common case is an agent with an
// empty mailbox ending a turn, and it must produce no text at all — a Stop hook
// that says something every time is one the agent learns to ignore.
func TestNoMailCostsNothing(t *testing.T) {
	f := newFixture(t)
	f.register(t, "claude-code", "sess-quiet")

	if text := MailText(CheckMail("claude-code", "sess-quiet", f.worktree, true)); text != "" {
		t.Errorf("an empty mailbox produced %q, want silence", text)
	}
	// An unregistered session has no mailbox, and must not be nagged about one.
	if text := MailText(CheckMail("claude-code", "sess-unregistered", f.worktree, true)); text != "" {
		t.Errorf("an unregistered session produced %q, want silence", text)
	}
}

// TestHeartbeatFromAHookKeepsClaimsAlive: the claim guard fires on every edit, so
// an agent that is working proves it is alive by working. This is what lets the
// root TTL be short enough that a crashed agent's claims lapse in minutes.
func TestHeartbeatFromAHookKeepsClaimsAlive(t *testing.T) {
	f := newFixture(t)
	root := f.register(t, "claude-code", "sess-worker")

	before, err := f.db.GetRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	Heartbeat("claude-code", "sess-worker", f.worktree)

	after, err := f.db.GetRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastSeenAt < before.LastSeenAt {
		t.Fatalf("heartbeat moved last_seen_at backwards: %s -> %s", before.LastSeenAt, after.LastSeenAt)
	}
	if !after.Live() {
		t.Fatal("a root that just heartbeated does not read as live")
	}

	// Best-effort everywhere else: an unregistered session, or a directory that is
	// not a stigmergy project, must not cost the agent its edit.
	Heartbeat("claude-code", "sess-nobody", f.worktree)
	Heartbeat("claude-code", "sess-worker", t.TempDir())
}

// TestHooksNeverMigrateTheSchema. The delivery hooks write — a heartbeat, a
// notified_at stamp — and it would be very easy for that to mean "opened
// read-write, therefore migrated". It must not.
//
// These hooks fire in every project an agent touches. A hook that migrated would
// mean that installing a new stigmergy silently upgrades the schema of every
// repository an agent visits, mid-edit, under a 250ms lock timeout, racing the
// MCP server that another still-running session opened with the *old* binary.
// Schema changes belong to init, doctor and the server, which can take their time
// and say what they did.
func TestHooksNeverMigrateTheSchema(t *testing.T) {
	f := newFixture(t)
	f.register(t, "claude-code", "sess-worker")

	// Wind the database back to a schema this binary does not recognise, as an
	// un-migrated project would be to a freshly-installed newer stigmergy.
	latest, err := store.LatestVersion(store.Project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`DELETE FROM schema_migrations WHERE version = ?`, latest); err != nil {
		t.Fatal(err)
	}

	// Every hook path, against a schema it must refuse to touch.
	Heartbeat("claude-code", "sess-worker", f.worktree)
	if mail := CheckMail("claude-code", "sess-worker", f.worktree, true); !mail.empty() {
		t.Error("a delivery hook read a database whose schema it does not recognise")
	}
	if text := SessionStartText("claude-code", "sess-worker", f.worktree); text == "" {
		t.Error("session-start went silent; it must still tell the agent to register")
	}

	var version int
	if err := f.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version == latest {
		t.Fatalf("a hook migrated the database to v%d — schema changes are for init, doctor and the server", version)
	}
}

// TestConflictNamesTheOwnerAndWhetherItCanAnswer: the blocked agent is told the
// root id to write to at the moment it is blocked, so it never has to reconstruct
// one from memory — which is how mail ends up addressed to an agent that died an
// hour ago.
func TestConflictNamesTheOwnerAndWhetherItCanAnswer(t *testing.T) {
	f := newFixture(t)
	owner := f.claim(t, "sess-owner", "src/main.go", false)

	d := Guard("claude-code", "sess-other", f.worktree, []string{"src/main.go"})
	if d.Allow {
		t.Fatal("an edit into another agent's claim was allowed")
	}
	for _, want := range []string{owner, "mailbox_send", "live"} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("denial omits %q:\n%s", want, d.Reason)
		}
	}
}
