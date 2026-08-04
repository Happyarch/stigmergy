package store

import (
	"time"

	"github.com/happyarch/stigmergy/internal/serr"
)

// Retention windows for housekeeping.
//
// The audit log is the record of who did what, so it is kept long enough to
// investigate anything anyone still remembers, and no longer. Resolved mail is
// a settled conversation: worth keeping while it is still fresh enough to be
// re-read, worthless after that.
const (
	AuditRetention = 90 * 24 * time.Hour
	MailRetention  = 30 * 24 * time.Hour
	// EpisodeRetention is how long an episode that grounds no memory and sits
	// in no correction/continuation chain may be reclaimed after. Consolidation
	// as retention policy (docs/association-model.md §2, §10.3): distilled and
	// cited history is provenance and stays; undistilled, unchained residue is
	// the compactable scaffold.
	EpisodeRetention = 180 * 24 * time.Hour
)

// GC prunes records that have outlived their usefulness, returning how many of
// each were removed.
//
// It never touches memories or open claims. Those are the state of the system,
// not its history, and nothing but an explicit delete should ever remove them.
func (d *DB) GC() (auditPruned, mailPruned, episodesPruned int, err error) {
	now := NowTime()

	res, err := d.Exec(`DELETE FROM audit_log WHERE at < ?`, Stamp(now.Add(-AuditRetention)))
	if err != nil {
		return 0, 0, 0, serr.Internalf(err, "failed to prune the audit log")
	}
	n, _ := res.RowsAffected()
	auditPruned = int(n)

	// Only messages in settled threads: an old message in a still-open thread is
	// exactly the context someone would need to understand why it is still open.
	res, err = d.Exec(`
		DELETE FROM mailbox_messages
		 WHERE sent_at < ?
		   AND thread_id IN (SELECT id FROM mailbox_threads
		                      WHERE state IN ('resolved','abandoned') AND updated_at < ?)`,
		Stamp(now.Add(-MailRetention)), Stamp(now.Add(-MailRetention)))
	if err != nil {
		return auditPruned, 0, 0, serr.Internalf(err, "failed to prune resolved mail")
	}
	n, _ = res.RowsAffected()
	mailPruned = int(n)

	if _, err := d.Exec(`DELETE FROM mailbox_threads
	                      WHERE state IN ('resolved','abandoned') AND updated_at < ?
	                        AND NOT EXISTS (SELECT 1 FROM mailbox_messages m WHERE m.thread_id = mailbox_threads.id)`,
		Stamp(now.Add(-MailRetention))); err != nil {
		return auditPruned, mailPruned, 0, serr.Internalf(err, "failed to prune empty threads")
	}

	// Episodes: history, not state (docs/association-model.md §4, §10.3).
	// Prunable only when ALL of old, ungrounding and unchained hold — a
	// memory's provenance citation or a correction/continuation chain is
	// exactly the kind of thing GC must never quietly take away.
	res, err = d.Exec(`
		DELETE FROM episodes
		 WHERE at < ?
		   AND NOT EXISTS (SELECT 1 FROM episode_memory em WHERE em.episode_id = episodes.id)
		   AND NOT EXISTS (SELECT 1 FROM episode_links el
		                    WHERE el.episode_id = episodes.id OR el.successor_id = episodes.id)`,
		Stamp(now.Add(-EpisodeRetention)))
	if err != nil {
		return auditPruned, mailPruned, 0, serr.Internalf(err, "failed to prune old episodes")
	}
	n, _ = res.RowsAffected()
	episodesPruned = int(n)

	return auditPruned, mailPruned, episodesPruned, nil
}
