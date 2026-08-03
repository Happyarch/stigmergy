package store

import (
	"testing"
	"time"
)

func TestAgentLabelIsStableAndReadable(t *testing.T) {
	cases := []struct{ session, kind, id, want string }{
		{"S", "general-purpose", "a1", "S#general-purpose:a1"},
		// No type: still unique, still an agent.
		{"S", "", "a1", "S#a1"},
		// No agent: the main thread, whose label must not move.
		{"S", "general-purpose", "", "S"},
		{"", "general-purpose", "a1", ""},
	}
	for _, c := range cases {
		if got := AgentLabel(c.session, c.kind, c.id); got != c.want {
			t.Errorf("AgentLabel(%q,%q,%q) = %q, want %q", c.session, c.kind, c.id, got, c.want)
		}
		if got := SessionOf(AgentLabel(c.session, c.kind, c.id)); c.session != "" && got != c.session {
			t.Errorf("SessionOf(%q) = %q, want %q", c.want, got, c.session)
		}
	}
}

func TestCallerTicketIsConsumedExactlyOnce(t *testing.T) {
	db := testProject(t)
	label := AgentLabel("S", "explorer", "a1")
	if err := db.PutCallerTicket(CallerTicket{
		ToolUseID: "toolu_1", SessionLabel: label, AgentKind: "claude-code", Worktree: "/w",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := db.TakeCallerTicket("toolu_1")
	if err != nil || got == nil {
		t.Fatalf("TakeCallerTicket = %+v, %v; want the ticket", got, err)
	}
	if got.SessionLabel != label || got.Worktree != "/w" {
		t.Fatalf("ticket came back wrong: %+v", got)
	}

	// A ticket answers ONE call. Reading it twice would let a later, unstamped
	// call by some other agent inherit this one's identity.
	again, err := db.TakeCallerTicket("toolu_1")
	if err != nil {
		t.Fatal(err)
	}
	if again != nil {
		t.Fatalf("the ticket was still there on the second read: %+v", again)
	}
}

func TestUnknownAndExpiredTicketsResolveToNothing(t *testing.T) {
	db := testProject(t)

	got, err := db.TakeCallerTicket("never-stamped")
	if err != nil || got != nil {
		t.Fatalf("TakeCallerTicket(unknown) = %+v, %v; want nil, nil", got, err)
	}

	if err := db.PutCallerTicket(CallerTicket{
		ToolUseID: "toolu_old", SessionLabel: "S#explorer:a1", AgentKind: "claude-code", Worktree: "/w",
	}); err != nil {
		t.Fatal(err)
	}
	// A ticket outliving its call means the call never came — refused at a
	// permission prompt, most likely. Honouring it later would attribute somebody
	// else's call to the agent that was refused.
	future := NowTime().Add(CallerTicketTTL + time.Second)
	defer SetClock(func() time.Time { return future })()

	got, err = db.TakeCallerTicket("toolu_old")
	if err != nil || got != nil {
		t.Fatalf("an expired ticket was honoured: %+v, %v", got, err)
	}
}
