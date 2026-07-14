package store

import (
	"strings"
	"testing"
	"time"

	"github.com/happyarch/stigmergy/internal/serr"
)

// The mailbox used to be a pull channel with nothing to pull it: a message an
// agent was never told about was indistinguishable from one it had chosen to
// ignore. These tests pin the difference — notified is stigmergy's record that it
// put the mail in front of the agent, and it happens exactly once per message.

func TestPendingMailIsDeliveredOnceAndCarriesSenderLiveness(t *testing.T) {
	db := testProject(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	var sender, recipient string
	at(t, now, func() {
		sender = mustRoot(t, db, "claude-code", "sess-sender")
		recipient = mustRoot(t, db, "codex", "sess-recipient")
		if _, err := db.SendMessage(SendRequest{
			FromRoot: sender, ToRoot: recipient,
			Subject: "internal/store/claims.go", Body: "may I have it?",
		}); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	})

	at(t, now.Add(time.Minute), func() {
		pending, err := db.PendingMail(recipient)
		if err != nil {
			t.Fatalf("PendingMail: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("PendingMail returned %d messages, want 1", len(pending))
		}
		// The sender's liveness rides along because it decides what the recipient
		// should do: reply to the living, act on the dead.
		if !strings.HasPrefix(pending[0].FromLiveness, "live") {
			t.Errorf("sender liveness = %q, want it to read as live", pending[0].FromLiveness)
		}
		if pending[0].FromKind != "claude-code" {
			t.Errorf("sender kind = %q, want claude-code", pending[0].FromKind)
		}

		if err := db.MarkNotified(recipient, []int64{pending[0].ID}); err != nil {
			t.Fatalf("MarkNotified: %v", err)
		}
	})

	// Delivered once. An agent that was shown the mail and pressed on regardless
	// must not be interrupted by the same message forever: that is nagging, not
	// delivering, and an agent nagged in a loop cannot finish a turn at all.
	at(t, now.Add(2*time.Minute), func() {
		pending, err := db.PendingMail(recipient)
		if err != nil {
			t.Fatalf("PendingMail after MarkNotified: %v", err)
		}
		if len(pending) != 0 {
			t.Fatalf("message was announced twice: %d still pending", len(pending))
		}
	})

	// Still unread, though. Delivery is not reading, and the inbox must not
	// pretend the agent looked.
	unread, err := db.Inbox(recipient, true)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(unread) != 1 {
		t.Fatalf("unread = %d, want 1: notifying is not reading", len(unread))
	}
}

// TestMailToADeadRootNamesTheLivingInstead is the failure that started this: an
// agent, blocked on a file, wrote to a root it had seen named somewhere — one
// that had since died — while the agent actually holding the file was never
// asked. Refusing the send is necessary but not sufficient; the refusal has to
// say who to write to instead, or the agent just guesses again.
func TestMailToADeadRootNamesTheLivingInstead(t *testing.T) {
	db := testProject(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	var dead, live, sender string
	at(t, now.Add(-2*RootTTL), func() { // long enough ago to be past the TTL
		dead = mustRoot(t, db, "codex", "sess-dead")
	})
	at(t, now, func() {
		live = mustRoot(t, db, "claude-code", "sess-live")
		sender = mustRoot(t, db, "claude-code", "sess-sender")
		if _, err := db.AcquireClaim(ClaimRequest{
			RootID: live, ScopePath: "internal/store/claims.go",
			Worktree: "/repo", Reason: "rewriting the overlap check",
		}); err != nil {
			t.Fatalf("AcquireClaim: %v", err)
		}

		_, err := db.SendMessage(SendRequest{
			FromRoot: sender, ToRoot: dead,
			Subject: "claims.go", Body: "can I have it?",
		})
		if err == nil {
			t.Fatal("mail to a dead root was accepted; it would never have been read")
		}
		e, ok := serr.As(err)
		if !ok || e.Code != serr.RecipientInactive {
			t.Fatalf("code = %v, want recipient_inactive", err)
		}
		// The whole point: the agent is handed the roster rather than left to
		// guess a second time.
		if !strings.Contains(e.Message, live) {
			t.Errorf("refusal does not name the live root %s: %q", live, e.Message)
		}
		if !strings.Contains(e.Message, "internal/store/claims.go") {
			t.Errorf("refusal does not say what the live root holds: %q", e.Message)
		}
		if e.Context["active_roots"] == nil {
			t.Error("refusal carries no active_roots for the agent to act on")
		}
	})
}

// TestThreadGoesStalledWhenTheOtherAgentDies covers the other half of the same
// failure. An agent that asked for a file and got no answer cannot tell "they are
// thinking" from "they died an hour ago" — so it waits, forever, on a claim that
// has already lapsed.
func TestThreadGoesStalledWhenTheOtherAgentDies(t *testing.T) {
	db := testProject(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	var asker, owner string
	at(t, now, func() {
		asker = mustRoot(t, db, "claude-code", "sess-asker")
		owner = mustRoot(t, db, "codex", "sess-owner")
		if _, err := db.SendMessage(SendRequest{
			FromRoot: asker, ToRoot: owner, Subject: "handlers.go", Body: "may I?",
		}); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	})

	// While the owner is alive, the asker is simply waiting. Nothing is wrong.
	at(t, now.Add(time.Minute), func() {
		threads, err := db.Threads(asker, ThreadOpen)
		if err != nil {
			t.Fatalf("Threads: %v", err)
		}
		if len(threads) != 1 {
			t.Fatalf("threads = %d, want 1", len(threads))
		}
		if threads[0].Stalled() {
			t.Error("thread reported stalled while the other agent is still alive")
		}
	})

	// The owner goes silent past the TTL. Now the wait is pointless, and the
	// asker has to be told so.
	at(t, now.Add(2*RootTTL), func() {
		threads, err := db.Threads(asker, ThreadOpen)
		if err != nil {
			t.Fatalf("Threads: %v", err)
		}
		if !threads[0].Stalled() {
			t.Fatalf("thread with a dead agent is not stalled: with_liveness = %q", threads[0].WithLiveness)
		}
	})
}

// TestALapsedRootLosesItsClaimsWhenItComesBack guards the hole that a short TTL
// opens. A root that goes silent past the TTL has had its claims declared free;
// another agent may already hold the path. If the original then wakes up and
// heartbeats, a naive refresh would bring its claims back to life and two agents
// would each have been told the same file was theirs.
func TestALapsedRootLosesItsClaimsWhenItComesBack(t *testing.T) {
	db := testProject(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	var sleeper string
	at(t, now, func() {
		sleeper = mustRoot(t, db, "claude-code", "sess-sleeper")
		if _, err := db.AcquireClaim(ClaimRequest{
			RootID: sleeper, ScopePath: "src/main.go", Worktree: "/repo",
			Reason: "refactor", TTLSeconds: int(MaxClaimTTL.Seconds()),
		}); err != nil {
			t.Fatalf("AcquireClaim: %v", err)
		}
	})

	// It goes quiet past the TTL, and someone else takes the file — as they were
	// entitled to, because the claim had stopped binding.
	var taker string
	at(t, now.Add(2*RootTTL), func() {
		taker = mustRoot(t, db, "codex", "sess-taker")
		if _, err := db.AcquireClaim(ClaimRequest{
			RootID: taker, ScopePath: "src/main.go", Worktree: "/repo", Reason: "bug fix",
		}); err != nil {
			t.Fatalf("the lapsed root's claim still blocked a new one: %v", err)
		}
	})

	// The sleeper wakes. It may work again — but not on the strength of a promise
	// that has already been given to someone else.
	at(t, now.Add(2*RootTTL+time.Minute), func() {
		if err := db.Heartbeat(sleeper); err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
		claims, err := db.ActiveClaims("")
		if err != nil {
			t.Fatalf("ActiveClaims: %v", err)
		}
		for _, c := range claims {
			if c.RootID == sleeper {
				t.Fatalf("a lapsed root's claim came back to life on heartbeat: %+v", c)
			}
		}
		if len(claims) != 1 || claims[0].RootID != taker {
			t.Fatalf("claims = %+v, want only the taker's", claims)
		}
	})
}

// TestHeartbeatSessionKeepsAWorkingAgentAlive covers what pays for the short TTL:
// the hooks refresh liveness from the host session id alone, so an agent that is
// heads-down editing never looks dead.
func TestHeartbeatSessionKeepsAWorkingAgentAlive(t *testing.T) {
	db := testProject(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	var root string
	at(t, now, func() { root = mustRoot(t, db, "claude-code", "sess-worker") })

	// Most of the way to the TTL, doing nothing stigmergy can see — but editing,
	// which the claim guard turns into a heartbeat.
	at(t, now.Add(RootTTL-time.Minute), func() {
		if err := db.HeartbeatSession("claude-code", "sess-worker"); err != nil {
			t.Fatalf("HeartbeatSession: %v", err)
		}
	})

	// Past the original deadline, and still live: the silence was never real.
	at(t, now.Add(RootTTL+time.Minute), func() {
		roots, err := db.ActiveRoots()
		if err != nil {
			t.Fatalf("ActiveRoots: %v", err)
		}
		if len(roots) != 1 || roots[0].RootID != root {
			t.Fatalf("a working agent was reaped: active roots = %+v", roots)
		}
	})

	// An unregistered session has no claims to keep alive, and says so rather
	// than inventing a root.
	if err := db.HeartbeatSession("claude-code", "sess-nobody"); err != ErrNoRoot {
		t.Errorf("HeartbeatSession for an unregistered session = %v, want ErrNoRoot", err)
	}
}
