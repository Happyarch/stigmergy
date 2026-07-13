package store

import (
	"database/sql"
	"errors"

	"github.com/happyarch/stigmergy/internal/serr"
)

// ErrNoThread reports an unknown mailbox thread.
var ErrNoThread = errors.New("store: thread not found")

// Thread states.
const (
	ThreadOpen      = "open"
	ThreadResolved  = "resolved"
	ThreadAbandoned = "abandoned"
)

// Message is one piece of mail between two roots.
type Message struct {
	ID       int64  `json:"id"`
	ThreadID int64  `json:"thread_id"`
	FromRoot string `json:"from_root"`
	ToRoot   string `json:"to_root"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
	SentAt   string `json:"sent_at"`
	ReadAt   string `json:"read_at,omitempty"`
}

// Thread is a conversation, usually about a claim that is blocking someone.
type Thread struct {
	ID         int64     `json:"id"`
	ClaimID    *int64    `json:"claim_id,omitempty"`
	CreatedBy  string    `json:"created_by"`
	State      string    `json:"state"`
	Resolution string    `json:"resolution,omitempty"`
	CreatedAt  string    `json:"created_at"`
	UpdatedAt  string    `json:"updated_at"`
	Messages   []Message `json:"messages,omitempty"`
}

// SendRequest is a mailbox_send or mailbox_reply.
type SendRequest struct {
	FromRoot string
	ToRoot   string
	Subject  string
	Body     string
	ClaimID  *int64
	ThreadID *int64 // nil starts a new thread
}

// SendMessage delivers mail to another root.
//
// The recipient must be active. Mail to a dead root would sit unread forever
// while the sender waits for an answer that cannot come — so this fails loudly,
// and the sender learns the blocking claim's owner is gone and its claim with
// it.
func (d *DB) SendMessage(req SendRequest) (*Message, error) {
	if req.ToRoot == "" || req.Subject == "" || req.Body == "" {
		return nil, serr.E(serr.InvalidInput, "to_root, subject and body are all required")
	}
	if req.ToRoot == req.FromRoot {
		return nil, serr.E(serr.InvalidInput, "you cannot send mail to yourself")
	}

	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	var ended sql.NullString
	var lastSeen string
	err = tx.QueryRow(`SELECT COALESCE(ended_at, ''), last_seen_at FROM roots WHERE root_id = ?`,
		req.ToRoot).Scan(&ended, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, serr.E(serr.RecipientInactive,
			"there is no root %s in this project — check claim_list_active for who actually holds the claim", req.ToRoot)
	}
	if err != nil {
		return nil, serr.Internalf(err, "failed to look up the recipient")
	}
	if ended.String != "" || lastSeen <= RootTTLCutoff() {
		return nil, serr.E(serr.RecipientInactive,
			"root %s is no longer active, so it cannot answer you — its claims have lapsed with it, and the paths it held are free",
			req.ToRoot).With("root_id", req.ToRoot)
	}

	now := Now()
	threadID := int64(0)
	if req.ThreadID != nil {
		threadID = *req.ThreadID
		var state string
		err := tx.QueryRow(`SELECT state FROM mailbox_threads WHERE id = ?`, threadID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoThread
		}
		if err != nil {
			return nil, serr.Internalf(err, "failed to look up the thread")
		}
		if _, err := tx.Exec(`UPDATE mailbox_threads SET updated_at = ? WHERE id = ?`, now, threadID); err != nil {
			return nil, serr.Internalf(err, "failed to update the thread")
		}
	} else {
		res, err := tx.Exec(
			`INSERT INTO mailbox_threads(claim_id, created_by, state, created_at, updated_at)
			 VALUES(?, ?, 'open', ?, ?)`, req.ClaimID, req.FromRoot, now, now)
		if err != nil {
			return nil, serr.Internalf(err, "failed to open the thread")
		}
		threadID, _ = res.LastInsertId()
	}

	res, err := tx.Exec(
		`INSERT INTO mailbox_messages(thread_id, from_root, to_root, subject, body, sent_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		threadID, req.FromRoot, req.ToRoot, req.Subject, req.Body, now)
	if err != nil {
		return nil, serr.Internalf(err, "failed to send the message")
	}
	id, _ := res.LastInsertId()

	if err := audit(tx, AuditEntry{
		Actor: req.FromRoot, Action: "mailbox_send", Target: req.ToRoot, Detail: req.Subject,
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit the message")
	}
	return &Message{
		ID: id, ThreadID: threadID, FromRoot: req.FromRoot, ToRoot: req.ToRoot,
		Subject: req.Subject, Body: req.Body, SentAt: now,
	}, nil
}

// Inbox lists mail addressed to a root, newest first. unreadOnly narrows it to
// what still needs an answer.
func (d *DB) Inbox(rootID string, unreadOnly bool) ([]Message, error) {
	q := `SELECT id, thread_id, from_root, to_root, subject, body, sent_at, COALESCE(read_at, '')
	        FROM mailbox_messages WHERE to_root = ?`
	if unreadOnly {
		q += ` AND read_at IS NULL`
	}
	q += ` ORDER BY sent_at DESC, id DESC`

	rows, err := d.Query(q, rootID)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read the inbox")
	}
	defer rows.Close()

	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.FromRoot, &m.ToRoot, &m.Subject, &m.Body,
			&m.SentAt, &m.ReadAt); err != nil {
			return nil, serr.Internalf(err, "failed to read the inbox")
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read the inbox")
	}
	return out, nil
}

