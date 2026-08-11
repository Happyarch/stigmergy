package syncx

import (
	"reflect"
	"testing"
)

// rec builds a record whose digest is consistent with its content, which is
// what every caller of Reconcile is required to hand it.
func rec(key, body string) MemoryRecord {
	r := MemoryRecord{
		Key:         key,
		Type:        "project",
		Description: "desc for " + key,
		Body:        body,
		CreatedAt:   "2026-08-01T00:00:00.000000000Z",
		UpdatedAt:   "2026-08-02T00:00:00.000000000Z",
	}
	r.Digest = Digest(r.Key, r.Type, r.Description, r.Body)
	return r
}

func side(records ...MemoryRecord) Side {
	s := Side{Memories: map[string]MemoryRecord{}, Tombstones: map[string]Tombstone{}}
	for _, r := range records {
		s.Memories[r.Key] = r
	}
	return s
}

// tomb marks a key as deleted on one side, carrying the digest of what was
// deleted — the comparison that separates a propagating delete from a
// delete/edit divergence.
func (s Side) tomb(key, body, device string) Side {
	r := rec(key, body)
	s.Tombstones[key] = Tombstone{
		Kind: "memory", Ident: key, Digest: r.Digest, DeviceID: device,
		At: "2026-08-03T00:00:00.000000000Z",
	}
	return s
}

func baseAt(key, body string) Base {
	r := rec(key, body)
	return Base{Key: key, Digest: r.Digest, DeviceID: "d-remote", At: "2026-08-01T00:00:00.000000000Z"}
}

func bases(b ...Base) map[string]Base {
	m := map[string]Base{}
	for _, x := range b {
		m[x.Key] = x
	}
	return m
}

// only returns the single decision for a key, failing when the merger produced
// none or produced more than one for it.
func only(t *testing.T, decisions []Decision, key string) Decision {
	t.Helper()
	var found []Decision
	for _, d := range decisions {
		if d.Key == key {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		t.Fatalf("decisions for %q = %d, want exactly 1 (%+v)", key, len(found), decisions)
	}
	return found[0]
}

// The eight-row rule set of docs/sync-model.md §3.4, as a table.
//
// This is the centre of the whole design — docs/TODO.md named CAS merge
// semantics as the reason cross-machine sync went unbuilt — so every row is
// pinned, including the two that must NOT produce a conflict. Rows 3 and 8 are
// the convergence property from §3.3: two machines on which the same correction
// was typed independently agree rather than colliding, because the digest
// covers content and nothing else.
func TestReconcileRuleMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mine   Side
		theirs Side
		base   map[string]Base
		want   Action
	}{
		{
			name:   "created here only",
			mine:   side(rec("alpha", "local text")),
			theirs: side(),
			base:   bases(),
			want:   ActionPush,
		},
		{
			name:   "created there only",
			mine:   side(),
			theirs: side(rec("alpha", "remote text")),
			base:   bases(),
			want:   ActionPull,
		},
		{
			name:   "no base, both sides identical",
			mine:   side(rec("alpha", "same text")),
			theirs: side(rec("alpha", "same text")),
			base:   bases(),
			want:   ActionAgree,
		},
		{
			name:   "no base, both created the same key differently",
			mine:   side(rec("alpha", "local text")),
			theirs: side(rec("alpha", "remote text")),
			base:   bases(),
			want:   ActionConflict,
		},
		{
			name:   "only the remote moved",
			mine:   side(rec("alpha", "agreed text")),
			theirs: side(rec("alpha", "remote text")),
			base:   bases(baseAt("alpha", "agreed text")),
			want:   ActionPull,
		},
		{
			name:   "only this machine moved",
			mine:   side(rec("alpha", "local text")),
			theirs: side(rec("alpha", "agreed text")),
			base:   bases(baseAt("alpha", "agreed text")),
			want:   ActionPush,
		},
		{
			name:   "both moved, differently",
			mine:   side(rec("alpha", "local text")),
			theirs: side(rec("alpha", "remote text")),
			base:   bases(baseAt("alpha", "agreed text")),
			want:   ActionConflict,
		},
		{
			name:   "both moved the same way",
			mine:   side(rec("alpha", "corrected text")),
			theirs: side(rec("alpha", "corrected text")),
			base:   bases(baseAt("alpha", "agreed text")),
			want:   ActionAgree,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := only(t, Reconcile(tc.mine, tc.theirs, tc.base), "alpha")
			if got.Action != tc.want {
				t.Fatalf("action = %q, want %q (reason: %q)", got.Action, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Errorf("decision carries no reason; a run summary a human cannot read is not a summary")
			}
		})
	}
}

