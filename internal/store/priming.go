package store

import "github.com/happyarch/stigmergy/internal/serr"

// PrimingDelivered reports which of the given memory keys have already been
// shown to this root in a priming note, so a session hears about a memory
// exactly once (docs/association-model.md §9).
func (d *DB) PrimingDelivered(rootID string, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	keys = dedupeKeys(keys)
	if rootID == "" || len(keys) == 0 {
		return out, nil
	}
	ph := placeholders(len(keys))
	args := make([]any, 0, len(keys)+1)
	args = append(args, rootID)
	for _, k := range keys {
		args = append(args, k)
	}
	rows, err := d.Query(
		`SELECT key FROM priming_delivered WHERE root_id = ? AND key IN (`+ph+`)`, args...)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read priming history")
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, serr.Internalf(err, "failed to read a priming record")
		}
		out[k] = true
	}
	return out, rows.Err()
}

// MarkPrimed records that this root's priming note named these memories.
// Idempotent — a key already marked is left alone — and meant to be called
// only after the note text has actually been composed, mirroring
// MarkDelivered's notified_at rule: marking first and composing second would
// let a crash in between eat the note while the record says it was shown.
func (d *DB) MarkPrimed(rootID string, keys []string) error {
	if rootID == "" || len(keys) == 0 {
		return nil
	}
	now := Now()
	tx, err := d.Begin()
	if err != nil {
		return serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()
	for _, k := range keys {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO priming_delivered(root_id, key, at) VALUES(?, ?, ?)`,
			rootID, k, now,
		); err != nil {
			return serr.Internalf(err, "failed to record priming for %q", k)
		}
	}
	return tx.Commit()
}
