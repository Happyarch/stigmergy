package store

import (
	"sync"
	"testing"
)

// Two agents are two processes with two connections to the same file. Inside one
// process the pool is capped at a single connection, so nothing here is testable
// through one handle — the races that matter only exist between handles, which
// is exactly the situation stigmergy is for.

// second opens another handle to the same database, the way a second agent's
// MCP server would.
func second(t *testing.T, db *DB) *DB {
	t.Helper()
	other, err := OpenProjectAt(db.Path)
	if err != nil {
		t.Fatalf("second handle: %v", err)
	}
	t.Cleanup(func() { other.Close() })
	return other
}

// The promise the whole tool rests on: two agents cannot both be told they hold
// the same path.
//
// The dangerous shape is not a crash. Both connections read the claim table,
// both see the path free, and both insert — there is no unique constraint that
// would stop them, so a losing writer would have to be refused by transaction
// isolation or not at all. If that fails, two agents each get a claim object
// with a full TTL and start editing the same file, which is the one outcome
// claims exist to prevent.
func TestTwoAgentsCannotBothAcquireTheSamePath(t *testing.T) {
	db := testProject(t)
	handles := []*DB{db, second(t, db), second(t, db), second(t, db)}
	for i, h := range handles {
		if _, _, err := h.RegisterRoot(Registration{
			RootID:    rootID(i),
			AgentKind: "claude-code", Worktree: "/wt", SessionLabel: sessionLabel(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	results := make([]error, len(handles))
	start := make(chan struct{})
	for i, h := range handles {
		wg.Add(1)
		go func(i int, h *DB) {
			defer wg.Done()
			<-start
			_, results[i] = h.AcquireClaim(ClaimRequest{
				ScopePath: "internal", Recursive: true, RootID: rootID(i),
				Worktree: "/wt", Reason: "editing", TTLSeconds: 1800,
			})
		}(i, h)
	}
	close(start)
	wg.Wait()

	won := 0
	for i, err := range results {
		if err == nil {
			won++
			continue
		}
		t.Logf("%s lost: %v", rootID(i), err)
	}
	if won != 1 {
		t.Fatalf("%d of %d agents were told they hold internal/** — they will edit the same files",
			won, len(handles))
	}

	active, err := db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("%d active claims on one path", len(active))
	}
}

// The same question for memory: a compare-and-swap that silently loses a write
// is worse than one that refuses it, because the agent goes on believing its
// edit landed.
func TestConcurrentCASWritesDoNotLoseOne(t *testing.T) {
	db := testProject(t)
	if _, err := write(t, db, "contended", "original", nil); err != nil {
		t.Fatal(err)
	}
	handles := []*DB{db, second(t, db), second(t, db), second(t, db)}

	var wg sync.WaitGroup
	results := make([]error, len(handles))
	bodies := make([]string, len(handles))
	start := make(chan struct{})
	for i, h := range handles {
		bodies[i] = "written by " + rootID(i)
		wg.Add(1)
		go func(i int, h *DB) {
			defer wg.Done()
			<-start
			_, results[i] = h.WriteMemory(MemoryWrite{
				Key: "contended", Type: "project", Description: "d",
				Body: bodies[i], UpdatedBy: rootID(i), ExpectedVersion: intp(1),
			}, "claude-code")
		}(i, h)
	}
	close(start)
	wg.Wait()

	winners := []int{}
	for i, err := range results {
		if err == nil {
			winners = append(winners, i)
		} else {
			t.Logf("%s lost: %v", rootID(i), err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("%d writers were told their update landed at version 1", len(winners))
	}

	m, err := db.ReadMemory("contended")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 2 {
		t.Errorf("version = %d, want 2", m.Version)
	}
	// The stored body must be the one the winner sent. A write reported as
	// successful whose content is not in the database is the worst outcome
	// available: the agent believes its edit is shared and it is not.
	if m.Body != bodies[winners[0]] {
		t.Errorf("the successful writer's body is not what is stored:\n  won  %q\n  have %q",
			bodies[winners[0]], m.Body)
	}
}

// Verification rows are append-only, so concurrent writers should all succeed —
// but each has to land exactly once.
func TestConcurrentVerificationsAllLand(t *testing.T) {
	db := testProject(t)
	if _, err := write(t, db, "checked", "body", nil); err != nil {
		t.Fatal(err)
	}
	handles := []*DB{db, second(t, db), second(t, db), second(t, db)}

	var wg sync.WaitGroup
	errs := make([]error, len(handles))
	start := make(chan struct{})
	for i, h := range handles {
		wg.Add(1)
		go func(i int, h *DB) {
			defer wg.Done()
			<-start
			_, errs[i] = h.RecordVerification(VerificationRecord{
				Key: "checked", Outcome: Reaffirmed, ExpectedMemoryVersion: 1,
				Reason: "checked by " + rootID(i), Actor: rootID(i),
			})
		}(i, h)
	}
	close(start)
	wg.Wait()

	landed := 0
	for i, err := range errs {
		if err == nil {
			landed++
		} else {
			t.Logf("%s failed: %v", rootID(i), err)
		}
	}
	history, err := db.VerificationHistory("checked")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != landed {
		t.Fatalf("%d verifications reported success but %d rows exist", landed, len(history))
	}
	if landed == 0 {
		t.Fatal("every concurrent verification failed; append-only writes should not contend")
	}
}

func rootID(i int) string       { return "r-agent-" + string(rune('a'+i)) }
func sessionLabel(i int) string { return "sess-" + string(rune('a'+i)) }

// KNOWN GAP, pinned deliberately: claim matching is byte-exact, and a filesystem
// need not be.
//
// A scope is a string an agent typed; the guard compares it to a path the host
// handed it. Where the filesystem treats two different byte strings as the same
// file, a claim on one does not block an edit to the other and two agents each
// believe they hold it. Two ways in, and the second is the likelier one:
//
//   - Unicode: "caf\u00e9.go" composed (U+00E9) versus decomposed (e + U+0301).
//     APFS normalises, so both name the same file on macOS.
//   - Case: APFS and NTFS are case-INSENSITIVE by default, so "README.md" and
//     "readme.md" are one file on most Macs and every Windows box.
//
// This test asserts what happens today rather than what should. Closing it means
// canonicalising a path the way the host's filesystem does — which differs per
// platform, cannot be inferred from the string alone, and would have to run on
// the claim-guard hook path where the budget is milliseconds. That is a design
// decision, not a patch. Half-fixing it would be worse than the gap: a claim
// that matches sometimes is harder to reason about than one that never does.
//
// On Linux, where filenames are bytes and nothing normalises, the two spellings
// really are different files and the current behaviour is correct.
func TestSpellingVariantsDoNotCollide_KnownGap(t *testing.T) {
	cases := []struct{ name, first, second string }{
		{"unicode composition", "caf\u00e9.go", "cafe\u0301.go"},
		{"letter case", "README.md", "readme.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.first == tc.second {
				t.Fatal("fixture is wrong: these must be different byte strings")
			}
			db := testProject(t)
			for i, id := range []string{"r-one", "r-two"} {
				if _, _, err := db.RegisterRoot(Registration{
					RootID: id, AgentKind: "claude-code", Worktree: "/wt",
					SessionLabel: sessionLabel(i),
				}); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := db.AcquireClaim(ClaimRequest{
				ScopePath: tc.first, RootID: "r-one", Worktree: "/wt", Reason: "editing",
			}); err != nil {
				t.Fatal(err)
			}
			_, err := db.AcquireClaim(ClaimRequest{
				ScopePath: tc.second, RootID: "r-two", Worktree: "/wt", Reason: "also editing",
			})
			if err != nil {
				t.Fatalf("the two spellings collided: %v\n"+
					"If this now fails the gap has been closed — delete this test and say so.", err)
			}
			t.Logf("%q and %q are held by different roots at the same time", tc.first, tc.second)
		})
	}
}