// MarkRead marks messages read. Only the recipient may: read state is a signal
// to the sender that the message landed, and it would be worthless if anyone
// could set it.
func (d *DB) MarkRead(rootID string, messageIDs []int64) (int, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}
	tx, err := d.Begin()
	if err != nil {
		return 0, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	now := Now()
	n := 0
	for _, id := range messageIDs {
		res, err := tx.Exec(
			`UPDATE mailbox_messages SET read_at = ? WHERE id = ? AND to_root = ? AND read_at IS NULL`,
			now, id, rootID)
		if err != nil {
			return 0, serr.Internalf(err, "failed to mark the message read")
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, serr.Internalf(err, "failed to commit")
	}
	return n, nil
}

// GetThread returns a thread with its messages in order.
func (d *DB) GetThread(id int64) (*Thread, error) {
	var t Thread
	var claimID sql.NullInt64
	var resolution sql.NullString
	err := d.QueryRow(
		`SELECT id, claim_id, created_by, state, resolution, created_at, updated_at
		   FROM mailbox_threads WHERE id = ?`, id).
		Scan(&t.ID, &claimID, &t.CreatedBy, &t.State, &resolution, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoThread
	}
	if err != nil {
		return nil, serr.Internalf(err, "failed to read the thread")
	}
	if claimID.Valid {
		t.ClaimID = &claimID.Int64
	}
	t.Resolution = resolution.String

	msgs, err := d.threadMessages(id)
	if err != nil {
		return nil, err
	}
	t.Messages = msgs
	return &t, nil
}

// threadMessages reads a thread's messages in the order they were sent.
func (d *DB) threadMessages(id int64) ([]Message, error) {
	rows, err := d.Query(
		`SELECT id, thread_id, from_root, to_root, subject, body, sent_at, COALESCE(read_at, '')
		   FROM mailbox_messages WHERE thread_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read the thread's messages")
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.FromRoot, &m.ToRoot, &m.Subject, &m.Body,
			&m.SentAt, &m.ReadAt); err != nil {
			return nil, serr.Internalf(err, "failed to read the thread's messages")
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read the thread's messages")
	}
	return out, nil
}

// ResolveThread closes a negotiation and records what was agreed.
//
// Either participant may resolve it: a negotiation is over when either side
// says it is (the claim was released, the work moved elsewhere), and requiring
// the opener to close it would leave threads open whenever they are the one who
// walked away.
func (d *DB) ResolveThread(id int64, rootID, resolution, state string) (*Thread, error) {
	if state != ThreadResolved && state != ThreadAbandoned {
		return nil, serr.E(serr.InvalidInput, "state must be %q or %q", ThreadResolved, ThreadAbandoned)
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	var participant int
	err = tx.QueryRow(
		`SELECT count(*) FROM mailbox_threads t
		  WHERE t.id = ? AND (t.created_by = ?
		     OR EXISTS (SELECT 1 FROM mailbox_messages m
		                 WHERE m.thread_id = t.id AND (m.from_root = ? OR m.to_root = ?)))`,
		id, rootID, rootID, rootID).Scan(&participant)
	if err != nil {
		return nil, serr.Internalf(err, "failed to check the thread")
	}
	if participant == 0 {
		var exists int
		_ = tx.QueryRow(`SELECT count(*) FROM mailbox_threads WHERE id = ?`, id).Scan(&exists)
		if exists == 0 {
			return nil, ErrNoThread
		}
		return nil, serr.E(serr.NotOwner, "thread %d is not yours to resolve: you are not a participant", id)
	}

	if _, err := tx.Exec(
		`UPDATE mailbox_threads SET state = ?, resolution = ?, updated_at = ? WHERE id = ?`,
		state, nullStr(resolution), Now(), id); err != nil {
		return nil, serr.Internalf(err, "failed to resolve the thread")
	}
	if err := audit(tx, AuditEntry{
		Actor: rootID, Action: "mailbox_resolve", Detail: state + ": " + resolution,
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit")
	}
	return d.GetThread(id)
}
