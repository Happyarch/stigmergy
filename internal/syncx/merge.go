package syncx

import (
	"cmp"
	"slices"
)

// Action is what Reconcile decided for one key. The set is closed, the same
// way internal/serr's error codes are: a caller branches on these, so a new
// one is a considered addition, never a synonym for an existing one.
type Action string

const (
	// ActionNone means nothing moves: the key is unchanged on both sides, or
	// both sides have already deleted it, or there is nothing left to say
	// about it (docs/sync-model.md §3.4, §3.7).
	ActionNone Action = "none"
	// ActionPush means this machine's copy is written into the export tree.
	ActionPush Action = "push"
	// ActionPull means the remote's copy is applied locally, through
	// store.ImportMemory.
	ActionPull Action = "pull"
	// ActionAgree means both sides moved to the same content independently;
	// nothing is written except the recorded base.
	ActionAgree Action = "agree"
	// ActionConflict means neither side is touched. The key is staged for a
	// human to resolve (docs/sync-model.md §3.5) — nothing here auto-merges.
	ActionConflict Action = "conflict"
	// ActionDeleteLocal means a remote tombstone applies here: this machine's
	// copy is unchanged since the digest the tombstone names, so the delete
	// propagates and removes it locally.
	ActionDeleteLocal Action = "delete-local"
	// ActionDeleteRemote means a local tombstone propagates: the remote's copy
	// is unchanged since the digest this machine deleted, so the delete
	// applies there on its next import.
	ActionDeleteRemote Action = "delete-remote"
)

// Decision is what Reconcile returned for one key: the action to take, why,
// and whichever of the two records are relevant to it. Mine and Theirs are
// nil whenever that side has no memory record to show — a tombstone, or no
// record at all — never a zero MemoryRecord standing in for absence.
type Decision struct {
	Key    string
	Action Action
	Reason string // one line, human-readable, names why this rule fired
	Mine   *MemoryRecord
	Theirs *MemoryRecord
}

// Side is one machine's exported view of one scope: every memory it holds,
// keyed, and every tombstone it has recorded for a memory it used to hold but
// deleted. Tombstones is keyed by Ident and carries memory-kind tombstones
// only — a Side is one scope's view, and link tombstones are a caller-side
// concern (§3.6's union-by-identity merge, not something Reconcile decides).
type Side struct {
	Memories   map[string]MemoryRecord
	Tombstones map[string]Tombstone
}

// Reconcile is the pure merge described by docs/sync-model.md §3.4 (the
// no-tombstone rule table) and §3.7 (deletes and the resurrection loop).
// Deterministic, no clock read, no I/O: the only inputs are the two exported
// views and the recorded bases, and the only output is a plan a caller may or
// may not carry out. Decisions are sorted by Key so a run is reproducible and
// a diff between two runs' output is meaningful.
func Reconcile(mine, theirs Side, base map[string]Base) []Decision {
	keys := map[string]struct{}{}
	for k := range mine.Memories {
		keys[k] = struct{}{}
	}
	for k := range theirs.Memories {
		keys[k] = struct{}{}
	}
	for k := range mine.Tombstones {
		keys[k] = struct{}{}
	}
	for k := range theirs.Tombstones {
		keys[k] = struct{}{}
	}
	for k := range base {
		keys[k] = struct{}{}
	}

	out := make([]Decision, 0, len(keys))
	for k := range keys {
		var b *Base
		if v, ok := base[k]; ok {
			b = &v
		}
		out = append(out, decide(k, sideState(mine, k), sideState(theirs, k), b))
	}
	slices.SortFunc(out, func(a, b Decision) int { return cmp.Compare(a.Key, b.Key) })
	return out
}

// state is one side's condition for one key: present with a record, tombstoned
// with the digest that was deleted, or neither (no record here at all).
type state struct {
	rec    *MemoryRecord
	digest string // rec.Digest when present; "" otherwise
	tomb   *Tombstone
}

// sideState reads one side's condition for one key.
//
// A live record beats a tombstone for the same key on the same side, and that
// precedence is load-bearing rather than tidy. The tombstone rules run before
// the base table is consulted, so a tombstone shadowing a present memory would
// propose deleting the other machine's copy of a memory this side currently
// holds — or, against a remote that has never seen the key, propose nothing at
// all and never sync it. A record is present state; a tombstone is a record of
// something that happened. Present state wins.
//
// Every write path now clears the tombstone for a key it writes, so this state
// should not arise from stigmergy's own code. It is honoured here because a
// database repaired by hand, or one written by a build predating that clearing,
// can still present it — and the failure it causes is invisible.
func sideState(s Side, key string) state {
	var st state
	if r, ok := s.Memories[key]; ok {
		rc := r
		st.rec = &rc
		st.digest = r.Digest
		return st
	}
	if t, ok := s.Tombstones[key]; ok {
		tc := t
		st.tomb = &tc
	}
	return st
}

