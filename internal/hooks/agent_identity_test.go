package hooks

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/happyarch/stigmergy/internal/store"
)

// Two agents inside ONE host session are two agents. This is the property the
// whole caller-ticket mechanism exists to buy, stated as directly as it can be:
// a peer's claim blocks its neighbour, even though the host gave both of them
// the same session id.
func TestPeersInOneSessionBlockEachOther(t *testing.T) {
	f := newFixture(t)
	session := "sess-shared"
	peerA := store.AgentLabel(session, "explorer", "a1")
	peerB := store.AgentLabel(session, "explorer", "b2")

	owner := f.claim(t, peerA, "src", true)

	d := f.guard(peerB, filepath.Join(f.worktree, "src", "main.go"))
	if d.Allow {
		t.Fatal("a peer must be blocked by its neighbour's claim, not waved through as the session")
	}
	if !strings.Contains(d.Reason, owner) {
		t.Errorf("the denial must name the peer that holds the path:\n%s", d.Reason)
	}

	// And the holder is still not blocked by itself.
	if d := f.guard(peerA, filepath.Join(f.worktree, "src", "main.go")); !d.Allow {
		t.Fatalf("the claim holder was blocked by its own claim: %s", d.Reason)
	}
}

// The main thread's identity must not move. It is the one that has been holding
// claims all along, and a label that changed under it would leave every one of
// them foreign to the agent that took them.
func TestTheMainThreadKeepsTheBareSessionLabel(t *testing.T) {
	f := newFixture(t)
	session := "sess-main"
	f.claim(t, session, "src", true)

	main := &ClaudeInput{SessionID: session}
	if d := f.guard(main.Label(), filepath.Join(f.worktree, "src", "main.go")); !d.Allow {
		t.Fatalf("the session's own claim blocked the session: %s", d.Reason)
	}
}

// A session ending takes its agents with it. Otherwise a peer's claim outlives
// the process that held it and blocks the repository for the full TTL, owned by
// a root nobody can negotiate with.
func TestEndSessionReleasesThePeersClaimsToo(t *testing.T) {
	f := newFixture(t)
	session := "sess-ending"
	f.claim(t, session, "src/main.go", false)
	f.claim(t, store.AgentLabel(session, "explorer", "a1"), "src/other.go", false)

	EndSession("claude-code", session, f.worktree)

	claims, err := f.db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("claims survived the session that held them: %+v", claims)
	}
}

// One agent finishing must free that agent's claims and nobody else's.
func TestEndAgentLeavesTheSessionAlone(t *testing.T) {
	f := newFixture(t)
	session := "sess-live"
	peer := store.AgentLabel(session, "explorer", "a1")
	f.claim(t, session, "src/main.go", false)
	f.claim(t, peer, "src/other.go", false)

	EndAgent("claude-code", peer, f.worktree)

	claims, err := f.db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].ScopePath != "src/main.go" {
		t.Fatalf("EndAgent should have released exactly the peer's claim, left: %+v", claims)
	}
}

// StampCaller is what joins the hook to the MCP server. It has to land a ticket
// the server can find under the host's tool-use id.
func TestStampCallerLeavesATicketTheServerCanRead(t *testing.T) {
	f := newFixture(t)
	label := store.AgentLabel("sess", "explorer", "a1")

	StampCaller("claude-code", label, f.worktree, "toolu_01ABC")

	got, err := f.db.TakeCallerTicket("toolu_01ABC")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no ticket was written")
	}
	if got.SessionLabel != label {
		t.Fatalf("ticket names %q, want %q", got.SessionLabel, label)
	}
	if got.Worktree != f.worktree {
		t.Fatalf("ticket worktree = %q, want %q", got.Worktree, f.worktree)
	}
}

// A session that dispatches a peer and waits for it must not look dead. The
// agent's work is the session's work — same process — and the session is
// usually the one holding the claims that have to survive the wait.
func TestAPeersWorkKeepsItsSessionAlive(t *testing.T) {
	f := newFixture(t)
	session := "sess-waiting"
	peer := store.AgentLabel(session, "explorer", "a1")
	f.claim(t, session, "src/main.go", false)
	f.claim(t, peer, "src/other.go", false)

	// Wind the session root back to the edge of the TTL, as an agent that has
	// been waiting on a subagent for fifteen minutes would be.
	stale := store.NowTime().Add(-store.RootTTL + time.Minute)
	root, err := f.db.RootBySession("claude-code", session)
	if err != nil {
		t.Fatal(err)
	}
	before := root.LastSeenAt

	restore := store.SetClock(func() time.Time { return stale.Add(store.RootTTL) })
	Heartbeat("claude-code", peer, f.worktree)
	restore()

	root, err = f.db.RootBySession("claude-code", session)
	if err != nil {
		t.Fatalf("the session root lapsed while its peer was working: %v", err)
	}
	if root.LastSeenAt == before {
		t.Fatal("a peer's heartbeat did not refresh the session it runs in")
	}
}
