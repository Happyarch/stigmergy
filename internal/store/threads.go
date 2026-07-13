package store

import (
	"database/sql"

	"github.com/happyarch/stigmergy/internal/serr"
)

// ThreadSummary is a conversation seen from one root's point of view: who it is
// with, what it was last about, and whether anything is waiting to be read.
//
// It exists because an inbox is not a conversation list. Inbox answers "who has
// written to me?", and a root that has sent a message nobody has answered yet
// appears in no inbox at all — its own least of all. Without a way to enumerate
// its own threads, an agent that is compacted mid-negotiation loses the thread id
// and, with it, any route back to the conversation it started. It would then
// either send the same message again or, far worse, read the silence as consent
// and edit anyway.
type ThreadSummary struct {
	ID            int64  `json:"id"`
	ClaimID       *int64 `json:"claim_id,omitempty"`
	State         string `json:"state"`
	Resolution    string `json:"resolution,omitempty"`
	With          string `json:"with"`            // the other root in the conversation
	Subject       string `json:"subject"`         // of the most recent message
	LastMessageAt string `json:"last_message_at"` // when it was sent
	LastFromSelf  bool   `json:"last_from_self"`  // true when the ball is in their court
	Unread        int    `json:"unread"`          // messages to you that you have not read
	Messages      int    `json:"messages"`
}

// Threads lists the conversations a root takes part in, most recently active
// first. An empty state lists them all; otherwise it filters to that state.
func (d *DB) Threads(rootID, state string) ([]ThreadSummary, error) {
	if rootID == "" {
		return nil, serr.E(serr.InvalidInput, "a root is needed to list its threads")
	}
	if state != "" && state != ThreadOpen && state != ThreadResolved && state != ThreadAbandoned {
		return nil, serr.E(serr.InvalidInput,
			"state %q is invalid: must be %q, %q or %q", state, ThreadOpen, ThreadResolved, ThreadAbandoned)
	}

	// The last message decides the subject, the counterparty and who spoke last;
	// a correlated max(id) is the cheap way to get it without a window function.
	q := `
SELECT t.id, t.claim_id, t.state, COALESCE(t.resolution, ''),
       CASE WHEN last.from_root = :self THEN last.to_root ELSE last.from_root END,
       last.subject, last.sent_at, last.from_root = :self,
       (SELECT count(*) FROM mailbox_messages u
         WHERE u.thread_id = t.id AND u.to_root = :self AND u.read_at IS NULL),
       (SELECT count(*) FROM mailbox_messages c WHERE c.thread_id = t.id)
  FROM mailbox_threads t
  JOIN mailbox_messages last
    ON last.id = (SELECT max(m2.id) FROM mailbox_messages m2 WHERE m2.thread_id = t.id)
 WHERE EXISTS (SELECT 1 FROM mailbox_messages m
                WHERE m.thread_id = t.id AND (m.from_root = :self OR m.to_root = :self))`
	if state != "" {
		q += ` AND t.state = :state`
	}
	q += ` ORDER BY last.sent_at DESC, t.id DESC`

	args := []any{sql.Named("self", rootID)}
	if state != "" {
		args = append(args, sql.Named("state", state))
	}
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, serr.Internalf(err, "failed to list threads")
	}
	defer rows.Close()

	out := []ThreadSummary{}
	for rows.Next() {
		var s ThreadSummary
		var claimID sql.NullInt64
		if err := rows.Scan(&s.ID, &claimID, &s.State, &s.Resolution, &s.With,
			&s.Subject, &s.LastMessageAt, &s.LastFromSelf, &s.Unread, &s.Messages); err != nil {
			return nil, serr.Internalf(err, "failed to read a thread")
		}
		if claimID.Valid {
			s.ClaimID = &claimID.Int64
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AllThreads lists every conversation in the project, newest activity first,
// with its messages. This is the human's view — `stigmergy watch` — and it is
// deliberately not exposed to agents: an agent reads its own mail, not everyone
// else's.
func (d *DB) AllThreads(limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.Query(`
SELECT t.id, t.claim_id, t.created_by, t.state, COALESCE(t.resolution, ''), t.created_at, t.updated_at
  FROM mailbox_threads t
 ORDER BY t.updated_at DESC, t.id DESC
 LIMIT ?`, limit)
	if err != nil {
		return nil, serr.Internalf(err, "failed to list threads")
	}
	defer rows.Close()

	var out []Thread
	for rows.Next() {
		var t Thread
		var claimID sql.NullInt64
		var resolution sql.NullString
		if err := rows.Scan(&t.ID, &claimID, &t.CreatedBy, &t.State, &resolution,
			&t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, serr.Internalf(err, "failed to read a thread")
		}
		if claimID.Valid {
			t.ClaimID = &claimID.Int64
		}
		t.Resolution = resolution.String
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read the threads")
	}

	for i := range out {
		msgs, err := d.threadMessages(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Messages = msgs
	}
	return out, nil
}
