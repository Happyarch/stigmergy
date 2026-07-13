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
)

// GC prunes records that have outlived their usefulness, returning how many of
// each were removed.
//
// It never touches memories or open claims. Those are the state of the system,
// not its history, and nothing but an explicit delete should ever remove them.
func (d *DB) GC() (auditPruned, mailPruned int, err error) {
	now := NowTime()

	res, err := d.Exec(`DELETE FROM audit_log WHERE at < ?`, Stamp(now.Add(-AuditRetention)))
	if err != nil {
		return 0, 0, serr.Internalf(err, "failed to prune the audit log")
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
		return auditPruned, 0, serr.Internalf(err, "failed to prune resolved mail")
	}
	n, _ = res.RowsAffected()
	mailPruned = int(n)

	if _, err := d.Exec(`DELETE FROM mailbox_threads
	                      WHERE state IN ('resolved','abandoned') AND updated_at < ?
	                        AND NOT EXISTS (SELECT 1 FROM mailbox_messages m WHERE m.thread_id = mailbox_threads.id)`,
		Stamp(now.Add(-MailRetention))); err != nil {
		return auditPruned, mailPruned, serr.Internalf(err, "failed to prune empty threads")
	}
	return auditPruned, mailPruned, nil
}
