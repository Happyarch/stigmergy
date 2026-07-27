package store

import (
	"testing"
	"time"

	"github.com/happyarch/stigmergy/internal/serr"
)

func mustTime(t *testing.T, stamp string) time.Time {
	t.Helper()
	when, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t.Fatalf("bad test stamp %q: %v", stamp, err)
	}
	return when
}

func verifyDB(t *testing.T) *DB {
	t.Helper()
	db := testProject(t)
	if _, err := write(t, db, "wire-format", "The wire format is versioned.", nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	return db
}

func record(t *testing.T, db *DB, r VerificationRecord) (*Verification, error) {
	t.Helper()
	if r.Key == "" {
		r.Key = "wire-format"
	}
	if r.Outcome == "" {
		r.Outcome = Reaffirmed
	}
	if r.Actor == "" {
		r.Actor = "r-test"
	}
	if r.AgentKind == "" {
		r.AgentKind = "claude-code"
	}
	return db.RecordVerification(r)
}

func TestRecordVerification(t *testing.T) {
	db := verifyDB(t)
	v, err := record(t, db, VerificationRecord{
		ExpectedMemoryVersion: 1,
		Reason:                "read internal/wire and the version constant is still there",
	})
	if err != nil {
		t.Fatalf("RecordVerification: %v", err)
	}
	if v.Outcome != Reaffirmed || v.MemoryVersion != 1 {
		t.Fatalf("verification = %+v", v)
	}
	// No policy existed, so the policy version stays NULL — which must remain
	// distinguishable from having had evidence that showed nothing.
	if v.PolicyVersion != nil {
		t.Errorf("policy version = %v, want nil", v.PolicyVersion)
	}
	if v.At == "" {
		t.Error("no assertion time was recorded")
	}
}

// The version guard is the point: an outcome recorded against a version the
// agent never read is a judgement of different text.
func TestVerificationRefusesAStaleVersion(t *testing.T) {
	db := verifyDB(t)
	if _, err := write(t, db, "wire-format", "Rewritten by someone else.", intp(1)); err != nil {
		t.Fatal(err)
	}
	_, err := record(t, db, VerificationRecord{ExpectedMemoryVersion: 1})
	requireCode(t, err, serr.CASConflict)

	// And nothing was written.
	history, err := db.VerificationHistory("wire-format")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Errorf("a refused verification left %d rows behind", len(history))
	}
}

func TestVerificationValidation(t *testing.T) {
	db := verifyDB(t)

	t.Run("an outcome outside the vocabulary is refused", func(t *testing.T) {
		_, err := record(t, db, VerificationRecord{ExpectedMemoryVersion: 1, Outcome: "probably-fine"})
		requireCode(t, err, serr.InvalidInput)
	})
	t.Run("a memory that does not exist is refused", func(t *testing.T) {
		_, err := record(t, db, VerificationRecord{Key: "no-such-thing", ExpectedMemoryVersion: 1})
		requireCode(t, err, serr.CASConflict)
	})
	t.Run("every outcome in the vocabulary is accepted", func(t *testing.T) {
		for _, outcome := range ValidOutcomes {
			if _, err := record(t, db, VerificationRecord{ExpectedMemoryVersion: 1, Outcome: outcome}); err != nil {
				t.Errorf("%s was refused: %v", outcome, err)
			}
		}
	})
}

// The sequence is what carries the meaning — reaffirmed, reaffirmed, revised —
// so it is stored and returned in the order it happened.
func TestVerificationHistoryIsAppendOnlyAndOrdered(t *testing.T) {
	db := verifyDB(t)
	writeAt(t, db, "seq", "2026-01-01T00:00:00Z")

	for i, at := range []string{
		"2026-01-02T00:00:00Z", "2026-02-01T00:00:00Z", "2026-03-01T00:00:00Z",
	} {
		restore := SetClock(func() time.Time { return mustTime(t, at) })
		outcome := Reaffirmed
		if i == 2 {
			outcome = Revised
		}
		if _, err := record(t, db, VerificationRecord{Key: "seq", ExpectedMemoryVersion: 1, Outcome: outcome}); err != nil {
			t.Fatal(err)
		}
		restore()
	}

	history, err := db.VerificationHistory("seq")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("got %d rows, want 3", len(history))
	}
	if history[0].At > history[2].At {
		t.Error("history is not oldest-first")
	}
	if history[2].Outcome != Revised {
		t.Errorf("last outcome = %s, want %s", history[2].Outcome, Revised)
	}
	// Recording an outcome never touches the memory itself.
	m, _ := db.ReadMemory("seq")
	if m.Version != 1 {
		t.Errorf("recording a verification bumped the memory to version %d", m.Version)
	}
}

func TestVerificationSummaries(t *testing.T) {
	db := verifyDB(t)
	if _, err := write(t, db, "never-checked", "Nobody has looked at this.", nil); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{Reaffirmed, Reaffirmed, Refuted} {
		if _, err := record(t, db, VerificationRecord{ExpectedMemoryVersion: 1, Outcome: outcome}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := db.VerificationSummaries()
	if err != nil {
		t.Fatalf("VerificationSummaries: %v", err)
	}
	s := all["wire-format"]
	if s == nil {
		t.Fatal("no summary for a memory with history")
	}
	if s.Total != 3 || s.Counts[Reaffirmed] != 2 || s.Counts[Refuted] != 1 {
		t.Errorf("summary = %+v", s)
	}
	if s.Last == nil || s.Last.Outcome != Refuted {
		t.Errorf("last = %+v, want the refuted one", s.Last)
	}
	// A memory nobody has checked simply has no row here; the caller decides how
	// to render that, and must not be handed a zero that looks like a finding.
	if _, present := all["never-checked"]; present {
		t.Error("a memory with no history got a summary")
	}
}

// The history belongs to the memory. Deleting one takes the other, the same as
// the evidence policy — a judgement about text that no longer exists is not a
// record of anything.
func TestVerificationCascadesWithTheMemory(t *testing.T) {
	db := verifyDB(t)
	if _, err := record(t, db, VerificationRecord{ExpectedMemoryVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeleteMemory("wire-format", 1, "r-test", "claude-code"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM memory_verification`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d verification rows survived the memory", n)
	}
}

// Nothing infers a verification. A write is not a check, however much text
// changed — that is the §7 rule, and it is the reason this table exists at all
// rather than the model reading meaning into updated_at.
func TestAWriteRecordsNoVerification(t *testing.T) {
	db := verifyDB(t)
	if _, err := write(t, db, "wire-format", "Substantially rewritten.", intp(1)); err != nil {
		t.Fatal(err)
	}
	history, err := db.VerificationHistory("wire-format")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("a write produced %d verification rows", len(history))
	}
}
