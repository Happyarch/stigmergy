package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/happyarch/stigmergy/internal/serr"
)

func evidenceDB(t *testing.T) *DB {
	t.Helper()
	db := testProject(t)
	if err := db.AddRepo("app", "/tmp/app/.git", "/tmp/app"); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	if err := db.AddRepo("service", "/tmp/service/.git", "/tmp/service"); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	if _, err := write(t, db, "wire-format", "The wire format is versioned.", nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	return db
}

func set(t *testing.T, db *DB, s EvidenceSet) (*EvidencePolicy, error) {
	t.Helper()
	if s.Key == "" {
		s.Key = "wire-format"
	}
	if s.Actor == "" {
		s.Actor = "r-test"
	}
	if s.Members == nil {
		s.Members = []EvidenceMember{{RepoID: "app", BaseOID: "aaaa"}}
	}
	return db.SetEvidencePolicy(s)
}

func TestEvidencePolicyRoundTrip(t *testing.T) {
	db := evidenceDB(t)
	p, err := set(t, db, EvidenceSet{
		ExpectedMemoryVersion: 1,
		Members: []EvidenceMember{
			{RepoID: "app", BaseOID: "aaaa", Paths: []EvidencePath{
				{Kind: PathGlob, Pattern: "internal/**"},
				{Kind: PathLiteral, Pattern: "go.mod"},
			}},
			{RepoID: "service", BaseOID: "bbbb"},
		},
	})
	if err != nil {
		t.Fatalf("SetEvidencePolicy: %v", err)
	}
	if p.Version != 1 {
		t.Errorf("version = %d, want 1", p.Version)
	}

	got, err := db.ReadEvidencePolicy("wire-format")
	if err != nil {
		t.Fatalf("ReadEvidencePolicy: %v", err)
	}
	if len(got.Members) != 2 {
		t.Fatalf("got %d members, want 2", len(got.Members))
	}
	if got.Members[0].RepoID != "app" || len(got.Members[0].Paths) != 2 {
		t.Errorf("app member = %+v", got.Members[0])
	}
	// Zero paths is whole-member observation, and must survive the round trip as
	// zero paths rather than as anything invented to fill the gap.
	if got.Members[1].RepoID != "service" || len(got.Members[1].Paths) != 0 {
		t.Errorf("service member = %+v, want no paths", got.Members[1])
	}
	if got.Members[0].BaseOID != "aaaa" {
		t.Errorf("base oid = %q", got.Members[0].BaseOID)
	}
}

func TestEvidencePolicyAbsent(t *testing.T) {
	db := evidenceDB(t)
	if _, err := db.ReadEvidencePolicy("wire-format"); !errors.Is(err, ErrNoPolicy) {
		t.Fatalf("err = %v, want ErrNoPolicy", err)
	}
	if db.HasEvidencePolicy("wire-format") {
		t.Error("HasEvidencePolicy is true with no policy")
	}
}

// Both versions are checked, and each for its own reason: the policy version
// stops two agents racing, and the memory version stops a baseline being pinned
// to a proposition the caller never read.
func TestEvidencePolicyCASMatrix(t *testing.T) {
	t.Run("wrong memory version is refused", func(t *testing.T) {
		db := evidenceDB(t)
		_, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 7})
		requireCode(t, err, serr.CASConflict)
	})

	t.Run("declaring over an existing policy without naming it is refused", func(t *testing.T) {
		db := evidenceDB(t)
		if _, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 1}); err != nil {
			t.Fatal(err)
		}
		_, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 1})
		e := requireCode(t, err, serr.CASConflict)
		if e.Context["current_policy_version"] != 1 {
			t.Errorf("conflict does not carry the current policy version: %+v", e.Context)
		}
	})

	t.Run("a stale policy version is refused", func(t *testing.T) {
		db := evidenceDB(t)
		if _, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 1}); err != nil {
			t.Fatal(err)
		}
		_, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 1, ExpectedPolicyVersion: intp(9)})
		requireCode(t, err, serr.CASConflict)
	})

	t.Run("naming a policy version when there is none is refused", func(t *testing.T) {
		db := evidenceDB(t)
		_, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 1, ExpectedPolicyVersion: intp(1)})
		requireCode(t, err, serr.CASConflict)
	})

	t.Run("a correct replacement bumps the policy version", func(t *testing.T) {
		db := evidenceDB(t)
		if _, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 1}); err != nil {
			t.Fatal(err)
		}
		p, err := set(t, db, EvidenceSet{
			ExpectedMemoryVersion: 1, ExpectedPolicyVersion: intp(1),
			Members: []EvidenceMember{{RepoID: "service", BaseOID: "cccc"}},
		})
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
		if p.Version != 2 {
			t.Errorf("version = %d, want 2", p.Version)
		}
		// Replacement is wholesale: the previous member is gone, not merged.
		got, _ := db.ReadEvidencePolicy("wire-format")
		if len(got.Members) != 1 || got.Members[0].RepoID != "service" {
			t.Errorf("members after replace = %+v", got.Members)
		}
	})

	t.Run("clear requires both versions", func(t *testing.T) {
		db := evidenceDB(t)
		if _, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 1}); err != nil {
			t.Fatal(err)
		}
		requireCode(t, db.ClearEvidencePolicy("wire-format", 9, 1, "r-test", "claude-code"), serr.CASConflict)
		requireCode(t, db.ClearEvidencePolicy("wire-format", 1, 9, "r-test", "claude-code"), serr.CASConflict)
		if err := db.ClearEvidencePolicy("wire-format", 1, 1, "r-test", "claude-code"); err != nil {
			t.Fatalf("clear: %v", err)
		}
		if _, err := db.ReadEvidencePolicy("wire-format"); !errors.Is(err, ErrNoPolicy) {
			t.Errorf("policy survived the clear: %v", err)
		}
	})

	t.Run("clearing what is not there says so", func(t *testing.T) {
		db := evidenceDB(t)
		err := db.ClearEvidencePolicy("wire-format", 1, 1, "r-test", "claude-code")
		if !errors.Is(err, ErrNoPolicy) {
			t.Fatalf("err = %v, want ErrNoPolicy", err)
		}
	})

	t.Run("a policy on a memory that does not exist is refused", func(t *testing.T) {
		db := evidenceDB(t)
		_, err := set(t, db, EvidenceSet{Key: "no-such-memory", ExpectedMemoryVersion: 1})
		requireCode(t, err, serr.CASConflict)
	})
}

