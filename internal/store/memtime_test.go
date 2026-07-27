package store

import (
	"strings"
	"testing"
	"time"

	"github.com/happyarch/stigmergy/internal/serr"
)

// atClock fixes the clock at a moment so writes land at known, distinct times.
func atClock(t *testing.T, stamp string) {
	t.Helper()
	when, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t.Fatalf("bad test stamp %q: %v", stamp, err)
	}
	restore := SetClock(func() time.Time { return when })
	t.Cleanup(restore)
}

// writeAt writes one memory as though the clock read stamp.
func writeAt(t *testing.T, db *DB, key, stamp string) {
	t.Helper()
	restore := SetClock(func() time.Time {
		when, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			t.Fatalf("bad test stamp %q: %v", stamp, err)
		}
		return when
	})
	defer restore()
	if _, err := write(t, db, key, "body of "+key, nil); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
}

// setRawStamp bypasses every write path to plant a timestamp exactly as stored,
// which is the only way to reproduce what older stigmergy versions left behind.
func setRawStamp(t *testing.T, db *DB, key, updated string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE memories SET updated_at = ? WHERE key = ?`, updated, key); err != nil {
		t.Fatalf("planting raw timestamp: %v", err)
	}
}

func keysOf(entries []IndexEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Key)
	}
	return out
}

func TestQueryMemoriesFiltersAndOrders(t *testing.T) {
	db := testProject(t)
	writeAt(t, db, "oldest", "2026-01-01T00:00:00Z")
	writeAt(t, db, "middle", "2026-03-01T00:00:00Z")
	writeAt(t, db, "newest", "2026-05-01T00:00:00Z")

	cases := []struct {
		name string
		q    MemoryQuery
		want []string
	}{
		{"default is key order", MemoryQuery{}, []string{"middle", "newest", "oldest"}},
		{"recent is newest first", MemoryQuery{OrderBy: OrderByRecent}, []string{"newest", "middle", "oldest"}},
		{"since excludes older", MemoryQuery{UpdatedSince: "2026-03-01T00:00:00Z"}, []string{"middle", "newest"}},
		{"before excludes newer", MemoryQuery{UpdatedBefore: "2026-03-01T00:00:00Z"}, []string{"middle", "oldest"}},
		{"both bounds", MemoryQuery{
			UpdatedSince:  "2026-02-01T00:00:00Z",
			UpdatedBefore: "2026-04-01T00:00:00Z",
		}, []string{"middle"}},
		{"a window matching nothing is empty, not an error", MemoryQuery{
			UpdatedSince: "2027-01-01T00:00:00Z",
		}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.QueryMemories(tc.q)
			if err != nil {
				t.Fatalf("QueryMemories: %v", err)
			}
			if strings.Join(keysOf(got), ",") != strings.Join(tc.want, ",") {
				t.Fatalf("keys = %v, want %v", keysOf(got), tc.want)
			}
		})
	}
}

// Both bounds are inclusive, which is the kind of thing a caller reads once and
// then relies on forever.
func TestQueryMemoryBoundsAreInclusive(t *testing.T) {
	db := testProject(t)
	writeAt(t, db, "exact", "2026-03-01T00:00:00Z")

	for _, q := range []MemoryQuery{
		{UpdatedSince: "2026-03-01T00:00:00Z"},
		{UpdatedBefore: "2026-03-01T00:00:00Z"},
	} {
		got, err := db.QueryMemories(q)
		if err != nil {
			t.Fatalf("QueryMemories(%+v): %v", q, err)
		}
		if len(got) != 1 {
			t.Fatalf("QueryMemories(%+v) returned %d entries, want the boundary entry itself", q, len(got))
		}
	}
}

// A bad bound must be refused, not ignored: an ignored bound returns a result
// set that looks answered and is not.
func TestQueryMemoriesRejectsBadInput(t *testing.T) {
	db := testProject(t)
	writeAt(t, db, "anything", "2026-03-01T00:00:00Z")

	cases := map[string]MemoryQuery{
		"unparseable since":   {UpdatedSince: "last tuesday"},
		"unparseable before":  {UpdatedBefore: "2026-13-45"},
		"date without a time": {UpdatedSince: "2026-03-01"},
		"inverted range": {
			UpdatedSince:  "2026-05-01T00:00:00Z",
			UpdatedBefore: "2026-01-01T00:00:00Z",
		},
		"unknown order_by": {OrderBy: "freshness"},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := db.QueryMemories(q)
			requireCode(t, err, serr.InvalidInput)
			if got != nil {
				t.Fatalf("a rejected query returned %d entries; it must return none", len(got))
			}
		})
	}
}

// The bug §14.4 of docs/memory-model.md exists for: SQLite filters and orders
// raw TEXT, so a short timestamp would be excluded or misordered before Go ever
// saw it. Nothing here runs a repair first — an unrepaired database must sort
// correctly on its own.
func TestNonCanonicalTimestampsSortAndFilterWithoutRepair(t *testing.T) {
	for _, scope := range []struct {
		name string
		open func(*testing.T) *DB
	}{
		{"project", testProject},
		{"global", testGlobal},
	} {
		t.Run(scope.name, func(t *testing.T) {
			db := scope.open(t)
			writeAt(t, db, "later", "2026-03-01T00:00:00Z")
			writeAt(t, db, "earlier", "2026-03-01T00:00:00Z")

			// These two are half a second apart, and their RAW spellings sort the
			// other way round: 'Z' (0x5A) beats '.' (0x2E), so the second-precision
			// "…:00Z" compares ABOVE the fractional "…:00.5…Z" that really came
			// after it. Second-precision is the shape older stigmergy wrote; the
			// live global machine-navi31-hard-locks row is the same family of bug
			// with three fractional digits.
			setRawStamp(t, db, "earlier", "2026-03-01T00:00:00Z")
			setRawStamp(t, db, "later", "2026-03-01T00:00:00.500000000Z")

			// Guard on the premise. If SQLite ever ordered these two the way a
			// human means them, this test would pass against a SQL-side
			// implementation and stop protecting anything.
			var rawFirst string
			if err := db.QueryRow(
				`SELECT key FROM memories ORDER BY updated_at DESC LIMIT 1`).Scan(&rawFirst); err != nil {
				t.Fatalf("raw order probe: %v", err)
			}
			if rawFirst != "earlier" {
				t.Fatalf("raw TEXT order puts %q first, so this fixture no longer reproduces the bug", rawFirst)
			}

			got, err := db.QueryMemories(MemoryQuery{OrderBy: OrderByRecent})
			if err != nil {
				t.Fatalf("QueryMemories: %v", err)
			}
			want := []string{"later", "earlier"}
			if strings.Join(keysOf(got), ",") != strings.Join(want, ",") {
				t.Fatalf("order = %v, want %v — sorted as raw text", keysOf(got), want)
			}

			// Same disagreement on the filter: as raw text "…:00Z" compares above
			// the bound, so a SQL-side WHERE would wrongly keep "earlier".
			got, err = db.QueryMemories(MemoryQuery{UpdatedSince: "2026-03-01T00:00:00.250000000Z"})
			if err != nil {
				t.Fatalf("QueryMemories: %v", err)
			}
			want = []string{"later"}
			if strings.Join(keysOf(got), ",") != strings.Join(want, ",") {
				t.Fatalf("filtered = %v, want %v — filtered as raw text", keysOf(got), want)
			}
		})
	}
}

func TestEveryReadPathReturnsCanonicalTimestamps(t *testing.T) {
	db := testProject(t)
	writeAt(t, db, "widget-conventions", "2026-03-01T00:00:00Z")
	setRawStamp(t, db, "widget-conventions", "2026-03-01T00:00:00.5Z")
	const want = "2026-03-01T00:00:00.500000000Z"

	m, err := db.ReadMemory("widget-conventions")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if m.UpdatedAt != want {
		t.Errorf("ReadMemory updated_at = %q, want %q", m.UpdatedAt, want)
	}

	list, err := db.ListMemories()
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if list[0].UpdatedAt != want {
		t.Errorf("ListMemories updated_at = %q, want %q", list[0].UpdatedAt, want)
	}

	q, err := db.QueryMemories(MemoryQuery{})
	if err != nil {
		t.Fatalf("QueryMemories: %v", err)
	}
	if q[0].UpdatedAt != want {
		t.Errorf("QueryMemories updated_at = %q, want %q", q[0].UpdatedAt, want)
	}

	sug, err := db.SuggestSimilar("widget conventions", 3)
	if err != nil {
		t.Fatalf("SuggestSimilar: %v", err)
	}
	if len(sug) == 0 || sug[0].UpdatedAt != want {
		t.Errorf("SuggestSimilar updated_at = %v, want %q", sug, want)
	}

	hits, err := db.SearchMemories("widget-conventions")
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(hits) == 0 || hits[0].UpdatedAt != want {
		t.Errorf("SearchMemories updated_at = %v, want %q", hits, want)
	}
}

// Adding a column to the search SELECT must not disturb snippet(), whose column
// index addresses memories_fts and not the projection.
func TestSearchHitCarriesTimeAndStillHighlightsTheBody(t *testing.T) {
	db := testProject(t)
	atClock(t, "2026-03-01T00:00:00Z")
	if _, err := db.WriteMemory(MemoryWrite{
		Key: "sandbox-notes", Type: "reference", Description: "how the sandbox is mounted",
		Body:      "The overlay must be applied before the tmpfs or workers read each other.",
		UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatalf("write: %v", err)
	}

	hits, err := db.SearchMemories("tmpfs")
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if !strings.Contains(hits[0].Snippet, "[tmpfs]") {
		t.Errorf("snippet %q does not highlight the body term — the FTS column index moved", hits[0].Snippet)
	}
	if hits[0].UpdatedAt != "2026-03-01T00:00:00.000000000Z" {
		t.Errorf("hit updated_at = %q, want the write time", hits[0].UpdatedAt)
	}
}

// An unparseable timestamp cannot be ordered, so it is a read error rather than
// a value passed through into a result set that claims to be sorted.
func TestUnparseableTimestampIsAReadError(t *testing.T) {
	db := testProject(t)
	writeAt(t, db, "corrupt-row", "2026-03-01T00:00:00Z")
	setRawStamp(t, db, "corrupt-row", "sometime last year")

	if _, err := db.ReadMemory("corrupt-row"); err == nil {
		t.Error("ReadMemory succeeded on an unreadable timestamp")
	}
	if _, err := db.ListMemories(); err == nil {
		t.Error("ListMemories succeeded on an unreadable timestamp")
	}
	if _, err := db.QueryMemories(MemoryQuery{OrderBy: OrderByRecent}); err == nil {
		t.Error("QueryMemories succeeded on an unreadable timestamp")
	}
}

func TestRepairMemoryTimestamps(t *testing.T) {
	for _, scope := range []struct {
		name string
		open func(*testing.T) *DB
	}{
		{"project", testProject},
		{"global", testGlobal},
	} {
		t.Run(scope.name, func(t *testing.T) {
			db := scope.open(t)
			writeAt(t, db, "already-fine", "2026-01-01T00:00:00Z")
			writeAt(t, db, "short-form", "2026-03-01T00:00:00Z")
			writeAt(t, db, "unreadable", "2026-05-01T00:00:00Z")
			setRawStamp(t, db, "short-form", "2026-03-01T00:00:00.5Z")
			setRawStamp(t, db, "unreadable", "whenever")

			rep, err := db.RepairMemoryTimestamps()
			if err != nil {
				t.Fatalf("RepairMemoryTimestamps: %v", err)
			}
			if rep.Repaired != 1 {
				t.Errorf("repaired %d rows, want exactly the one non-canonical readable row", rep.Repaired)
			}
			if len(rep.Bad) != 1 || rep.Bad[0] != "unreadable" {
				t.Errorf("bad = %v, want [unreadable]", rep.Bad)
			}

			m, err := db.ReadMemory("short-form")
			if err != nil {
				t.Fatalf("ReadMemory after repair: %v", err)
			}
			if m.UpdatedAt != "2026-03-01T00:00:00.500000000Z" {
				t.Errorf("repaired updated_at = %q", m.UpdatedAt)
			}
			// A repair is not a write: nobody asserted anything, so nothing about
			// the memory's own history may move.
			if m.Version != 1 {
				t.Errorf("repair bumped version to %d", m.Version)
			}

			// The unreadable row is left exactly as found — there is no safe
			// instant to guess, and a second run must report it again.
			var raw string
			if err := db.QueryRow(`SELECT updated_at FROM memories WHERE key = 'unreadable'`).Scan(&raw); err != nil {
				t.Fatalf("reading the untouched row: %v", err)
			}
			if raw != "whenever" {
				t.Errorf("the unreadable timestamp was modified to %q", raw)
			}

			again, err := db.RepairMemoryTimestamps()
			if err != nil {
				t.Fatalf("second RepairMemoryTimestamps: %v", err)
			}
			if again.Repaired != 0 || len(again.Bad) != 1 {
				t.Errorf("second pass repaired=%d bad=%v, want 0 and the same bad row", again.Repaired, again.Bad)
			}
		})
	}
}

func TestCanonicalStamp(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2026-03-01T00:00:00.000000000Z", "2026-03-01T00:00:00.000000000Z", true},
		{"2026-03-01T00:00:00Z", "2026-03-01T00:00:00.000000000Z", true},
		{"2026-07-25T22:40:57.901Z", "2026-07-25T22:40:57.901000000Z", true},
		{"2026-03-01T02:00:00+02:00", "2026-03-01T00:00:00.000000000Z", true},
		{"2026-03-01", "", false},
		{"", "", false},
		{"whenever", "", false},
	}
	for _, tc := range cases {
		got, err := CanonicalStamp(tc.in)
		if tc.ok && err != nil {
			t.Errorf("CanonicalStamp(%q) errored: %v", tc.in, err)
			continue
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("CanonicalStamp(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("CanonicalStamp(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