// decide applies docs/sync-model.md §3.4 and §3.7 to one key. Tombstones are
// resolved first, because a delete is a stronger claim than an edit and §3.7
// has its own rule set independent of the base-comparison table.
func decide(key string, mine, theirs state, base *Base) Decision {
	switch {
	case mine.tomb != nil && theirs.tomb != nil:
		// Both sides have already deleted this key. Nothing propagates because
		// there is nothing left to propagate; each machine's own tombstone
		// already closes its own loop.
		return Decision{Key: key, Action: ActionNone,
			Reason: "deleted on both sides; nothing to propagate"}

	case mine.tomb != nil && theirs.rec != nil:
		if theirs.digest == mine.tomb.Digest {
			// The remote's copy is exactly what was deleted here — unchanged
			// since, so the delete propagates (§3.7: "a tombstone beats an
			// unchanged remote").
			return Decision{Key: key, Action: ActionDeleteRemote, Theirs: theirs.rec,
				Reason: "deleted locally; the remote copy is unchanged since, so the deletion propagates"}
		}
		// The remote moved past the digest this tombstone names. A tombstone
		// loses to an edit — never silently destroy what somebody typed after
		// the delete — so this surfaces as a conflict rather than a delete or a
		// silent revival, with the default being to keep the remote's content.
		return Decision{Key: key, Action: ActionConflict, Theirs: theirs.rec,
			Reason: "deleted locally, but the remote has changed since the deleted content; the deletion does not override an edit"}

	case mine.tomb != nil:
		// Deleted locally; the remote has no record — no memory and no
		// tombstone of its own. Nothing to do: the remote either never had it,
		// already converged, or has yet to receive this tombstone, and any of
		// those resolves on a later run without help from this one.
		return Decision{Key: key, Action: ActionNone,
			Reason: "deleted locally; the remote has no record of this key"}

	case theirs.tomb != nil && mine.rec != nil:
		if mine.digest == theirs.tomb.Digest {
			return Decision{Key: key, Action: ActionDeleteLocal, Mine: mine.rec,
				Reason: "deleted on the remote; this copy is unchanged since, so the deletion applies here"}
		}
		return Decision{Key: key, Action: ActionConflict, Mine: mine.rec,
			Reason: "deleted on the remote, but this copy has changed since the deleted content; the deletion does not override an edit"}

	case theirs.tomb != nil:
		return Decision{Key: key, Action: ActionNone,
			Reason: "deleted on the remote; there is no local record of this key"}
	}

	// Neither side has a tombstone for this key. §3.4's base-comparison table.
	if base == nil {
		switch {
		case mine.rec != nil && theirs.rec == nil:
			return Decision{Key: key, Action: ActionPush, Mine: mine.rec,
				Reason: "created on this machine; the remote has no record of it"}
		case mine.rec == nil && theirs.rec != nil:
			return Decision{Key: key, Action: ActionPull, Theirs: theirs.rec,
				Reason: "exists on the remote only; there is no local record of it"}
		case mine.rec != nil && theirs.rec != nil:
			if mine.digest == theirs.digest {
				return Decision{Key: key, Action: ActionAgree, Mine: mine.rec, Theirs: theirs.rec,
					Reason: "both machines independently created the same content"}
			}
			return Decision{Key: key, Action: ActionConflict, Mine: mine.rec, Theirs: theirs.rec,
				Reason: "both machines independently created this key with different content"}
		default:
			// No base, no tombstone anywhere, no record anywhere: nothing ever
			// existed here as far as either export can tell.
			return Decision{Key: key, Action: ActionNone,
				Reason: "no record of this key on either side"}
		}
	}

	// A base is recorded, so this key has synced before. The invariant that
	// makes the rest of this branch sound — a delete always removes the base
	// row (ON DELETE CASCADE, §3.2) at the same time it writes a tombstone —
	// means mine.rec and theirs.rec should both be present whenever base is.
	// The two guards below exist for the state that invariant is not supposed
	// to allow: a row that predates Stage A's tombstone-on-delete change, or a
	// database repaired by hand. They report rather than assume.
	if mine.rec == nil {
		if theirs.rec != nil {
			return Decision{Key: key, Action: ActionPull, Theirs: theirs.rec,
				Reason: "no local record though a shared base is recorded; adopting the remote's copy"}
		}
		return Decision{Key: key, Action: ActionNone,
			Reason: "no local or remote record though a shared base is recorded"}
	}
	if theirs.rec == nil {
		return Decision{Key: key, Action: ActionPush, Mine: mine.rec,
			Reason: "no remote record though a shared base is recorded; pushing the local copy"}
	}

	mineMoved := mine.digest != base.Digest
	theirsMoved := theirs.digest != base.Digest

	switch {
	case !mineMoved && theirsMoved:
		return Decision{Key: key, Action: ActionPull, Mine: mine.rec, Theirs: theirs.rec,
			Reason: "only the remote has changed since the last sync"}
	case mineMoved && !theirsMoved:
		return Decision{Key: key, Action: ActionPush, Mine: mine.rec, Theirs: theirs.rec,
			Reason: "only this machine has changed since the last sync"}
	case !mineMoved && !theirsMoved:
		return Decision{Key: key, Action: ActionNone, Mine: mine.rec, Theirs: theirs.rec,
			Reason: "unchanged on both sides since the last sync"}
	default: // both moved
		if mine.digest == theirs.digest {
			return Decision{Key: key, Action: ActionAgree, Mine: mine.rec, Theirs: theirs.rec,
				Reason: "both machines changed this key to the same content since the last sync"}
		}
		return Decision{Key: key, Action: ActionConflict, Mine: mine.rec, Theirs: theirs.rec,
			Reason: "both machines changed this key since the last sync, to different content"}
	}
}
