package mcpserver

import (
	"testing"
	"time"

	"github.com/happyarch/stigmergy/internal/store"
)

// A sender must be able to find the conversation it started, before anyone has
// answered it.
//
// The inbox cannot do this: it is "mail addressed to me", so an unanswered
// message you sent appears in nobody's inbox, least of all your own. An agent
// compacted mid-negotiation would lose the thread id and have no route back —
// and would then either re-send, or take the silence for consent and edit
// anyway. That is the failure this tool exists to prevent.
func TestMailboxThreadsShowsWhatYouSentBeforeAnyReply(t *testing.T) {
	a, b := twoRoots(t)
	rootA := a.session.root.RootID

	b.call("mailbox_send", map[string]any{
		"to_root": rootA, "subject": "blocked on src/api",
		"body": "I need to add an endpoint. Can you release it, or tell me what is changing?",
	})

	// A has not replied. B's own inbox is therefore empty...
	if msgs := b.call("mailbox_inbox", map[string]any{})["messages"].([]any); len(msgs) != 0 {
		t.Fatalf("the sender's inbox should not contain its own message: %#v", msgs)
	}

	// ...but B can still find the conversation it opened.
	threads := b.call("mailbox_threads", map[string]any{})["threads"].([]any)
	if len(threads) != 1 {
		t.Fatalf("sender sees %d threads, want the one it started", len(threads))
	}
	th := threads[0].(map[string]any)
	if th["with"] != rootA {
		t.Errorf("thread is with %v, want the recipient %v", th["with"], rootA)
	}
	if th["last_from_self"] != true {
		t.Error("last_from_self should be true: the sender is waiting on an answer")
	}
	if th["state"] != "open" || th["subject"] != "blocked on src/api" {
		t.Errorf("thread = %#v, want an open thread with the subject it was sent under", th)
	}

	// And once A answers, it lands as unread on B's side and the ball changes court.
	a.call("mailbox_send", map[string]any{
		"to_root": b.session.root.RootID, "thread_id": int64(th["id"].(float64)),
		"subject": "re: blocked on src/api", "body": "Take handlers.go, I am only touching types.go.",
	})
	th = b.call("mailbox_threads", map[string]any{"state": "open"})["threads"].([]any)[0].(map[string]any)
	if th["last_from_self"] != false || th["unread"].(float64) != 1 {
		t.Errorf("after a reply: %#v, want last_from_self=false and one unread", th)
	}
}

// The scenario stigmergy is built for, start to finish: two agents want the
// same file, and they talk their way out of it instead of clobbering each other.
func TestClaimConflictNegotiatedThroughTheMailbox(t *testing.T) {
	a, b := twoRoots(t)
	rootA := a.session.root.RootID
	rootB := b.session.root.RootID

	claim := a.call("claim_acquire", map[string]any{
		"scope_path": "src/api", "recursive": true, "reason": "refactoring the API layer",
	})["claim"].(map[string]any)

	// B is blocked, and learns from the conflict who to ask.
	_, _, e := b.tryCall("claim_acquire", map[string]any{
		"scope_path": "src/api/handlers.go", "reason": "urgent bug fix",
	})
	owner := e["conflict"].(map[string]any)["root_id"].(string)
	if owner != rootA {
		t.Fatalf("the conflict names %q as the owner, want %q", owner, rootA)
	}

	// B asks A to hand it over.
	msg := b.call("mailbox_send", map[string]any{
		"to_root": owner, "claim_id": claim["id"],
		"subject": "Need src/api/handlers.go for an urgent fix",
		"body":    "There is a null deref in handlers.go crashing production. Can you release the claim, or shall I patch it and hand it back?",
	})["message"].(map[string]any)
	threadID := msg["thread_id"]

	// A finds it in the inbox.
	inbox := a.call("mailbox_inbox", map[string]any{"unread_only": true})["messages"].([]any)
	if len(inbox) != 1 {
		t.Fatalf("A's inbox has %d unread messages, want 1", len(inbox))
	}
	got := inbox[0].(map[string]any)
	if got["from_root"] != rootB || got["thread_id"] != threadID {
		t.Fatalf("inbox message = %#v", got)
	}

	a.call("mailbox_mark_read", map[string]any{"message_ids": []any{got["id"]}})
	if left := a.call("mailbox_inbox", map[string]any{"unread_only": true})["messages"].([]any); len(left) != 0 {
		t.Fatalf("after marking read, %d unread remain", len(left))
	}

	// A replies in the same thread, then releases the claim.
	a.call("mailbox_send", map[string]any{
		"to_root": rootB, "thread_id": threadID,
		"subject": "Re: Need src/api/handlers.go",
		"body":    "Releasing it now — I have not touched handlers.go yet. Take it.",
	})
	a.call("claim_release", map[string]any{"claim_id": claim["id"]})

	// B sees the reply and can now take the file.
	reply := b.call("mailbox_inbox", map[string]any{"unread_only": true})["messages"].([]any)
	if len(reply) != 1 || reply[0].(map[string]any)["from_root"] != rootA {
		t.Fatalf("B did not receive the reply: %#v", reply)
	}
	b.call("claim_acquire", map[string]any{
		"scope_path": "src/api/handlers.go", "reason": "urgent bug fix",
	})

	// The thread holds the whole exchange, and closing it records the outcome.
	thread := b.call("mailbox_thread", map[string]any{"thread_id": threadID})["thread"].(map[string]any)
	if msgs := thread["messages"].([]any); len(msgs) != 2 {
		t.Fatalf("thread has %d messages, want 2", len(msgs))
	}
	resolved := b.call("mailbox_resolve", map[string]any{
		"thread_id": threadID, "resolution": "A released the claim; B fixed handlers.go",
	})["thread"].(map[string]any)
	if resolved["state"] != "resolved" || resolved["resolution"] == "" {
		t.Fatalf("resolved thread = %#v", resolved)
	}
}