func TestEvidencePolicyValidation(t *testing.T) {
	db := evidenceDB(t)
	cases := map[string][]EvidenceMember{
		"no members at all":     {},
		"an unregistered repo":  {{RepoID: "ghost", BaseOID: "aaaa"}},
		"a repo named twice":    {{RepoID: "app", BaseOID: "a"}, {RepoID: "app", BaseOID: "b"}},
		"an empty repo name":    {{RepoID: "", BaseOID: "aaaa"}},
		"no base commit":        {{RepoID: "app"}},
		"an absolute pattern":   {{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{{Kind: PathLiteral, Pattern: "/etc/passwd"}}}},
		"a climbing pattern":    {{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{{Kind: PathGlob, Pattern: "../other/**"}}}},
		"an empty pattern":      {{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{{Kind: PathLiteral, Pattern: "  "}}}},
		"an unknown path kind":  {{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{{Kind: "regex", Pattern: "x"}}}},
		"an unnormalised path":  {{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{{Kind: PathLiteral, Pattern: "internal//store/"}}}},
		"a NUL in the pattern":  {{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{{Kind: PathLiteral, Pattern: "a\x00b"}}}},
		"a bare .. in the path": {{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{{Kind: PathLiteral, Pattern: ".."}}}},
	}
	for name, members := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 1, Members: members})
			requireCode(t, err, serr.InvalidInput)
			if db.HasEvidencePolicy("wire-format") {
				t.Error("a rejected declaration left a policy behind")
			}
		})
	}
}

// Evaluation costs a subprocess per pattern, so the pattern count is a cost an
// agent chooses for everyone: memory_list with drift shares one deadline across
// every policy, and the members that do not finish come back as degraded
// coverage. One over-eager policy would quietly downgrade every other memory's
// answer.
func TestTooManyPathsAreRefused(t *testing.T) {
	db := evidenceDB(t)

	atLimit := make([]EvidencePath, MaxEvidencePaths)
	for i := range atLimit {
		atLimit[i] = EvidencePath{Kind: PathLiteral, Pattern: fmt.Sprintf("f%d.go", i)}
	}
	if _, err := set(t, db, EvidenceSet{
		ExpectedMemoryVersion: 1,
		Members:               []EvidenceMember{{RepoID: "app", BaseOID: "a", Paths: atLimit}},
	}); err != nil {
		t.Fatalf("exactly the limit was refused: %v", err)
	}

	over := append(atLimit, EvidencePath{Kind: PathLiteral, Pattern: "one-too-many.go"})
	_, err := set(t, db, EvidenceSet{
		ExpectedMemoryVersion: 1, ExpectedPolicyVersion: intp(1),
		Members: []EvidenceMember{{RepoID: "app", BaseOID: "a", Paths: over}},
	})
	requireCode(t, err, serr.InvalidInput)
}

// A duplicate would be collapsed by the primary key on insert while the returned
// policy still echoed both — showing the caller a boundary that is not the one
// stored. Refusing is the only way those two stay the same thing.
func TestADuplicatePatternIsRefused(t *testing.T) {
	db := evidenceDB(t)
	_, err := set(t, db, EvidenceSet{
		ExpectedMemoryVersion: 1,
		Members: []EvidenceMember{{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{
			{Kind: PathGlob, Pattern: "internal/**"},
			{Kind: PathGlob, Pattern: "internal/**"},
		}}},
	})
	requireCode(t, err, serr.InvalidInput)

	// Same pattern under a different kind is NOT a duplicate: they compile to
	// different pathspecs and mean different things.
	if _, err := set(t, db, EvidenceSet{
		ExpectedMemoryVersion: 1,
		Members: []EvidenceMember{{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{
			{Kind: PathGlob, Pattern: "internal"},
			{Kind: PathLiteral, Pattern: "internal"},
		}}},
	}); err != nil {
		t.Fatalf("two kinds of the same pattern were refused as duplicates: %v", err)
	}
}

