package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
//
// ReadAt and NotifiedAt answer different questions. NotifiedAt is stigmergy's:
// did we ever put this in front of the recipient? ReadAt is the recipient's: did
// it look? Keeping them apart is what lets mail be delivered rather than merely
// left lying about — see the 0002 migration.
type Message struct {
	ID         int64  `json:"id"`
	ThreadID   int64  `json:"thread_id"`
	FromRoot   string `json:"from_root"`
	ToRoot     string `json:"to_root"`
	Subject    string `json:"subject"`
	Body       string `json:"body"`
	SentAt     string `json:"sent_at"`
	ReadAt     string `json:"read_at,omitempty"`
	NotifiedAt string `json:"notified_at,omitempty"`
}

// Delivery is a message on its way to an agent that has not been told about it
// yet, carrying what the agent needs in order to act on it without a second
// query: who sent it, and whether that sender is still there to be answered.
type Delivery struct {
	Message
	FromKind     string `json:"from_kind"`
	FromLiveness string `json:"from_liveness"`
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
	req.Subject, req.Body = NormalizeText(req.Subject), NormalizeText(req.Body)
	if req.ToRoot == "" || req.Subject == "" || req.Body == "" {
		return nil, serr.E(serr.InvalidInput, "to_root, subject and body are all required")
	}
	if req.ToRoot == req.FromRoot {
		return nil, serr.E(serr.InvalidInput, "you cannot send mail to yourself")
	}
	// Mail is the highest-value target for anything hiding in text: hooks put it
	// in front of the recipient at the start of a turn, so whatever is in here is
	// rendered into another agent's terminal and context without either of them
	// choosing to look at it.
	if err := ValidateLine("subject", req.Subject, MaxLineLength); err != nil {
		return nil, err
	}
	if err := ValidateBlock("body", req.Body); err != nil {
		return nil, err
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
		return nil, deadLetter(tx, req.ToRoot,
			"there is no root %s in this project — you may have copied an id from an old message or a stale note", req.ToRoot)
	}
	if err != nil {
		return nil, serr.Internalf(err, "failed to look up the recipient")
	}
	if ended.String != "" || lastSeen <= RootTTLCutoff() {
		return nil, deadLetter(tx, req.ToRoot,
			"root %s is no longer active, so it cannot answer you — its claims have lapsed with it, and the paths it held are free",
			req.ToRoot)
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
		// A message into a settled thread REOPENS it. The state was read here and
		// then thrown away, which is worse than not reading it: it looks like a
		// check.
		//
		// Resolving says the matter is closed; a new message says it is not, and
		// the message wins because it is the more recent claim about the same
		// question. Leaving the thread resolved made a live conversation invisible
		// — it dropped out of every open-thread listing, and Stalled() requires
		// ThreadOpen, so the "no answer is coming, that agent is gone" warning
		// could never fire for it. An agent waiting on a reply in a reopened
		// thread would wait forever, which is the exact failure the stalled-thread
		// machinery was built to prevent.
		//
		// Unlike a claim, reviving a thread endangers nobody: there is no second
		// party whose safety depended on it staying shut. The resolution text goes,
		// because a settlement that did not settle anything is worse than none.
		if state != ThreadOpen {
			if _, err := tx.Exec(
				`UPDATE mailbox_threads SET state = ?, resolution = NULL, updated_at = ? WHERE id = ?`,
				ThreadOpen, now, threadID); err != nil {
				return nil, serr.Internalf(err, "failed to reopen the thread")
			}
			if err := audit(tx, AuditEntry{
				Actor: req.FromRoot, Action: "mailbox_reopen",
				Detail: fmt.Sprintf("thread=%d was %s", threadID, state),
			}); err != nil {
				return nil, serr.Internalf(err, "failed to write audit record")
			}
		} else if _, err := tx.Exec(
			`UPDATE mailbox_threads SET updated_at = ? WHERE id = ?`, now, threadID); err != nil {
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

// deadLetter is the answer to mail addressed to someone who is not there.
//
// Refusing is not enough. An agent that has just been told "that root is gone"
// still has the problem it was writing about — a file it cannot edit — and the
// most likely reason it got the id wrong is that it read it somewhere stale: an
// old conflict message, a memory, its own compacted context. Left to itself it
// will guess again, and there is no reason its second guess should be better
// than its first.
//
// So the refusal carries the roster: who is actually here, right now, and what
// each of them holds. The agent does not have to remember who to ask, and it
// does not have to be right the first time — it only has to try, and it will be
// handed the answer.
func deadLetter(tx *sql.Tx, toRoot, format string, args ...any) error {
	e := serr.E(serr.RecipientInactive, format, args...).With("root_id", toRoot)

	rows, err := tx.Query(
		`SELECT r.root_id, r.agent_kind, r.last_seen_at,
		        (SELECT group_concat(c.scope_path, ', ') FROM claims c
		          WHERE c.root_id = r.root_id AND c.released_at IS NULL AND c.expires_at > ?)
		   FROM roots r
		  WHERE r.ended_at IS NULL AND r.last_seen_at > ?
		  ORDER BY r.last_seen_at DESC`, Now(), RootTTLCutoff())
	if err != nil {
		return e
	}
	defer rows.Close()

	type liveRoot struct {
		RootID    string `json:"root_id"`
		AgentKind string `json:"agent_kind"`
		Liveness  string `json:"liveness"`
		Holds     string `json:"holds,omitempty"`
	}
	var live []liveRoot
	for rows.Next() {
		var r Root
		var holds sql.NullString
		if err := rows.Scan(&r.RootID, &r.AgentKind, &r.LastSeenAt, &holds); err != nil {
			return e
		}
		if r.RootID == toRoot {
			continue
		}
		live = append(live, liveRoot{
			RootID: r.RootID, AgentKind: r.AgentKind, Liveness: r.Liveness(), Holds: holds.String,
		})
	}
	if len(live) == 0 {
		return e.With("active_roots", []liveRoot{}).With("advice",
			"no other agent is active in this project right now, so there is no one to negotiate with: "+
				"nothing is holding the path against you, and you can go ahead")
	}

	var names []string
	for _, r := range live {
		if r.Holds != "" {
			names = append(names, fmt.Sprintf("%s (%s, holds %s)", r.RootID, r.AgentKind, r.Holds))
		} else {
			names = append(names, fmt.Sprintf("%s (%s)", r.RootID, r.AgentKind))
		}
	}
	e.Message += ". The agents actually active here are: " + strings.Join(names, "; ") +
		". Address the one that holds the path you want — root_list_active shows this too."
	return e.With("active_roots", live)
}

// PendingMail is mail the recipient has never been told about: unread, and never
// announced. It is the queue the delivery hooks drain, oldest first, because a
// negotiation is a conversation and reading it backwards makes no sense.
//
// Sender liveness rides along because it changes what the recipient should do. A
// message from an agent that has since died needs no reply — but it may well
// need action, since whatever it was asking for is now free.
func (d *DB) PendingMail(rootID string) ([]Delivery, error) {
	if rootID == "" {
		return nil, nil
	}
	rows, err := d.Query(
		`SELECT m.id, m.thread_id, m.from_root, m.to_root, m.subject, m.body, m.sent_at,
		        COALESCE(m.read_at, ''), COALESCE(m.notified_at, ''),
		        COALESCE(r.agent_kind, ''), COALESCE(r.last_seen_at, ''), COALESCE(r.ended_at, '')
		   FROM mailbox_messages m
		   LEFT JOIN roots r ON r.root_id = m.from_root
		  WHERE m.to_root = ? AND m.notified_at IS NULL AND m.read_at IS NULL
		  ORDER BY m.sent_at, m.id`, rootID)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read pending mail")
	}
	defer rows.Close()

	out := []Delivery{}
	for rows.Next() {
		var p Delivery
		var sender Root
		var senderEnded string
		if err := rows.Scan(&p.ID, &p.ThreadID, &p.FromRoot, &p.ToRoot, &p.Subject, &p.Body,
			&p.SentAt, &p.ReadAt, &p.NotifiedAt, &sender.AgentKind, &sender.LastSeenAt, &senderEnded); err != nil {
			return nil, serr.Internalf(err, "failed to read pending mail")
		}
		p.FromKind = sender.AgentKind
		switch {
		case sender.LastSeenAt == "":
			p.FromLiveness = "unknown"
		case senderEnded != "":
			p.FromLiveness = "gone (ended its session)"
		default:
			p.FromLiveness = sender.Liveness()
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkNotified records that these messages have been put in front of their
// recipient, so they never interrupt twice.
//
// It is stigmergy's own bookkeeping, not the agent's, which is why it is not an
// MCP tool: an agent that could suppress its own notifications would eventually
// do so. Only the delivery hooks call this, and only after the agent has been
// shown the mail.
func (d *DB) MarkNotified(rootID string, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	now := Now()
	for _, id := range messageIDs {
		if _, err := tx.Exec(
			`UPDATE mailbox_messages SET notified_at = ? WHERE id = ? AND to_root = ? AND notified_at IS NULL`,
			now, id, rootID); err != nil {
			return serr.Internalf(err, "failed to record the notification")
		}
	}
	if err := tx.Commit(); err != nil {
		return serr.Internalf(err, "failed to commit")
	}
	return nil
}

// Inbox lists mail addressed to a root, newest first. unreadOnly narrows it to
// what still needs an answer.
func (d *DB) Inbox(rootID string, unreadOnly bool) ([]Message, error) {
	q := `SELECT id, thread_id, from_root, to_root, subject, body, sent_at,
	             COALESCE(read_at, ''), COALESCE(notified_at, '')
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
			&m.SentAt, &m.ReadAt, &m.NotifiedAt); err != nil {
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
		`SELECT id, thread_id, from_root, to_root, subject, body, sent_at,
		        COALESCE(read_at, ''), COALESCE(notified_at, '')
		   FROM mailbox_messages WHERE thread_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read the thread's messages")
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.FromRoot, &m.ToRoot, &m.Subject, &m.Body,
			&m.SentAt, &m.ReadAt, &m.NotifiedAt); err != nil {
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
