package store

import "testing"

// A message into a settled thread reopens it.
//
// Resolving says the matter is closed; a new message says it is not. Leaving the
// thread resolved made a live conversation invisible: it dropped out of every
// open-thread listing, and Stalled() requires ThreadOpen — so the "no answer is
// coming, that agent is gone" warning could never fire for it, and an agent
// waiting on a reply there would wait forever.
func TestAMessageReopensASettledThread(t *testing.T) {
	for _, closed := range []string{ThreadResolved, ThreadAbandoned} {
		t.Run(closed, func(t *testing.T) {
			db := testProject(t)
			a, _, err := db.RegisterRoot(Registration{
				RootID: "r-a", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sa"})
			if err != nil {
				t.Fatal(err)
			}
			b, _, err := db.RegisterRoot(Registration{
				RootID: "r-b", AgentKind: "codex", Worktree: "/wt", SessionLabel: "sb"})
			if err != nil {
				t.Fatal(err)
			}

			msg, err := db.SendMessage(SendRequest{
				FromRoot: a.RootID, ToRoot: b.RootID, Subject: "src", Body: "may I have it?"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ResolveThread(msg.ThreadID, b.RootID, "it is yours", closed); err != nil {
				t.Fatal(err)
			}

			// The matter turns out not to be settled after all.
			if _, err := db.SendMessage(SendRequest{
				FromRoot: b.RootID, ToRoot: a.RootID, ThreadID: &msg.ThreadID,
				Subject: "src again", Body: "actually I still need it",
			}); err != nil {
				t.Fatalf("sending into a %s thread: %v", closed, err)
			}

			th, err := db.GetThread(msg.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			if th.State != ThreadOpen {
				t.Fatalf("thread is %q after a new message, so it is invisible to every open listing", th.State)
			}
			// A settlement that did not settle anything is worse than none.
			if th.Resolution != "" {
				t.Errorf("the stale resolution %q survived reopening", th.Resolution)
			}

			open, err := db.Threads(a.RootID, ThreadOpen)
			if err != nil {
				t.Fatal(err)
			}
			if len(open) != 1 {
				t.Errorf("%d open threads, want the reopened one", len(open))
			}
		})
	}
}

// Reopening is only for a thread that was closed. An ordinary reply must not
// churn the state or lose anything.
func TestAReplyToAnOpenThreadLeavesItOpen(t *testing.T) {
	db := testProject(t)
	a, _, err := db.RegisterRoot(Registration{
		RootID: "r-a", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "sa"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := db.RegisterRoot(Registration{
		RootID: "r-b", AgentKind: "codex", Worktree: "/wt", SessionLabel: "sb"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.SendMessage(SendRequest{
		FromRoot: a.RootID, ToRoot: b.RootID, Subject: "src", Body: "may I have it?"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SendMessage(SendRequest{
		FromRoot: b.RootID, ToRoot: a.RootID, ThreadID: &msg.ThreadID,
		Subject: "re: src", Body: "give me ten minutes",
	}); err != nil {
		t.Fatal(err)
	}
	th, err := db.GetThread(msg.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if th.State != ThreadOpen {
		t.Errorf("state = %q", th.State)
	}
	var reopens int
	if err := db.QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = 'mailbox_reopen'`).Scan(&reopens); err != nil {
		t.Fatal(err)
	}
	if reopens != 0 {
		t.Errorf("an ordinary reply logged %d reopen(s)", reopens)
	}
}