// What is returned must be what was stored, or the echoed boundary is a lie.
func TestTheReturnedPolicyMatchesWhatWasStored(t *testing.T) {
	db := evidenceDB(t)
	returned, err := set(t, db, EvidenceSet{
		ExpectedMemoryVersion: 1,
		Members: []EvidenceMember{{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{
			{Kind: PathGlob, Pattern: "internal/**"},
			{Kind: PathLiteral, Pattern: "go.mod"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.ReadEvidencePolicy("wire-format")
	if err != nil {
		t.Fatal(err)
	}
	if len(returned.Members[0].Paths) != len(stored.Members[0].Paths) {
		t.Fatalf("returned %d paths, stored %d", len(returned.Members[0].Paths), len(stored.Members[0].Paths))
	}
}

// A glob keeps its "**", which path.Clean would eat — the normalisation rule
// applies to literals only.
func TestGlobPatternsAreNotNormalised(t *testing.T) {
	db := evidenceDB(t)
	if _, err := set(t, db, EvidenceSet{
		ExpectedMemoryVersion: 1,
		Members: []EvidenceMember{{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{
			{Kind: PathGlob, Pattern: "internal/**/*.go"},
		}}},
	}); err != nil {
		t.Fatalf("a legitimate glob was rejected: %v", err)
	}
	got, _ := db.ReadEvidencePolicy("wire-format")
	if got.Members[0].Paths[0].Pattern != "internal/**/*.go" {
		t.Errorf("pattern = %q, want it stored verbatim", got.Members[0].Paths[0].Pattern)
	}
}

// The FK cascades are declared, but declaring them proves nothing: enforcement
// is a runtime pragma, and the existing foreign_key_check test proves
// consistency rather than that anything is switched on.
func TestEvidenceCascades(t *testing.T) {
	db := evidenceDB(t)

	var on int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatalf("reading the pragma: %v", err)
	}
	if on != 1 {
		t.Fatal("foreign keys are OFF at runtime, so nothing below cascades")
	}

	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		return n
	}

	t.Run("deleting a repository takes its evidence", func(t *testing.T) {
		if _, err := set(t, db, EvidenceSet{
			ExpectedMemoryVersion: 1,
			Members: []EvidenceMember{
				{RepoID: "app", BaseOID: "a", Paths: []EvidencePath{{Kind: PathGlob, Pattern: "**"}}},
				{RepoID: "service", BaseOID: "b"},
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.RemoveRepo("app"); err != nil {
			t.Fatalf("RemoveRepo: %v", err)
		}
		if n := count("memory_evidence_member"); n != 1 {
			t.Errorf("members = %d, want only service left", n)
		}
		if n := count("memory_evidence_path"); n != 0 {
			t.Errorf("paths = %d, want 0 — a departed member's paths must go with it", n)
		}
		// The policy itself survives: it still names a repository that is here.
		if !db.HasEvidencePolicy("wire-format") {
			t.Error("removing one member destroyed the whole policy")
		}
	})

	t.Run("deleting the memory takes everything", func(t *testing.T) {
		if _, _, err := db.DeleteMemory("wire-format", 1, "r-test", "claude-code"); err != nil {
			t.Fatalf("DeleteMemory: %v", err)
		}
		for _, tbl := range []string{"memory_evidence_policy", "memory_evidence_member", "memory_evidence_path"} {
			if n := count(tbl); n != 0 {
				t.Errorf("%s has %d rows after the memory was deleted", tbl, n)
			}
		}
	})
}

// EvidencePolicies is the whole-database read that makes include_drift
// affordable; it must agree with the per-key read.
func TestEvidencePoliciesLoadsAllOfThemAtOnce(t *testing.T) {
	db := evidenceDB(t)
	if _, err := write(t, db, "second-memory", "Another one.", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := set(t, db, EvidenceSet{ExpectedMemoryVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := set(t, db, EvidenceSet{Key: "second-memory", ExpectedMemoryVersion: 1,
		Members: []EvidenceMember{{RepoID: "service", BaseOID: "bbbb",
			Paths: []EvidencePath{{Kind: PathLiteral, Pattern: "go.mod"}}}}}); err != nil {
		t.Fatal(err)
	}

	all, err := db.EvidencePolicies()
	if err != nil {
		t.Fatalf("EvidencePolicies: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d policies, want 2", len(all))
	}
	if all["second-memory"].Members[0].RepoID != "service" ||
		len(all["second-memory"].Members[0].Paths) != 1 {
		t.Errorf("second-memory = %+v", all["second-memory"].Members)
	}
	if len(all["wire-format"].Members[0].Paths) != 0 {
		t.Errorf("paths leaked across memories: %+v", all["wire-format"].Members)
	}
}
