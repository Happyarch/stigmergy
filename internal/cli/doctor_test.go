package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/store"
)

// An unreadable timestamp is a FAILURE, not a warning: every read of that memory
// errors until someone decides what the instant was, and doctor's exit status is
// what tells a script something needs a human.
func TestRepairTimestampsReportsAndFailsCorrectly(t *testing.T) {
	newDB := func(t *testing.T) *store.DB {
		t.Helper()
		db, err := store.OpenGlobal(filepath.Join(t.TempDir(), "global.sqlite3"))
		if err != nil {
			t.Fatalf("OpenGlobal: %v", err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	put := func(t *testing.T, db *store.DB, key, raw string) {
		t.Helper()
		if _, err := db.WriteMemory(store.MemoryWrite{
			Key: key, Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
		}, "claude-code"); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := db.Exec(`UPDATE memories SET updated_at = ? WHERE key = ?`, raw, key); err != nil {
			t.Fatalf("planting raw timestamp: %v", err)
		}
	}

	t.Run("a repairable row passes and is counted", func(t *testing.T) {
		db := newDB(t)
		put(t, db, "short-form", "2026-03-01T00:00:00.5Z")

		var buf bytes.Buffer
		d := &diag{out: &buf}
		repairTimestamps(d, db, "global")

		if d.failed {
			t.Error("a repairable timestamp must not fail doctor")
		}
		if !strings.Contains(buf.String(), "1 global memory timestamp(s) rewritten") {
			t.Errorf("output does not report the repair:\n%s", buf.String())
		}
	})

	t.Run("an unreadable row fails and is named", func(t *testing.T) {
		db := newDB(t)
		put(t, db, "corrupt-row", "whenever")

		var buf bytes.Buffer
		d := &diag{out: &buf}
		repairTimestamps(d, db, "global")

		if !d.failed {
			t.Error("an unreadable timestamp must fail doctor")
		}
		if !strings.Contains(buf.String(), "corrupt-row") {
			t.Errorf("output does not name the bad key:\n%s", buf.String())
		}
	})

	t.Run("a clean database says nothing", func(t *testing.T) {
		db := newDB(t)
		if _, err := db.WriteMemory(store.MemoryWrite{
			Key: "fine", Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
		}, "claude-code"); err != nil {
			t.Fatalf("write: %v", err)
		}

		var buf bytes.Buffer
		d := &diag{out: &buf}
		repairTimestamps(d, db, "global")

		if d.failed || buf.Len() != 0 {
			t.Errorf("a clean database produced output:\n%s", buf.String())
		}
	})
}