// A conflict never carries a decision about which side wins, and it must hand
// back both sides so `sync status` can diff them. §3.5: neither side is
// written, and the human resolves.
func TestReconcileConflictCarriesBothSides(t *testing.T) {
	mine := side(rec("alpha", "local text"))
	theirs := side(rec("alpha", "remote text"))

	d := only(t, Reconcile(mine, theirs, bases(baseAt("alpha", "agreed text"))), "alpha")
	if d.Action != ActionConflict {
		t.Fatalf("action = %q, want conflict", d.Action)
	}
	if d.Mine == nil || d.Theirs == nil {
		t.Fatalf("conflict must carry both sides, got mine=%v theirs=%v", d.Mine, d.Theirs)
	}
	if d.Mine.Body != "local text" || d.Theirs.Body != "remote text" {
		t.Fatalf("conflict sides are crossed or empty: mine=%q theirs=%q", d.Mine.Body, d.Theirs.Body)
	}
}

// A tombstone beats an unchanged remote: the delete propagates. Without this
// the user's delete is undone on the next run, and the user learns that delete
// does not work (§3.7, A.8).
func TestReconcileTombstonePropagatesToUnchangedRemote(t *testing.T) {
	mine := side().tomb("alpha", "agreed text", "d-local")
	theirs := side(rec("alpha", "agreed text"))

	d := only(t, Reconcile(mine, theirs, bases(baseAt("alpha", "agreed text"))), "alpha")
	if d.Action != ActionDeleteRemote {
		t.Fatalf("action = %q, want delete-remote", d.Action)
	}
}

// The mirror: a tombstone arriving from the other machine deletes here, when
// this machine has not touched the memory since.
func TestReconcileRemoteTombstoneDeletesUnchangedLocal(t *testing.T) {
	mine := side(rec("alpha", "agreed text"))
	theirs := side().tomb("alpha", "agreed text", "d-remote")

	d := only(t, Reconcile(mine, theirs, bases(baseAt("alpha", "agreed text"))), "alpha")
	if d.Action != ActionDeleteLocal {
		t.Fatalf("action = %q, want delete-local", d.Action)
	}
}

// A tombstone loses to a side that CHANGED after the tombstoned digest. That is
// delete/edit divergence, and §3.7 settles it as a conflict defaulting to keep:
// never silently destroy beats converge.
func TestReconcileTombstoneLosesToAnEditOnTheOtherSide(t *testing.T) {
	t.Run("deleted here, edited there", func(t *testing.T) {
		mine := side().tomb("alpha", "agreed text", "d-local")
		theirs := side(rec("alpha", "text edited after the delete"))

		d := only(t, Reconcile(mine, theirs, bases(baseAt("alpha", "agreed text"))), "alpha")
		if d.Action != ActionConflict {
			t.Fatalf("action = %q, want conflict — a delete must not destroy the other machine's edit", d.Action)
		}
	})

	t.Run("edited here, deleted there", func(t *testing.T) {
		mine := side(rec("alpha", "text edited after the delete"))
		theirs := side().tomb("alpha", "agreed text", "d-remote")

		d := only(t, Reconcile(mine, theirs, bases(baseAt("alpha", "agreed text"))), "alpha")
		if d.Action != ActionConflict {
			t.Fatalf("action = %q, want conflict — an inbound delete must not destroy a local edit", d.Action)
		}
	})
}

// Both machines deleted the same memory. Whatever the merger calls this, the
// one outcome that must never happen is a resurrection: the memory is gone on
// both sides and nothing may propose writing it back.
func TestReconcileBothDeletedNeverResurrects(t *testing.T) {
	mine := side().tomb("alpha", "agreed text", "d-local")
	theirs := side().tomb("alpha", "agreed text", "d-remote")

	for _, d := range Reconcile(mine, theirs, bases(baseAt("alpha", "agreed text"))) {
		switch d.Action {
		case ActionPull, ActionPush, ActionConflict:
			t.Fatalf("both sides deleted %q, yet the merger proposed %q", d.Key, d.Action)
		}
	}
}

