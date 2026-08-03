package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/happyarch/stigmergy/internal/serr"
)

// CallerTicketTTL is how long a ticket is worth reading.
//
// It is short because a ticket describes ONE call that is about to happen: the
// hook writes it microseconds before the host sends the request, and the server
// consumes it as the request arrives. Anything still here a minute later
// describes a call that never came — refused at a permission prompt, or
// interrupted — and reading it would attribute some later call to whichever
// agent happened to be denied earlier. Expiry is therefore part of the
// correctness argument, not housekeeping.
const CallerTicketTTL = 60 * time.Second

// AgentLabel is the identity of one agent inside a host session.
//
// A host session is not an agent. Claude Code runs a main thread and its peers
// in one process, sharing one session id and one MCP connection, and until this
// existed they all resolved to a single root — so a peer either inherited an
// identity it never asked for or was refused. The label is what gives each of
// them a root of its own without inventing a second identity space: it is still
// a session label, still the thing RootBySession matches on, still what the
// claim guard compares. It just says which agent within the session.
//
// agentID alone would be enough to be unique. The type is carried too because
// this string is read by people — it is what `stigmergy watch` shows and what a
// conflict message names — and "explorer:a4d39a33" tells an operator what they
// are negotiating with while a bare hex id tells them nothing.
//
// An empty agentID returns the session id unchanged: that is the main thread,
// whose payload carries no agent_id, and its label must keep being exactly what
// it has always been. Anything else would strand every claim it already holds.
func AgentLabel(sessionID, agentType, agentID string) string {
	if sessionID == "" || agentID == "" {
		return sessionID
	}
	var sb strings.Builder
	sb.WriteString(sessionID)
	sb.WriteString("#")
	if agentType != "" {
		sb.WriteString(agentType)
		sb.WriteString(":")
	}
	sb.WriteString(agentID)
	return sb.String()
}

// SessionOf is the host session a label belongs to, agent labels included. It is
// how a session-wide event — the host exiting — reaches the roots of the agents
// that ran inside it.
func SessionOf(label string) string {
	session, _, found := strings.Cut(label, "#")
	if !found {
		return label
	}
	return session
}

// CallerTicket is one call's identity, written by a hook and read by the server.
type CallerTicket struct {
	ToolUseID    string
	SessionLabel string
	AgentKind    string
	Worktree     string
}

// PutCallerTicket records who is about to make a call.
//
// Called from the hook path, which runs inside the agent's edit latency budget,
// so it is one INSERT and nothing else — no sweep, no read-back. REPLACE rather
// than INSERT because a tool-use id is unique per call and a duplicate can only
// mean the same call being stamped twice (a hook configured in two matchers,
// say); the later stamp is as true as the earlier one.
func (d *DB) PutCallerTicket(t CallerTicket) error {
	if t.ToolUseID == "" || t.SessionLabel == "" {
		return nil // Nothing to say; not an error worth failing a hook over.
	}
	if _, err := d.Exec(
		`INSERT OR REPLACE INTO caller_tickets(tool_use_id, session_label, agent_kind, worktree, created_at)
		 VALUES(?, ?, ?, ?, ?)`,
		t.ToolUseID, t.SessionLabel, t.AgentKind, t.Worktree, Now()); err != nil {
		return serr.Internalf(err, "failed to record the caller ticket")
	}
	return nil
}

// TakeCallerTicket consumes the ticket for a tool-use id, returning nil if there
// is none.
//
// Consuming rather than reading is deliberate: a ticket answers one call, and a
// ticket left behind could answer a later call that has nothing to do with it.
// The same transaction sweeps whatever has expired, so the table stays the size
// of the calls in flight and nothing else ever has to remember to clean it.
//
// A missing ticket is not an error. It is the ordinary case for the main thread,
// for hosts that install no hooks, and for a call made before this shipped — all
// of which mean "you are the session", which is what the caller does with nil.
func (d *DB) TakeCallerTicket(toolUseID string) (*CallerTicket, error) {
	if toolUseID == "" {
		return nil, nil
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	cutoff := Stamp(NowTime().Add(-CallerTicketTTL))
	var t CallerTicket
	t.ToolUseID = toolUseID
	err = tx.QueryRow(
		`SELECT session_label, agent_kind, worktree FROM caller_tickets
		  WHERE tool_use_id = ? AND created_at > ?`, toolUseID, cutoff).
		Scan(&t.SessionLabel, &t.AgentKind, &t.Worktree)
	found := true
	if errors.Is(err, sql.ErrNoRows) {
		found = false
	} else if err != nil {
		return nil, serr.Internalf(err, "failed to read the caller ticket")
	}

	if _, err := tx.Exec(
		`DELETE FROM caller_tickets WHERE tool_use_id = ? OR created_at <= ?`,
		toolUseID, cutoff); err != nil {
		return nil, serr.Internalf(err, "failed to consume the caller ticket")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit the caller ticket")
	}
	if !found {
		return nil, nil
	}
	return &t, nil
}
