package store

import (
	"fmt"
	"testing"
)

// Every timestamp in this schema is compared as raw TEXT in SQL, which is only
// correct while all of them are the same fixed width. A single short value skews
// its own comparisons: "…:00Z" sorts ABOVE "…:00.000000000Z" because 'Z' beats
// '.', so the row reads as newer than rows written after it.
//
// Two columns make that more than cosmetic. claims.expires_at decides whether a
// claim still binds — a value reading as newer than it is means a claim that
// never expires and blocks every other agent indefinitely. roots.last_seen_at
// decides whether a root is alive, and so whether its claims are swept and
// whether anyone can write to it.
//
// This asserts the property holds BY CONSTRUCTION after the real write paths
// have run, rather than trusting that every one of them remembered to call
// Now(). A future path that formats its own time fails here.
func TestEveryWrittenTimestampIsCanonical(t *testing.T) {
	db := exerciseEveryWritePath(t)

	for _, c := range stampColumns {
		rows, err := db.Query(fmt.Sprintf(
			`SELECT %s FROM %s WHERE %s IS NOT NULL AND %s != ''`, c.column, c.table, c.column, c.column))
		if err != nil {
			continue // a table this scope does not have
		}
		n := 0
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			n++
			if _, err := ParseStamp(raw); err != nil {
				t.Errorf("%s.%s holds a non-canonical timestamp %q — it will compare wrongly against every canonical one",
					c.table, c.column, raw)
			}
		}
		rows.Close()
		if n == 0 {
			t.Errorf("%s.%s was never written, so this test does not cover it — "+
				"extend exerciseEveryWritePath", c.table, c.column)
		}
	}
}

// A planted short-form stamp must be repaired to the same instant at full width,
// and the repair must not disturb anything else about the row.
func TestRepairStampColumns(t *testing.T) {
	db := exerciseEveryWritePath(t)

	var claimID int64
	if err := db.QueryRow(`SELECT id FROM claims LIMIT 1`).Scan(&claimID); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`SELECT reason FROM claims WHERE id = ?`, claimID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE claims SET expires_at = '2026-03-01T00:00:00.5Z' WHERE id = ?`, claimID); err != nil {
		t.Fatal(err)
	}

	rep, err := db.RepairStampColumns()
	if err != nil {
		t.Fatalf("RepairStampColumns: %v", err)
	}
	if rep.Repaired != 1 {
		t.Errorf("repaired %d, want exactly the planted row", rep.Repaired)
	}

	var expires, reason string
	if err := db.QueryRow(`SELECT expires_at, reason FROM claims WHERE id = ?`, claimID).
		Scan(&expires, &reason); err != nil {
		t.Fatal(err)
	}
	if expires != "2026-03-01T00:00:00.500000000Z" {
		t.Errorf("expires_at = %q, want the same instant at full width", expires)
	}
	if reason != before {
		t.Errorf("the repair altered the row's content: %q became %q", before, reason)
	}

	// Idempotent: a second pass has nothing to do.
	again, err := db.RepairStampColumns()
	if err != nil {
		t.Fatal(err)
	}
	if again.Repaired != 0 {
		t.Errorf("second pass repaired %d rows", again.Repaired)
	}
}

// An unreadable stamp is left alone rather than guessed at, exactly as for
// memories: there is no safe instant to invent.
func TestRepairStampColumnsLeavesUnreadableValues(t *testing.T) {
	db := exerciseEveryWritePath(t)
	if _, err := db.Exec(`UPDATE claims SET expires_at = 'whenever'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RepairStampColumns(); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT expires_at FROM claims LIMIT 1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "whenever" {
		t.Errorf("an unreadable timestamp was rewritten to %q", got)
	}
}

// exerciseEveryWritePath drives the operations that write each timestamp column
// listed in stampColumns, so the assertions above run against real data.
func exerciseEveryWritePath(t *testing.T) *DB {
	t.Helper()
	db := testProject(t)

	sender, _, err := db.RegisterRoot(Registration{
		RootID: "r-sender", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	recipient, _, err := db.RegisterRoot(Registration{
		RootID: "r-recipient", AgentKind: "codex", Worktree: "/wt", SessionLabel: "s2",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A third root that ends, so roots.ended_at is populated.
	gone, _, err := db.RegisterRoot(Registration{
		RootID: "r-gone", AgentKind: "claude-code", Worktree: "/wt", SessionLabel: "s3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeregisterRoot(gone.RootID); err != nil {
		t.Fatal(err)
	}

	// claims.created_at, expires_at — and one released, for released_at.
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: sender.RootID,
		Worktree: "/wt", Reason: "editing", TTLSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	released, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "docs", RootID: sender.RootID, Worktree: "/wt", Reason: "notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseClaim(released.ID, sender.RootID); err != nil {
		t.Fatal(err)
	}

	// mailbox_threads.created_at/updated_at, mailbox_messages.sent_at, plus
	// read_at and notified_at.
	msg, err := db.SendMessage(SendRequest{
		FromRoot: sender.RootID, ToRoot: recipient.RootID,
		Subject: "src", Body: "may I have it?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkNotified(recipient.RootID, []int64{msg.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRead(recipient.RootID, []int64{msg.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveThread(msg.ThreadID, recipient.RootID, "handed it over", ThreadResolved); err != nil {
		t.Fatal(err)
	}

	// audit_log.at is written by everything above, but assert the premise.
	var audits int
	if err := db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits == 0 {
		t.Fatal("nothing wrote an audit row, so audit_log.at is not covered")
	}
	return db
}