// A live record beats a stale tombstone for the same key on the same side.
//
// The write paths clear a tombstone whenever the key is written again, so
// stigmergy's own code should never present this state. The merger honours it
// anyway, because a hand-repaired database can, and because the failure is
// invisible when it goes wrong: the tombstone rules run BEFORE the base table
// is consulted, so a shadowed record either destroys the other machine's copy
// or never syncs at all, and neither says anything.
func TestReconcileALiveRecordBeatsAStaleTombstoneOnTheSameSide(t *testing.T) {
	t.Run("the remote has never seen the key", func(t *testing.T) {
		mine := side(rec("alpha", "re-created here")).tomb("alpha", "the deleted text", "d-local")

		d := only(t, Reconcile(mine, side(), bases()), "alpha")
		if d.Action != ActionPush {
			t.Fatalf("action = %q, want push — a re-created memory that never syncs is invisible forever", d.Action)
		}
	})

	t.Run("the remote still holds the old content", func(t *testing.T) {
		mine := side(rec("alpha", "re-created here")).tomb("alpha", "the deleted text", "d-local")
		theirs := side(rec("alpha", "the deleted text"))

		d := only(t, Reconcile(mine, theirs, bases(baseAt("alpha", "the deleted text"))), "alpha")
		if d.Action == ActionDeleteRemote {
			t.Fatal("a stale local tombstone proposed deleting the remote's copy of a memory this machine has re-created")
		}
		if d.Action != ActionPush {
			t.Fatalf("action = %q, want push", d.Action)
		}
	})
}

// A memory this machine has already seen deleted must not come back when the
// remote is simply missing it. Absent-on-both is not a create.
func TestReconcileAbsentEverywhereProducesNoWrite(t *testing.T) {
	for _, d := range Reconcile(side(), side(), bases(baseAt("alpha", "agreed text"))) {
		switch d.Action {
		case ActionPull, ActionPush:
			t.Fatalf("nothing exists on either side, yet the merger proposed %q for %q", d.Action, d.Key)
		}
	}
}

// The merger is a pure function: same inputs, same decisions, in the same
// order. Reconcile runs before anything is written, and a run summary that
// reorders itself between a --dry-run and the real run is a summary nobody can
// trust (§3.10, §8 Stage A).
func TestReconcileIsDeterministicAndSorted(t *testing.T) {
	mine := side(rec("gamma", "g"), rec("alpha", "a"), rec("beta", "b-local"))
	theirs := side(rec("beta", "b-remote"), rec("delta", "d"))

	first := Reconcile(mine, theirs, bases(baseAt("beta", "b-agreed")))
	second := Reconcile(mine, theirs, bases(baseAt("beta", "b-agreed")))

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Reconcile is not deterministic:\nfirst  = %+v\nsecond = %+v", first, second)
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].Key > first[i].Key {
			t.Fatalf("decisions are not sorted by key: %q before %q", first[i-1].Key, first[i].Key)
		}
	}
}

// Every key present anywhere gets exactly one decision. A key silently dropped
// from the plan is a memory that never syncs and never reports why.
func TestReconcileCoversEveryKeyExactlyOnce(t *testing.T) {
	mine := side(rec("alpha", "a"), rec("beta", "b"))
	theirs := side(rec("beta", "b2"), rec("gamma", "c"))

	seen := map[string]int{}
	for _, d := range Reconcile(mine, theirs, bases()) {
		seen[d.Key]++
	}
	for _, key := range []string{"alpha", "beta", "gamma"} {
		if seen[key] != 1 {
			t.Errorf("key %q got %d decisions, want 1", key, seen[key])
		}
	}
}

// Reconcile must not mutate what it is handed. The caller holds the local side
// as read from the database, and a merger that edited it in place would make
// the plan and the database disagree before a single record was applied.
func TestReconcileDoesNotMutateItsInputs(t *testing.T) {
	mine := side(rec("alpha", "local text"))
	theirs := side(rec("alpha", "remote text"))
	base := bases(baseAt("alpha", "agreed text"))

	mineBefore := map[string]MemoryRecord{}
	for k, v := range mine.Memories {
		mineBefore[k] = v
	}
	baseBefore := map[string]Base{}
	for k, v := range base {
		baseBefore[k] = v
	}

	Reconcile(mine, theirs, base)

	if !reflect.DeepEqual(mine.Memories, mineBefore) {
		t.Errorf("Reconcile mutated the local side: %+v", mine.Memories)
	}
	if !reflect.DeepEqual(base, baseBefore) {
		t.Errorf("Reconcile mutated the base map: %+v", base)
	}
}
