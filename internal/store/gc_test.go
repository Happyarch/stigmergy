package store

import (
	"testing"
	"time"
)

// at runs f with the clock pinned, so the test can age records by writing them
// "in the past" rather than by waiting 90 days.
func at(t *testing.T, when time.Time, f func()) {
	t.Helper()
	restore := SetClock(func() time.Time { return when })
	defer restore()
	f()
}

func TestGCPrunesOldAuditAndResolvedMail(t *testing.T) {
	db := testProject(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	long := now.Add(-100 * 24 * time.Hour) // older than both retention windows

	at(t, long, func() {
		if err := db.Audit(AuditEntry{Actor: "r-old", Action: "memory_write", Target: "k"}); err != nil {
			t.Fatal(err)
		}
	})
	at(t, now, func() {
		if err := db.Audit(AuditEntry{Actor: "r-new", Action: "memory_write", Target: "k"}); err != nil {
			t.Fatal(err)
		}
	})

	// An old thread, resolved: prunable. Its messages go with it.
	var threadID int64
	at(t, long, func() {
		a := mustRoot(t, db, "claude-code", "sess-a")
		b := mustRoot(t, db, "codex", "sess-b")
		msg, err := db.SendMessage(SendRequest{FromRoot: a, ToRoot: b, Subject: "s", Body: "b"})
		if err != nil {
			t.Fatal(err)
		}
		threadID = msg.ThreadID
		if _, err := db.ResolveThread(threadID, b, "agreed", "resolved"); err != nil {
			t.Fatal(err)
		}
	})

	// An equally old thread left open: NOT prunable. An old message in a thread
	// nobody has settled is exactly the context needed to settle it.
	at(t, long, func() {
		a := mustRoot(t, db, "claude-code", "sess-c")
		b := mustRoot(t, db, "codex", "sess-d")
		if _, err := db.SendMessage(SendRequest{FromRoot: a, ToRoot: b, Subject: "still open", Body: "b"}); err != nil {
			t.Fatal(err)
		}
	})

	// A memory, written long ago. GC must never touch it.
	at(t, long, func() {
		if _, err := db.WriteMemory(MemoryWrite{
			Key: "keep-me", Type: "project", Description: "d", Body: "b", UpdatedBy: "r-old",
		}, "claude-code"); err != nil {
			t.Fatal(err)
		}
	})

	var audit, mail int
	at(t, now, func() {
		var err error
		audit, mail, _, err = db.GC()
		if err != nil {
			t.Fatal(err)
		}
	})

	if audit == 0 {
		t.Error("the 100-day-old audit record was not pruned")
	}
	if mail != 1 {
		t.Errorf("pruned %d messages, want exactly the one in the resolved thread", mail)
	}

	if n := count(t, db, `SELECT count(*) FROM audit_log WHERE actor = 'r-new'`); n != 1 {
		t.Errorf("today's audit record was pruned; audit_log holds %d of them", n)
	}
	if n := count(t, db, `SELECT count(*) FROM mailbox_messages WHERE subject = 'still open'`); n != 1 {
		t.Error("a message in an unresolved thread was pruned")
	}
	if n := count(t, db, `SELECT count(*) FROM mailbox_threads WHERE id = ?`, threadID); n != 0 {
		t.Error("the emptied, resolved thread was left behind")
	}
	if _, err := db.ReadMemory("keep-me"); err != nil {
		t.Errorf("GC deleted a memory: %v", err)
	}
}

// GC's episode pruning: consolidation as retention policy
// (docs/association-model.md §10.3). Old AND ungrounding AND unchained is
// prunable; any one of the other two properties keeps it.
func TestGCPrunesOnlyOldUngroundedUnchainedEpisodes(t *testing.T) {
	db := testProject(t)
	long := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := long.Add(200 * 24 * time.Hour) // older than EpisodeRetention

	var prunable, citedID, chainedID, recentID int64
	at(t, long, func() {
		mustWrite(t, db, "alpha")
		ep, err := db.RecordEpisode(RecordEpisode{
			Title: "prunable", Body: "b", Actor: "r-test", AgentKind: "claude-code",
		})
		if err != nil {
			t.Fatal(err)
		}
		prunable = ep.ID

		cited, err := db.RecordEpisode(RecordEpisode{
			Title: "cited", Body: "b", Actor: "r-test", AgentKind: "claude-code",
			MemoryKeys: []string{"alpha"}, Note: "grounds it",
		})
		if err != nil {
			t.Fatal(err)
		}
		citedID = cited.ID

		chained, err := db.RecordEpisode(RecordEpisode{
			Title: "chained", Body: "b", Actor: "r-test", AgentKind: "claude-code",
		})
		if err != nil {
			t.Fatal(err)
		}
		chainedID = chained.ID
	})
	// The correction is recent, but what makes "chained" unprunable is that
	// something points AT it — episode_links(episode_id=chainedID).
	at(t, now, func() {
		if _, err := db.RecordEpisode(RecordEpisode{
			Title: "the correction", Body: "b", Actor: "r-test", AgentKind: "claude-code",
			CorrectsEpisodeID: &chainedID,
		}); err != nil {
			t.Fatal(err)
		}
		recentEp, err := db.RecordEpisode(RecordEpisode{
			Title: "recent", Body: "b", Actor: "r-test", AgentKind: "claude-code",
		})
		if err != nil {
			t.Fatal(err)
		}
		recentID = recentEp.ID
	})

	var episodesPruned int
	at(t, now, func() {
		_, _, episodesPruned, _ = mustGC(t, db)
	})
	if episodesPruned != 1 {
		t.Fatalf("episodesPruned = %d, want exactly 1 (the prunable one)", episodesPruned)
	}

	if _, err := db.ReadEpisode(prunable); err == nil {
		t.Error("the old, ungrounded, unchained episode survived GC")
	}
	if _, err := db.ReadEpisode(citedID); err != nil {
		t.Errorf("an old but CITED episode was pruned: %v", err)
	}
	if _, err := db.ReadEpisode(chainedID); err != nil {
		t.Errorf("an old but CHAINED episode was pruned: %v", err)
	}
	if _, err := db.ReadEpisode(recentID); err != nil {
		t.Errorf("a recent episode was pruned: %v", err)
	}
}

func mustGC(t *testing.T, db *DB) (int, int, int, error) {
	t.Helper()
	audit, mail, episodes, err := db.GC()
	if err != nil {
		t.Fatal(err)
	}
	return audit, mail, episodes, err
}

func TestGCLeavesOpenClaims(t *testing.T) {
	db := testProject(t)
	root := mustRoot(t, db, "claude-code", "sess-x")
	if _, err := db.AcquireClaim(ClaimRequest{
		ScopePath: "src", Recursive: true, RootID: root,
		Worktree: "/wt", Reason: "refactor", TTLSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.GC(); err != nil {
		t.Fatal(err)
	}
	active, err := db.ActiveClaims("")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("GC removed a live claim: %d active, want 1", len(active))
	}
}

func mustRoot(t *testing.T, db *DB, kind, label string) string {
	t.Helper()
	r, _, err := db.RegisterRoot(Registration{
		RootID: "r-" + label, AgentKind: kind, Worktree: "/wt", SessionLabel: label,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.RootID
}

func count(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