// Mail to a dead root would wait forever for an answer that cannot come. The
// sender needs to know the recipient is gone — and that its claims went with it.
func TestSendingToAnInactiveRootFails(t *testing.T) {
	a, b := twoRoots(t)
	rootA := a.session.root.RootID

	a.call("root_deregister", map[string]any{})
	if code := b.errCode("mailbox_send", map[string]any{
		"to_root": rootA, "subject": "hello", "body": "are you there",
	}); code != "recipient_inactive" {
		t.Fatalf("sending to a deregistered root: code=%q, want recipient_inactive", code)
	}

	if code := b.errCode("mailbox_send", map[string]any{
		"to_root": "r-000000000000", "subject": "hello", "body": "anyone",
	}); code != "recipient_inactive" {
		t.Fatalf("sending to an unknown root: code=%q, want recipient_inactive", code)
	}
}

// A root that simply goes silent is just as unreachable as one that exited.
func TestSendingToASilentRootFails(t *testing.T) {
	a, b := twoRoots(t)
	rootA := a.session.root.RootID

	restore := store.SetClock(func() time.Time { return time.Now().Add(store.RootTTL + time.Minute) })
	defer restore()

	if code := b.errCode("mailbox_send", map[string]any{
		"to_root": rootA, "subject": "hello", "body": "still there?",
	}); code != "recipient_inactive" {
		t.Fatalf("sending to a root past its TTL: code=%q, want recipient_inactive", code)
	}
}

func TestMailboxValidation(t *testing.T) {
	a, _ := twoRoots(t)
	rootA := a.session.root.RootID

	if code := a.errCode("mailbox_send", map[string]any{
		"to_root": rootA, "subject": "s", "body": "b",
	}); code != "invalid_input" {
		t.Errorf("sending mail to yourself: code=%q, want invalid_input", code)
	}
	if code := a.errCode("mailbox_thread", map[string]any{"thread_id": 999}); code != "invalid_input" {
		t.Errorf("reading a nonexistent thread: code=%q, want invalid_input", code)
	}
}

// Read state tells a sender their message landed. It would mean nothing if a
// third party could set it.
func TestOnlyTheRecipientCanMarkMailRead(t *testing.T) {
	a, b := twoRoots(t)
	rootA := a.session.root.RootID

	msg := b.call("mailbox_send", map[string]any{
		"to_root": rootA, "subject": "question", "body": "may I edit src/main.go?",
	})["message"].(map[string]any)

	// The sender cannot mark its own message as read on the recipient's behalf.
	if out := b.call("mailbox_mark_read", map[string]any{"message_ids": []any{msg["id"]}}); out["marked_read"].(float64) != 0 {
		t.Fatalf("the sender was able to mark the recipient's mail read: %#v", out)
	}
	if unread := a.call("mailbox_inbox", map[string]any{"unread_only": true})["messages"].([]any); len(unread) != 1 {
		t.Fatal("the message should still be unread for the recipient")
	}
	if out := a.call("mailbox_mark_read", map[string]any{"message_ids": []any{msg["id"]}}); out["marked_read"].(float64) != 1 {
		t.Fatalf("the recipient could not mark its own mail read: %#v", out)
	}
}

// Everything that changes shared state has to leave a trace, or an operator
// cannot reconstruct who did what when two agents disagree.
func TestAuditCoversEveryMutation(t *testing.T) {
	a, b := twoRoots(t)
	rootA := a.session.root.RootID

	a.call("memory_write", map[string]any{
		"scope": "project", "key": "fact", "type": "project", "description": "d", "body": "b",
	})
	a.call("memory_write", map[string]any{
		"scope": "project", "key": "fact", "type": "project", "description": "d", "body": "b2",
		"expected_version": 1,
	})
	a.call("memory_promote", map[string]any{"key": "fact", "expected_version": 2})
	claim := a.call("claim_acquire", map[string]any{"scope_path": "src", "recursive": true, "reason": "work"})["claim"].(map[string]any)
	a.call("claim_renew", map[string]any{"claim_id": claim["id"]})
	msg := b.call("mailbox_send", map[string]any{"to_root": rootA, "subject": "s", "body": "b"})["message"].(map[string]any)
	b.call("mailbox_resolve", map[string]any{"thread_id": msg["thread_id"], "resolution": "done"})
	a.call("claim_release", map[string]any{"claim_id": claim["id"]})
	a.call("memory_delete", map[string]any{"scope": "project", "key": "fact", "expected_version": 2})

	want := []string{
		"root_register", "memory_create", "memory_update", "memory_promote",
		"claim_acquire", "claim_renew", "claim_release",
		"mailbox_send", "mailbox_resolve", "memory_delete",
	}
	for _, action := range want {
		var n int
		if err := a.session.project.QueryRow(
			`SELECT count(*) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("no audit record for %q — this mutation is invisible to an operator", action)
		}
	}

	// Audit rows must name who did it, or the log cannot settle a dispute.
	var actor string
	if err := a.session.project.QueryRow(
		`SELECT actor FROM audit_log WHERE action = 'claim_acquire' LIMIT 1`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != rootA {
		t.Fatalf("claim_acquire was audited as %q, want the acting root %q", actor, rootA)
	}
}
